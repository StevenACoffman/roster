package logging_test

import (
	"context"
	"io"
	"testing"

	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/StevenACoffman/roster/internal/logging"
)

// Benchmarks for the logging handler, which runs once per log line.
//
// The trace-context handler wraps every record to look for a span and add its
// ids. That lookup happens whether or not a span exists, so the no-span case is
// the one most lines actually take and the one that must stay cheap.
//
// Output goes to io.Discard: this measures the handler and the encoder, not the
// terminal or the disk.

func BenchmarkLoggerFormats(b *testing.B) {
	provider := trace.NewTracerProvider(trace.WithSampler(trace.AlwaysSample()))
	b.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			b.Errorf("provider shutdown: %v", err)
		}
	})

	spanCtx, span := provider.Tracer("logging_bench").Start(context.Background(), "bench")
	b.Cleanup(func() { span.End() })

	for _, format := range []string{"json", "text"} {
		logger := logging.New(io.Discard, "info", format)

		b.Run(format+"/outside a span", func(b *testing.B) {
			ctx := b.Context()
			b.ReportAllocs()

			for b.Loop() {
				logger.InfoContext(ctx, "serving a request", "op", "GetAllOrgs", "rows", 100)
			}
		})

		b.Run(format+"/inside a span", func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				logger.InfoContext(spanCtx, "serving a request", "op", "GetAllOrgs", "rows", 100)
			}
		})

		// Below the configured level, so the record is dropped. Debug lines left
		// in the code cost this much each, which is what decides whether leaving
		// them in is reasonable.
		b.Run(format+"/suppressed below level", func(b *testing.B) {
			ctx := b.Context()
			b.ReportAllocs()

			for b.Loop() {
				logger.DebugContext(ctx, "cursor decoded", "after", "sch-1")
			}
		})
	}
}

// BenchmarkLoggerWith covers the re-wrapping that makes trace ids survive a
// With call. A service builds these per component at startup, but a handler that
// derived one per request would pay this each time.
func BenchmarkLoggerWith(b *testing.B) {
	logger := logging.New(io.Discard, "info", "json")

	b.ReportAllocs()

	for b.Loop() {
		logger.With("component", "oneroster")
	}
}
