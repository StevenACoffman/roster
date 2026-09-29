package logging

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// Attribute keys added to every log record emitted inside a span.
const (
	TraceIDKey = "trace_id"
	SpanIDKey  = "span_id"
)

// traceHandler copies the active trace and span ids onto every record, making the
// trace id the join key between logs and traces.
//
// Only the *Context variants carry a span; a plain slog.Info cannot.
type traceHandler struct {
	inner slog.Handler
}

func (h traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle adds the active span's identifiers, when there is one.
func (h traceHandler) Handle(ctx context.Context, record slog.Record) error {
	if spanCtx := trace.SpanContextFromContext(ctx); spanCtx.IsValid() {
		record.AddAttrs(
			slog.String(TraceIDKey, spanCtx.TraceID().String()),
			slog.String(SpanIDKey, spanCtx.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, record)
}

// WithAttrs must rewrap, or slog.With would drop the decoration.
func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{inner: h.inner.WithAttrs(attrs)}
}

// WithGroup must rewrap, as WithAttrs does. Note that slog nests every attribute
// under an open group, so ids land at req.trace_id rather than the top level; this
// service does not group its service loggers.
func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{inner: h.inner.WithGroup(name)}
}

// WithTraceContext wraps inner so records logged inside a span carry its ids. It
// only adds attributes, never suppressing or rewriting a record.
func WithTraceContext(inner slog.Handler) slog.Handler {
	return traceHandler{inner: inner}
}
