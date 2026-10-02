package resilience_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/StevenACoffman/roster/internal/resilience"
)

// emptypb.Empty carries these requests rather than a generated roster message.
// This package knows nothing about rosters, and a test that imported the domain
// protos would be the first thing to point it at them.

// observedDeadline runs one request through the timeout interceptor and reports
// the deadline the handler saw.
func observedDeadline(
	ctx context.Context, t *testing.T, timeout time.Duration,
) (deadline time.Time, hasDeadline bool) {
	t.Helper()

	handler := resilience.NewTimeoutInterceptor(timeout)(
		func(innerCtx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			deadline, hasDeadline = innerCtx.Deadline()
			return connect.NewResponse(&emptypb.Empty{}), nil
		})

	if _, err := handler(ctx, connect.NewRequest(&emptypb.Empty{})); err != nil {
		t.Fatalf("the handler returned %v", err)
	}
	return deadline, hasDeadline
}

// TestTimeoutInterceptorAppliesADeadline covers the case the interceptor exists
// for: a client that set no deadline of its own still gets one, so a slow query
// cannot hold a pool connection indefinitely.
func TestTimeoutInterceptorAppliesADeadline(t *testing.T) {
	t.Parallel()

	const timeout = time.Minute

	// A bound, not a tolerance. Asserting the deadline lands within some
	// milliseconds of an expected instant is the shape that makes a suite flake
	// on a loaded machine; asserting it falls inside the window the interceptor
	// promises cannot.
	before := time.Now()
	deadline, hasDeadline := observedDeadline(t.Context(), t, timeout)
	if !hasDeadline {
		t.Fatal("the handler ran with no deadline")
	}

	if deadline.Before(before) || deadline.After(time.Now().Add(timeout)) {
		t.Errorf("deadline %v is outside the window the interceptor promises", deadline)
	}
}

// TestTimeoutInterceptorHonoursAStricterCaller pins the half that protects the
// client: one that has already committed to a shorter wait keeps it, because it
// knows its own patience better than this server does.
func TestTimeoutInterceptorHonoursAStricterCaller(t *testing.T) {
	t.Parallel()

	callerCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)

	callerDeadline, ok := callerCtx.Deadline()
	if !ok {
		t.Fatal("the caller context has no deadline to honour")
	}

	deadline, hasDeadline := observedDeadline(callerCtx, t, time.Hour)
	if !hasDeadline {
		t.Fatal("the handler ran with no deadline")
	}

	// Equal, not merely close: the interceptor must pass the caller's own
	// context through untouched rather than derive a new deadline from it.
	if !deadline.Equal(callerDeadline) {
		t.Errorf("deadline = %v, want the caller's own %v", deadline, callerDeadline)
	}
}

// TestTimeoutInterceptorClampsAGenerousCaller is the half that protects the
// server: a client willing to wait an hour does not get to hold a database
// connection for one.
func TestTimeoutInterceptorClampsAGenerousCaller(t *testing.T) {
	t.Parallel()

	callerCtx, cancel := context.WithTimeout(t.Context(), time.Hour)
	t.Cleanup(cancel)

	callerDeadline, _ := callerCtx.Deadline()

	deadline, hasDeadline := observedDeadline(callerCtx, t, time.Minute)
	if !hasDeadline {
		t.Fatal("the handler ran with no deadline")
	}

	if !deadline.Before(callerDeadline) {
		t.Errorf("deadline %v was not clamped below the caller's %v",
			deadline, callerDeadline)
	}
}

// TestTimeoutInterceptorIsDisabledByANonPositiveTimeout covers the reading of a
// misconfigured flag. Deriving a context from a zero timeout would expire every
// request before the handler ran, turning a configuration mistake into a total
// outage, so the interceptor steps aside instead.
func TestTimeoutInterceptorIsDisabledByANonPositiveTimeout(t *testing.T) {
	t.Parallel()

	for _, timeout := range []time.Duration{0, -time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			t.Parallel()

			_, hasDeadline := observedDeadline(t.Context(), t, timeout)
			if hasDeadline {
				t.Errorf("a timeout of %v still imposed a deadline", timeout)
			}
		})
	}
}

// TestRateLimitInterceptorShedsExcess covers admission control doing its job:
// traffic beyond the configured rate is refused rather than queued.
//
// Queuing is the failure mode worth guarding against. A request held past its
// caller's own timeout costs the database the work and nobody receives the
// answer, which is how an overload becomes an outage.
func TestRateLimitInterceptorShedsExcess(t *testing.T) {
	t.Parallel()

	const burst = 20

	// One permit per second with a millisecond of patience, so the burst below
	// resolves immediately and without a sleep: the second request onwards
	// cannot get a permit inside MaxWait.
	handlerCalls := 0
	handler := resilience.NewRateLimitInterceptor(resilience.RateLimitConfig{
		RequestsPerSecond: 1,
		MaxWait:           time.Millisecond,
	})(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		handlerCalls++
		return connect.NewResponse(&emptypb.Empty{}), nil
	})

	admitted, shed := 0, 0
	for range burst {
		_, err := handler(t.Context(), connect.NewRequest(&emptypb.Empty{}))
		switch {
		case err == nil:
			admitted++
		case connect.CodeOf(err) == connect.CodeResourceExhausted:
			shed++
		default:
			t.Fatalf("a shed request returned %v (code %v), want ResourceExhausted",
				err, connect.CodeOf(err))
		}
	}

	if admitted == 0 {
		t.Error("the limiter admitted nothing; it is refusing traffic it should serve")
	}
	if shed == 0 {
		t.Error("the limiter admitted the whole burst; nothing was shed")
	}
	// Every request got exactly one outcome, so none was dropped silently or
	// counted twice.
	if admitted+shed != burst {
		t.Errorf("accounted for %d of %d requests", admitted+shed, burst)
	}
	// A shed request must never reach the handler: the point is to not do the
	// work, not to do it and discard the answer.
	if handlerCalls != admitted {
		t.Errorf("the handler ran %d times for %d admitted requests", handlerCalls, admitted)
	}
}
