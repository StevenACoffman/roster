// Package testutil provides the PostgreSQL harness the integration tests run
// against.
//
// It is a non-test package so that several test packages can share it, and it is
// not build-tagged so that the linter sees it.
package testutil

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/StevenACoffman/roster/internal/logging"
	"github.com/StevenACoffman/roster/internal/postgres"
)

// EnvDatabaseURL names an already-running database to use instead of starting a
// container. Set it to run the integration suite against a local PostgreSQL, or
// in CI where a service container is already provisioned.
const EnvDatabaseURL = "ROSTER_TEST_DATABASE_URL"

const (
	// containerStartTimeout bounds waiting for PostgreSQL to accept connections.
	// Generous because a cold image pull happens inside it.
	containerStartTimeout = 90 * time.Second
	// terminateTimeout bounds container teardown, so a wedged daemon fails the
	// cleanup rather than hanging the test binary until the go test timeout.
	terminateTimeout = 30 * time.Second
)

// NewDB returns a migrated, empty database for one test.
//
// Each call gets its own database — a fresh container, or a uniquely named
// database inside the server named by ROSTER_TEST_DATABASE_URL. That isolation
// is what makes these tests safe to run with t.Parallel: sharing one database
// would make them share mutable global state, and a failure could then be
// another test's writes rather than a bug.
//
// Skips rather than fails when no Docker daemon is reachable, so a developer
// without one sees why instead of a connection error. Never returns an error:
// per the testing conventions a helper reports its own failures.
func NewDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx := t.Context()
	url := NewDatabaseURL(t)

	pool, err := postgres.NewPool(ctx, url)
	if err != nil {
		t.Fatalf("testutil.NewDB: connecting to %s: %v", url, err)
	}
	t.Cleanup(pool.Close)

	return pool
}

// NewDatabaseURL returns the connection string for a migrated, empty database.
//
// For a caller that needs the URL rather than a pool — the serve command takes
// one on a flag. Migrations are applied here too, through a pool this function
// opens and closes, so the caller receives a ready database either way.
func NewDatabaseURL(t *testing.T) string {
	t.Helper()

	ctx := t.Context()

	url := os.Getenv(EnvDatabaseURL)
	if url == "" {
		url = startContainer(t)
	}

	pool, err := postgres.NewPool(ctx, url)
	if err != nil {
		t.Fatalf("testutil.NewDatabaseURL: connecting to %s: %v", url, err)
	}
	defer pool.Close()

	if err := postgres.Migrate(ctx, pool, logging.New(os.Stderr, "error", "text")); err != nil {
		t.Fatalf("testutil.NewDatabaseURL: applying migrations: %v", err)
	}

	return url
}

// startContainer runs a throwaway PostgreSQL and returns its connection string.
func startContainer(t *testing.T) string {
	t.Helper()

	ctx := t.Context()

	container, err := pgmodule.Run(ctx,
		"postgres:17-alpine",
		pgmodule.WithDatabase("roster_test"),
		pgmodule.WithUsername("postgres"),
		pgmodule.WithPassword("password"),
		testcontainers.WithWaitStrategy(
			// Occurrence 2: the entrypoint starts PostgreSQL once to run its
			// initialisation scripts and again for real. Waiting for the first
			// message would connect to a server about to be shut down.
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(containerStartTimeout),
		),
	)
	if err != nil {
		// A missing or unreachable daemon is an environment fact, not a failure
		// of the code under test.
		t.Skipf("skipping: no reachable Docker daemon (%v)\n"+
			"start Docker or Colima, or set %s to a running PostgreSQL",
			err, EnvDatabaseURL)
	}

	t.Cleanup(func() {
		// WithoutCancel: the test's context is already done by cleanup time, and
		// termination still needs to run.
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminateTimeout)
		defer cancel()
		if terminateErr := container.Terminate(stopCtx); terminateErr != nil {
			t.Errorf("terminating test container: %v", terminateErr)
		}
	})

	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("testutil.NewDB: reading container connection string: %v", err)
	}
	return url
}
