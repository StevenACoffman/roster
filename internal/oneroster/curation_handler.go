package oneroster

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/StevenACoffman/roster/internal/db"
)

// The curation shell: resolve the caller, read the clock, merge in the pure
// core, then issue one guarded statement. Every authorization decision lives in
// that statement — see sql/queries/curation.sql.
//
// A guarded write that matches nothing returns pgx.ErrNoRows, which is ambiguous
// between three causes. writeOutcome resolves it; everything else here is a flat
// sequence of steps.

// errStaleWrite reports that the record changed since the caller read it.
var errStaleWrite = errors.New(
	"the record changed since you read it; reload it and reapply your edit")

// planUpdate validates the parts of an update request that do not depend on
// which entity is being updated: who is calling, that the target is named, that
// the mask is usable, and that a concurrency token is present.
//
// Returns the caller's subject, the target's id, the mask paths, and the expected
// version. Any error is already translated, so a caller returns it unchanged.
func (h *Handler) planUpdate(
	ctx context.Context,
	op, sourcedID string,
	mask *fieldmaskpb.FieldMask,
	expected *timestamppb.Timestamp,
	msg proto.Message,
) (subject, id string, paths []string, version pgtype.Timestamptz, err error) {
	subject, err = h.caller(ctx)
	if err != nil {
		return "", "", nil, pgtype.Timestamptz{}, translate(ctx, op, err)
	}

	id, err = requireSourcedID(sourcedID)
	if err != nil {
		return "", "", nil, pgtype.Timestamptz{}, translate(ctx, op, err)
	}

	paths, err = requireMask(mask, msg)
	if err != nil {
		return "", "", nil, pgtype.Timestamptz{}, translate(ctx, op, err)
	}

	version, err = requireExpectedVersion(expected)
	if err != nil {
		return "", "", nil, pgtype.Timestamptz{}, translate(ctx, op, err)
	}

	return subject, id, paths, version, nil
}

// planWrite validates the preamble a create or delete shares.
func (h *Handler) planWrite(
	ctx context.Context, op, sourcedID string,
) (subject, id string, err error) {
	subject, err = h.caller(ctx)
	if err != nil {
		return "", "", translate(ctx, op, err)
	}
	id, err = requireSourcedID(sourcedID)
	if err != nil {
		return "", "", translate(ctx, op, err)
	}
	return subject, id, nil
}

// mergeFault reports a failure to merge an update into the write type.
//
// Nothing a caller sends can cause one: requireMask has already checked the
// paths, so the only remaining causes are a field added to an entity and not to
// its write twin, or a kind changed on one side only. Both are our bugs, so the
// caller gets an opaque Internal and the detail goes to the log.
//
// Separate from translate because that function's fallback reports a database
// failure, which this is not; a log line blaming the database for a descriptor
// mismatch would send someone to the wrong place entirely.
func mergeFault(ctx context.Context, op string, err error) error {
	slog.ErrorContext(ctx, "an update could not be merged into its write type",
		"op", op, "error", err)
	return connect.NewError(connect.CodeInternal, errInternal)
}

// refusedWrite maps the zero-row result of a guarded insert or soft delete.
//
// Neither carries a version check, so unlike an update there is only one thing
// zero rows can mean:
//
//   - on a create, there is no pre-existing row that could have been hidden, so
//     the guard must have refused the caller;
//   - on a delete, the row is not visible to this caller. A row that exists and
//     is already flagged deleted still matches the guard and is flagged again,
//     which is what makes the operation idempotent.
//
// Both answer NotFound, which is also what an out-of-scope read returns, so a
// write cannot be used to discover whether a record exists.
func refusedWrite(ctx context.Context, op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return connect.NewError(connect.CodeNotFound, errNotFound)
	}
	return translate(ctx, op, err)
}

// writeOutcome turns a guarded update's error into the one a client should see.
//
// A guarded UPDATE affects no rows for three different reasons, and the driver
// reports all three identically as pgx.ErrNoRows:
//
//   - the row does not exist;
//   - it exists, but the caller holds no curating grant over it;
//   - it exists and is curatable, but its date_last_modified has moved.
//
// Telling a curator "not found" when their edit actually lost a race would send
// them hunting for a record that is still there. The probe separates the third
// case out as Aborted. The first two stay merged into NotFound deliberately, so
// that a write cannot be used to discover whether an out-of-scope record exists.
func (h *Handler) writeOutcome(
	ctx context.Context,
	op string,
	err error,
	visible func(context.Context) (bool, error),
) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return translate(ctx, op, err)
	}

	curatable, probeErr := visible(ctx)
	if probeErr != nil {
		return translate(ctx, op, probeErr)
	}
	if curatable {
		return connect.NewError(connect.CodeAborted, errStaleWrite)
	}
	return connect.NewError(connect.CodeNotFound, errNotFound)
}

// inTx runs op inside one transaction, committing on success.
//
// Needed where an entity and its required children must land together: a class
// with its terms, a user with their roles. Without the transaction a failure
// between the two inserts would leave exactly the spec-invalid state the
// validation exists to prevent — and for a user, one that no caller could then
// reach to correct.
//
// Ensures: on any error the transaction is rolled back and nothing is visible.
func (h *Handler) inTx(ctx context.Context, op func(*db.Queries) error) error {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return err
	}
	// Unconditional rollback: after a successful Commit this is a no-op, and on
	// every other path it is what releases the locks.
	defer func() { _ = tx.Rollback(ctx) }()

	if err := op(h.queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
