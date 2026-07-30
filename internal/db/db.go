// Package db owns the connection pool and the tenant scoping that row level security needs.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotScoped means an org-scoped operation was attempted without an organization.
var ErrNotScoped = errors.New("db: operation requires an organization")

// Pool wraps pgxpool with the tenant scoping helpers.
type Pool struct {
	pool *pgxpool.Pool
}

// Open connects and verifies the role cannot bypass row level security. A misconfigured
// role would silently disable tenant isolation, so this refuses to start instead.
func Open(ctx context.Context, url string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MaxConnLifetime = time.Hour
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	p := &Pool{pool: pool}
	if err := p.verifyRole(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return p, nil
}

func (p *Pool) verifyRole(ctx context.Context) error {
	var super, bypassRLS bool
	err := p.pool.QueryRow(ctx,
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`,
	).Scan(&super, &bypassRLS)
	if err != nil {
		return fmt.Errorf("inspect current role: %w", err)
	}
	if super || bypassRLS {
		return errors.New("database role can bypass row level security: connect as a NOSUPERUSER, NOBYPASSRLS role that does not own the tables")
	}
	return nil
}

// Close releases every connection.
func (p *Pool) Close() { p.pool.Close() }

// Ping reports whether the database is reachable.
func (p *Pool) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

// Scope is what the database is allowed to see for one transaction. A zero Scope sees no
// tenant rows at all, so forgetting to set it fails closed.
type Scope struct {
	OrgID  uuid.UUID
	UserID uuid.UUID
}

// Unscoped runs work outside any organization, such as looking up an account by email.
func (p *Pool) Unscoped(ctx context.Context, fn func(pgx.Tx) error) error {
	return p.Tx(ctx, Scope{}, fn)
}

// AsUser runs work on behalf of a user with no organization chosen yet, such as listing
// the organizations they belong to.
func (p *Pool) AsUser(ctx context.Context, userID uuid.UUID, fn func(pgx.Tx) error) error {
	if userID == uuid.Nil {
		return ErrNotScoped
	}
	return p.Tx(ctx, Scope{UserID: userID}, fn)
}

// InOrg runs work inside one organization with no acting user, for background jobs.
func (p *Pool) InOrg(ctx context.Context, orgID uuid.UUID, fn func(pgx.Tx) error) error {
	if orgID == uuid.Nil {
		return ErrNotScoped
	}
	return p.Tx(ctx, Scope{OrgID: orgID}, fn)
}

// InOrgAsUser is the normal request path: one organization, one acting user.
func (p *Pool) InOrgAsUser(ctx context.Context, orgID, userID uuid.UUID, fn func(pgx.Tx) error) error {
	if orgID == uuid.Nil || userID == uuid.Nil {
		return ErrNotScoped
	}
	return p.Tx(ctx, Scope{OrgID: orgID, UserID: userID}, fn)
}

// Tx applies the scope inside the transaction, so it reverts on commit or rollback and can
// never leak onto the next request using the same pooled connection.
func (p *Pool) Tx(ctx context.Context, s Scope, fn func(pgx.Tx) error) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := applyScope(ctx, tx, s); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// applyScope sets both settings with is_local so they are transaction-bound.
func applyScope(ctx context.Context, tx pgx.Tx, s Scope) error {
	settings := [...]struct {
		name string
		id   uuid.UUID
	}{
		{"app.org_id", s.OrgID},
		{"app.user_id", s.UserID},
	}
	for _, set := range settings {
		if set.id == uuid.Nil {
			continue
		}
		if _, err := tx.Exec(ctx, `SELECT set_config($1, $2, true)`, set.name, set.id.String()); err != nil {
			return fmt.Errorf("apply scope %s: %w", set.name, err)
		}
	}
	return nil
}

// IsUniqueViolation reports whether the error is a duplicate-key failure, so callers can
// turn it into a message about the value the user chose.
func IsUniqueViolation(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == "23505"
}

// IsRLSViolation reports whether a write was blocked by tenant isolation. Reaching this
// means a bug placed a write outside its organization.
func IsRLSViolation(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == "42501"
}
