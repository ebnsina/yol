package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/proto"
	"github.com/ebnsina/yol/internal/ssh"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Where the agent and its files live on a customer's server.
const (
	agentBinaryPath = "/usr/local/bin/yol-agent"
	agentStateDir   = "/var/lib/yol"
	agentUnitPath   = "/etc/systemd/system/yol-agent.service"
)

// BootstrapArgs identifies the server to set up. Identifiers only; the worker loads what it
// needs, so no credential is ever written into job arguments.
type BootstrapArgs struct {
	ServerID uuid.UUID `json:"serverId"`
	OrgID    uuid.UUID `json:"orgId"`
}

// Kind names the job.
func (BootstrapArgs) Kind() string { return "server.bootstrap" }

// Bootstrapper installs the agent on a server. This is the first thing that changes a
// customer's machine, and it only runs once they have seen what we found and said to go ahead.
type Bootstrapper struct {
	svc          *Service
	hub          *Hub
	signer       *proto.SigningKey
	agentBinDir  string
	controlPlane string
}

// NewBootstrapper builds the setup worker.
func NewBootstrapper(svc *Service, hub *Hub, signer *proto.SigningKey, agentBinDir, controlPlaneURL string) *Bootstrapper {
	return &Bootstrapper{
		svc: svc, hub: hub, signer: signer,
		agentBinDir: agentBinDir, controlPlane: controlPlaneURL,
	}
}

// How long to wait for the agent to appear before saying something is wrong.
const agentArrivalWindow = 90 * time.Second

// Run installs the agent, reporting each step in the words the person waiting reads.
func (b *Bootstrapper) Run(ctx context.Context, args BootstrapArgs) error {
	surveyArgs := SurveyArgs{ServerID: args.ServerID, OrgID: args.OrgID}

	target, cred, err := b.svc.loadTargetFor(ctx, surveyArgs)
	if err != nil {
		b.fail(ctx, args, "connect", err)
		return nil
	}

	b.setStatus(ctx, args, StatusInstalling)
	b.event(ctx, args, "install", "Connecting to set this server up.", "info")

	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client, _, err := ssh.Dial(dialCtx, target, cred)
	if err != nil {
		b.fail(ctx, args, "connect", err)
		return nil
	}
	defer client.Close()

	if err := b.install(ctx, args, client); err != nil {
		b.fail(ctx, args, "install", err)
		return nil
	}

	// The password was only ever needed to get here, and keeping other people's server
	// passwords is the worst thing to be breached with.
	b.forgetPassword(ctx, args)

	b.event(ctx, args, "install",
		"The agent is installed and starting. This server will appear as connected shortly.", "info")

	b.awaitAgent(ctx, args)
	return nil
}

// awaitAgent waits for the agent to dial in. Without this the interface would say a server is
// being set up forever, with no hint that the agent cannot reach us.
func (b *Bootstrapper) awaitAgent(ctx context.Context, args BootstrapArgs) {
	deadline := time.Now().Add(agentArrivalWindow)

	for time.Now().Before(deadline) {
		if _, connected := b.hub.Get(args.ServerID); connected {
			return // the connection itself records the server as online
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}

	message := "The agent was installed but has not connected. Check that this server can reach " +
		b.controlPlane + " over the internet."
	b.event(ctx, args, "install", message, "error")

	_ = b.svc.pool.InOrg(ctx, args.OrgID, func(tx pgx.Tx) error {
		return sqlc.New(tx).UpdateServerStatus(ctx, sqlc.UpdateServerStatusParams{
			ID: args.ServerID, Status: sqlc.ServerStatusFailed, FailureReason: &message,
		})
	})
}

// install does the work, in an order that leaves the machine usable if it stops partway.
func (b *Bootstrapper) install(ctx context.Context, args BootstrapArgs, client *ssh.Client) error {
	arch, err := b.architecture(ctx, client)
	if err != nil {
		return err
	}

	if err := b.ensureDocker(ctx, args, client); err != nil {
		return err
	}

	b.event(ctx, args, "install", "Copying the agent onto this server.", "info")
	if err := b.uploadAgent(ctx, client, arch); err != nil {
		return err
	}

	token, err := b.svc.IssueEnrollmentToken(ctx, args.OrgID, args.ServerID)
	if err != nil {
		return err
	}

	if _, err := client.Run(ctx, fmt.Sprintf("mkdir -p %q && chmod 700 %q", agentStateDir, agentStateDir)); err != nil {
		return fmt.Errorf("prepare %s: %w", agentStateDir, err)
	}
	// Readable by its owner alone, and consumed by the agent the first time it starts.
	if err := client.WriteText(ctx, filepath.Join(agentStateDir, "enrollment"), "600", token); err != nil {
		return err
	}

	// The public half of the signing key, so the agent can check that an instruction to change
	// this server really came from us. Not a secret, so it is world readable.
	if err := client.WriteText(ctx, filepath.Join(agentStateDir, "spec.pub"), "644",
		b.signer.PublicKey()); err != nil {
		return err
	}

	b.event(ctx, args, "install", "Setting the agent to start automatically.", "info")
	if err := client.WriteText(ctx, agentUnitPath, "644", b.unitFile()); err != nil {
		return err
	}

	result, err := client.Run(ctx, "systemctl daemon-reload && systemctl enable --now yol-agent")
	if err != nil {
		return err
	}
	if !result.Ok() {
		return fmt.Errorf("start the agent: %s", strings.TrimSpace(result.Stderr))
	}
	return nil
}

// architecture picks which build of the agent this machine needs.
func (b *Bootstrapper) architecture(ctx context.Context, client *ssh.Client) (string, error) {
	switch machine := client.Output(ctx, "uname -m"); machine {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("this server's processor type (%s) is not supported yet", machine)
	}
}

// ensureDocker installs Docker when it is missing, and leaves everything alone when it is
// already there. A machine already running containers must not have its engine replaced.
func (b *Bootstrapper) ensureDocker(ctx context.Context, args BootstrapArgs, client *ssh.Client) error {
	if version := client.Output(ctx, "docker version --format '{{.Server.Version}}' 2>/dev/null"); version != "" {
		b.event(ctx, args, "install",
			fmt.Sprintf("Docker %s is already installed here, so it has been left as it is.", version), "info")
		return nil
	}

	b.event(ctx, args, "install",
		"Installing Docker, which this server does not have yet. This can take a few minutes.", "info")

	// The official installer, which handles the differences between Ubuntu and Debian
	// versions rather than us guessing at package names.
	result, err := client.Run(ctx,
		"curl -fsSL https://get.docker.com -o /tmp/get-docker.sh && sh /tmp/get-docker.sh && rm -f /tmp/get-docker.sh")
	if err != nil {
		return err
	}
	if !result.Ok() {
		return fmt.Errorf("install docker: %s", lastLine(result.Stderr))
	}

	if out := client.Output(ctx, "systemctl enable --now docker && docker version --format '{{.Server.Version}}'"); out == "" {
		return fmt.Errorf("docker was installed but is not running")
	}
	b.event(ctx, args, "install", "Docker is installed and running.", "info")
	return nil
}

// uploadAgent copies the binary for this machine's processor over the existing connection,
// so the server does not need to fetch it from anywhere.
func (b *Bootstrapper) uploadAgent(ctx context.Context, client *ssh.Client, arch string) error {
	path := filepath.Join(b.agentBinDir, "yol-agent-linux-"+arch)

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("the agent for %s is not available on the control plane", arch)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("read the agent binary: %w", err)
	}
	return client.Upload(ctx, agentBinaryPath, "755", info.Size(), file)
}

// unitFile is the service definition, so the agent starts on boot and is restarted if it
// stops. Values are fixed here rather than templated from anything a user controls.
func (b *Bootstrapper) unitFile() string {
	return fmt.Sprintf(`[Unit]
Description=yol agent
Documentation=https://github.com/ebnsina/yol
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
Environment=YOL_CONTROL_PLANE_URL=%s
Environment=YOL_STATE_DIR=%s
Environment=YOL_DOCKER_HOST=unix:///var/run/docker.sock
Environment=YOL_RECONCILE_EVERY=10s
ExecStart=%s
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`, b.controlPlane, agentStateDir, agentBinaryPath)
}

func (b *Bootstrapper) forgetPassword(ctx context.Context, args BootstrapArgs) {
	_ = b.svc.pool.InOrg(ctx, args.OrgID, func(tx pgx.Tx) error {
		return sqlc.New(tx).ClearServerPassword(ctx, args.ServerID)
	})
}

func (b *Bootstrapper) fail(ctx context.Context, args BootstrapArgs, step string, cause error) {
	slog.Error("server setup failed",
		"serverId", args.ServerID, "orgId", args.OrgID, "step", step, "error", cause)

	message := explainSetup(cause)
	b.event(ctx, args, step, message, "error")

	_ = b.svc.pool.InOrg(ctx, args.OrgID, func(tx pgx.Tx) error {
		return sqlc.New(tx).UpdateServerStatus(ctx, sqlc.UpdateServerStatusParams{
			ID: args.ServerID, Status: sqlc.ServerStatusFailed, FailureReason: &message,
		})
	})
}

// explainSetup turns a failure into something the person waiting can act on.
func explainSetup(err error) string {
	switch {
	case err == nil:
		return "Setting up this server did not finish. Please try again."
	case strings.Contains(err.Error(), "processor type"):
		return err.Error() + "."
	case strings.Contains(err.Error(), "install docker"):
		return "We could not install Docker on this server. Check that it can reach the internet, then try again."
	case strings.Contains(err.Error(), "agent for"):
		return "The agent for this server's processor is not available. Please get in touch."
	default:
		return "We could not finish setting up this server. Please try again."
	}
}

func (b *Bootstrapper) event(ctx context.Context, args BootstrapArgs, step, message, level string) {
	_ = b.svc.pool.InOrg(ctx, args.OrgID, func(tx pgx.Tx) error {
		return recordEvent(ctx, sqlc.New(tx), args.OrgID, args.ServerID, step, message, level)
	})
}

func (b *Bootstrapper) setStatus(ctx context.Context, args BootstrapArgs, status Status) {
	_ = b.svc.pool.InOrg(ctx, args.OrgID, func(tx pgx.Tx) error {
		return sqlc.New(tx).UpdateServerStatus(ctx, sqlc.UpdateServerStatusParams{
			ID: args.ServerID, Status: sqlc.ServerStatus(status),
		})
	})
}

// lastLine keeps a failure message short; installer output can run to hundreds of lines.
func lastLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
