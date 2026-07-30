// Package inventory reads what is on a server.
//
// It is used from two places that reach a machine differently: over SSH before anything is
// installed, and by the agent once it is running. Both ask the same questions and parse the
// same answers, so the picture of a server does not depend on how we happened to look at it.
package inventory

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/ebnsina/yol/internal/proto"
)

// Runner executes a command on the machine and returns its trimmed output, empty when it
// fails. A missing tool is an ordinary answer, not an error: probing a machine means
// expecting some commands not to be there.
type Runner interface {
	Output(ctx context.Context, command string) string
}

// RouterPorts are what our router needs when it handles web traffic itself.
var RouterPorts = []int{80, 443}

// Facts reads what the machine is.
func Facts(ctx context.Context, r Runner) proto.HostFacts {
	facts := proto.HostFacts{
		Hostname:      r.Output(ctx, "hostname"),
		Arch:          r.Output(ctx, "uname -m"),
		Kernel:        r.Output(ctx, "uname -r"),
		DockerVersion: r.Output(ctx, "docker version --format '{{.Server.Version}}' 2>/dev/null"),
	}

	for line := range strings.SplitSeq(r.Output(ctx, "cat /etc/os-release 2>/dev/null"), "\n") {
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

	if count, err := strconv.Atoi(r.Output(ctx, "nproc 2>/dev/null")); err == nil {
		facts.CPUCount = count
	}
	// MemTotal is reported in kibibytes.
	if kb, err := strconv.ParseInt(r.Output(ctx, "awk '/MemTotal/ {print $2}' /proc/meminfo"), 10, 64); err == nil {
		facts.MemoryBytes = kb * 1024
	}
	return facts
}

// Usage is what the machine is consuming now.
func Usage(ctx context.Context, r Runner) proto.Usage {
	usage := proto.Usage{}

	total, _ := strconv.ParseInt(r.Output(ctx, "awk '/MemTotal/ {print $2}' /proc/meminfo"), 10, 64)
	available, _ := strconv.ParseInt(r.Output(ctx, "awk '/MemAvailable/ {print $2}' /proc/meminfo"), 10, 64)
	if total > 0 {
		usage.MemoryTotalBytes = total * 1024
		usage.MemoryUsedBytes = (total - available) * 1024
	}

	// The first value in loadavg is the one-minute average.
	if fields := strings.Fields(r.Output(ctx, "cat /proc/loadavg")); len(fields) > 0 {
		if load, err := strconv.ParseFloat(fields[0], 64); err == nil {
			usage.LoadAverage1 = load
			if cpus, err := strconv.Atoi(r.Output(ctx, "nproc")); err == nil && cpus > 0 {
				// Load per processor is a fair stand-in for how busy a machine is.
				usage.CPUPercent = min(load/float64(cpus)*100, 100)
			}
		}
	}
	return usage
}

// Collect reads everything on the machine, ours and the customer's alike.
func Collect(ctx context.Context, r Runner) proto.Inventory {
	inv := proto.Inventory{At: time.Now().UTC()}

	inv.Ports = ports(ctx, r, &inv)
	inv.Services = services(ctx, r, &inv)

	// Everything below needs Docker, which a machine may not have yet.
	if r.Output(ctx, "docker version --format '{{.Server.Version}}' 2>/dev/null") != "" {
		inv.Containers = containers(ctx, r, &inv)
		inv.Images = images(ctx, r)
		inv.Volumes = volumes(ctx, r)
	}

	inv.Databases = DetectDatabases(inv)
	return inv
}

// Conflicts reports anything already holding the ports our router would want, so the user is
// asked rather than having their web server stopped without warning.
func Conflicts(inv proto.Inventory) []proto.RoutingConflict {
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
	for _, wanted := range RouterPorts {
		for _, port := range inv.Ports {
			if port.Number != wanted {
				continue
			}
			conflict := proto.RoutingConflict{Port: wanted, Process: port.Process}
			// Naming the container is actionable; naming the proxy process is not.
			if name, ok := byContainer[wanted]; ok {
				conflict.Container = name
			}
			out = append(out, conflict)
			break
		}
	}
	return out
}

func containers(ctx context.Context, r Runner, inv *proto.Inventory) []proto.Container {
	raw := r.Output(ctx, "docker ps --all --no-trunc --format '{{json .}}' 2>/dev/null")
	if raw == "" {
		return nil
	}

	var out []proto.Container
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row struct {
			ID        string `json:"ID"`
			Names     string `json:"Names"`
			Image     string `json:"Image"`
			State     string `json:"State"`
			Status    string `json:"Status"`
			Labels    string `json:"Labels"`
			Ports     string `json:"Ports"`
			CreatedAt string `json:"CreatedAt"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			inv.Incomplete = append(inv.Incomplete, "One container could not be read.")
			continue
		}

		labels := ParseLabels(row.Labels)
		out = append(out, proto.Container{
			ID:             row.ID,
			Name:           FirstName(row.Names),
			Image:          row.Image,
			State:          row.State,
			Status:         row.Status,
			CreatedAt:      ParseDockerTime(row.CreatedAt),
			Ports:          PublishedPorts(row.Ports),
			Labels:         labels,
			Managed:        proto.IsManaged(labels),
			ComposeProject: labels["com.docker.compose.project"],
		})
	}
	return out
}

func images(ctx context.Context, r Runner) []proto.Image {
	raw := r.Output(ctx, "docker images --no-trunc --format '{{json .}}' 2>/dev/null")
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
		image := proto.Image{ID: row.ID, SizeBytes: ParseSize(row.Size), CreatedAt: ParseDockerTime(row.CreatedAt)}
		if row.Repository != "" && row.Repository != "<none>" {
			image.Tags = []string{row.Repository + ":" + row.Tag}
		}
		out = append(out, image)
	}
	return out
}

func volumes(ctx context.Context, r Runner) []proto.Volume {
	raw := r.Output(ctx, "docker volume ls --format '{{json .}}' 2>/dev/null")
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
			Managed:    proto.IsManaged(ParseLabels(row.Labels)),
		})
	}
	return out
}

func services(ctx context.Context, r Runner, inv *proto.Inventory) []proto.Service {
	raw := r.Output(ctx,
		"systemctl list-units --type=service --all --no-legend --no-pager --plain 2>/dev/null")
	if raw == "" {
		inv.Incomplete = append(inv.Incomplete, "We could not read the list of services on this server.")
		return nil
	}

	var out []proto.Service
	for line := range strings.SplitSeq(raw, "\n") {
		fields := strings.Fields(line)
		// Units that exist only because something referenced them are noise.
		if len(fields) < 4 || fields[1] == "not-found" {
			continue
		}
		out = append(out, proto.Service{
			Name:        strings.TrimSuffix(fields[0], ".service"),
			Active:      fields[2],
			Description: strings.Join(fields[4:], " "),
		})
	}
	return out
}

func ports(ctx context.Context, r Runner, inv *proto.Inventory) []proto.Port {
	raw := r.Output(ctx, "ss -tulpnH 2>/dev/null")
	if raw == "" {
		inv.Incomplete = append(inv.Incomplete, "We could not read which ports are in use on this server.")
		return nil
	}

	seen := make(map[string]bool)
	var out []proto.Port
	for line := range strings.SplitSeq(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		number, ok := PortFromAddress(fields[4])
		if !ok {
			continue
		}
		key := fields[0] + ":" + strconv.Itoa(number)
		if seen[key] {
			continue
		}
		seen[key] = true

		out = append(out, proto.Port{
			Number:   number,
			Protocol: fields[0],
			Address:  fields[4],
			Process:  ProcessName(line),
		})
	}
	return out
}
