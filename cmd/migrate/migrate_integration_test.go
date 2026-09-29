//go:build integration

package migrate_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/StevenACoffman/roster/cmd"
	"github.com/StevenACoffman/roster/cmd/root"
	"github.com/StevenACoffman/roster/internal/testutil"
)

// The migration lifecycle against a real database, driven through cmd.Run.
//
// Asserting on captured stdout is only possible because the migration logger is
// a parameter rather than slog.Default(): before that change the progress lines
// went straight to the process's stderr, where no test could see them.

// runAgainst invokes a migrate subcommand against the given database.
func runAgainst(t *testing.T, databaseURL string, args ...string) (stdout string, err error) {
	t.Helper()

	var out bytes.Buffer
	full := append([]string{"migrate"}, args...)
	full = append(full, "--database-url", databaseURL, "--log-level", "error")

	err = cmd.Run(t.Context(), full, strings.NewReader(""), &out, io.Discard)
	return out.String(), err
}

// TestMigrateLifecycle walks the sequence an operator actually performs, in one
// flat test: a deploy runs status, then up, then possibly down, and each step's
// output is what they read to decide the next one.
func TestMigrateLifecycle(t *testing.T) {
	t.Parallel()

	// A container with no migrations applied. NewDatabaseURL migrates, so this
	// deliberately starts from the migrated state and rolls back first, which is
	// also the more interesting direction to verify.
	url := testutil.NewDatabaseURL(t)

	// --- version reports a bare number, for a deploy script to compare ---
	out, err := runAgainst(t, url, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	topVersion := strings.TrimSpace(out)
	if topVersion == "0" {
		t.Fatal("the harness produced an unmigrated database")
	}
	if strings.ContainsAny(topVersion, " \t") {
		t.Errorf("version printed %q; a script needs a bare number", topVersion)
	}

	// --- up on an already-migrated database is a no-op, not an error ---
	out, err = runAgainst(t, url, "up")
	if err != nil {
		t.Fatalf("up on an up-to-date database: %v", err)
	}
	if !strings.Contains(out, "already up to date") {
		t.Errorf("up said %q, want it to report that nothing was pending", strings.TrimSpace(out))
	}

	// --- status lists every migration and its state ---
	out, err = runAgainst(t, url, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"VERSION", "STATE", "001_oneroster.sql", "002_authorization.sql", "applied"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output does not mention %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "database is at version "+topVersion) {
		t.Errorf("status does not report the current version %q:\n%s", topVersion, out)
	}

	// --- down without --confirm refuses and exits non-zero ---
	out, err = runAgainst(t, url, "down")
	if err == nil {
		t.Fatal("down rolled back without --confirm")
	}
	exitErr, isExit := errors.AsType[root.ExitError](err)
	if !isExit {
		t.Fatalf("down without --confirm returned %v, want a root.ExitError", err)
	}
	if int(exitErr) != 1 {
		t.Errorf("exit code %d, want 1", int(exitErr))
	}
	// The refusal goes to stderr, so stdout stays clean for results.
	if strings.TrimSpace(out) != "" {
		t.Errorf("the refusal wrote %q to stdout; it belongs on stderr", out)
	}

	// And nothing was rolled back.
	out, err = runAgainst(t, url, "version")
	if err != nil {
		t.Fatalf("version after the refusal: %v", err)
	}
	if strings.TrimSpace(out) != topVersion {
		t.Errorf("the refused rollback changed the version to %q, want %q",
			strings.TrimSpace(out), topVersion)
	}

	// --- down --confirm rolls back exactly one migration ---
	out, err = runAgainst(t, url, "down", "--confirm")
	if err != nil {
		t.Fatalf("down --confirm: %v", err)
	}
	if !strings.Contains(out, "rolled back from version "+topVersion) {
		t.Errorf("down reported %q, want it to name the versions it moved between",
			strings.TrimSpace(out))
	}

	// --- and up brings it back, so the pair round-trips ---
	out, err = runAgainst(t, url, "up")
	if err != nil {
		t.Fatalf("up after a rollback: %v", err)
	}
	if !strings.Contains(out, "to "+topVersion) {
		t.Errorf("up reported %q, want it to return to version %q",
			strings.TrimSpace(out), topVersion)
	}
}

// TestMigrateDownToEmptyThenUp exercises the full rollback, which is the path a
// Down section's correctness actually depends on: dropping everything and
// rebuilding it proves each Down undid what its Up created.
func TestMigrateDownToEmptyThenUp(t *testing.T) {
	t.Parallel()

	url := testutil.NewDatabaseURL(t)

	out, err := runAgainst(t, url, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	top := strings.TrimSpace(out)

	// Roll all the way down. The loop is bounded so a Down that fails to advance
	// fails the test rather than spinning.
	for range 10 {
		out, err = runAgainst(t, url, "version")
		if err != nil {
			t.Fatalf("version: %v", err)
		}
		if strings.TrimSpace(out) == "0" {
			break
		}
		if _, err = runAgainst(t, url, "down", "--confirm"); err != nil {
			t.Fatalf("down --confirm: %v", err)
		}
	}

	out, err = runAgainst(t, url, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if got := strings.TrimSpace(out); got != "0" {
		t.Fatalf("version after a full rollback is %q, want 0", got)
	}

	// Rolling back further is a no-op, not an error: there is nothing to undo,
	// and that is a success rather than a failure to report.
	out, err = runAgainst(t, url, "down", "--confirm")
	if err != nil {
		t.Fatalf("down on an empty database: %v", err)
	}
	if !strings.Contains(out, "nothing to roll back") {
		t.Errorf("down on an empty database said %q", strings.TrimSpace(out))
	}

	// And back up to the top.
	if _, err = runAgainst(t, url, "up"); err != nil {
		t.Fatalf("up from empty: %v", err)
	}
	out, err = runAgainst(t, url, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if got := strings.TrimSpace(out); got != top {
		t.Errorf("version after rebuilding is %q, want %q", got, top)
	}
}

// TestMigrateLogsGoToTheGivenWriter pins the fix that made these tests
// possible: the migration logger writes to the command's stderr, not the
// process's.
func TestMigrateLogsGoToTheGivenWriter(t *testing.T) {
	t.Parallel()

	url := testutil.NewDatabaseURL(t)

	// Roll back so that the next `up` has something to log about.
	if _, err := runAgainst(t, url, "down", "--confirm"); err != nil {
		t.Fatalf("down --confirm: %v", err)
	}

	var out, errOut bytes.Buffer
	err := cmd.Run(t.Context(),
		[]string{"migrate", "up", "--database-url", url, "--log-format", "json"},
		strings.NewReader(""), &out, &errOut)
	if err != nil {
		t.Fatalf("up: %v", err)
	}

	// The progress line is a log record, so it belongs on stderr in the format
	// asked for.
	if !strings.Contains(errOut.String(), `"msg":"applied migration"`) {
		t.Errorf("the migration log did not reach the given stderr:\n%s", errOut.String())
	}
	// And the human-readable summary is the command's result, so it is stdout.
	if !strings.Contains(out.String(), "migrated from version") {
		t.Errorf("the summary did not reach the given stdout:\n%s", out.String())
	}
}
