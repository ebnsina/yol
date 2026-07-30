package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/ebnsina/yol/internal/proto"
)

// AgentHandler serves the endpoints agents use. These are the only routes reachable with an
// agent credential rather than a person's session.
type AgentHandler struct {
	svc     *Service
	hub     *Hub
	streams *Streams
}

// NewAgentHandler builds the agent endpoints.
func NewAgentHandler(svc *Service, hub *Hub, streams *Streams) *AgentHandler {
	return &AgentHandler{svc: svc, hub: hub, streams: streams}
}

// Routes registers the agent endpoints.
func (h *AgentHandler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/agent/enroll", h.enroll)
	mux.HandleFunc("GET /v1/agent/connect", h.connect)
}

type enrollRequest struct {
	EnrollmentToken string `json:"enrollmentToken"`
}

type enrollResponse struct {
	AgentToken string `json:"agentToken"`
	ServerID   string `json:"serverId"`
	Mode       string `json:"mode"`
}

func (h *AgentHandler) enroll(w http.ResponseWriter, r *http.Request) {
	var req enrollRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	token, identity, err := h.svc.Enroll(r.Context(), req.EnrollmentToken)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, enrollResponse{
		AgentToken: token,
		ServerID:   identity.ServerID.String(),
		Mode:       string(identity.Mode),
	})
}

// connect upgrades to a long-lived connection. The agent dials out to us, so a customer
// opens no inbound port on their server.
func (h *AgentHandler) connect(w http.ResponseWriter, r *http.Request) {
	identity, err := h.svc.AuthenticateAgent(r.Context(), agentToken(r))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	// Agents are not browsers, so the same-origin protection does not apply to them.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		slog.Warn("agent connection could not be established", "serverId", identity.ServerID, "error", err)
		return
	}

	// Detached from the request so the connection outlives it.
	ctx := context.WithoutCancel(r.Context())
	h.serve(ctx, conn, *identity)
}

// serve runs one agent connection until it ends.
func (h *AgentHandler) serve(ctx context.Context, conn *websocket.Conn, identity AgentIdentity) {
	defer conn.CloseNow()

	hello, err := h.readHello(ctx, conn)
	if err != nil {
		slog.Warn("agent did not introduce itself", "serverId", identity.ServerID, "error", err)
		_ = conn.Close(websocket.StatusPolicyViolation, "expected an introduction")
		return
	}

	connection := &Connection{
		Identity: identity,
		Version:  hello.AgentVersion,
		Caps:     capabilitySet(hello.Capabilities),
		Since:    time.Now(),
		conn:     conn,
	}

	// The agent is told its mode and enforces it itself, so a mistake here cannot cause a
	// change on a server someone asked us only to watch.
	if err := connection.Send(ctx, proto.TypeWelcome, proto.Welcome{
		ServerID:     identity.ServerID.String(),
		Mode:         identity.Mode,
		HeartbeatSec: int(HeartbeatInterval.Seconds()),
		InventorySec: int(InventoryInterval.Seconds()),
	}); err != nil {
		slog.Warn("could not greet agent", "serverId", identity.ServerID, "error", err)
		return
	}

	h.hub.Add(connection)
	defer h.hub.Remove(connection)

	slog.Info("agent connected",
		"serverId", identity.ServerID, "version", hello.AgentVersion, "mode", identity.Mode)

	if err := h.svc.RecordHeartbeat(ctx, identity, hello.AgentVersion, hello.Facts); err != nil {
		slog.Error("could not record agent arrival", "serverId", identity.ServerID, "error", err)
	}
	h.svc.recordAgentEvent(ctx, identity, "agent", "The agent connected and this server is now online.", "info")

	h.readLoop(ctx, connection)

	slog.Info("agent disconnected", "serverId", identity.ServerID)
	h.svc.MarkOffline(ctx, identity)
	h.svc.recordAgentEvent(ctx, identity, "agent", "This server stopped responding.", "warning")
}

// readHello waits for the agent's introduction before doing anything else.
func (h *AgentHandler) readHello(ctx context.Context, conn *websocket.Conn) (*proto.Hello, error) {
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, data, err := conn.Read(readCtx)
	if err != nil {
		return nil, err
	}
	envelope, err := proto.Decode(data)
	if err != nil {
		return nil, err
	}
	if envelope.Type != proto.TypeHello {
		return nil, errors.New("server: first message was not an introduction")
	}

	var hello proto.Hello
	if err := envelope.Into(&hello); err != nil {
		return nil, err
	}
	return &hello, nil
}

// readLoop handles messages until the connection ends. A silent agent is treated as gone,
// because a socket can stay open long after the machine behind it has stopped answering.
func (h *AgentHandler) readLoop(ctx context.Context, c *Connection) {
	for {
		readCtx, cancel := context.WithTimeout(ctx, readTimeout)
		_, data, err := c.conn.Read(readCtx)
		cancel()

		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return
			}
			slog.Debug("agent connection ended", "serverId", c.Identity.ServerID, "error", err)
			return
		}

		envelope, err := proto.Decode(data)
		if err != nil {
			slog.Warn("unreadable message from agent", "serverId", c.Identity.ServerID, "error", err)
			continue
		}
		h.handle(ctx, c, envelope)
	}
}

// handle dispatches one message. An unknown type is ignored rather than treated as an error,
// so a newer agent talking to an older control plane does not break the connection.
func (h *AgentHandler) handle(ctx context.Context, c *Connection, envelope *proto.Envelope) {
	switch envelope.Type {
	case proto.TypeHeartbeat:
		var beat proto.Heartbeat
		if err := envelope.Into(&beat); err != nil {
			slog.Warn("unreadable heartbeat", "serverId", c.Identity.ServerID, "error", err)
			return
		}
		if err := h.svc.RecordHeartbeat(ctx, c.Identity, c.Version, beat.Facts); err != nil {
			slog.Error("could not record heartbeat", "serverId", c.Identity.ServerID, "error", err)
		}

	case proto.TypeInventory:
		var inventory proto.Inventory
		if err := envelope.Into(&inventory); err != nil {
			slog.Warn("unreadable inventory", "serverId", c.Identity.ServerID, "error", err)
			return
		}
		if err := h.svc.RecordInventory(ctx, c.Identity, inventory); err != nil {
			slog.Error("could not record inventory", "serverId", c.Identity.ServerID, "error", err)
		}

	case proto.TypeLogChunk:
		var chunk proto.LogChunk
		if err := envelope.Into(&chunk); err != nil {
			return
		}
		h.streams.Deliver(chunk)

	default:
		slog.Debug("ignoring message this build does not handle",
			"serverId", c.Identity.ServerID, "type", envelope.Type)
	}
}

func capabilitySet(list []proto.Capability) map[proto.Capability]bool {
	set := make(map[proto.Capability]bool, len(list))
	for _, capability := range list {
		set[capability] = true
	}
	return set
}

// agentToken reads the credential an agent presents.
func agentToken(r *http.Request) string {
	if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return ""
}
