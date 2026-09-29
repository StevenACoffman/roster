package oneroster

import (
	"context"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
)

// GetRoster returns every entity in the caller's scope as one snapshot.
//
// The seven entity loads run inside a single repeatable-read transaction so the
// snapshot is internally consistent: without it, a bulk import committing
// between the class load and the enrollment load would produce enrollments
// referencing classes absent from the same response. Repeatable read rather than
// serializable because this is a read-only transaction — it needs a stable
// snapshot, and it makes no check-then-act decision that could produce write
// skew (§8 Transaction Isolation).
//
// Loads run sequentially rather than concurrently: they share one transaction,
// and a pgx transaction is not safe for concurrent use. Parallelising would need
// a connection per entity and would lose the shared snapshot that is the point.
func (h *Handler) GetRoster(
	ctx context.Context, req *connect.Request[v1.GetRosterRequest],
) (*connect.Response[v1.GetRosterResponse], error) {
	const op = "Handler.GetRoster"

	subject, err := h.caller(ctx)
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, translate(ctx, op, err)
	}
	// Rollback on every path: this transaction only reads, so there is nothing to
	// commit, and an early return must not leave it holding a snapshot open.
	defer func() { _ = tx.Rollback(ctx) }()

	// A handler bound to the transaction, so every loader below reads from the
	// same snapshot instead of the pool.
	txHandler := &Handler{pool: h.pool, queries: h.queries.WithTx(tx)}

	roster := &v1.Roster{}
	for _, load := range txHandler.rosterSources(req.Msg.GetOrgSourcedId()) {
		if err := load(ctx, subject, roster); err != nil {
			return nil, translate(ctx, op, err)
		}
	}

	return connect.NewResponse(&v1.GetRosterResponse{Roster: roster}), nil
}
