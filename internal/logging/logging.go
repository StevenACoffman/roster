// Package logging builds the structured logger used by the service binaries.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// ParseLevel maps a level name onto a slog.Level, defaulting to Info. An operator
// typo degrades to the default rather than stopping the process.
func ParseLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// New builds a logger writing to w: "json" for collectors, anything else for
// humans. The handler is wrapped for trace correlation; see WithTraceContext.
// Mutates no global state — the caller decides whether to slog.SetDefault it.
func New(w io.Writer, level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: ParseLevel(level)}

	var handler slog.Handler
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}

	return slog.New(WithTraceContext(handler))
}
