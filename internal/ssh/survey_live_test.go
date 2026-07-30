//go:build live

// Survey checks against a real server from the development harness. Run with
// `make test-live` after `make vps-up`.
package ssh

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ebnsina/yol/internal/proto"
)

func liveClient(t *testing.T) *Client {
	t.Helper()

	host := os.Getenv("YOL_LIVE_HOST")
	keyPath := os.Getenv("YOL_LIVE_KEY")
	if host == "" || keyPath == "" {
		t.Skip("YOL_LIVE_HOST and YOL_LIVE_KEY not set")
	}
	port, err := strconv.Atoi(os.Getenv("YOL_LIVE_PORT"))
	if err != nil {
		port = 22
	}

	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, hostKey, err := Dial(ctx, Target{Host: host, Port: port},
		Credential{User: "root", Key: string(key)})
	if err != nil {
		t.Fatalf("dial %s:%d: %v", host, port, err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if hostKey == "" {
		t.Error("no host key was recorded, so it could never be pinned later")
	}
	return client
}

// snapshot captures what is on the machine, so a survey can be proved not to have changed it.
func snapshot(t *testing.T, c *Client) string {
	t.Helper()
	ctx := context.Background()
	return strings.Join([]string{
		c.Output(ctx, "docker ps -a --format '{{.Names}}:{{.State}}' | sort"),
		c.Output(ctx, "docker volume ls -q | sort"),
		c.Output(ctx, "docker images -q | sort"),
		c.Output(ctx, "systemctl list-units --type=service --state=active --no-legend --plain | awk '{print $1}' | sort"),
		c.Output(ctx, "ls -A /etc/yol 2>/dev/null"),
		c.Output(ctx, "ls -A /var/lib/yol 2>/dev/null"),
	}, "\n--\n")
}

// The guarantee the whole flow rests on: looking at a server changes nothing, so someone who
// cancels after seeing what we found is left exactly as they were.
func TestSurveyChangesNothing(t *testing.T) {
	c := liveClient(t)

	before := snapshot(t, c)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := Survey(ctx, c); err != nil {
		t.Fatalf("Survey: %v", err)
	}

	after := snapshot(t, c)
	if before != after {
		t.Errorf("the survey changed the server.\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

func TestSurveyReadsTheMachine(t *testing.T) {
	c := liveClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result, err := Survey(ctx, c)
	if err != nil {
		t.Fatalf("Survey: %v", err)
	}

	if result.Facts.OSName == "" || result.Facts.Arch == "" {
		t.Errorf("basic facts missing: %+v", result.Facts)
	}
	if result.Facts.CPUCount == 0 || result.Facts.MemoryBytes == 0 {
		t.Errorf("hardware not read: %d CPU, %d bytes", result.Facts.CPUCount, result.Facts.MemoryBytes)
	}
	if result.Unsupported != "" {
		t.Errorf("harness server reported unsupported: %s", result.Unsupported)
	}
	if !result.DockerPresent || result.Facts.DockerVersion == "" {
		t.Error("Docker is installed on the harness but was not detected")
	}
	if len(result.Inventory.Services) == 0 {
		t.Error("no services read, so the machine looks emptier than it is")
	}

	// sshd is how we got here, so it must appear.
	var foundSSH bool
	for _, port := range result.Inventory.Ports {
		if port.Number == 22 {
			foundSSH = true
		}
	}
	if !foundSSH {
		t.Error("port 22 not reported, though the connection came in on it")
	}
}

// The scenario that matters: a server already in use. Requires the fixtures set up by
// `make vps-messy`.
func TestSurveyFindsExistingWork(t *testing.T) {
	if os.Getenv("YOL_LIVE_MESSY") == "" {
		t.Skip("YOL_LIVE_MESSY not set; run make vps-messy first")
	}
	c := liveClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result, err := Survey(ctx, c)
	if err != nil {
		t.Fatalf("Survey: %v", err)
	}

	byName := make(map[string]proto.Container)
	for _, container := range result.Inventory.Containers {
		byName[container.Name] = container
	}

	// Their containers must be seen, and none of them mistaken for ours.
	for _, name := range []string{"their-nginx", "their-postgres", "old-worker"} {
		container, found := byName[name]
		if !found {
			t.Errorf("%s was not reported, so the server would look emptier than it is", name)
			continue
		}
		if container.Managed {
			t.Errorf("%s was treated as ours; the destructive path would then consider it", name)
		}
	}

	// A stopped container must still be listed, since it explains disk use and name clashes.
	if worker, found := byName["old-worker"]; found && worker.State == "running" {
		t.Error("old-worker should be reported as stopped")
	}

	// The conflict on 80 and 443 is what the user has to be asked about.
	conflictPorts := make(map[int]bool)
	for _, conflict := range result.Conflicts {
		conflictPorts[conflict.Port] = true
		if conflict.Process == "" && conflict.Container == "" {
			t.Errorf("port %d conflict names nothing, so the user cannot act on it", conflict.Port)
		}
	}
	for _, port := range []int{80, 443} {
		if !conflictPorts[port] {
			t.Errorf("port %d is held by their nginx but was not reported as a conflict", port)
		}
	}

	// Their hand-run Postgres should be found, and reported as certain since the image says so.
	var foundPostgres bool
	for _, db := range result.Inventory.Databases {
		if db.Engine != proto.EnginePostgres {
			continue
		}
		foundPostgres = true
		if db.Confidence != proto.ConfidenceCertain {
			t.Errorf("postgres confidence = %q, want certain from an image name", db.Confidence)
		}
		if db.Managed {
			t.Error("their postgres was reported as ours")
		}
		if !db.InDocker {
			t.Error("their postgres runs in Docker but was not reported as such")
		}
	}
	if !foundPostgres {
		t.Error("their postgres was not detected")
	}

	if len(result.Inventory.Volumes) == 0 {
		t.Error("no volumes reported, though one was created by hand")
	}
}
