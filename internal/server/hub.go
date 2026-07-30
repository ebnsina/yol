package server

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/ebnsina/yol/internal/proto"
	"github.com/google/uuid"
)

// How often an agent is expected to report, and how long we wait before deciding it has gone.
const (
	HeartbeatInterval = 20 * time.Second
	InventoryInterval = 5 * time.Minute
	readTimeout       = HeartbeatInterval * 3
	writeTimeout      = 10 * time.Second
)

// Connection is one connected agent.
type Connection struct {
	Identity  AgentIdentity
	Version   string
	Caps      map[proto.Capability]bool
	Since     time.Time
	conn      *websocket.Conn
	writeLock sync.Mutex
}

// Supports reports whether the agent can do something. Behaviour is gated on this rather
// than on a version string, so a partly upgraded fleet behaves predictably.
func (c *Connection) Supports(capability proto.Capability) bool {
	return c.Caps[capability]
}

// Send writes a message to the agent.
func (c *Connection) Send(ctx context.Context, msgType proto.Type, payload any) error {
	encoded, err := proto.Encode(msgType, payload)
	if err != nil {
		return err
	}

	// One writer at a time; the library does not allow concurrent writes.
	c.writeLock.Lock()
	defer c.writeLock.Unlock()

	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return c.conn.Write(writeCtx, websocket.MessageText, encoded)
}

// Hub tracks which agents are connected right now. Connections do not survive a restart of
// this process, which is why nothing durable is stored here: agents reconnect on their own.
type Hub struct {
	mu          sync.RWMutex
	connections map[uuid.UUID]*Connection
}

// NewHub builds an empty hub.
func NewHub() *Hub {
	return &Hub{connections: make(map[uuid.UUID]*Connection)}
}

// Add registers a connection, replacing any earlier one for the same server. A server
// reconnecting after a network drop would otherwise leave a dead entry behind.
func (h *Hub) Add(c *Connection) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if previous, exists := h.connections[c.Identity.ServerID]; exists {
		_ = previous.conn.Close(websocket.StatusNormalClosure, "replaced by a newer connection")
	}
	h.connections[c.Identity.ServerID] = c
}

// Remove deregisters a connection, unless it has already been replaced by a newer one.
func (h *Hub) Remove(c *Connection) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if current, exists := h.connections[c.Identity.ServerID]; exists && current == c {
		delete(h.connections, c.Identity.ServerID)
	}
}

// Get returns the connection for a server, if it is connected.
func (h *Hub) Get(serverID uuid.UUID) (*Connection, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	c, ok := h.connections[serverID]
	return c, ok
}

// Count reports how many agents are connected.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.connections)
}

// CloseAll ends every connection, so a shutdown does not leave agents waiting on a socket
// that is never going to answer.
func (h *Hub) CloseAll() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for id, c := range h.connections {
		_ = c.conn.Close(websocket.StatusGoingAway, "the control plane is restarting")
		delete(h.connections, id)
	}
	slog.Info("closed all agent connections")
}

// ErrClosed reports that a connection ended normally rather than failing.
var ErrClosed = errors.New("server: agent connection closed")
