package ssh

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ebnsina/yol/internal/proto"
)

// Ports our router needs when it handles web traffic itself.
var routerPorts = []int{80, 443}

// Survey looks at a server and changes nothing on it. Every command below reads. A user who
// cancels after seeing the result leaves their machine exactly as it was, which is the whole
// point of doing this before installing anything.
func Survey(ctx context.Context, c *Client) (*proto.SurveyResult, error) {
	out := &proto.SurveyResult{Inventory: proto.Inventory{At: time.Now().UTC()}}

	facts, err := hostFacts(ctx, c)
	if err != nil {
		return nil, err
	}
	out.Facts = facts

	if reason := unsupported(facts); reason != "" {
		out.Unsupported = reason
		// Still returned, because showing what is on an unsupported machine is more useful
		// than refusing to say anything.
	}

	out.Inventory.Ports = listeningPorts(ctx, c, &out.Inventory)
	out.Inventory.Services = systemdUnits(ctx, c, &out.Inventory)

	if version := c.Output(ctx, "docker version --format '{{.Server.Version}}' 2>/dev/null"); version != "" {
		out.DockerPresent = true
		out.Facts.DockerVersion = version
		out.Inventory.Containers = containers(ctx, c, &out.Inventory)
		out.Inventory.Images = images(ctx, c)
		out.Inventory.Volumes = volumes(ctx, c)
	}

	out.Inventory.Databases = databases(out.Inventory)
	out.Conflicts = conflicts(out.Inventory)
	return out, nil
}

// hostFacts reads what the machine is.
func hostFacts(ctx context.Context, c *Client) (proto.HostFacts, error) {
	facts := proto.HostFacts{
		Hostname: c.Output(ctx, "hostname"),
		Arch:     c.Output(ctx, "uname -m"),
		Kernel:   c.Output(ctx, "uname -r"),
	}
	if facts.Arch == "" {
		return facts, fmt.Errorf("ssh: could not read basic details from the server")
	}

	for line := range strings.SplitSeq(c.Output(ctx, "cat /etc/os-release 2>/dev/null"), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		value = strings.Trim(value, `"`)
		switch key {
		case "NAME":
			facts.OSName = value
		case "VERSION_ID":
			facts.OSVersion = value
		}
	}

	if count, err := strconv.Atoi(c.Output(ctx, "nproc 2>/dev/null")); err == nil {
		facts.CPUCount = count
	}
	// MemTotal is in kibibytes.
	if kb, err := strconv.ParseInt(c.Output(ctx, "awk '/MemTotal/ {print $2}' /proc/meminfo"), 10, 64); err == nil {
		facts.MemoryBytes = kb * 1024
	}
	return facts, nil
}

// unsupported returns a plain-language reason, or empty when the machine is fine. Being
// narrow on purpose: half-working support for an untested distribution is worse than a clear no.
func unsupported(facts proto.HostFacts) string {
	name := strings.ToLower(facts.OSName)
	switch {
	case name == "":
		return "We could not tell which operating system this server runs. Ubuntu and Debian are supported."
	case strings.Contains(name, "ubuntu"), strings.Contains(name, "debian"):
		return ""
	default:
		return fmt.Sprintf("This server runs %s. Only Ubuntu and Debian are supported at the moment.", facts.OSName)
	}
}

// listeningPorts reads what is listening and which process holds it. This is what makes a
// port conflict explainable rather than mysterious.
func listeningPorts(ctx context.Context, c *Client, inv *proto.Inventory) []proto.Port {
	raw := c.Output(ctx, "ss -tulpnH 2>/dev/null")
	if raw == "" {
		inv.Incomplete = append(inv.Incomplete, "We could not read which ports are in use on this server.")
		return nil
	}

	seen := make(map[string]bool)
	var ports []proto.Port
	for line := range strings.SplitSeq(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		protocol := fields[0]
		address := fields[4]
		number, ok := portFromAddress(address)
		if !ok {
			continue
		}

		key := protocol + ":" + strconv.Itoa(number)
		if seen[key] {
			continue
		}
		seen[key] = true

		ports = append(ports, proto.Port{
			Number:   number,
			Protocol: protocol,
			Address:  address,
			Process:  processName(line),
		})
	}
	return ports
}

// portFromAddress pulls the port from forms like 0.0.0.0:80, [::]:443 and *:22.
func portFromAddress(address string) (int, bool) {
	idx := strings.LastIndex(address, ":")
	if idx < 0 {
		return 0, false
	}
	number, err := strconv.Atoi(address[idx+1:])
	if err != nil || number <= 0 || number > 65535 {
		return 0, false
	}
	return number, true
}

// processName pulls the name out of the users:(("nginx",pid=123,fd=6)) field.
func processName(line string) string {
	_, rest, found := strings.Cut(line, `users:(("`)
	if !found {
		return ""
	}
	name, _, found := strings.Cut(rest, `"`)
	if !found {
		return ""
	}
	return name
}

func systemdUnits(ctx context.Context, c *Client, inv *proto.Inventory) []proto.Service {
	raw := c.Output(ctx,
		"systemctl list-units --type=service --all --no-legend --no-pager --plain 2>/dev/null")
	if raw == "" {
		inv.Incomplete = append(inv.Incomplete, "We could not read the list of services on this server.")
		return nil
	}

	var services []proto.Service
	for line := range strings.SplitSeq(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		name := strings.TrimSuffix(fields[0], ".service")
		// Units that only exist because something referenced them are noise.
		if fields[1] == "not-found" {
			continue
		}
		services = append(services, proto.Service{
			Name:        name,
			Active:      fields[2],
			Description: strings.Join(fields[4:], " "),
		})
	}
	return services
}

// dockerContainer is the shape of `docker ps` in JSON, kept separate from our own type so a
// change in Docker's output cannot quietly reshape what we store.
type dockerContainer struct {
	ID        string `json:"ID"`
	Names     string `json:"Names"`
	Image     string `json:"Image"`
	State     string `json:"State"`
	Status    string `json:"Status"`
	Labels    string `json:"Labels"`
	Ports     string `json:"Ports"`
	CreatedAt string `json:"CreatedAt"`
}

func containers(ctx context.Context, c *Client, inv *proto.Inventory) []proto.Container {
	raw := c.Output(ctx, "docker ps --all --no-trunc --format '{{json .}}' 2>/dev/null")
	if raw == "" {
		return nil
	}

	var out []proto.Container
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row dockerContainer
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			inv.Incomplete = append(inv.Incomplete, "One container could not be read.")
			continue
		}

		labels := parseLabels(row.Labels)
		out = append(out, proto.Container{
			ID:             row.ID,
			Name:           firstName(row.Names),
			Image:          row.Image,
			State:          row.State,
			Status:         row.Status,
			CreatedAt:      parseDockerTime(row.CreatedAt),
			Ports:          parsePublishedPorts(row.Ports),
			Labels:         labels,
			Managed:        proto.IsManaged(labels),
			ComposeProject: labels["com.docker.compose.project"],
		})
	}
	return out
}

// parseLabels reads Docker's comma-separated key=value label string.
func parseLabels(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	labels := make(map[string]string)
	for pair := range strings.SplitSeq(raw, ",") {
		key, value, found := strings.Cut(pair, "=")
		if found {
			labels[strings.TrimSpace(key)] = value
		}
	}
	return labels
}

// firstName takes one name; Docker can report several separated by commas.
func firstName(names string) string {
	name, _, _ := strings.Cut(names, ",")
	return strings.TrimPrefix(strings.TrimSpace(name), "/")
}

// parsePublishedPorts pulls host ports out of "0.0.0.0:8080->80/tcp, :::8080->80/tcp".
func parsePublishedPorts(raw string) []int {
	if raw == "" {
		return nil
	}
	seen := make(map[int]bool)
	var ports []int
	for mapping := range strings.SplitSeq(raw, ",") {
		host, _, found := strings.Cut(strings.TrimSpace(mapping), "->")
		if !found {
			continue
		}
		number, ok := portFromAddress(host)
		if !ok || seen[number] {
			continue
		}
		seen[number] = true
		ports = append(ports, number)
	}
	return ports
}

// parseDockerTime reads Docker's CreatedAt, which is not RFC 3339.
func parseDockerTime(raw string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05 -0700",
		time.RFC3339,
	} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(raw)); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func images(ctx context.Context, c *Client) []proto.Image {
	raw := c.Output(ctx, "docker images --no-trunc --format '{{json .}}' 2>/dev/null")
	if raw == "" {
		return nil
	}

	var out []proto.Image
	for line := range strings.SplitSeq(raw, "\n") {
		var row struct {
			ID         string `json:"ID"`
			Repository string `json:"Repository"`
			Tag        string `json:"Tag"`
			Size       string `json:"Size"`
			CreatedAt  string `json:"CreatedAt"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &row); err != nil {
			continue
		}
		image := proto.Image{ID: row.ID, SizeBytes: parseSize(row.Size), CreatedAt: parseDockerTime(row.CreatedAt)}
		if row.Repository != "" && row.Repository != "<none>" {
			image.Tags = []string{row.Repository + ":" + row.Tag}
		}
		out = append(out, image)
	}
	return out
}

func volumes(ctx context.Context, c *Client) []proto.Volume {
	raw := c.Output(ctx, "docker volume ls --format '{{json .}}' 2>/dev/null")
	if raw == "" {
		return nil
	}

	var out []proto.Volume
	for line := range strings.SplitSeq(raw, "\n") {
		var row struct {
			Name       string `json:"Name"`
			Driver     string `json:"Driver"`
			Mountpoint string `json:"Mountpoint"`
			Labels     string `json:"Labels"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &row); err != nil {
			continue
		}
		out = append(out, proto.Volume{
			Name:       row.Name,
			Driver:     row.Driver,
			Mountpoint: row.Mountpoint,
			Managed:    proto.IsManaged(parseLabels(row.Labels)),
		})
	}
	return out
}

// parseSize reads Docker's human sizes like "1.61GB" and "48.2MB".
func parseSize(raw string) int64 {
	raw = strings.TrimSpace(raw)
	multipliers := []struct {
		suffix string
		factor float64
	}{
		{"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"KB", 1e3}, {"B", 1},
	}
	for _, m := range multipliers {
		if number, found := strings.CutSuffix(raw, m.suffix); found {
			value, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
			if err != nil {
				return 0
			}
			return int64(value * m.factor)
		}
	}
	return 0
}

// knownDatabases maps a recognisable name to an engine and its usual port.
var knownDatabases = []struct {
	match  string
	engine proto.DatabaseEngine
	port   int
}{
	{"postgres", proto.EnginePostgres, 5432},
	{"postgresql", proto.EnginePostgres, 5432},
	{"timescale", proto.EnginePostgres, 5432},
	{"pgvector", proto.EnginePostgres, 5432},
	{"mysql", proto.EngineMySQL, 3306},
	{"mariadb", proto.EngineMySQL, 3306},
	{"redis", proto.EngineRedis, 6379},
	{"valkey", proto.EngineRedis, 6379},
	{"clickhouse", proto.EngineClickHouse, 9000},
	{"mongo", proto.EngineMongo, 27017},
}

// databases guesses at what is storing data here, from containers, units and ports already
// collected. This is a heuristic and will sometimes be wrong or incomplete, so each result
// carries how confident it is and nothing is ever acted on automatically.
func databases(inv proto.Inventory) []proto.Database {
	var found []proto.Database
	claimed := make(map[string]bool)

	// An image name is the strongest signal available.
	for _, container := range inv.Containers {
		for _, known := range knownDatabases {
			if !strings.Contains(strings.ToLower(container.Image), known.match) {
				continue
			}
			port := known.port
			if len(container.Ports) > 0 {
				port = container.Ports[0]
			}
			found = append(found, proto.Database{
				Engine:     known.engine,
				Version:    versionFromImage(container.Image),
				Port:       port,
				Confidence: proto.ConfidenceCertain,
				Source:     container.Name,
				InDocker:   true,
				Managed:    container.Managed,
			})
			claimed[string(known.engine)] = true
			break
		}
	}

	// A systemd unit means one installed natively rather than in a container.
	for _, service := range inv.Services {
		for _, known := range knownDatabases {
			if !strings.HasPrefix(strings.ToLower(service.Name), known.match) {
				continue
			}
			if claimed[string(known.engine)] {
				break
			}
			found = append(found, proto.Database{
				Engine:     known.engine,
				Port:       known.port,
				Confidence: proto.ConfidenceLikely,
				Source:     service.Name,
			})
			claimed[string(known.engine)] = true
			break
		}
	}

	// A well-known port with nothing else to explain it is worth mentioning, but weakly.
	for _, port := range inv.Ports {
		for _, known := range knownDatabases {
			if port.Number != known.port || claimed[string(known.engine)] {
				continue
			}
			found = append(found, proto.Database{
				Engine:     known.engine,
				Port:       port.Number,
				Confidence: proto.ConfidencePossible,
				Source:     orUnknown(port.Process),
			})
			claimed[string(known.engine)] = true
			break
		}
	}
	return found
}

// versionFromImage reads the tag, which is usually the version.
func versionFromImage(image string) string {
	_, tag, found := strings.Cut(image, ":")
	if !found || tag == "latest" {
		return ""
	}
	return tag
}

func orUnknown(process string) string {
	if process == "" {
		return "an unidentified process"
	}
	return process
}

// conflicts reports anything already holding the ports our router would want. Reported so the
// user is asked rather than having their web server stopped without warning.
func conflicts(inv proto.Inventory) []proto.RoutingConflict {
	byContainer := make(map[int]string)
	for _, container := range inv.Containers {
		if container.State != "running" {
			continue
		}
		for _, port := range container.Ports {
			byContainer[port] = container.Name
		}
	}

	var out []proto.RoutingConflict
	for _, wanted := range routerPorts {
		for _, port := range inv.Ports {
			if port.Number != wanted {
				continue
			}
			conflict := proto.RoutingConflict{Port: wanted, Process: port.Process}
			if name, ok := byContainer[wanted]; ok {
				conflict.Container = name
			}
			out = append(out, conflict)
			break
		}
	}
	return out
}
