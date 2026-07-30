package db

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// These tests need the local database from `make dev-db migrate-up`.
func testPool(t *testing.T) *Pool {
	t.Helper()
	url := os.Getenv("YOL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("YOL_TEST_DATABASE_URL not set")
	}
	p, err := Open(context.Background(), url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// seedOrg creates an organization with one member and removes it afterwards.
func seedOrg(t *testing.T, p *Pool) (orgID, userID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	orgID, userID = uuid.New(), uuid.New()

	err := p.Unscoped(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			orgID, "Test "+orgID.String()[:8], "test-"+orgID.String()[:8]); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO users (id, email, name, password_hash) VALUES ($1, $2, $3, 'x')`,
			userID, userID.String()[:8]+"@test.io", "Test User")
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	err = p.InOrg(ctx, orgID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO memberships (id, org_id, user_id, role) VALUES ($1, $2, $3, 'owner')`,
			uuid.New(), orgID, userID)
		return err
	})
	if err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	t.Cleanup(func() {
		_ = p.Unscoped(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
			return err
		})
	})
	return orgID, userID
}

func countMemberships(t *testing.T, tx pgx.Tx, where string, args ...any) int {
	t.Helper()
	var n int
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM memberships `+where, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestInOrgSeesOnlyItsOwnRows(t *testing.T) {
	p := testPool(t)
	ctx := context.Background()
	orgA, _ := seedOrg(t, p)
	orgB, _ := seedOrg(t, p)

	err := p.InOrg(ctx, orgA, func(tx pgx.Tx) error {
		if n := countMemberships(t, tx, ""); n != 1 {
			t.Errorf("visible rows = %d, want 1", n)
		}
		// Even naming the other organization explicitly must return nothing.
		if n := countMemberships(t, tx, `WHERE org_id = $1`, orgB); n != 0 {
			t.Errorf("leaked %d rows from the other organization", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InOrg: %v", err)
	}
}

// A query that forgets its org filter must return nothing rather than everything.
func TestUnscopedSeesNoTenantRows(t *testing.T) {
	p := testPool(t)
	ctx := context.Background()
	seedOrg(t, p)

	err := p.Unscoped(ctx, func(tx pgx.Tx) error {
		if n := countMemberships(t, tx, ""); n != 0 {
			t.Errorf("unscoped query saw %d tenant rows, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Unscoped: %v", err)
	}
}

func TestInOrgRejectsWritesToAnotherOrg(t *testing.T) {
	p := testPool(t)
	ctx := context.Background()
	orgA, userA := seedOrg(t, p)
	orgB, _ := seedOrg(t, p)

	err := p.InOrg(ctx, orgA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO memberships (id, org_id, user_id, role) VALUES ($1, $2, $3, 'member')`,
			uuid.New(), orgB, userA)
		return err
	})
	if err == nil {
		t.Fatal("write into another organization succeeded")
	}
	if !IsRLSViolation(err) {
		t.Errorf("expected an isolation failure, got: %v", err)
	}
}

// A user must see their own membership rows across organizations, and no one else's.
func TestAsUserSeesOnlyOwnMemberships(t *testing.T) {
	p := testPool(t)
	ctx := context.Background()
	_, userA := seedOrg(t, p)
	seedOrg(t, p)

	err := p.AsUser(ctx, userA, func(tx pgx.Tx) error {
		if n := countMemberships(t, tx, ""); n != 1 {
			t.Errorf("visible rows = %d, want 1 (only their own)", n)
		}
		if n := countMemberships(t, tx, `WHERE user_id <> $1`, userA); n != 0 {
			t.Errorf("saw %d memberships belonging to other users", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("AsUser: %v", err)
	}
}

// The user-scope policy is SELECT only, so it must not become a way to join an org.
func TestAsUserCannotJoinAnotherOrg(t *testing.T) {
	p := testPool(t)
	ctx := context.Background()
	_, userA := seedOrg(t, p)
	orgB, _ := seedOrg(t, p)

	err := p.AsUser(ctx, userA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO memberships (id, org_id, user_id, role) VALUES ($1, $2, $3, 'owner')`,
			uuid.New(), orgB, userA)
		return err
	})
	if err == nil {
		t.Fatal("a user added themselves to an organization they do not belong to")
	}
	if !IsRLSViolation(err) {
		t.Errorf("expected an isolation failure, got: %v", err)
	}
}

func TestInOrgRequiresAnOrg(t *testing.T) {
	p := testPool(t)
	err := p.InOrg(context.Background(), uuid.Nil, func(pgx.Tx) error { return nil })
	if err != ErrNotScoped {
		t.Errorf("err = %v, want ErrNotScoped", err)
	}
}

// Scope must not survive the transaction that set it.
func TestScopeDoesNotLeakBetweenTransactions(t *testing.T) {
	p := testPool(t)
	ctx := context.Background()
	orgA, _ := seedOrg(t, p)

	if err := p.InOrg(ctx, orgA, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("InOrg: %v", err)
	}

	// Repeated until the pool hands back the connection the scoped transaction used.
	for range 20 {
		err := p.Unscoped(ctx, func(tx pgx.Tx) error {
			var scope *string
			if err := tx.QueryRow(ctx, `SELECT nullif(current_setting('app.org_id', true), '')`).Scan(&scope); err != nil {
				return err
			}
			if scope != nil {
				t.Errorf("organization scope leaked into a later transaction: %q", *scope)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Unscoped: %v", err)
		}
	}
}

func TestOpenRejectsRoleThatBypassesRLS(t *testing.T) {
	url := os.Getenv("YOL_TEST_OWNER_DATABASE_URL")
	if url == "" {
		t.Skip("YOL_TEST_OWNER_DATABASE_URL not set")
	}
	if _, err := Open(context.Background(), url); err == nil {
		t.Fatal("Open accepted a role that bypasses row level security")
	}
}

func TestIsUniqueViolation(t *testing.T) {
	p := testPool(t)
	ctx := context.Background()
	orgID, _ := seedOrg(t, p)

	var slug string
	if err := p.Unscoped(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT slug FROM organizations WHERE id = $1`, orgID).Scan(&slug)
	}); err != nil {
		t.Fatalf("read slug: %v", err)
	}

	err := p.Unscoped(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, 'dup', $2)`, uuid.New(), slug)
		return err
	})
	if !IsUniqueViolation(err) {
		t.Errorf("IsUniqueViolation(%v) = false, want true", err)
	}
}
