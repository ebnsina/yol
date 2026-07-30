// Package agent is the program that runs on a customer's server. It dials out to the control
// plane, so the customer opens no inbound port, and it keeps working from what it has on disk
// when the control plane cannot be reached.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/ebnsina/yol/internal/config"
	"github.com/ebnsina/yol/internal/proto"
)

// Version identifies this build to the control plane.
const Version = "0.1.0"

// Files the agent keeps. The credential is the only secret, and is written readable by its
// owner alone.
const (
	credentialFile = "credential"
	enrollmentFile = "enrollment"
	stateFile      = "state.json"
)

// Reconnect timing. A server that has been unreachable for a while should not hammer the
// control plane, but should still come back quickly once it can.
const (
	minBackoff = 2 * time.Second
	maxBackoff = 2 * time.Minute
)

// Agent is the running program.
type Agent struct {
	cfg   *config.Agent
	token string
	mode  proto.Mode

	collector Collector
}

// Collector reads what is on the machine. An interface so the agent can be exercised without
// a real Docker daemon.
type Collector interface {
	Facts(ctx context.Context) proto.HostFacts
	Usage(ctx context.Context) proto.Usage
	Inventory(ctx context.Context) proto.Inventory
}

// New builds an agent.
func New(cfg *config.Agent, collector Collector) *Agent {
	return &Agent{cfg: cfg, collector: collector, mode: proto.ModeWatch}
}

// Run connects and keeps working until the context ends. It never returns because of a
// network failure: a server losing its connection must reconnect on its own.
func (a *Agent) Run(ctx context.Context) error {
	if err := os.MkdirAll(a.cfg.StateDir, 0o700); err != nil {
		return fmt.Errorf("agent: prepare %s: %w", a.cfg.StateDir, err)
	}

	token, err := a.credential(ctx)
	if err != nil {
		return err
	}
	a.token = token

	backoff := minBackoff
	for {
		err := a.session(ctx)
		if ctx.Err() != nil {
			return nil
		}

		if err != nil && !errors.Is(err, errConnectionClosed) {
			slog.Warn("connection to the control plane ended", "error", err, "retryIn", backoff)
		}

		// Jitter keeps a fleet that lost the control plane together from all returning at once.
		wait := backoff + time.Duration(rand.Int64N(int64(backoff/2)))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}

		backoff = min(backoff*2, maxBackoff)
	}
}

var errConnectionClosed = errors.New("agent: connection closed")

// credential returns the lasting token, enrolling first if this is the machine's first start.
func (a *Agent) credential(ctx context.Context) (string, error) {
	if token, err := a.readFile(credentialFile); err == nil && token != "" {
		return token, nil
	}

	enrollment, err := a.readFile(enrollmentFile)
	if err != nil || enrollment == "" {
		return "", fmt.Errorf("agent: no credential and no enrollment token in %s", a.cfg.StateDir)
	}

	token, err := a.enroll(ctx, enrollment)
	if err != nil {
		return "", err
	}
	if err := a.writeFile(credentialFile, token); err != nil {
		return "", err
	}
	// Consumed, so leaving it on disk would only be a liability.
	_ = os.Remove(filepath.Join(a.cfg.StateDir, enrollmentFile))

	slog.Info("registered with the control plane")
	return token, nil
}

func (a *Agent) readFile(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(a.cfg.StateDir, name))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func (a *Agent) writeFile(name, contents string) error {
	path := filepath.Join(a.cfg.StateDir, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		return fmt.Errorf("agent: write %s: %w", path, err)
	}
	return nil
}

// session runs one connection: introduce, then report until it ends.
func (a *Agent) session(ctx context.Context) error {
	url := strings.TrimSuffix(a.cfg.ControlPlaneURL.String(), "/") + "/v1/agent/connect"
	url = strings.Replace(strings.Replace(url, "https://", "wss://", 1), "http://", "ws://", 1)

	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(dialCtx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + a.token}},
	})
	if err != nil {
		return fmt.Errorf("agent: connect: %w", err)
	}
	defer conn.CloseNow()

	if err := a.introduce(ctx, conn); err != nil {
		return err
	}

	welcome, err := a.awaitWelcome(ctx, conn)
	if err != nil {
		return err
	}
	a.mode = welcome.Mode
	slog.Info("connected to the control plane", "mode", a.mode, "serverId", welcome.ServerID)

	return a.report(ctx, conn, welcome)
}

func (a *Agent) introduce(ctx context.Context, conn *websocket.Conn) error {
	encoded, err := proto.Encode(proto.TypeHello, proto.Hello{
		AgentVersion: Version,
		Capabilities: a.capabilities(),
		Facts:        a.collector.Facts(ctx),
		SpecVersion:  a.specVersion(),
	})
	if err != nil {
		return err
	}

	writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, encoded)
}

// capabilities are what this build can do. The control plane gates behaviour on these rather
// than on the version, so an older agent is simply asked to do less.
func (a *Agent) capabilities() []proto.Capability {
	return []proto.Capability{proto.CapInventory, proto.CapMetrics}
}

func (a *Agent) awaitWelcome(ctx context.Context, conn *websocket.Conn) (*proto.Welcome, error) {
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, data, err := conn.Read(readCtx)
	if err != nil {
		return nil, fmt.Errorf("agent: waiting to be greeted: %w", err)
	}
	envelope, err := proto.Decode(data)
	if err != nil {
		return nil, err
	}
	if envelope.Type != proto.TypeWelcome {
		return nil, fmt.Errorf("agent: expected a greeting, got %s", envelope.Type)
	}

	var welcome proto.Welcome
	if err := envelope.Into(&welcome); err != nil {
		return nil, err
	}
	return &welcome, nil
}

// report sends heartbeats and inventory until the connection ends.
func (a *Agent) report(ctx context.Context, conn *websocket.Conn, welcome *proto.Welcome) error {
	heartbeat := time.NewTicker(interval(welcome.HeartbeatSec, 20*time.Second))
	defer heartbeat.Stop()

	inventory := time.NewTicker(interval(welcome.InventorySec, 5*time.Minute))
	defer inventory.Stop()

	// Reading is what notices the connection dying, so it runs alongside the reporting.
	incoming := make(chan error, 1)
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				incoming <- err
				return
			}
		}
	}()

	// Sent immediately so a server appears with its details rather than blank for a while.
	if err := a.sendInventory(ctx, conn); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			_ = conn.Close(websocket.StatusNormalClosure, "shutting down")
			return nil

		case err := <-incoming:
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return errConnectionClosed
			}
			return err

		case <-heartbeat.C:
			if err := a.sendHeartbeat(ctx, conn); err != nil {
				return err
			}

		case <-inventory.C:
			if err := a.sendInventory(ctx, conn); err != nil {
				return err
			}
		}
	}
}

func (a *Agent) sendHeartbeat(ctx context.Context, conn *websocket.Conn) error {
	encoded, err := proto.Encode(proto.TypeHeartbeat, proto.Heartbeat{
		At:          time.Now().UTC(),
		Facts:       a.collector.Facts(ctx),
		Usage:       a.collector.Usage(ctx),
		SpecVersion: a.specVersion(),
	})
	if err != nil {
		return err
	}
	return a.write(ctx, conn, encoded)
}

func (a *Agent) sendInventory(ctx context.Context, conn *websocket.Conn) error {
	inventory := a.collector.Inventory(ctx)
	inventory.At = time.Now().UTC()

	encoded, err := proto.Encode(proto.TypeInventory, inventory)
	if err != nil {
		return err
	}
	return a.write(ctx, conn, encoded)
}

func (a *Agent) write(ctx context.Context, conn *websocket.Conn, data []byte) error {
	writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, data)
}

// specVersion is what the agent currently has on disk, so the control plane can skip sending
// an unchanged one.
func (a *Agent) specVersion() int64 {
	// Reconciliation arrives next; until then there is nothing applied.
	return 0
}

func interval(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}
