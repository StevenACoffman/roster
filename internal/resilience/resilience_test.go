package resilience_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/StevenACoffman/roster/internal/resilience"
)

// Every test here is deterministic: the assertions are about how many times an
// operation ran and which error came back, never about elapsed time. The
// breaker's half-open recovery is time-dependent and is left to the library's own
// tests — waiting for it here would be slow and would flake on a loaded machine.

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

// discardLogger keeps breaker state-change warnings out of the test output.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// testConfig makes the thresholds small so a test can reach them in a few calls,
// and the delays negligible so retrying costs no wall-clock time.
func testConfig() resilience.Config {
	return resilience.Config{
		MaxRetries:       2,
		BaseDelay:        time.Microsecond,
		MaxDelay:         time.Microsecond,
		JitterFactor:     0,
		FailureThreshold: 3,
		SuccessThreshold: 1,
		OpenDelay:        time.Hour, // long: no test here waits for recovery
	}
}

// transient is a SQLSTATE the retry allowlist accepts.
func transient() error {
	return &pgconn.PgError{Code: "40001", Message: "serialization failure"}
}

// permanent is a SQLSTATE that means the database answered and the caller was
// wrong, so it must neither be retried nor count towards the breaker.
func permanent() error {
	return &pgconn.PgError{Code: "23505", Message: "unique violation"}
}

func TestIsRetryable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil is not a failure", err: nil, want: false},
		{name: "serialization failure replays", err: transient(), want: true},
		{name: "deadlock replays", err: &pgconn.PgError{Code: "40P01"}, want: true},
		{name: "connection exception replays", err: &pgconn.PgError{Code: "08000"}, want: true},
		{name: "connection failure replays", err: &pgconn.PgError{Code: "08006"}, want: true},
		{name: "cannot connect now replays", err: &pgconn.PgError{Code: "57P03"}, want: true},
		{name: "unique violation does not", err: permanent(), want: false},
		{name: "check violation does not", err: &pgconn.PgError{Code: "23514"}, want: false},
		{
			name: "an unmodelled sqlstate does not; widening the list is deliberate",
			err:  &pgconn.PgError{Code: "99999"},
			want: false,
		},
		{name: "a cancelled caller does not", err: context.Canceled, want: false},
		{name: "an expired deadline does not", err: context.DeadlineExceeded, want: false},
		{name: "an unrecognised error does not", err: errors.New("who knows"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := resilience.IsRetryable(tt.err); got != tt.want {
				t.Errorf("IsRetryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestReadReplaysATransientFailure(t *testing.T) {
	t.Parallel()

	db := resilience.NewDB(testConfig(), discardLogger())

	calls := 0
	got, err := resilience.Read(t.Context(), db, func(context.Context) (string, error) {
		calls++
		if calls < 3 {
			return "", transient()
		}
		return "recovered", nil
	})

	ok(t, err)
	equals(t, got, "recovered")
	equals(t, calls, 3) // two failures replayed, the third succeeded
}

func TestReadStopsAtMaxRetries(t *testing.T) {
	t.Parallel()

	db := resilience.NewDB(testConfig(), discardLogger())

	calls := 0
	_, err := resilience.Read(t.Context(), db, func(context.Context) (string, error) {
		calls++
		return "", transient()
	})

	if err == nil {
		t.Fatal("a permanently failing read returned no error")
	}
	// MaxRetries is 2, so the original attempt plus two replays.
	equals(t, calls, 3)
}

func TestReadDoesNotReplayAPermanentFailure(t *testing.T) {
	t.Parallel()

	db := resilience.NewDB(testConfig(), discardLogger())

	calls := 0
	_, err := resilience.Read(t.Context(), db, func(context.Context) (string, error) {
		calls++
		return "", permanent()
	})

	if err == nil {
		t.Fatal("a constraint violation returned no error")
	}
	equals(t, calls, 1)
}

// TestWriteNeverReplays is the asymmetry the package exists to enforce. A
// retried insert that already committed would duplicate the row.
func TestWriteNeverReplays(t *testing.T) {
	t.Parallel()

	db := resilience.NewDB(testConfig(), discardLogger())

	calls := 0
	_, err := resilience.Write(t.Context(), db, func(context.Context) (string, error) {
		calls++
		return "", transient() // retryable, and still must not be retried
	})

	if err == nil {
		t.Fatal("a failing write returned no error")
	}
	equals(t, calls, 1)
}

func TestBreakerOpensAfterConsecutiveInfrastructureFailures(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	db := resilience.NewDB(cfg, discardLogger())

	// Writes bypass the retry policy, so each call is exactly one breaker
	// observation — which makes the threshold arithmetic here unambiguous.
	for range cfg.FailureThreshold {
		_, _ = resilience.Write(t.Context(), db, func(context.Context) (string, error) {
			return "", transient()
		})
	}

	if db.Healthy() {
		t.Fatalf("breaker still healthy after %d failures; state = %s",
			cfg.FailureThreshold, db.State())
	}

	// And now it rejects without running the operation at all.
	calls := 0
	_, err := resilience.Write(t.Context(), db, func(context.Context) (string, error) {
		calls++
		return "fine", nil
	})

	if !errors.Is(err, resilience.ErrUnavailable) {
		t.Errorf("error = %v, want one wrapping ErrUnavailable", err)
	}
	equals(t, calls, 0)
}

// TestBreakerIgnoresCallerErrors guards against the failure mode where ordinary
// 404s and constraint violations take the service down.
func TestBreakerIgnoresCallerErrors(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	db := resilience.NewDB(cfg, discardLogger())

	// Far more failures than the threshold, all of them the caller's fault.
	for range cfg.FailureThreshold * 5 {
		_, _ = resilience.Write(t.Context(), db, func(context.Context) (string, error) {
			return "", permanent()
		})
	}

	if !db.Healthy() {
		t.Errorf("constraint violations opened the breaker; state = %s", db.State())
	}
}

func TestNilDBRunsTheOperationDirectly(t *testing.T) {
	t.Parallel()

	// NewHandler may be built without policies, and the operation must still run
	// rather than the nil being dereferenced.
	got, err := resilience.Read(t.Context(), nil, func(context.Context) (int, error) {
		return 42, nil
	})
	ok(t, err)
	equals(t, got, 42)

	got, err = resilience.Write(t.Context(), nil, func(context.Context) (int, error) {
		return 7, nil
	})
	ok(t, err)
	equals(t, got, 7)
}

func TestNilDBReportsHealthy(t *testing.T) {
	t.Parallel()

	var db *resilience.DB
	if !db.Healthy() {
		t.Error("a nil DB reported unhealthy; no policies means nothing is shedding")
	}
	equals(t, db.State(), "closed")
}

func TestReadPropagatesTheZeroValueOnFailure(t *testing.T) {
	t.Parallel()

	db := resilience.NewDB(testConfig(), discardLogger())

	got, err := resilience.Read(t.Context(), db, func(context.Context) (map[string]int, error) {
		return map[string]int{"should": 1}, permanent()
	})
	if err == nil {
		t.Fatal("expected the operation's error")
	}
	if got != nil {
		t.Errorf("got %v on failure, want the zero value", got)
	}
}

func TestRateLimitConfigEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rps  uint
		want bool
	}{
		{name: "zero disables rate limiting", rps: 0, want: false},
		{name: "a positive rate enables it", rps: 1, want: true},
		{name: "the default is enabled", rps: resilience.DefaultRateLimitConfig().RequestsPerSecond, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := resilience.RateLimitConfig{RequestsPerSecond: tt.rps}
			if got := cfg.Enabled(); got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
