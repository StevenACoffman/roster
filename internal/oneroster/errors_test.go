package oneroster

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/StevenACoffman/roster/internal/resilience"
)

// translate maps eleven distinct failures onto Connect codes and takes a context
// only for logging, which makes it a pure mapping and a unit test the right
// place to pin it.
//
// It is tested here because the mapping regressed once and was caught by running
// a live server against a killed database: the circuit-breaker rejection came
// back as Internal rather than Unavailable, which tells a client not to retry
// exactly when it should. A pure function should not need a container and a
// deliberate outage to verify.

func TestTranslate(t *testing.T) {
	t.Parallel()

	// pgError builds a server-side error, which by definition means the database
	// answered — so it is the caller who was wrong, not the infrastructure.
	pgError := func(code string) error {
		return &pgconn.PgError{Code: code, Message: "from the server"}
	}

	tests := []struct {
		name     string
		err      error
		wantCode connect.Code
		// wantMessage, where the client-facing text matters: these are the cases
		// where the message is deliberately less specific than the cause.
		wantMessage string
	}{
		{
			name:     "nil is not an error",
			err:      nil,
			wantCode: 0,
		},
		{
			name:        "a core validation failure is the caller's own and is echoed",
			err:         fmt.Errorf("%w: page_token is not valid base64url", errInvalid),
			wantCode:    connect.CodeInvalidArgument,
			wantMessage: "invalid argument: page_token is not valid base64url",
		},
		{
			name:        "an anonymous request",
			err:         errUnauthenticated,
			wantCode:    connect.CodeUnauthenticated,
			wantMessage: errUnauthenticated.Error(),
		},
		{
			name:        "a handler built without a pool",
			err:         errNoDatabase,
			wantCode:    connect.CodeUnavailable,
			wantMessage: errNoDatabase.Error(),
		},
		{
			// The case that regressed. Unavailable asks the client to retry;
			// Internal would make a transient outage look like a bug and suppress
			// the retry the breaker is asking for.
			name:        "a circuit-breaker rejection asks the caller to retry",
			err:         fmt.Errorf("%w: database circuit breaker is open", resilience.ErrUnavailable),
			wantCode:    connect.CodeUnavailable,
			wantMessage: errUnavailable.Error(),
		},
		{
			name:        "no rows is not found, wrapped",
			err:         fmt.Errorf("loading the org: %w", pgx.ErrNoRows),
			wantCode:    connect.CodeNotFound,
			wantMessage: errNotFound.Error(),
		},
		{
			name:        "a unique violation already exists",
			err:         pgError("23505"),
			wantCode:    connect.CodeAlreadyExists,
			wantMessage: errAlreadyExists.Error(),
		},
		{
			name:        "a foreign key violation is a failed precondition",
			err:         pgError("23503"),
			wantCode:    connect.CodeFailedPrecondition,
			wantMessage: errConstraint.Error(),
		},
		{
			name:        "a check violation is a failed precondition",
			err:         pgError("23514"),
			wantCode:    connect.CodeFailedPrecondition,
			wantMessage: errConstraint.Error(),
		},
		{
			name:        "a not-null violation is a failed precondition",
			err:         pgError("23502"),
			wantCode:    connect.CodeFailedPrecondition,
			wantMessage: errConstraint.Error(),
		},
		{
			name:        "a serialization failure is worth retrying",
			err:         pgError("40001"),
			wantCode:    connect.CodeAborted,
			wantMessage: errConflict.Error(),
		},
		{
			name:        "a deadlock is worth retrying",
			err:         pgError("40P01"),
			wantCode:    connect.CodeAborted,
			wantMessage: errConflict.Error(),
		},
		{
			// An unmodelled SQLSTATE must not leak the server's own text: it can
			// name columns, constraints and values.
			name:        "an unmodelled sqlstate becomes internal and says nothing",
			err:         pgError("42601"),
			wantCode:    connect.CodeInternal,
			wantMessage: errInternal.Error(),
		},
		{
			name:        "an unrecognised error becomes internal",
			err:         errors.New("something nobody modelled"),
			wantCode:    connect.CodeInternal,
			wantMessage: errInternal.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := translate(t.Context(), "Test.Op", tt.err)

			if tt.err == nil {
				if got != nil {
					t.Fatalf("translate(nil) = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("translate(%v) = nil, want a %v error", tt.err, tt.wantCode)
			}

			var connectErr *connect.Error
			if !errors.As(got, &connectErr) {
				t.Fatalf("translate returned %v (%T), want a *connect.Error", got, got)
			}
			if connectErr.Code() != tt.wantCode {
				t.Errorf("code = %v, want %v", connectErr.Code(), tt.wantCode)
			}
			if tt.wantMessage != "" && connectErr.Message() != tt.wantMessage {
				t.Errorf("message = %q, want %q", connectErr.Message(), tt.wantMessage)
			}
		})
	}
}

// TestTranslateNeverLeaksServerText guards the property that matters for a
// service holding student records: a database error's own message can name
// columns, constraint names and the offending values, and none of that belongs
// in a response to a caller who may be untrusted.
func TestTranslateNeverLeaksServerText(t *testing.T) {
	t.Parallel()

	const secret = "pg_constraint_detail_with_student_name_Bo_Chen"

	for _, sqlState := range []string{
		"23505", "23503", "23514", "23502", "40001", "40P01", "42601", "99999",
	} {
		err := translate(t.Context(), "Test.Op", &pgconn.PgError{
			Code:    sqlState,
			Message: secret,
			Detail:  secret,
		})

		var connectErr *connect.Error
		if !errors.As(err, &connectErr) {
			t.Fatalf("sqlstate %s: got %v, want a *connect.Error", sqlState, err)
		}

		// The client-facing message must be one of the fixed strings, never
		// anything derived from what the server said.
		allowed := []string{
			errAlreadyExists.Error(),
			errConstraint.Error(),
			errConflict.Error(),
			errInternal.Error(),
		}
		if got := connectErr.Message(); !slices.Contains(allowed, got) {
			t.Errorf("sqlstate %s produced message %q, which is not one of the fixed "+
				"client-facing messages %v", sqlState, got, allowed)
		}
		if strings.Contains(connectErr.Error(), secret) {
			t.Errorf("sqlstate %s leaked the server's own text into the response", sqlState)
		}
	}
}

// TestTranslatePreservesTheCoreMessage is the counterpart: a validation failure
// is the caller's own mistake, so echoing it is both safe and the only way they
// can fix the request.
func TestTranslatePreservesTheCoreMessage(t *testing.T) {
	t.Parallel()

	err := translate(t.Context(), "Test.Op",
		fmt.Errorf("%w: update_mask names %q, which is not a field of Org", errInvalid, "nonsense"))

	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("got %v, want a *connect.Error", err)
	}
	if !errors.Is(connectErr, errInvalid) {
		t.Error("the returned error no longer wraps errInvalid")
	}
	for _, want := range []string{"update_mask", "nonsense", "not a field"} {
		if !strings.Contains(connectErr.Message(), want) {
			t.Errorf("message %q does not mention %q", connectErr.Message(), want)
		}
	}
}
