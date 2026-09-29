//go:build integration

package postgres_test

import (
	"flag"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/StevenACoffman/roster/internal/logging"
	"github.com/StevenACoffman/roster/internal/postgres"
	"github.com/StevenACoffman/roster/internal/testutil"
)

// An external test package, so these exercise the exported API a caller actually
// has rather than reaching into the package's internals.

// update regenerates the golden file instead of comparing against it.
var update = flag.Bool("update", false, "update golden files")

func ok(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func equals[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// quietLogger keeps migration progress out of the test output. A failure is
// reported through the returned error, so the log adds only noise here.
func quietLogger() *slog.Logger {
	return logging.New(os.Stderr, "error", "text")
}

// userTables lists the application tables in the public schema, excluding
// goose's own bookkeeping. Ordered in SQL so the result is stable without
// sorting in Go.
func userTables(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()

	rows, err := pool.Query(t.Context(), `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = 'public'
		  AND table_type = 'BASE TABLE'
		  AND table_name <> 'goose_db_version'
		ORDER BY table_name`)
	ok(t, err)
	defer rows.Close()

	names := make([]string, 0, 32)
	for rows.Next() {
		var name string
		ok(t, rows.Scan(&name))
		names = append(names, name)
	}
	ok(t, rows.Err())
	return names
}

func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t) // the harness has already migrated once
	ctx := t.Context()

	first, err := postgres.GetDBVersion(ctx, pool)
	ok(t, err)

	// Running again must be a no-op, not an error and not a re-application.
	ok(t, postgres.Migrate(ctx, pool, quietLogger()))

	second, err := postgres.GetDBVersion(ctx, pool)
	ok(t, err)
	equals(t, second, first)
}

func TestMigrateDownAndUpRoundTrips(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	ctx := t.Context()

	top, err := postgres.GetDBVersion(ctx, pool)
	ok(t, err)
	if top == 0 {
		t.Fatal("harness produced an unmigrated database")
	}

	// Walk all the way down. A Down section that failed to drop something would
	// surface as the next Down erroring on the leftover object.
	for version := top; version > 0; version-- {
		ok(t, postgres.MigrateDown(ctx, pool, quietLogger()))

		at, versionErr := postgres.GetDBVersion(ctx, pool)
		ok(t, versionErr)
		equals(t, at, version-1)
	}

	if remaining := userTables(t, pool); len(remaining) != 0 {
		t.Errorf("after a full rollback these tables survived: %v", remaining)
	}

	// Back up again: a Down that left the schema subtly wrong fails here rather
	// than passing silently.
	ok(t, postgres.Migrate(ctx, pool, quietLogger()))

	back, err := postgres.GetDBVersion(ctx, pool)
	ok(t, err)
	equals(t, back, top)
}

func TestMigrationStatusReportsEveryMigrationApplied(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)

	statuses, err := postgres.MigrationStatus(t.Context(), pool)
	ok(t, err)

	if len(statuses) == 0 {
		t.Fatal("no migrations reported; the embedded FS may be empty")
	}
	for _, s := range statuses {
		if s.State != goose.StateApplied {
			t.Errorf("migration %d (%s) is %s after the harness migrated it; want applied",
				s.Source.Version, s.Source.Path, s.State)
		}
	}
}

// TestSchemaMatchesGoldenTableList is a characterization test: it pins the set of
// tables the migrations produce, so an unintended schema change shows up as a
// diff here rather than as a surprise in a query months later.
//
// Regenerate with:
//
//	go test -tags=integration ./internal/postgres/ -update
//
// then read the diff before committing it.
func TestSchemaMatchesGoldenTableList(t *testing.T) {
	t.Parallel()

	pool := testutil.NewDB(t)
	got := strings.Join(userTables(t, pool), "\n") + "\n"

	// Relative path: go test sets the working directory to the package directory.
	const golden = "testdata/schema_tables.golden"

	if *update {
		ok(t, os.MkdirAll("testdata", 0o750))
		ok(t, os.WriteFile(golden, []byte(got), 0o600))
		t.Logf("updated %s", golden)
		return
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("reading %s (run with -update to create it): %v", golden, err)
	}
	if got != string(want) {
		t.Errorf("the set of tables changed\ngot:\n%s\nwant:\n%s", got, want)
	}
}
