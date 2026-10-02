package serve

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/StevenACoffman/roster/internal/profiling"
	"github.com/StevenACoffman/roster/internal/telemetry"
)

// otelShutdownTimeout bounds the final flush of buffered spans and metrics.
const otelShutdownTimeout = 5 * time.Second

// startTelemetry brings up tracing and metrics, returning the metrics handler
// for the admin listener and one function that shuts both down.
//
// Telemetry never fails startup. A collector that is unreachable, or a sample
// percentage that makes no sense, must not stop the service from serving
// rosters — so every failure here is logged and the service continues without
// that signal. The caller therefore gets no error, only a possibly-nil handler.
//
// Ensures: the returned shutdown is always safe to call, even when nothing
// started.
func (cfg *Config) startTelemetry(
	ctx context.Context, logger *slog.Logger,
) (metricsHandler http.Handler, shutdown func()) {
	telemetryCfg := telemetry.Config{
		ServiceName:    cfg.OTelServiceName,
		ServiceVersion: cfg.OTelServiceVersion,
		Exporter:       cfg.OTelExporter,
		Endpoint:       cfg.OTelEndpoint,
		Insecure:       cfg.OTelInsecure,
		SamplePercent:  cfg.OTelSamplePercent,
		// Asked of the profiling config rather than re-testing the endpoint here,
		// so "profiling is on" keeps one definition.
		ProfileCorrelation: profiling.Config{Endpoint: cfg.PyroscopeEndpoint}.Enabled(),
	}

	// The resource is built once and shared, so a span and a metric series
	// attribute to the same deployment.
	resource, err := telemetry.NewResource(ctx, telemetryCfg)
	if err != nil {
		logger.WarnContext(ctx, "building telemetry resource; continuing without telemetry",
			"error", err)
		return nil, func() {}
	}

	stops := make([]func(context.Context) error, 0, 2)

	// Metrics before traces: the otelconnect interceptor resolves the global
	// meter provider when it is constructed, and a provider installed later is
	// never picked up.
	if metrics, metricsErr := telemetry.InitMetrics(resource); metricsErr != nil {
		logger.WarnContext(ctx, "metrics unavailable; continuing without them",
			"error", metricsErr)
	} else {
		metricsHandler = metrics.Handler
		stops = append(stops, metrics.Shutdown)
	}

	if stopTraces, traceErr := telemetry.Init(ctx, telemetryCfg); traceErr != nil {
		logger.WarnContext(ctx, "tracing unavailable; continuing without it",
			"error", traceErr)
	} else {
		stops = append(stops, stopTraces)
		logger.InfoContext(ctx, "tracing configured",
			"exporter", cfg.OTelExporter,
			"endpoint", cfg.OTelEndpoint,
			"sample_percent", cfg.OTelSamplePercent,
		)
	}

	return metricsHandler, func() {
		// WithoutCancel: ctx is already cancelled by the time this runs, but the
		// flush still needs to happen and still belongs to this service's trace.
		flushCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx), otelShutdownTimeout)
		defer cancel()

		for _, stop := range stops {
			if err := stop(flushCtx); err != nil {
				logger.ErrorContext(flushCtx, "shutting down telemetry", "error", err)
			}
		}
	}
}
