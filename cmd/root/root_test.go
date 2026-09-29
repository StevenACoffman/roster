package root_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/StevenACoffman/roster/cmd/root"
)

// ExitError is the silent-exit contract main() depends on: run() checks for it
// with errors.As and exits with its value instead of printing "error: ...".
// A command that has already told the operator what went wrong returns one.
//
// root.New is deliberately untested. It returns a struct whose fields are its
// arguments, so a test would assert that the compiler assigns them.

func TestExitErrorCarriesItsCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		code root.ExitError
		want string
	}{
		{name: "the conventional failure", code: root.ExitError(1), want: "exit status 1"},
		{name: "a distinct code", code: root.ExitError(42), want: "exit status 42"},
		{
			// Zero would exit successfully, which is not what returning an error
			// means. Nothing should construct this, but Error() must still be
			// honest about it rather than reporting something else.
			name: "zero still reports zero",
			code: root.ExitError(0),
			want: "exit status 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.code.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestExitErrorSurvivesWrapping is the property main() relies on: a command may
// wrap the ExitError on its way out, and errors.As must still find it and the
// code must still be readable.
func TestExitErrorSurvivesWrapping(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("migrate down: %w", root.ExitError(1))

	got, found := errors.AsType[root.ExitError](wrapped)
	if !found {
		t.Fatalf("errors.AsType did not find an ExitError in %v", wrapped)
	}
	if int(got) != 1 {
		t.Errorf("recovered code %d, want 1", int(got))
	}
}

// TestExitErrorIsNotConfusedWithOtherErrors guards the other half: main() prints
// an ordinary error and exits 1, so a plain error must not be mistaken for a
// request to exit silently with some arbitrary code.
func TestExitErrorIsNotConfusedWithOtherErrors(t *testing.T) {
	t.Parallel()

	if _, found := errors.AsType[root.ExitError](errors.New("an ordinary failure")); found {
		t.Error("an ordinary error was matched as an ExitError")
	}
}
