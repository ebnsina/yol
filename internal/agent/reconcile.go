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
	"sync"
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

// defaultDrain is how long the version being replaced is left running after traffic has moved off
// it. Long enough for a request already being served to finish, short enough that a deploy still
// feels like one thing happening rather than two.
const defaultDrain = 10 * time.Second

// apply brings the machine in line with the specification and reports what changed.
//
// The order is the whole of what makes a deploy free of dropped requests, so it is stated once
// here and followed exactly: start the new version, wait for it to answer, move traffic, and only
// then take the old one away. Nothing is removed while its replacement is unproven.
func (a *Agent) apply(ctx context.Context, spec *proto.Spec) proto.Applied {
	applied := proto.Applied{SpecVersion: spec.Version, At: time.Now().UTC()}

	if a.mode != proto.ModeManaged {
		applied.Refused = "This server is being watched only, so nothing on it was changed."
		return applied
	}

	plan := planRollout(a.managedContainers(ctx, spec), spec, a.abandonedContainers())
	applied.Unchanged = plan.unchanged

	if err := a.ensureNetwork(ctx); err != nil {
		slog.Error("could not create the private network", "error", err)
	}
	for _, volume := range spec.Volumes {
		if err := a.ensureVolume(ctx, volume); err != nil {
			slog.Error("could not create volume", "volume", volume.Name, "error", err)
		}
	}

	started := a.start(ctx, plan, &applied)
	unproven := a.gate(ctx, started, &applied)

	// Traffic is moved only to containers that answered. A route pointing at one that did not is
	// left where it was, which is what keeps the version already serving in front of people.
	a.syncRouter(ctx, withoutRoutesTo(spec, unproven))

	// Nothing at all is removed on a pass where something failed to come up. The old version is
	// the only working thing left, and it is worth more than a tidy machine.
	if len(unproven) > 0 {
		slog.Warn("kept the previous version serving because the new one did not answer",
			"containers", len(unproven))
		return applied
	}
	a.retire(ctx, plan, len(started) > 0, &applied)
	return applied
}

// rolloutPlan is what one pass will do, worked out before anything changes so that the order can
// be read and reasoned about in one place rather than inferred from the loops that carry it out.
type rolloutPlan struct {
	// Wanted and not running under that name at all. A deploy produces one of these, since a
	// container is named for its deployment and so never collides with the one already serving.
	create []proto.SpecContainer
	// Running under the wanted name but not as wanted. Docker cannot change a running container's
	// shape, so this one is interrupted: there is nothing to fall back to while it restarts.
	replace []proto.SpecContainer
	// Ours, and no longer wanted.
	remove    []string
	unchanged int
}

func planRollout(actual map[string]proto.Container, spec *proto.Spec, abandoned map[string]bool) rolloutPlan {
	var plan rolloutPlan

	desired := make(map[string]bool, len(spec.Containers))
	for _, want := range spec.Containers {
		desired[want.Name] = true

		// A version already given up on is not started again. A container is named for its
		// deployment, so this is that one version and nothing else, and the control plane drops it
		// from the desired state as soon as it hears. Until then, starting it would begin another
		// wait for something that has already been shown not to answer.
		if abandoned[want.Name] {
			continue
		}

		current, exists := actual[want.Name]
		switch {
		case !exists:
			plan.create = append(plan.create, want)
		case matches(current, want):
			plan.unchanged++
		default:
			plan.replace = append(plan.replace, want)
		}
	}

	for name := range actual {
		if !desired[name] {
			plan.remove = append(plan.remove, name)
		}
	}
	slices.Sort(plan.remove)
	return plan
}

// start brings up what is wanted and returns the containers now running that were not before,
// which are the ones a health check applies to.
func (a *Agent) start(ctx context.Context, plan rolloutPlan, applied *proto.Applied) []proto.SpecContainer {
	var started []proto.SpecContainer

	for _, want := range plan.replace {
		if err := a.removeContainer(ctx, want.Name); err != nil {
			applied.Failures = append(applied.Failures, proto.ApplyFailure{
				Container: want.Name, Deployment: want.Labels[proto.LabelDeployment],
				Reason: "could not be replaced",
			})
			continue
		}
		if a.create(ctx, want, applied) {
			started = append(started, want)
		}
	}

	for _, want := range plan.create {
		if a.create(ctx, want, applied) {
			started = append(started, want)
		}
	}
	return started
}

func (a *Agent) create(ctx context.Context, want proto.SpecContainer, applied *proto.Applied) bool {
	if err := a.createContainer(ctx, want); err != nil {
		applied.Failures = append(applied.Failures, proto.ApplyFailure{
			Container: want.Name, Deployment: want.Labels[proto.LabelDeployment],
			Reason: "could not be started",
		})
		slog.Error("could not start container", "container", want.Name, "error", err)
		return false
	}
	applied.Created = append(applied.Created, want.Name)
	return true
}

// gate waits for each newly started container that declares a check, and returns those that never
// answered. One that was started under a new name is taken away again, since the version it was
// meant to replace is still running and still serving.
func (a *Agent) gate(ctx context.Context, started []proto.SpecContainer, applied *proto.Applied) map[string]bool {
	unproven := make(map[string]bool)

	var (
		wait     sync.WaitGroup
		mu       sync.Mutex
		outcomes []proto.Rollout
	)
	for _, want := range started {
		if want.HealthCheck == nil {
			continue
		}

		wait.Add(1)
		go func(want proto.SpecContainer) {
			defer wait.Done()

			outcome := proto.Rollout{Container: want.Name, Deployment: want.Labels[proto.LabelDeployment]}
			err := a.awaitHealthy(ctx, want.Name, want.HealthCheck)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				slog.Error("a new container never started serving", "container", want.Name, "error", err)
				unproven[want.Name] = true
				outcome.Reason = "This version started but never began answering, so it was not put in front of anyone."

				// Remembered, so the next pass does not start it again and wait all over.
				a.abandon(want.Name)
				if err := a.removeContainer(ctx, want.Name); err != nil {
					slog.Error("could not remove a container that never served", "container", want.Name, "error", err)
				}
			} else {
				outcome.Healthy = true
			}
			outcomes = append(outcomes, outcome)
		}(want)
	}
	wait.Wait()

	applied.Rollouts = append(applied.Rollouts, outcomes...)
	return unproven
}

// retire takes away what is no longer wanted, once its replacement is answering. Waiting first
// gives requests already in flight time to finish rather than being cut off mid-response.
func (a *Agent) retire(ctx context.Context, plan rolloutPlan, drain bool, applied *proto.Applied) {
	if len(plan.remove) == 0 {
		return
	}

	if drain {
		select {
		case <-ctx.Done():
			return
		case <-time.After(a.drain):
		}
	}

	for _, name := range plan.remove {
		if err := a.removeContainer(ctx, name); err != nil {
			applied.Failures = append(applied.Failures, proto.ApplyFailure{
				Container: name, Reason: "could not be removed",
			})
			slog.Error("could not remove container", "container", name, "error", err)
			continue
		}
		applied.Removed = append(applied.Removed, name)
	}
}

// withoutRoutesTo copies the specification with any route to an unproven container dropped, so
// configuring the router cannot move traffic onto something that never answered.
func withoutRoutesTo(spec *proto.Spec, unproven map[string]bool) *proto.Spec {
	if len(unproven) == 0 {
		return spec
	}

	trimmed := *spec
	trimmed.Routes = nil
	for _, route := range spec.Routes {
		if !unproven[route.Container] {
			trimmed.Routes = append(trimmed.Routes, route)
		}
	}
	return &trimmed
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
func matches(current proto.Container, want proto.SpecContainer) bool {
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
		published := fmt.Sprintf("%d:%d/%s", mapping.HostPort, mapping.ContainerPort, protocol)
		if mapping.HostIP != "" {
			published = mapping.HostIP + ":" + published
		}
		args = append(args, "--publish", published)
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

// ensureNetwork creates the private network managed containers share. Creating one that exists
// is an error from Docker but not from our point of view, so the outcome is ignored.
func (a *Agent) ensureNetwork(ctx context.Context) error {
	if out := a.mustOutput(ctx, "network", "ls", "--filter", "name=^"+proto.Network+"$", "--format", "{{.Name}}"); out == proto.Network {
		return nil
	}
	_, err := a.docker(ctx, 30*time.Second, "network", "create", proto.Network)
	return err
}

// mustOutput runs a command and returns its output, empty when it fails.
func (a *Agent) mustOutput(ctx context.Context, args ...string) string {
	out, err := a.docker(ctx, 20*time.Second, args...)
	if err != nil {
		return ""
	}
	return out
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

// lastLine is what to report when a command fails. Docker often ends with a suggestion to read its
// help, which explains nothing, so the line that actually says what went wrong is preferred.
func lastLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "Run '") || strings.HasPrefix(line, "See '") {
			continue
		}
		return line
	}
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
