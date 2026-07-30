// Package jobs runs work that outlives a request: surveying a server, installing the agent,
// and later building and deploying.
//
// Job arguments carry identifiers only, never credentials. Arguments are stored in the
// database as readable JSON and are kept after a job finishes, so a password placed in them
// would sit in clear text long after it was needed. Workers load what they need themselves.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// Schema keeps job tables out of public, where every table is required to enforce tenant
// isolation. Job tables are not tenant data and have no such policies.
const Schema = "jobs"

// Queue names. Work that a person is waiting on is kept away from slow background work, so a
// long backup cannot delay someone watching a server connect.
const (
	QueueInteractive = "interactive"
	QueueBackground  = "background"
)

// Runner owns the job client.
type Runner struct {
	client *river.Client[pgx.Tx]
	pool   *pgxpool.Pool
}

// Workers collects the workers to register. Kept as a type so packages can add their own
// without jobs importing them, which would be a cycle.
type Workers = river.Workers

// NewWorkers builds an empty worker set.
func NewWorkers() *Workers { return river.NewWorkers() }

// New builds a runner. The pool must be the application's, so jobs are inserted in the same
// transaction as the change that caused them and cannot be enqueued for work that rolled back.
func New(pool *pgxpool.Pool, workers *Workers) (*Runner, error) {
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Schema:  Schema,
		Workers: workers,
		Queues: map[string]river.QueueConfig{
			QueueInteractive: {MaxWorkers: 10},
			QueueBackground:  {MaxWorkers: 5},
		},
		Logger: slog.Default(),
		// A job that has run for this long has hung; connecting a server should never take it.
		JobTimeout: 15 * time.Minute,
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: create client: %w", err)
	}
	return &Runner{client: client, pool: pool}, nil
}

// Start begins processing.
func (r *Runner) Start(ctx context.Context) error {
	if err := r.client.Start(ctx); err != nil {
		return fmt.Errorf("jobs: start: %w", err)
	}
	return nil
}

// Stop finishes running jobs and stops taking new ones.
func (r *Runner) Stop(ctx context.Context) error {
	return r.client.Stop(ctx)
}

// Client exposes the underlying client for enqueuing.
func (r *Runner) Client() *river.Client[pgx.Tx] { return r.client }
