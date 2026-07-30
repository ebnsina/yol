package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ebnsina/yol/internal/proto"
)

// Reconciliation makes the machine match the specification. It is idempotent: running it twice
// changes nothing the second time, which is what lets it run on a timer, on a push, and after a
// reboot without special cases.
//
// The destructive half only ever considers containers this platform owns. A container is ours
// when it carries our label, or when the specification names it as adopted — a label cannot be
// added to a container that already exists, so an adopted one is recognised by name instead.
// Everything else is invisible to removal.

// apply brings the machine in line with the specification and reports what changed.
func (a *Agent) apply(ctx context.Context, spec *proto.Spec) proto.Applied {
	applied := proto.Applied{SpecVersion: spec.Version, At: time.Now().UTC()}

	if a.mode != proto.ModeManaged {
		applied.Refused = "This server is being watched only, so nothing on it was changed."
		return applied
	}

	actual := a.managedContainers(ctx, spec)
	desired := make(map[string]proto.SpecContainer, len(spec.Containers))
	for _, container := range spec.Containers {
		desired[container.Name] = container
	}

	// Remove what we own and no longer want. Ordered before creating so a renamed container
	// releases its ports first.
	for name := range actual {
		if _, wanted := desired[name]; wanted {
			continue
		}
		if err := a.removeContainer(ctx, name); err != nil {
			applied.Failures = append(applied.Failures, proto.ApplyFailure{
				Container: name, Reason: "could not be removed",
			})
			slog.Error("could not remove container", "container", name, "error", err)
			continue
		}
		applied.Removed = append(applied.Removed, name)
	}

	for _, volume := range spec.Volumes {
		if err := a.ensureVolume(ctx, volume); err != nil {
			slog.Error("could not create volume", "volume", volume.Name, "error", err)
		}
	}

	for name, want := range desired {
		current, exists := actual[name]
		if exists && a.matches(current, want) {
			applied.Unchanged++
			continue
		}
		if exists {
			// Docker cannot change a running container's shape, so replacing is the only way.
			if err := a.removeContainer(ctx, name); err != nil {
				applied.Failures = append(applied.Failures, proto.ApplyFailure{
					Container: name, Reason: "could not be replaced",
				})
				continue
			}
		}
		if err := a.createContainer(ctx, want); err != nil {
			applied.Failures = append(applied.Failures, proto.ApplyFailure{
				Container: name, Reason: "could not be started",
			})
			slog.Error("could not start container", "container", name, "error", err)
			continue
		}
		applied.Created = append(applied.Created, name)
	}
	return applied
}

// managedContainers returns only the containers this platform owns. Anything the customer
// created is deliberately absent, so the removal loop above cannot see it.
func (a *Agent) managedContainers(ctx context.Context, spec *proto.Spec) map[string]proto.Container {
	adopted := make(map[string]time.Time, len(spec.Adopted))
	for _, entry := range spec.Adopted {
		adopted[entry.Name] = entry.CreatedAt
	}

	owned := make(map[string]proto.Container)
	for _, container := range a.collector.Inventory(ctx).Containers {
		if container.Managed {
			owned[container.Name] = container
			continue
		}
		// An adopted container carries no label of ours, so it is matched by name. The creation
		// time guards against a different container that happens to reuse the name.
		if createdAt, ok := adopted[container.Name]; ok {
			if createdAt.IsZero() || container.CreatedAt.IsZero() ||
				container.CreatedAt.Equal(createdAt) {
				owned[container.Name] = container
			}
		}
	}
	return owned
}

// matches reports whether a running container already looks like what is wanted. Only what
// cannot be changed in place is compared, since anything else means replacing it.
func (a *Agent) matches(current proto.Container, want proto.SpecContainer) bool {
	if current.Image != want.Image {
		return false
	}
	if current.State != "running" {
		return false
	}
	// The deployment label changes on every deploy, which is what makes a new one replace the
	// container rather than leaving the old one serving.
	if current.Labels[proto.LabelDeployment] != want.Labels[proto.LabelDeployment] {
		return false
	}

	wanted := make([]int, 0, len(want.Ports))
	for _, mapping := range want.Ports {
		wanted = append(wanted, mapping.HostPort)
	}
	slices.Sort(wanted)

	published := slices.Clone(current.Ports)
	slices.Sort(published)
	return slices.Equal(published, wanted)
}

// createContainer starts a container as described. Arguments are passed separately rather than
// through a shell, so a name or value cannot run anything.
func (a *Agent) createContainer(ctx context.Context, want proto.SpecContainer) error {
	args := []string{"run", "--detach", "--name", want.Name}

	for key, value := range want.Labels {
		args = append(args, "--label", key+"="+value)
	}
	for key, value := range want.Env {
		args = append(args, "--env", key+"="+value)
	}
	for _, mapping := range want.Ports {
		protocol := mapping.Protocol
		if protocol == "" {
			protocol = "tcp"
		}
		args = append(args, "--publish",
			fmt.Sprintf("%d:%d/%s", mapping.HostPort, mapping.ContainerPort, protocol))
	}
	for _, mount := range want.Mounts {
		spec := mount.Source + ":" + mount.Target
		if mount.ReadOnly {
			spec += ":ro"
		}
		args = append(args, "--volume", spec)
	}
	if want.Network != "" {
		args = append(args, "--network", want.Network)
	}

	// Always set. One customer's runaway process must not take down everything else on their
	// server, or the platform gets blamed for their bug.
	if want.MemoryLimitBytes > 0 {
		args = append(args, "--memory", strconv.FormatInt(want.MemoryLimitBytes, 10))
	}
	if want.CPUShares > 0 {
		args = append(args, "--cpu-shares", strconv.FormatInt(want.CPUShares, 10))
	}

	policy := want.RestartPolicy
	if policy == "" {
		policy = "unless-stopped"
	}
	args = append(args, "--restart", policy, want.Image)
	args = append(args, want.Command...)

	if _, err := a.docker(ctx, 3*time.Minute, args...); err != nil {
		return err
	}
	return nil
}

// removeContainer stops and deletes a container we own.
func (a *Agent) removeContainer(ctx context.Context, name string) error {
	_, err := a.docker(ctx, time.Minute, "rm", "--force", "--volumes=false", name)
	return err
}

// ensureVolume creates a volume if it is missing. Volumes are never removed automatically,
// because one holds the only copy of someone's data.
func (a *Agent) ensureVolume(ctx context.Context, want proto.SpecVolume) error {
	args := []string{"volume", "create"}
	for key, value := range want.Labels {
		args = append(args, "--label", key+"="+value)
	}
	args = append(args, want.Name)

	_, err := a.docker(ctx, 30*time.Second, args...)
	return err
}

// docker runs the daemon client with separate arguments, never through a shell.
func (a *Agent) docker(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "docker", args...)
	if a.cfg.DockerHost != "" {
		cmd.Env = append(cmd.Environ(), "DOCKER_HOST="+a.cfg.DockerHost)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, lastLine(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// storedSpec is what the agent keeps on disk, so it can carry on when the control plane cannot
// be reached and pick up again after a reboot.
type storedSpec struct {
	Signed  proto.SignedSpec `json:"signed"`
	SavedAt time.Time        `json:"savedAt"`
}

// saveSpec records the specification as it arrived, signature included, so what is reloaded is
// checked again rather than trusted because it came from our own disk.
func (a *Agent) saveSpec(signed proto.SignedSpec) error {
	encoded, err := json.Marshal(storedSpec{Signed: signed, SavedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	path := filepath.Join(a.cfg.StateDir, specFile)

	// Written beside and moved into place, so an interrupted write cannot leave a half-file
	// that fails to load after a reboot.
	temp := path + ".partial"
	if err := os.WriteFile(temp, encoded, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

// loadSpec reads what was last applied and verifies it again.
func (a *Agent) loadSpec() (*proto.Spec, *proto.SignedSpec, error) {
	data, err := os.ReadFile(filepath.Join(a.cfg.StateDir, specFile))
	if err != nil {
		return nil, nil, err
	}

	var stored storedSpec
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, nil, err
	}
	if a.verifier == nil {
		return nil, nil, fmt.Errorf("agent: no public key to check the stored specification")
	}

	spec, err := a.verifier.Verify(&stored.Signed)
	if err != nil {
		return nil, nil, err
	}
	return spec, &stored.Signed, nil
}

// reconcileLoop keeps the machine matching the specification. It runs on a timer as well as on
// a push, so a container that stopped, or a reboot, is corrected without anyone asking.
func (a *Agent) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.ReconcileEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.reconcileOnce(ctx, false)
		}
	}
}

// reconcileOnce applies what the agent currently holds. report says whether to tell the control
// plane, which is worth doing after a change but not on every quiet pass.
func (a *Agent) reconcileOnce(ctx context.Context, report bool) {
	spec := a.currentSpec()
	if spec == nil {
		return
	}

	applied := a.apply(ctx, spec)
	if len(applied.Created) > 0 || len(applied.Removed) > 0 || len(applied.Failures) > 0 {
		slog.Info("brought this server in line with its specification",
			"created", applied.Created, "removed", applied.Removed, "failures", len(applied.Failures))
		report = true
	}

	if report {
		if encoded, err := proto.Encode(proto.TypeApplied, applied); err == nil {
			_ = a.sendRaw(ctx, encoded)
		}
	}
}

func lastLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// acceptSpec verifies an incoming specification and applies it. An unverified one is refused
// outright: reaching the connection is not the same as being allowed to run containers on a
// customer's machine.
func (a *Agent) acceptSpec(ctx context.Context, envelope *proto.Envelope) {
	var signed proto.SignedSpec
	if err := envelope.Into(&signed); err != nil {
		slog.Warn("unreadable specification", "error", err)
		return
	}
	if a.verifier == nil {
		slog.Error("refused a specification because this server has no public key to check it with")
		return
	}

	spec, err := a.verifier.Verify(&signed)
	if err != nil {
		slog.Error("refused a specification that does not verify", "error", err)
		return
	}

	a.setSpec(spec)
	if err := a.saveSpec(signed); err != nil {
		// Worth applying anyway; the cost is only that a reboot waits for the control plane.
		slog.Warn("could not save the specification", "error", err)
	}

	slog.Info("received a new specification", "version", spec.Version,
		"containers", len(spec.Containers))
	a.reconcileOnce(ctx, true)
}
