// Command jobsmigrate creates the background job tables and grants the application role
// access to them. Run as the owning role, after the ordinary migrations.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/ebnsina/yol/internal/jobs"
)

func main() {
	url := os.Getenv("YOL_OWNER_DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "YOL_OWNER_DATABASE_URL is not set")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		fail("connect", err)
	}
	defer pool.Close()

	migrator, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Schema: jobs.Schema})
	if err != nil {
		fail("create migrator", err)
	}

	result, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		fail("migrate", err)
	}
	for _, version := range result.Versions {
		fmt.Printf("jobs: applied version %d\n", version.Version)
	}
	if len(result.Versions) == 0 {
		fmt.Println("jobs: already up to date")
	}

	// Default privileges only cover tables created afterwards, so anything the migrator just
	// created needs granting explicitly.
	for _, statement := range []string{
		fmt.Sprintf(`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s TO yol_app`, jobs.Schema),
		fmt.Sprintf(`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %s TO yol_app`, jobs.Schema),
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			fail("grant", err)
		}
	}
	fmt.Println("jobs: application role granted access")
}

func fail(step string, err error) {
	fmt.Fprintf(os.Stderr, "jobs migrate: %s: %v\n", step, err)
	os.Exit(1)
}
