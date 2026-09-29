package serve

import (
	"context"
	"log/slog"

	"github.com/StevenACoffman/roster/internal/profiling"
)

// startProfiling begins pushing continuous profiles, returning the function that
// stops them.
//
// Profiling never fails startup, for the same reason telemetry does not: an
// unreachable Pyroscope must not stop the service from serving rosters. A
// failure is logged and the service continues without profiles.
//
// Ensures: the returned stop is always safe to call, even when nothing started.
func (cfg *Config) startProfiling(ctx context.Context, logger *slog.Logger) func() {
	profilingCfg := profiling.Config{
		Endpoint: cfg.PyroscopeEndpoint,
		// Taken from the telemetry flags rather than their own, so a profile and
		// a trace agree on which deployment they came from.
		ServiceName:       cfg.OTelServiceName,
		ServiceVersion:    cfg.OTelServiceVersion,
		Environment:       cfg.DeploymentEnvironment,
		BasicAuthUser:     cfg.PyroscopeAuthUser,
		BasicAuthPassword: cfg.PyroscopeAuthPassword,
	}

	stop, err := profiling.Start(profilingCfg)
	if err != nil {
		logger.WarnContext(ctx, "continuous profiling unavailable; continuing without it",
			"error", err)
		return stop
	}
	if profilingCfg.Enabled() {
		logger.InfoContext(ctx, "pushing continuous profiles",
			"endpoint", profilingCfg.Endpoint,
			"environment", cfg.DeploymentEnvironment,
		)
	}
	return stop
}
