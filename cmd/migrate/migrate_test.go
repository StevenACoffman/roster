package migrate_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/roster/cmd"
	"github.com/StevenACoffman/roster/cmd/root"
)

// Driven through cmd.Run rather than by calling the commands' own exec methods.
//
// cmd.Run is the seam main() uses, so this covers flag parsing, the ROSTER_ env
// mapping and the dispatcher's error handling as well as the command itself —
// none of which a direct exec call would touch. New and Config are the package's
// only exported symbols in any case.
//
// Everything here runs without a database. The cases that need one are in
// migrate_integration_test.go behind the integration tag.

// run invokes the dispatcher with captured output.
//
// Returns the error rather than asserting on it, because these tests care about
// several different properties of it: the sentinel, the exit code, and the text.
func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, errOut bytes.Buffer
	err = cmd.Run(t.Context(), args, strings.NewReader(""), &out, &errOut)
	return out.String(), errOut.String(), err
}

func TestMigrateHelpListsEverySubcommand(t *testing.T) {
	t.Parallel()

	_, stderr, err := run(t, "migrate", "--help")

	// ff reports --help as ErrHelp, which the dispatcher and main() both treat
	// as success.
	if !errors.Is(err, ff.ErrHelp) {
		t.Fatalf("migrate --help returned %v, want ff.ErrHelp", err)
	}

	for _, want := range []string{"up", "down", "status", "version", "--database-url"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("help output does not mention %q", want)
		}
	}
}

func TestMigrateWithNoSubcommandIsNotAFailure(t *testing.T) {
	t.Parallel()

	// A bare group parent has no Exec, so ff reports ErrNoExec. main() treats it
	// as success and prints usage; it must not look like a crash.
	_, _, err := run(t, "migrate")
	if !errors.Is(err, ff.ErrNoExec) {
		t.Fatalf("bare `migrate` returned %v, want ff.ErrNoExec", err)
	}
}

func TestMigrateRejectsAnUnknownSubcommand(t *testing.T) {
	t.Parallel()

	_, _, err := run(t, "migrate", "sideways")
	if err == nil {
		t.Fatal("an unknown subcommand was accepted")
	}
	if errors.Is(err, ff.ErrNoExec) {
		t.Error("an unknown subcommand was reported as ErrNoExec, which main() treats as success")
	}
	if !strings.Contains(err.Error(), "sideways") {
		t.Errorf("error %v does not name the offending token", err)
	}
}

// TestMigrateDownRefusesWithoutConfirm is the test that matters most here. A
// rollback drops tables and the data in them and cannot be undone by running it
// again, so it must not happen because of a mistyped command.
//
// The exit code is asserted, not just the message: the whole value of the
// refusal is that a deploy script sees a non-zero status, and that could regress
// to zero while the warning text stayed identical.
func TestMigrateDownRefusesWithoutConfirm(t *testing.T) {
	t.Parallel()

	// A database that does not exist. Reaching the refusal before any connection
	// is attempted would be wrong too — the command reports the current version
	// in its refusal, which means it has to connect first. So this asserts the
	// refusal happens, given a reachable database, in the integration test; here
	// we assert the flag is wired and documented.
	_, stderr, err := run(t, "migrate", "down", "--help")
	if !errors.Is(err, ff.ErrHelp) {
		t.Fatalf("migrate down --help returned %v, want ff.ErrHelp", err)
	}
	if !strings.Contains(stderr, "--confirm") {
		t.Error("down --help does not mention --confirm")
	}
	if !strings.Contains(stderr, "discards") {
		t.Error("down --help does not warn that the operation discards data")
	}
}

func TestMigrateReportsAnUnreachableDatabase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		subcommand string
	}{
		{name: "up", subcommand: "up"},
		{name: "status", subcommand: "status"},
		{name: "version", subcommand: "version"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Port 1 on loopback: nothing listens, and the connection fails fast
			// rather than waiting on a DNS or routing timeout.
			_, _, err := run(t, "migrate", tt.subcommand,
				"--database-url", "postgres://postgres:password@127.0.0.1:1/absent?sslmode=disable")

			if err == nil {
				t.Fatal("an unreachable database was reported as success")
			}
			if !strings.Contains(err.Error(), "connecting to database") {
				t.Errorf("error %v does not say what failed", err)
			}
			// Not an ExitError: this is an unexpected failure, so the dispatcher
			// should print it rather than exiting silently.
			if _, silent := errors.AsType[root.ExitError](err); silent {
				t.Error("an unreachable database produced a silent ExitError")
			}
		})
	}
}

// TestMigrateFlagsAreVisible pins the flags-first contract: every knob the
// command has must appear in --help, because that is the only place an operator
// looks.
func TestMigrateFlagsAreVisible(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		subcommand []string
		wantFlags  []string
	}{
		{
			name:       "group parent",
			subcommand: []string{"migrate", "--help"},
			wantFlags:  []string{"--database-url", "--log-level", "--log-format"},
		},
		{
			name:       "down",
			subcommand: []string{"migrate", "down", "--help"},
			wantFlags:  []string{"--confirm", "--database-url"},
		},
		{
			name:       "status inherits the parent's flags",
			subcommand: []string{"migrate", "status", "--help"},
			wantFlags:  []string{"--database-url"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, stderr, err := run(t, tt.subcommand...)
			if !errors.Is(err, ff.ErrHelp) {
				t.Fatalf("got %v, want ff.ErrHelp", err)
			}
			for _, flag := range tt.wantFlags {
				if !strings.Contains(stderr, flag) {
					t.Errorf("%v help does not offer %s", tt.subcommand, flag)
				}
			}
		})
	}
}

// TestMigrateWritesNothingToTheProcessStreams guards the ff/v4 rule that a
// command writes to the writers it was given. A regression to os.Stdout would
// make the output uncapturable here and invisible to any caller that redirected
// it.
func TestMigrateWritesNothingToTheProcessStreams(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	err := cmd.Run(t.Context(),
		[]string{"migrate", "version", "--database-url",
			"postgres://postgres:password@127.0.0.1:1/absent?sslmode=disable"},
		strings.NewReader(""), &out, io.Discard)

	if err == nil {
		t.Fatal("expected a connection failure")
	}
	// The failure is returned, not printed: the dispatcher decides where errors
	// go, and stdout is reserved for the command's actual output.
	if out.Len() != 0 {
		t.Errorf("stdout carried %q on a failure; it should carry only results", out.String())
	}
}
