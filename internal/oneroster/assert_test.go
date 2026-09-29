//go:build integration

package oneroster

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
)

// Assertion helpers, per the testing conventions: three small ones defined in
// the package rather than an assertion library. Each calls t.Helper() so a
// failure points at the call site, and each fails the test itself rather than
// returning an error for the caller to check.

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

// wantCode asserts that err is a Connect error carrying the given code.
//
// Specific to this package because every RPC failure is expected to arrive as
// one: a bare error reaching a caller would mean translate() was bypassed.
func wantCode(t *testing.T, err error, want connect.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("got no error, want %v", want)
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("got %v (%T), want a *connect.Error with code %v", err, err, want)
	}
	if connectErr.Code() != want {
		t.Fatalf("got code %v (%v), want %v", connectErr.Code(), connectErr.Message(), want)
	}
}

// pgText wraps a string for a nullable column, or returns NULL for the empty
// string. Local to the tests: production code uses the typed helpers in
// tostorage.go, which distinguish absent from empty.
func pgText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
