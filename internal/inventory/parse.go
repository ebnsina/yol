package inventory

import (
	"strconv"
	"strings"
	"time"

	"github.com/ebnsina/yol/internal/proto"
)

// Parsers for the output of the commands above. Exported so they can be tested directly,
// which is where the subtle mistakes live.

// PortFromAddress pulls the port from forms like 0.0.0.0:80, [::]:443 and *:22.
func PortFromAddress(address string) (int, bool) {
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

// ProcessName pulls the name out of the users:(("nginx",pid=123,fd=6)) field.
func ProcessName(line string) string {
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

// ParseLabels reads Docker's comma-separated key=value label string.
func ParseLabels(raw string) map[string]string {
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

// FirstName takes one name; Docker can report several separated by commas.
func FirstName(names string) string {
	name, _, _ := strings.Cut(names, ",")
	return strings.TrimPrefix(strings.TrimSpace(name), "/")
}

// PublishedPorts pulls host ports out of "0.0.0.0:8080->80/tcp, :::8080->80/tcp".
func PublishedPorts(raw string) []int {
	if raw == "" {
		return nil
	}
	seen := make(map[int]bool)
	var out []int
	for mapping := range strings.SplitSeq(raw, ",") {
		host, _, found := strings.Cut(strings.TrimSpace(mapping), "->")
		if !found {
			continue
		}
		number, ok := PortFromAddress(host)
		if !ok || seen[number] {
			continue
		}
		seen[number] = true
		out = append(out, number)
	}
	return out
}

// ParseDockerTime reads Docker's CreatedAt, which is not RFC 3339.
func ParseDockerTime(raw string) time.Time {
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

// ParseSize reads Docker's human sizes like "1.61GB" and "48.2MB".
func ParseSize(raw string) int64 {
	raw = strings.TrimSpace(raw)
	for _, unit := range []struct {
		suffix string
		factor float64
	}{
		{"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"KB", 1e3}, {"B", 1},
	} {
		if number, found := strings.CutSuffix(raw, unit.suffix); found {
			value, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
			if err != nil {
				return 0
			}
			return int64(value * unit.factor)
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

// DetectDatabases guesses at what is storing data here, from what has already been collected.
// This is a heuristic and will sometimes be wrong or incomplete, so each result carries how
// confident it is and nothing found this way is ever acted on automatically.
func DetectDatabases(inv proto.Inventory) []proto.Database {
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

	// A unit means one installed natively rather than in a container.
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
