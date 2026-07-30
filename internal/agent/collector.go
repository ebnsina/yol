package agent

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ebnsina/yol/internal/inventory"
	"github.com/ebnsina/yol/internal/proto"
)

// hostCollector reads this machine. It runs the same commands the survey runs over SSH, and
// the answers are parsed by the same code, so a server looks the same however we looked at it.
//
// Shelling out rather than using libraries keeps the agent a single dependency-free binary,
// which is what makes installing it a matter of copying one file.
type hostCollector struct {
	dockerHost string
}

// NewCollector builds a collector for this machine.
func NewCollector(dockerHost string) Collector {
	return &hostCollector{dockerHost: dockerHost}
}

// Output runs a command and returns its trimmed output, empty when it fails. The commands are
// fixed strings from this package, never anything the control plane sends, so there is nothing
// for an attacker to inject into.
func (c *hostCollector) Output(ctx context.Context, command string) string {
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "sh", "-c", command)
	if c.dockerHost != "" {
		cmd.Env = append(os.Environ(), "DOCKER_HOST="+c.dockerHost)
	}

	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Facts describes the machine.
func (c *hostCollector) Facts(ctx context.Context) proto.HostFacts {
	return inventory.Facts(ctx, c)
}

// Usage is what the machine is consuming now.
func (c *hostCollector) Usage(ctx context.Context) proto.Usage {
	return inventory.Usage(ctx, c)
}

// Inventory reports everything on the machine, ours and the customer's alike.
func (c *hostCollector) Inventory(ctx context.Context) proto.Inventory {
	return inventory.Collect(ctx, c)
}
