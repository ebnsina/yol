// Package deploy owns projects, the services inside them, and getting those services running.
package deploy

import (
	"context"
	"errors"
	"fmt"

	"github.com/ebnsina/yol/internal/db"
	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The range host ports are taken from. Deliberately high and well away from anything a customer
// is likely to be using themselves, since we are a guest on their machine.
const (
	PortRangeStart = 30000
	PortRangeEnd   = 32767
)

// What a port is for. A service can hold more than one, so the purpose distinguishes them.
const (
	PurposeApp     = "app"     // where the router sends web traffic
	PurposeService = "service" // a database or similar, when the user asks for it to be reachable
	PurposeMedia   = "media"   // media servers need several, and not all of them speak HTTP
)

// ErrNoPortsLeft means the server's range is full. Reaching this needs thousands of services on
// one machine, so it means something is leaking allocations rather than that the range is small.
var ErrNoPortsLeft = errors.New("deploy: no host ports left on this server")

// Ports hands out host ports so that two projects on one server cannot collide.
//
// Allocation happens in the control plane rather than by letting the agent pick, because only
// the control plane can see every project on a machine at once.
type Ports struct {
	pool *db.Pool
}

// NewPorts builds the allocator.
func NewPorts(pool *db.Pool) *Ports {
	return &Ports{pool: pool}
}

// Allocate reserves a port for a service, or returns the one it already holds for this purpose.
// Idempotent, so a retried deploy does not consume a second port.
func (p *Ports) Allocate(ctx context.Context, tx pgx.Tx, orgID, serverID, serviceID uuid.UUID, purpose string) (int, error) {
	q := sqlc.New(tx)

	existing, err := q.GetServicePort(ctx, sqlc.GetServicePortParams{
		ServiceID: &serviceID, Purpose: purpose,
	})
	if err == nil {
		return int(existing.Port), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("deploy: read port allocation: %w", err)
	}

	row, err := q.AllocatePort(ctx, sqlc.AllocatePortParams{
		ID:         uuid.New(),
		OrgID:      orgID,
		ServerID:   serverID,
		ServiceID:  &serviceID,
		Purpose:    purpose,
		RangeStart: PortRangeStart,
		RangeEnd:   PortRangeEnd,
	})
	if err != nil {
		// No row comes back when every candidate in the range is taken.
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNoPortsLeft
		}
		// Two deploys racing for the same port: the loser retries and takes the next one.
		if db.IsUniqueViolation(err) {
			return p.Allocate(ctx, tx, orgID, serverID, serviceID, purpose)
		}
		return 0, fmt.Errorf("deploy: allocate port: %w", err)
	}
	return int(row.Port), nil
}

// Release gives back everything a service held, so removing a service does not leak ports.
func (p *Ports) Release(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID) error {
	if err := sqlc.New(tx).ReleaseServicePorts(ctx, &serviceID); err != nil {
		return fmt.Errorf("deploy: release ports: %w", err)
	}
	return nil
}

// Reserve records a port that something else on the machine is using, so we never hand it out.
// Used for what the survey found already listening.
func (p *Ports) Reserve(ctx context.Context, tx pgx.Tx, orgID, serverID uuid.UUID, port int, purpose string) error {
	q := sqlc.New(tx)

	_, err := q.AllocatePort(ctx, sqlc.AllocatePortParams{
		ID:         uuid.New(),
		OrgID:      orgID,
		ServerID:   serverID,
		Purpose:    purpose,
		RangeStart: int32(port),
		RangeEnd:   int32(port),
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) && !db.IsUniqueViolation(err) {
		return fmt.Errorf("deploy: reserve port %d: %w", port, err)
	}
	// Already reserved, or the exact port was taken: either way it will not be handed out.
	return nil
}

// explainPortFailure turns exhaustion into something a user can act on.
func explainPortFailure(err error) error {
	if errors.Is(err, ErrNoPortsLeft) {
		return httpx.Conflict(
			"This server has no free ports left for new services. Remove something you no longer need, or use another server.")
	}
	return httpx.Internal(err)
}
