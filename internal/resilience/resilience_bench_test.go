package resilience_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/StevenACoffman/roster/internal/resilience"
)

// Benchmarks for the interceptors, which sit on every RPC.
//
// Interceptor overhead is paid per request whatever the handler does, so it is
// worth knowing in absolute terms: a few hundred nanoseconds is free beside a
// database round trip, and a few microseconds is not. These measure the
// interceptor alone, with a handler that does nothing, so the number is the
// overhead rather than the request.

// noopHandler returns immediately, so what the benchmarks below measure is the
// interceptor wrapped around it and nothing else.
func noopHandler(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func BenchmarkTimeoutInterceptor(b *testing.B) {
	handler := resilience.NewTimeoutInterceptor(30 * time.Second)(noopHandler)
	req := connect.NewRequest(&emptypb.Empty{})

	// The usual case: the caller set no deadline, so the interceptor derives
	// one. That derivation is the cost being measured.
	b.Run("caller has no deadline", func(b *testing.B) {
		ctx := b.Context()
		b.ReportAllocs()

		for b.Loop() {
			if _, err := handler(ctx, req); err != nil {
				b.Fatalf("handler: %v", err)
			}
		}
	})

	// A stricter caller short-circuits before any context is derived, and
	// should be the cheaper path.
	b.Run("caller is already stricter", func(b *testing.B) {
		ctx, cancel := context.WithTimeout(b.Context(), time.Second)
		defer cancel()

		b.ReportAllocs()

		for b.Loop() {
			if _, err := handler(ctx, req); err != nil {
				b.Fatalf("handler: %v", err)
			}
		}
	})
}

// BenchmarkRateLimitInterceptorAdmits measures the admitted path only.
//
// The shed path cannot be benchmarked meaningfully: refusing is the limiter
// doing nothing, and a loop fast enough to measure it would spend the whole run
// shedding. A rate high enough that nothing is refused is what a healthy
// instance actually experiences, and the question worth answering is what
// admission control costs when it is not refusing anything.
func BenchmarkRateLimitInterceptorAdmits(b *testing.B) {
	handler := resilience.NewRateLimitInterceptor(resilience.RateLimitConfig{
		RequestsPerSecond: 1_000_000_000,
		MaxWait:           time.Millisecond,
	})(noopHandler)
	req := connect.NewRequest(&emptypb.Empty{})

	b.ReportAllocs()

	for b.Loop() {
		if _, err := handler(b.Context(), req); err != nil {
			b.Fatalf("handler: %v", err)
		}
	}
}
