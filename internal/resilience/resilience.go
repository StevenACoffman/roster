// Package resilience holds the database-facing failure policies: retries, a
// circuit breaker, and an admission rate limit.
//
// Two decisions shape the whole package.
//
// The retry predicate names specific SQLSTATEs rather than retrying anything
// that failed, because a replay is only safe for a failure known not to have
// applied. Anything unrecognised is not retried — replaying an unknown error
// risks performing a write twice, and this service has no idempotency key that
// would make that harmless.
//
// Reads and writes get different policies but share one breaker. A failing write
// must help open it, and once open it must reject both.
package resilience

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/failsafe-go/failsafe-go"
	"github.com/failsafe-go/failsafe-go/circuitbreaker"
	"github.com/failsafe-go/failsafe-go/retrypolicy"
	"github.com/jackc/pgx/v5/pgconn"
)

// SQLSTATEs that are transient and safe to replay: a serialization failure and a
// deadlock both rolled the transaction back, and the 08xxx class means the
// statement never reached a backend.
const (
	sqlStateSerializationFailure = "40001"
	sqlStateDeadlockDetected     = "40P01"
	sqlStateConnectionException  = "08000"
	sqlStateConnectionFailure    = "08006"
	sqlStateCannotConnectNow     = "57P03"
)

// Config tunes the database-facing policies.
type Config struct {
	// MaxRetries replays of a transient failure; zero disables retrying.
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	// JitterFactor keeps a recovering fleet from retrying in lockstep, which
	// would deliver the database a synchronised thundering herd just as it came
	// back.
	JitterFactor float64
	// FailureThreshold consecutive failures open the breaker, SuccessThreshold
	// consecutive successes close it again, and OpenDelay is how long it stays
	// open before admitting a trial request.
	FailureThreshold uint
	SuccessThreshold uint
	OpenDelay        time.Duration
}

// DefaultConfig returns policy settings suited to a PostgreSQL dependency on the
// same network.
func DefaultConfig() Config {
	return Config{
		MaxRetries:       3,
		BaseDelay:        20 * time.Millisecond,
		MaxDelay:         500 * time.Millisecond,
		JitterFactor:     0.3,
		FailureThreshold: 5,
		SuccessThreshold: 2,
		OpenDelay:        5 * time.Second,
	}
}

// ErrUnavailable reports that the breaker is open, so the call was rejected
// without being attempted.
var ErrUnavailable = errors.New("dependency unavailable")

// DB applies the database policies. Safe for concurrent use.
type DB struct {
	query   failsafe.Executor[any] // retry, then breaker
	exec    failsafe.Executor[any] // breaker only
	breaker circuitbreaker.CircuitBreaker[any]
}

// NewDB builds the policies.
//
// Retry is outermost, so the breaker counts one logical call once rather than
// counting every replay of it — otherwise a single failing operation with three
// retries would look like four failures and open the breaker far too eagerly.
func NewDB(cfg Config, logger *slog.Logger) *DB {
	breaker := circuitbreaker.NewBuilder[any]().
		HandleIf(func(_ any, err error) bool {
			// A validation failure or a missing row is a normal outcome, not
			// evidence that the database is unhealthy. Letting those trip the
			// breaker would take the service down over ordinary 404s.
			return err != nil && isInfrastructureFailure(err)
		}).
		WithFailureThreshold(cfg.FailureThreshold).
		WithSuccessThreshold(cfg.SuccessThreshold).
		WithDelay(cfg.OpenDelay).
		OnStateChanged(func(event circuitbreaker.StateChangedEvent) {
			logger.Warn("database circuit breaker changed state",
				"from", event.OldState.String(),
				"to", event.NewState.String(),
			)
		}).
		Build()

	retry := retrypolicy.NewBuilder[any]().
		HandleIf(func(_ any, err error) bool {
			return err != nil && IsRetryable(err)
		}).
		WithMaxRetries(cfg.MaxRetries).
		WithBackoff(cfg.BaseDelay, cfg.MaxDelay).
		WithJitterFactor(cfg.JitterFactor).
		Build()

	return &DB{
		query: failsafe.With[any](retry, breaker),
		// Writes get the breaker WITHOUT the retry policy. Replaying a write that
		// may already have committed is worse than surfacing the error: an
		// insert would duplicate, and a version-checked update would re-apply
		// against a version that has since moved. Give the service an
		// idempotency key and writes could join the retry path; until then they
		// only fail fast.
		exec:    failsafe.With[any](breaker),
		breaker: breaker,
	}
}

// State reports the breaker's current state, for the readiness endpoint.
//
// A nil DB reports "closed": no policies means nothing is shedding, which is
// what a reader of /readyz needs to know.
func (d *DB) State() string {
	if d == nil {
		return circuitbreaker.ClosedState.String()
	}
	return d.breaker.State().String()
}

// Healthy reports whether the breaker is admitting calls.
//
// Used by the readiness endpoint: an instance whose breaker is open cannot serve
// requests, and should be taken out of rotation until it recovers. Liveness
// deliberately does not consult this — a breaker open because the database is
// down must not get the container restarted.
func (d *DB) Healthy() bool {
	if d == nil {
		return true
	}
	return !d.breaker.IsOpen()
}

// Read runs an idempotent operation under retry and the breaker.
//
// Requires: op is idempotent, because a transient failure replays it.
// Ensures:  a nil db runs op directly; an open breaker returns ErrUnavailable
//
//	without invoking op at all.
func Read[T any](ctx context.Context, db *DB, op func(context.Context) (T, error)) (T, error) {
	if db == nil {
		return op(ctx)
	}
	return runUnder(ctx, db.query, op)
}

// Write runs a non-idempotent operation under the breaker alone.
//
// Ensures: op runs at most once; an open breaker returns ErrUnavailable without
// invoking op.
func Write[T any](ctx context.Context, db *DB, op func(context.Context) (T, error)) (T, error) {
	if db == nil {
		return op(ctx)
	}
	return runUnder(ctx, db.exec, op)
}

// runUnder unboxes the any-typed result failsafe works in.
func runUnder[T any](
	ctx context.Context, executor failsafe.Executor[any], op func(context.Context) (T, error),
) (T, error) {
	var zero T

	result, err := executor.WithContext(ctx).Get(func() (any, error) {
		return op(ctx)
	})
	if err != nil {
		if errors.Is(err, circuitbreaker.ErrOpen) {
			return zero, fmt.Errorf("%w: database circuit breaker is open", ErrUnavailable)
		}
		return zero, err
	}
	if result == nil {
		return zero, nil
	}
	typed, ok := result.(T)
	if !ok {
		return zero, fmt.Errorf("resilience: expected %T from the execution, got %T", zero, result)
	}
	return typed, nil
}

// IsRetryable reports whether err is a transient failure that is safe to replay.
//
// Defaults to false. That default is the point: retrying an unrecognised failure
// risks applying a write twice, so widening the allowlist below should be a
// deliberate act rather than something that happens by accident.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	// The caller gave up or ran out of time; replaying would ignore that.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		// No server-side error means no backend processed the statement. pgx's
		// own SafeToRetry knows which transport failures guarantee that.
		return pgconn.SafeToRetry(err) ||
			errors.Is(err, pgconn.ErrConnClosed) ||
			isNetworkFailure(err)
	}

	switch pgErr.Code {
	case sqlStateSerializationFailure,
		sqlStateDeadlockDetected,
		sqlStateConnectionException,
		sqlStateConnectionFailure,
		sqlStateCannotConnectNow:
		return true
	default:
		return false
	}
}

// isInfrastructureFailure distinguishes an unhealthy database from a bad request.
//
// Only the former should count towards opening the breaker. A constraint
// violation means the database answered — it is up, and the caller was wrong.
func isInfrastructureFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if IsRetryable(err) {
		return true
	}

	// A server-side error carrying a SQLSTATE means the database responded, so
	// it is healthy whatever it said.
	if _, isPgError := errors.AsType[*pgconn.PgError](err); isPgError {
		return false
	}
	return isNetworkFailure(err)
}

// isNetworkFailure reports a transport problem rather than a server response.
func isNetworkFailure(err error) bool {
	if _, isConnectError := errors.AsType[*pgconn.ConnectError](err); isConnectError {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}
