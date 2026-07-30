package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ebnsina/yol/internal/proto"
)

// Images are built on the machine that will run them. Nothing about the customer's code reaches
// us, and no build fleet is run on their behalf, so a deploy costs them only the server they
// already pay for.
//
// The cost of that choice is a build sharing a machine with live traffic, which is why every
// build runs inside a builder of its own with memory and processor limits. Passing limits to
// `docker build` does nothing at all under BuildKit; only a builder created with the container
// driver applies them, so that is the one path builds take here.

const (
	// The builder builds run inside. One per machine, recreated when the limits it was made with
	// no longer match what was asked for.
	buildBuilder      = "yol-builder"
	buildBuilderInner = "buildx_buildkit_" + buildBuilder + "0"

	defaultBuildTimeout = 20 * time.Minute
	// A build that infers how to run an app installs a package manager to do it, which does not
	// fit in less: at 128MB it is killed part way through resolving packages.
	minBuildMemoryBytes = 1 << 30

	// Images kept per service, so rolling back is running one already on the machine rather than
	// building again.
	imagesKeptPerService = 5

	// The source arrives as an archive of one commit. Bounded because it is expanded onto a
	// customer's disk.
	maxSourceBytes = 1 << 30
)

// errBuildNotStarted means the build never began, which is a different thing to tell a user than a
// build that ran and failed.
var errBuildNotStarted = errors.New("agent: the build could not be started")

// acceptBuild reads a build request only once its signature holds. A build request hands over a
// credential and runs code on the machine, so an unsigned one is refused the same way a
// specification is: control of the connection alone must not be enough to start a build.
func (a *Agent) acceptBuild(envelope *proto.Envelope) (proto.BuildRequest, bool) {
	var signed proto.SignedMessage
	if err := envelope.Into(&signed); err != nil {
		slog.Warn("unreadable build request", "error", err)
		return proto.BuildRequest{}, false
	}
	if a.verifier == nil {
		slog.Error("refused a build because this server has no public key to check it with")
		return proto.BuildRequest{}, false
	}

	var req proto.BuildRequest
	if err := a.verifier.VerifyMessage(&signed, &req); err != nil {
		slog.Error("refused a build request that does not verify", "error", err)
		return proto.BuildRequest{}, false
	}
	return req, true
}

// startBuild builds in the background, so heartbeats and log streams carry on meanwhile.
func (a *Agent) startBuild(ctx context.Context, req proto.BuildRequest) {
	go func() {
		result := a.runBuild(ctx, req)
		result.DeploymentID = req.DeploymentID
		result.At = time.Now().UTC()

		encoded, err := proto.Encode(proto.TypeBuildResult, result)
		if err != nil {
			return
		}
		if err := a.sendRaw(ctx, encoded); err != nil {
			slog.Warn("could not report how a build ended", "error", err)
		}
	}()
}

// runBuild produces the image, reporting the outcome in words a reader can act on. Every failure
// returns rather than logging and continuing: a half-built image must never be reported as ready.
func (a *Agent) runBuild(ctx context.Context, req proto.BuildRequest) proto.BuildResult {
	if req.DeploymentID == "" || req.SourceURL == "" || req.ImageRef == "" {
		return failed("This deployment was incomplete, so there was nothing to build.")
	}

	timeout := defaultBuildTimeout
	if req.TimeoutSec > 0 {
		timeout = time.Duration(req.TimeoutSec) * time.Second
	}
	buildCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	workspace := filepath.Join(a.cfg.StateDir, "builds", req.DeploymentID)
	if err := os.RemoveAll(workspace); err != nil {
		return failed("We could not prepare a place to build on this server.")
	}
	// Source code, and on a shared machine that is nobody else's business.
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return failed("We could not prepare a place to build on this server.")
	}
	defer os.RemoveAll(workspace)

	a.sayInBuild(buildCtx, req.DeploymentID, "Fetching the code at "+shortCommit(req.CommitSHA)+".")
	if err := a.fetchSource(buildCtx, req, workspace); err != nil {
		slog.Error("could not fetch the source for a build", "deployment", req.DeploymentID, "error", err)
		return failed("We could not fetch your code. Check that the repository is still connected.")
	}

	buildDir, err := buildContextDir(workspace, req.WorkDir)
	if err != nil {
		return failed("The directory to build from is not inside the repository.")
	}

	dockerfile, err := dockerfileIn(buildDir, req.DockerfilePath)
	if err != nil {
		return failed("The Dockerfile named for this app is not in the repository.")
	}

	builder, err := a.ensureBuilder(buildCtx, req)
	if err != nil {
		slog.Error("could not prepare a builder", "error", err)
		return failed("We could not start a build on this server. Its Docker installation may be too old.")
	}

	kind := proto.BuilderNixpacks
	if dockerfile != "" {
		kind = proto.BuilderDockerfile
	}
	a.sayInBuild(buildCtx, req.DeploymentID, describeBuilder(kind))

	if err := a.runBuildCommand(buildCtx, req, builder, buildDir, dockerfile, kind); err != nil {
		if errors.Is(buildCtx.Err(), context.DeadlineExceeded) {
			return failed("The build ran longer than allowed and was stopped.")
		}
		// A build that could not be started at all is not a build that failed, and telling
		// somebody to read output that was never produced sends them looking for nothing.
		if errors.Is(err, errBuildNotStarted) {
			slog.Error("could not start a build", "deployment", req.DeploymentID, "error", err)
			return failed("We could not start a build on this server.")
		}
		slog.Warn("a build failed", "deployment", req.DeploymentID, "error", err)
		return proto.BuildResult{
			Builder: kind,
			Reason:  "The build did not finish. The output above says where it stopped.",
		}
	}

	a.pruneImages(ctx, req.ServiceID)
	return proto.BuildResult{Succeeded: true, Builder: kind, ImageRef: req.ImageRef}
}

func failed(reason string) proto.BuildResult {
	return proto.BuildResult{Reason: reason}
}

func describeBuilder(kind proto.Builder) string {
	if kind == proto.BuilderDockerfile {
		return "Building from the Dockerfile in your repository."
	}
	return "No Dockerfile found, so we are working out how to build this app."
}

// buildContextDir resolves which directory inside the repository is being built, refusing one
// that points outside it.
func buildContextDir(workspace, workDir string) (string, error) {
	if workDir == "" {
		return workspace, nil
	}

	if climbs(workDir) {
		return "", fmt.Errorf("agent: %s points outside the repository", workDir)
	}

	resolved := filepath.Join(workspace, filepath.Clean("/"+workDir))
	if resolved != workspace && !strings.HasPrefix(resolved, workspace+string(os.PathSeparator)) {
		return "", fmt.Errorf("agent: %s is outside the repository", workDir)
	}
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return "", fmt.Errorf("agent: %s is not a directory in the repository", workDir)
	}
	return resolved, nil
}

// dockerfileIn returns the Dockerfile to build with, or empty when there is none. A Dockerfile
// the user wrote always wins, since they know things about their app that we cannot infer.
func dockerfileIn(dir, named string) (string, error) {
	if named != "" {
		if climbs(named) {
			return "", fmt.Errorf("agent: %s points outside the build directory", named)
		}
		path := filepath.Join(dir, filepath.Clean("/"+named))
		if !strings.HasPrefix(path, dir+string(os.PathSeparator)) {
			return "", fmt.Errorf("agent: %s is outside the build directory", named)
		}
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("agent: %s is not in the repository", named)
		}
		return path, nil
	}

	path := filepath.Join(dir, "Dockerfile")
	if _, err := os.Stat(path); err != nil {
		return "", nil
	}
	return path, nil
}

// ensureBuilder returns a builder that applies the limits asked for, creating it or replacing one
// made with different limits. Named rather than per-build so its cache survives between deploys,
// which is most of what makes a second build fast.
func (a *Agent) ensureBuilder(ctx context.Context, req proto.BuildRequest) (string, error) {
	memory := max(req.MemoryLimitBytes, minBuildMemoryBytes)
	quota := 0
	if req.CPUPercent > 0 {
		quota = req.CPUPercent * 1000 // a period of 100ms, so 50% is a quota of 50000
	}

	if current, err := a.builderLimits(ctx); err == nil && current == fmt.Sprintf("%d %d", memory, quota) {
		return buildBuilder, nil
	}
	// Limits are fixed when a builder is made, so different ones mean a new builder. Removed
	// unconditionally rather than only when it was readable, since one registered without a
	// container behind it cannot be created over.
	_, _ = a.docker(ctx, 30*time.Second, "buildx", "rm", buildBuilder)

	// Started as it is created, so the limits are in force before the first build rather than
	// applied whenever it happens to boot.
	args := []string{"buildx", "create", "--name", buildBuilder, "--driver", "docker-container",
		"--bootstrap", "--driver-opt", "memory=" + strconv.FormatInt(memory, 10)}
	if quota > 0 {
		args = append(args, "--driver-opt", "cpu-period=100000",
			"--driver-opt", "cpu-quota="+strconv.Itoa(quota))
	}

	if _, err := a.docker(ctx, time.Minute, args...); err != nil {
		return "", err
	}
	return buildBuilder, nil
}

// builderLimits reports the limits the existing builder carries, so it is reused only when they
// still match. Read from the builder itself rather than remembered, because it outlives the agent.
func (a *Agent) builderLimits(ctx context.Context) (string, error) {
	return a.docker(ctx, 20*time.Second, "inspect", "-f",
		"{{.HostConfig.Memory}} {{.HostConfig.CpuQuota}}", buildBuilderInner)
}

// runBuildCommand builds the image, sending output on as it appears.
func (a *Agent) runBuildCommand(
	ctx context.Context,
	req proto.BuildRequest,
	builder, dir, dockerfile string,
	kind proto.Builder,
) error {
	var cmd *exec.Cmd
	if kind == proto.BuilderDockerfile {
		args := []string{"buildx", "build", dir, "--file", dockerfile, "--tag", req.ImageRef, "--load"}
		for _, label := range sortedPairs(req.Labels) {
			args = append(args, "--label", label)
		}
		for _, variable := range sortedPairs(req.BuildEnv) {
			args = append(args, "--build-arg", variable)
		}
		cmd = a.dockerCmd(ctx, args...)
	} else {
		args := []string{"build", dir, "--name", req.ImageRef,
			// Without this the image is built and then discarded rather than kept on the machine.
			"--docker-output", "type=docker",
			// Shared between deploys of the same app, so a second build reuses the first's work.
			"--cache-key", req.ServiceID}
		for _, label := range sortedPairs(req.Labels) {
			args = append(args, "--label", label)
		}
		for _, variable := range sortedPairs(req.BuildEnv) {
			args = append(args, "--env", variable)
		}

		nixpacks, err := a.ensureNixpacks(ctx)
		if err != nil {
			return fmt.Errorf("%w: %w", errBuildNotStarted, err)
		}
		cmd = exec.CommandContext(ctx, nixpacks, args...)
		cmd.Env = a.withDockerHost(cmd.Environ())
	}

	// Both paths build inside the limited builder. Selected per build rather than by changing
	// what this machine builds with by default, which is the customer's setting and not ours.
	cmd.Env = append(cmd.Env, "BUILDX_BUILDER="+builder)
	cmd.Dir = dir

	return a.streamBuildOutput(ctx, req.DeploymentID, cmd)
}

// streamBuildOutput runs a build and forwards everything it prints, so a build is watched rather
// than waited on.
func (a *Agent) streamBuildOutput(ctx context.Context, deploymentID string, cmd *exec.Cmd) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: %w", errBuildNotStarted, err)
	}

	lines := make(chan proto.LogLine, 256)
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); scanInto(stdout, "stdout", false, lines) }()
	go func() { defer readers.Done(); scanInto(stderr, "stderr", false, lines) }()
	go func() { readers.Wait(); close(lines) }()

	// The last line is kept, because a build whose output could not be sent would otherwise fail
	// with nothing anywhere saying why.
	var last string
	forwardLines(ctx, lines, func(batch []proto.LogLine) {
		last = batch[len(batch)-1].Text
		a.sendBuildOutput(ctx, proto.BuildOutput{
			DeploymentID: deploymentID,
			At:           time.Now().UTC(),
			Lines:        batch,
		})
	})

	if err := cmd.Wait(); err != nil {
		slog.Error("a build did not finish", "deployment", deploymentID, "error", err, "lastLine", last)
		return err
	}
	return nil
}

// sayInBuild puts a line of ours among the build's own output, so the stages a user waits through
// are named rather than silent.
func (a *Agent) sayInBuild(ctx context.Context, deploymentID, text string) {
	a.sendBuildOutput(ctx, proto.BuildOutput{
		DeploymentID: deploymentID,
		At:           time.Now().UTC(),
		Lines:        []proto.LogLine{{At: time.Now().UTC(), Stream: "yol", Text: text}},
	})
}

func (a *Agent) sendBuildOutput(ctx context.Context, output proto.BuildOutput) {
	encoded, err := proto.Encode(proto.TypeBuildOutput, output)
	if err != nil {
		return
	}
	if err := a.sendRaw(ctx, encoded); err != nil {
		slog.Debug("could not send build output", "error", err)
	}
}

// pruneImages keeps the last few images of one service and removes the rest, so rolling back is
// instant without a machine filling up with every version ever deployed.
func (a *Agent) pruneImages(ctx context.Context, serviceID string) {
	if serviceID == "" {
		return
	}

	out, err := a.docker(ctx, 20*time.Second, "images",
		"--filter", "label="+proto.LabelService+"="+serviceID, "--format", "{{.ID}}", "--no-trunc")
	if err != nil {
		return
	}

	ids := uniqueLines(out)
	if len(ids) <= imagesKeptPerService {
		return
	}
	// Newest first, so what is dropped is the oldest. An image still in use refuses to be
	// removed, which is the behaviour wanted rather than something to work around.
	for _, id := range ids[imagesKeptPerService:] {
		_, _ = a.docker(ctx, 30*time.Second, "rmi", id)
	}
}

func uniqueLines(out string) []string {
	seen := make(map[string]bool)
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		lines = append(lines, line)
	}
	return lines
}

// sortedPairs renders a map as key=value in a stable order, so the same build produces the same
// command and its cache is reused.
func sortedPairs(values map[string]string) []string {
	pairs := make([]string, 0, len(values))
	for key, value := range values {
		pairs = append(pairs, key+"="+value)
	}
	sort.Strings(pairs)
	return pairs
}

// dockerCmd prepares a docker command whose output is read as it appears, which the helper used
// elsewhere cannot do because it waits for the command to finish.
func (a *Agent) dockerCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = a.withDockerHost(cmd.Environ())
	return cmd
}

func (a *Agent) withDockerHost(env []string) []string {
	if a.cfg.DockerHost == "" {
		return env
	}
	return append(env, "DOCKER_HOST="+a.cfg.DockerHost)
}

func shortCommit(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	if sha == "" {
		return "the requested commit"
	}
	return sha
}
