package ssh

import (
	"context"
	"fmt"
	"strings"

	"github.com/ebnsina/yol/internal/inventory"
	"github.com/ebnsina/yol/internal/proto"
)

// Survey looks at a server and changes nothing on it. Every command it runs reads. A user who
// cancels after seeing the result leaves their machine exactly as it was, which is the whole
// point of doing this before installing anything.
func Survey(ctx context.Context, c *Client) (*proto.SurveyResult, error) {
	facts := inventory.Facts(ctx, c)
	if facts.Arch == "" {
		return nil, fmt.Errorf("ssh: could not read basic details from the server")
	}

	out := &proto.SurveyResult{
		Facts:         facts,
		DockerPresent: facts.DockerVersion != "",
		// Reported even when unsupported, because showing what is on a machine is more useful
		// than refusing to say anything about it.
		Unsupported: unsupported(facts),
	}
	out.Inventory = inventory.Collect(ctx, c)
	out.Conflicts = inventory.Conflicts(out.Inventory)
	return out, nil
}

// unsupported returns a plain-language reason, or empty when the machine is fine. Being narrow
// on purpose: half-working support for an untested distribution is worse than a clear no.
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
