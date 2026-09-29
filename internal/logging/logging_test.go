package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/StevenACoffman/roster/internal/logging"
)

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

func TestParseLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want slog.Level
	}{
		{name: "debug", in: "debug", want: slog.LevelDebug},
		{name: "info", in: "info", want: slog.LevelInfo},
		{name: "warn", in: "warn", want: slog.LevelWarn},
		{name: "warning is accepted too", in: "warning", want: slog.LevelWarn},
		{name: "error", in: "error", want: slog.LevelError},
		{name: "mixed case", in: "DeBuG", want: slog.LevelDebug},
		{name: "surrounding space is trimmed", in: "  warn  ", want: slog.LevelWarn},
		{
			// An operator typo must not stop the process. Defaulting to info
			// keeps the service running and visible rather than silent.
			name: "an unrecognised level degrades to info",
			in:   "verbose",
			want: slog.LevelInfo,
		},
		{name: "empty degrades to info", in: "", want: slog.LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := logging.ParseLevel(tt.in); got != tt.want {
				t.Errorf("ParseLevel(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestNewSelectsTheFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		format   string
		wantJSON bool
	}{
		{name: "json for collectors", format: "json", wantJSON: true},
		{name: "case does not matter", format: "JSON", wantJSON: true},
		{name: "space does not matter", format: " json ", wantJSON: true},
		{name: "text for humans", format: "text", wantJSON: false},
		{name: "anything unrecognised is text", format: "yaml", wantJSON: false},
		{name: "empty is text", format: "", wantJSON: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			logging.New(&buf, "info", tt.format).Info("hello", "key", "value")

			line := buf.String()
			if line == "" {
				t.Fatal("nothing was written")
			}

			var decoded map[string]any
			isJSON := json.Unmarshal([]byte(line), &decoded) == nil

			if isJSON != tt.wantJSON {
				t.Errorf("format %q produced JSON=%v, want %v (line: %s)",
					tt.format, isJSON, tt.wantJSON, strings.TrimSpace(line))
			}
			if !strings.Contains(line, "hello") {
				t.Errorf("the message is missing from %q", line)
			}
		})
	}
}

func TestNewHonoursTheLevel(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := logging.New(&buf, "warn", "text")

	logger.Debug("suppressed")
	logger.Info("also suppressed")
	logger.Warn("kept")

	out := buf.String()
	for _, absent := range []string{"suppressed", "also suppressed"} {
		if strings.Contains(out, absent) {
			t.Errorf("a record below the level was emitted: %q", out)
		}
	}
	if !strings.Contains(out, "kept") {
		t.Errorf("the warning was suppressed: %q", out)
	}
}

// TestTraceContextSurvivesWith is the property this package exists for, and the
// one that would fail silently.
//
// traceHandler must re-wrap itself in WithAttrs and WithGroup. If it did not,
// slog.With would return the undecorated inner handler, and the trace ids would
// vanish from exactly the log lines someone added a With call to improve.
func TestTraceContextSurvivesWith(t *testing.T) {
	t.Parallel()

	// A real provider and a real span: the ids come from the span context, so
	// there is nothing meaningful to substitute here.
	provider := trace.NewTracerProvider(trace.WithSampler(trace.AlwaysSample()))
	t.Cleanup(func() { ok(t, provider.Shutdown(context.Background())) })

	ctx, span := provider.Tracer("logging_test").Start(t.Context(), "test-span")
	defer span.End()

	wantTrace := span.SpanContext().TraceID().String()
	wantSpan := span.SpanContext().SpanID().String()

	tests := []struct {
		name   string
		derive func(*slog.Logger) *slog.Logger
		// group is the open slog group the ids land under, empty for none.
		// slog nests every attribute added while a group is open, including the
		// ones this handler adds, so WithGroup moves them rather than dropping
		// them. trace.go documents that; this pins it, so the comment cannot go
		// stale unnoticed.
		group string
	}{
		{
			name:   "the logger as built",
			derive: func(l *slog.Logger) *slog.Logger { return l },
		},
		{
			name:   "after With",
			derive: func(l *slog.Logger) *slog.Logger { return l.With("component", "test") },
		},
		{
			name:   "after two With calls",
			derive: func(l *slog.Logger) *slog.Logger { return l.With("a", 1).With("b", 2) },
		},
		{
			name:   "after WithGroup, nested under it",
			derive: func(l *slog.Logger) *slog.Logger { return l.WithGroup("req") },
			group:  "req",
		},
		{
			name:   "after WithGroup then With, still nested",
			derive: func(l *slog.Logger) *slog.Logger { return l.WithGroup("req").With("id", "x") },
			group:  "req",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			tt.derive(logging.New(&buf, "info", "json")).InfoContext(ctx, "inside a span")

			var record map[string]any
			ok(t, json.Unmarshal(buf.Bytes(), &record))

			// Descend into the group when one is open.
			scope := record
			if tt.group != "" {
				nested, isGroup := record[tt.group].(map[string]any)
				if !isGroup {
					t.Fatalf("expected the ids under group %q, got record %v", tt.group, record)
				}
				scope = nested
			}

			gotTrace, _ := scope[logging.TraceIDKey].(string)
			gotSpan, _ := scope[logging.SpanIDKey].(string)

			equals(t, gotTrace, wantTrace)
			equals(t, gotSpan, wantSpan)
		})
	}
}

// TestTraceIDsAreTopLevelWithoutAGroup states the consequence of the nesting
// above, because it is the case that matters operationally: a log aggregator
// queries for trace_id at the top level, so the service loggers must not open a
// group. Nothing in this repository calls WithGroup, and this test is what would
// notice if that changed.
func TestTraceIDsAreTopLevelWithoutAGroup(t *testing.T) {
	t.Parallel()

	provider := trace.NewTracerProvider(trace.WithSampler(trace.AlwaysSample()))
	t.Cleanup(func() { ok(t, provider.Shutdown(context.Background())) })

	ctx, span := provider.Tracer("logging_test").Start(t.Context(), "test-span")
	defer span.End()

	var buf bytes.Buffer
	logging.New(&buf, "info", "json").
		With("service", "roster").
		InfoContext(ctx, "a service log line")

	var record map[string]any
	ok(t, json.Unmarshal(buf.Bytes(), &record))

	if _, present := record[logging.TraceIDKey]; !present {
		t.Errorf("%s is not at the top level of %v", logging.TraceIDKey, record)
	}
}

// TestNoTraceContextWithoutASpan covers the common case: almost every log line
// is written outside a span, and the handler must add nothing rather than
// emitting an all-zero id that looks like a real one.
func TestNoTraceContextWithoutASpan(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logging.New(&buf, "info", "json").InfoContext(t.Context(), "no span here")

	var record map[string]any
	ok(t, json.Unmarshal(buf.Bytes(), &record))

	for _, key := range []string{logging.TraceIDKey, logging.SpanIDKey} {
		if value, present := record[key]; present {
			t.Errorf("%s = %v was added outside a span", key, value)
		}
	}
}

// TestNewMutatesNoGlobalState pins the package comment's claim. A New that
// called slog.SetDefault would make two loggers in one process fight, and the
// migrate and serve commands each build their own.
//
// Not parallel: it reads slog.Default(), which is process-wide state another
// test running concurrently could replace.
//
//nolint:paralleltest // reads the process-wide slog.Default().
func TestNewMutatesNoGlobalState(t *testing.T) {
	before := slog.Default()

	var buf bytes.Buffer
	logging.New(&buf, "debug", "json")

	if slog.Default() != before {
		t.Error("New replaced the default logger")
	}
}
