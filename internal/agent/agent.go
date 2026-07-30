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
	"sync"
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
	publicKeyFile  = "spec.pub"
	specFile       = "spec.json"
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
	tails     *tails
	verifier  *proto.Verifier

	// How long a replaced container is left alone before being taken away, so requests already
	// in flight finish rather than being cut off. A field so a test need not wait it out.
	drain time.Duration

	// The specification currently held, guarded because reconciliation and the connection
	// read it from different goroutines.
	specMu sync.RWMutex
	spec   *proto.Spec

	// The active connection, guarded because log streams write from their own goroutines.
	writeMu sync.Mutex
	conn    *websocket.Conn
}

// Collector reads what is on the machine. An interface so the agent can be exercised without
// a real Docker daemon.
type Collector interface {
	Facts(ctx context.Context) proto.HostFacts
	Usage(ctx context.Context) proto.Usage
	Inventory(ctx context.Context) proto.Inventory
}

// New builds an agent. It starts in watch-only mode and is told otherwise by the control
// plane, so a failure to establish the mode leaves it unable to change anything.
func New(cfg *config.Agent, collector Collector) *Agent {
	return &Agent{
		cfg:       cfg,
		collector: collector,
		mode:      proto.ModeWatch,
		tails:     newTails(),
		drain:     defaultDrain,
	}
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

	a.loadVerifier()

	// Whatever was last applied is applied again before the control plane is reached, so a
	// server that rebooted comes back on its own rather than waiting for us.
	if spec, _, err := a.loadSpec(); err == nil {
		a.setSpec(spec)
		slog.Info("restored the specification from disk", "version", spec.Version)
	}

	go a.reconcileLoop(ctx)

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
	a.setConn(conn)
	defer a.clearConn()
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
	return []proto.Capability{proto.CapInventory, proto.CapMetrics, proto.CapLogTail, proto.CapBuild}
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

	// Reading both notices the connection dying and delivers instructions.
	incoming := make(chan error, 1)
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				incoming <- err
				return
			}
			a.handle(ctx, data)
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
	a.writeMu.Lock()
	defer a.writeMu.Unlock()

	writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, data)
}

// sendRaw writes on the current connection, for work that outlives a single send such as a
// log stream.
func (a *Agent) sendRaw(ctx context.Context, data []byte) error {
	a.writeMu.Lock()
	conn := a.conn
	a.writeMu.Unlock()

	if conn == nil {
		return errors.New("agent: not connected")
	}
	return a.write(ctx, conn, data)
}

func (a *Agent) setConn(conn *websocket.Conn) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	a.conn = conn
}

// clearConn drops the connection and ends anything streaming on it, since those messages
// would have nowhere to go.
func (a *Agent) clearConn() {
	a.tails.stopAll()

	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	a.conn = nil
}

// handle acts on an instruction from the control plane.
//
// Watch-only is enforced here rather than trusting the control plane to send only permitted
// instructions, so a mistake there cannot change a server someone asked us only to watch.
// Reading is always allowed: looking at logs changes nothing.
func (a *Agent) handle(ctx context.Context, data []byte) {
	envelope, err := proto.Decode(data)
	if err != nil {
		slog.Warn("unreadable instruction", "error", err)
		return
	}

	switch envelope.Type {
	case proto.TypeTailLogs:
		var req proto.TailLogs
		if err := envelope.Into(&req); err != nil {
			slog.Warn("unreadable log request", "error", err)
			return
		}
		a.startTail(ctx, req)

	case proto.TypeStopTail:
		var req proto.StopTail
		if err := envelope.Into(&req); err != nil {
			return
		}
		a.tails.stop(req.StreamID)

	case proto.TypeBuild:
		if !a.permits(envelope.Type) {
			slog.Warn("refused an instruction to build, since this server is watched only")
			a.refuse(ctx)
			return
		}
		req, ok := a.acceptBuild(envelope)
		if !ok {
			return
		}
		a.startBuild(ctx, req)

	case proto.TypeApplySpec:
		if !a.permits(envelope.Type) {
			slog.Warn("refused an instruction to change this server, which is watched only")
			a.refuse(ctx)
			return
		}
		a.acceptSpec(ctx, envelope)

	default:
		// Unknown types are ignored so a newer control plane does not break an older agent.
		slog.Debug("ignoring instruction this build does not handle", "type", envelope.Type)
	}
}

// permits reports whether this agent may act on an instruction.
//
// Watch-only is decided here rather than by trusting the control plane to send only permitted
// instructions, so a mistake there cannot change a server someone asked us only to watch.
// Reading is always allowed: looking at logs or listing containers changes nothing.
func (a *Agent) permits(instruction proto.Type) bool {
	switch instruction {
	case proto.TypeTailLogs, proto.TypeStopTail, proto.TypeSurvey:
		return true
	default:
		// Permitted only when the control plane has explicitly said this server is managed.
		// Testing for "not watch-only" would treat an unset mode as permission, so failing to
		// learn the mode would be the one case that allows everything.
		return a.mode == proto.ModeManaged
	}
}

// refuse tells the control plane that a change was declined, so it can say so rather than
// waiting for something that will never happen.
func (a *Agent) refuse(ctx context.Context) {
	encoded, err := proto.Encode(proto.TypeApplied, proto.Applied{
		At:      time.Now().UTC(),
		Refused: "This server is being watched only, so nothing on it was changed.",
	})
	if err != nil {
		return
	}
	_ = a.sendRaw(ctx, encoded)
}

// specVersion is what the agent currently holds, so the control plane can skip resending an
// unchanged specification.
func (a *Agent) specVersion() int64 {
	a.specMu.RLock()
	defer a.specMu.RUnlock()

	if a.spec == nil {
		return 0
	}
	return a.spec.Version
}

func (a *Agent) currentSpec() *proto.Spec {
	a.specMu.RLock()
	defer a.specMu.RUnlock()
	return a.spec
}

func (a *Agent) setSpec(spec *proto.Spec) {
	a.specMu.Lock()
	defer a.specMu.Unlock()
	a.spec = spec
}

// loadVerifier reads the public key written during setup. Without it no specification can be
// checked, and an unchecked specification is never applied.
func (a *Agent) loadVerifier() {
	encoded, err := a.readFile(publicKeyFile)
	if err != nil || encoded == "" {
		slog.Warn("no public key on this server, so no instruction to change it can be checked")
		return
	}
	verifier, err := proto.NewVerifier(encoded)
	if err != nil {
		slog.Error("the public key on this server could not be read", "error", err)
		return
	}
	a.verifier = verifier
}

func interval(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}
