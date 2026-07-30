package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/ebnsina/yol/internal/db"
	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// These tests need the local database from `make dev-db migrate-up`.
func testPool(t *testing.T) *db.Pool {
	t.Helper()
	dsn := os.Getenv("YOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("YOL_TEST_DATABASE_URL not set")
	}
	pool, err := db.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// fixture is one organization with a server and a couple of services to allocate for.
type fixture struct {
	pool     *db.Pool
	ports    *Ports
	orgID    uuid.UUID
	serverID uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()

	f := &fixture{pool: pool, ports: NewPorts(pool), orgID: uuid.New(), serverID: uuid.New()}
	short := f.orgID.String()[:8]

	err := pool.Tx(ctx, db.Scope{OrgID: f.orgID}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			f.orgID, "Ports "+short, "ports-"+short); err != nil {
			return err
		}
		// A real address, because code that decides between an A record and a CNAME reads it.
		_, err := tx.Exec(ctx,
			`INSERT INTO servers (id, org_id, name, host) VALUES ($1, $2, 'test', $3)`,
			f.serverID, f.orgID, fmt.Sprintf("10.0.%d.%d", f.serverID[0], f.serverID[1]))
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Cleanup(func() {
		_ = pool.InOrg(context.Background(), f.orgID, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, f.orgID)
			return err
		})
	})
	return f
}

// newService creates a service to allocate ports against.
func (f *fixture) newService(t *testing.T, name string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	projectID, envID, serviceID := uuid.New(), uuid.New(), uuid.New()

	err := f.pool.InOrg(ctx, f.orgID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO projects (id, org_id, name, slug) VALUES ($1, $2, $3, $4)`,
			projectID, f.orgID, name, name); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO environments (id, org_id, project_id, server_id, name, branch)
			 VALUES ($1, $2, $3, $4, 'production', 'main')`,
			envID, f.orgID, projectID, f.serverID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO services (id, org_id, env_id, name, kind) VALUES ($1, $2, $3, $4, 'app')`,
			serviceID, f.orgID, envID, name)
		return err
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	return serviceID
}

func (f *fixture) allocate(t *testing.T, serviceID uuid.UUID, purpose string) int {
	t.Helper()
	ctx := context.Background()

	var port int
	err := f.pool.InOrg(ctx, f.orgID, func(tx pgx.Tx) error {
		var err error
		port, err = f.ports.Allocate(ctx, tx, f.orgID, f.serverID, serviceID, purpose)
		return err
	})
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	return port
}

func TestAllocateGivesPortsInRange(t *testing.T) {
	f := newFixture(t)
	port := f.allocate(t, f.newService(t, "app-one"), PurposeApp)

	if port < PortRangeStart || port > PortRangeEnd {
		t.Errorf("port %d is outside the range %d-%d", port, PortRangeStart, PortRangeEnd)
	}
}

// Two projects on one server must never be given the same port, which is the whole reason the
// control plane hands them out rather than the agent choosing.
func TestAllocateNeverRepeatsOnOneServer(t *testing.T) {
	f := newFixture(t)
	seen := make(map[int]bool)

	for i := range 25 {
		service := f.newService(t, "app-"+uuid.New().String()[:8])
		port := f.allocate(t, service, PurposeApp)

		if seen[port] {
			t.Fatalf("port %d handed out twice (iteration %d)", port, i)
		}
		seen[port] = true
	}
}

// A retried deploy must not consume a second port.
func TestAllocateIsIdempotentPerPurpose(t *testing.T) {
	f := newFixture(t)
	service := f.newService(t, "app-repeat")

	first := f.allocate(t, service, PurposeApp)
	second := f.allocate(t, service, PurposeApp)

	if first != second {
		t.Errorf("the same service and purpose got %d then %d", first, second)
	}
}

// A service can hold several ports for different reasons, and they must differ.
func TestAllocateGivesADifferentPortPerPurpose(t *testing.T) {
	f := newFixture(t)
	service := f.newService(t, "app-multi")

	app := f.allocate(t, service, PurposeApp)
	media := f.allocate(t, service, PurposeMedia)

	if app == media {
		t.Errorf("both purposes got port %d", app)
	}
}

func TestReleaseGivesPortsBack(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	service := f.newService(t, "app-release")

	port := f.allocate(t, service, PurposeApp)

	err := f.pool.InOrg(ctx, f.orgID, func(tx pgx.Tx) error {
		return f.ports.Release(ctx, tx, service)
	})
	if err != nil {
		t.Fatalf("Release: %v", err)
	}

	// The freed port is the lowest free one again, so it comes back.
	reused := f.allocate(t, f.newService(t, "app-reuse"), PurposeApp)
	if reused != port {
		t.Errorf("released port %d was not reused; got %d", port, reused)
	}
}

// Something the customer is already running must never be handed out to one of our services.
func TestReservedPortsAreNotHandedOut(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	err := f.pool.InOrg(ctx, f.orgID, func(tx pgx.Tx) error {
		return f.ports.Reserve(ctx, tx, f.orgID, f.serverID, PortRangeStart, "theirs")
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	port := f.allocate(t, f.newService(t, "app-after-reserve"), PurposeApp)
	if port == PortRangeStart {
		t.Errorf("handed out port %d, which something else is using", port)
	}
}

// Reserving twice is ordinary: the survey runs repeatedly and reports the same ports each time.
func TestReserveIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	for range 3 {
		err := f.pool.InOrg(ctx, f.orgID, func(tx pgx.Tx) error {
			return f.ports.Reserve(ctx, tx, f.orgID, f.serverID, 31000, "theirs")
		})
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
	}
}

// Allocations belong to one server, so the same number is free on another.
func TestPortsAreScopedToOneServer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	first := f.allocate(t, f.newService(t, "app-server-one"), PurposeApp)

	other := uuid.New()
	err := f.pool.InOrg(ctx, f.orgID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO servers (id, org_id, name, host) VALUES ($1, $2, 'second', '10.9.9.9')`,
			other, f.orgID)
		return err
	})
	if err != nil {
		t.Fatalf("create second server: %v", err)
	}

	service := f.newService(t, "app-server-two")
	var second int
	err = f.pool.InOrg(ctx, f.orgID, func(tx pgx.Tx) error {
		var err error
		second, err = f.ports.Allocate(ctx, tx, f.orgID, other, service, PurposeApp)
		return err
	})
	if err != nil {
		t.Fatalf("allocate on second server: %v", err)
	}

	if second != first {
		t.Errorf("a different server got %d rather than reusing %d", second, first)
	}
}

// A full range must say so plainly rather than failing obscurely.
func TestExhaustionIsReportedClearly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	service := f.newService(t, "app-exhausted")

	// Fill a one-port range by taking the only candidate.
	err := f.pool.InOrg(ctx, f.orgID, func(tx pgx.Tx) error {
		return f.ports.Reserve(ctx, tx, f.orgID, f.serverID, PortRangeStart, "theirs")
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	// Ask for one from a range containing only that port.
	err = f.pool.InOrg(ctx, f.orgID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		_, err := q.AllocatePort(ctx, sqlc.AllocatePortParams{
			ID: uuid.New(), OrgID: f.orgID, ServerID: f.serverID,
			ServiceID: &service, Purpose: PurposeApp,
			RangeStart: PortRangeStart, RangeEnd: PortRangeStart,
		})
		return err
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("a full range gave %v, want no rows so the caller can report exhaustion", err)
	}

	if failure := explainPortFailure(ErrNoPortsLeft); failure == nil {
		t.Error("exhaustion produced no message for the user")
	}
}
