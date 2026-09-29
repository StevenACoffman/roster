package oneroster

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/StevenACoffman/roster/internal/resilience"
)

// PostgreSQL SQLSTATE codes this service distinguishes.
// https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	sqlStateUniqueViolation     = "23505"
	sqlStateForeignKeyViolation = "23503"
	sqlStateCheckViolation      = "23514"
	sqlStateNotNullViolation    = "23502"
	sqlStateSerializationFail   = "40001"
	sqlStateDeadlockDetected    = "40P01"
)

// Client-facing messages. Each says less than the error behind it: the detail
// goes to the log, where an operator can reach it, and not to a caller who may
// be untrusted.
var (
	errNotFound        = errors.New("not found")
	errAlreadyExists   = errors.New("already exists")
	errConflict        = errors.New("conflicting concurrent update, retry the request")
	errConstraint      = errors.New("request violates a data constraint")
	errInternal        = errors.New("internal error")
	errNoDatabase      = errors.New("database is not configured")
	errUnavailable     = errors.New("service temporarily unavailable, retry shortly")
	errUnauthenticated = errors.New("request carries no authenticated identity")
)

// translate converts a core or database error into the Connect error a client
// should see.
//
// This is the boundary §3 requires: no pgx or pgconn error escapes past here.
// Anything unrecognized becomes Internal and is logged with op, so the cause
// stays recoverable from the logs while the response stays uninformative.
func translate(ctx context.Context, op string, err error) error {
	if err == nil {
		return nil
	}

	// A validation failure from the core is the caller's own mistake, so echoing
	// it back is both safe and useful.
	if errors.Is(err, errInvalid) {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	if errors.Is(err, errUnauthenticated) {
		return connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	// Checked before the SQLSTATE cases because neither carries one: the request
	// was never attempted. Unavailable asks the client to retry, where Internal
	// would make a transient outage look like a bug and suppress exactly the
	// retry the breaker is asking for.
	if errors.Is(err, errNoDatabase) {
		slog.WarnContext(ctx, "request rejected before reaching the database",
			"op", op, "error", err)
		return connect.NewError(connect.CodeUnavailable, errNoDatabase)
	}
	if errors.Is(err, resilience.ErrUnavailable) {
		slog.WarnContext(ctx, "request shed by the circuit breaker",
			"op", op, "error", err)
		return connect.NewError(connect.CodeUnavailable, errUnavailable)
	}
	// A scoped read returning no rows is indistinguishable from one the caller
	// may not see, and deliberately so: NotFound for both denies an out-of-scope
	// caller the ability to probe for an entity's existence.
	if errors.Is(err, pgx.ErrNoRows) {
		return connect.NewError(connect.CodeNotFound, errNotFound)
	}

	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		if code := connectCodeForSQLState(pgErr.Code); code != connect.CodeInternal {
			slog.DebugContext(ctx, "database constraint rejected the request",
				"op", op, "sqlstate", pgErr.Code, "error", err)
			return connect.NewError(code, messageForSQLState(pgErr.Code))
		}
	}

	slog.ErrorContext(ctx, "database operation failed", "op", op, "error", err)
	return connect.NewError(connect.CodeInternal, errInternal)
}

// connectCodeForSQLState maps a SQLSTATE onto an RPC code, Internal when the
// state is not one this service models.
func connectCodeForSQLState(sqlState string) connect.Code {
	switch sqlState {
	case sqlStateUniqueViolation:
		return connect.CodeAlreadyExists
	case sqlStateForeignKeyViolation, sqlStateCheckViolation, sqlStateNotNullViolation:
		return connect.CodeFailedPrecondition
	case sqlStateSerializationFail, sqlStateDeadlockDetected:
		// Aborted tells a well-behaved client the request is worth retrying.
		return connect.CodeAborted
	default:
		return connect.CodeInternal
	}
}

// messageForSQLState is the client-facing message for a modelled SQLSTATE.
func messageForSQLState(sqlState string) error {
	switch sqlState {
	case sqlStateUniqueViolation:
		return errAlreadyExists
	case sqlStateSerializationFail, sqlStateDeadlockDetected:
		return errConflict
	default:
		return errConstraint
	}
}
