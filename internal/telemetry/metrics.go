package telemetry

import (
	"context"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// Metrics holds the metric pipeline and the handler that exposes it.
type Metrics struct {
	// Handler serves the Prometheus scrape endpoint. The caller mounts it on the
	// admin listener, never on the public mux: the endpoint enumerates every RPC
	// name and its traffic, which is operator information.
	Handler http.Handler
	// Shutdown flushes and stops the provider.
	Shutdown func(context.Context) error
}

// InitMetrics installs a global meter provider backed by Prometheus and returns
// the handler exposing it.
//
// Installing it globally is what gives the service RED metrics without any
// per-handler instrumentation: the otelconnect interceptor already records rate,
// errors and duration per RPC, and stays a no-op until a provider exists.
// Duration is recorded as a histogram, so p50/p95/p99 are queryable rather than
// only the mean — which matters because a tail latency is what a user notices.
//
// The Go and process collectors supply the saturation signal that RPC metrics
// alone cannot: goroutine count, heap, file descriptors, CPU.
//
// Pass the same resource the tracer uses so metrics and traces line up.
func InitMetrics(res *resource.Resource) (*Metrics, error) {
	// A dedicated registry rather than prometheus.DefaultRegisterer: the default
	// is package-level state that anything in the binary can register into, and a
	// duplicate registration there panics at startup.
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	exporter, err := promexporter.New(promexporter.WithRegisterer(registry))
	if err != nil {
		return nil, fmt.Errorf("creating prometheus metric exporter: %w", err)
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(exporter),
	)
	otel.SetMeterProvider(provider)

	return &Metrics{
		// ContinueOnError: a single broken collector should degrade the scrape,
		// not blank it. Monitoring that fails closed is monitoring that is absent
		// exactly when something is wrong.
		Handler: promhttp.HandlerFor(registry, promhttp.HandlerOpts{
			ErrorHandling: promhttp.ContinueOnError,
		}),
		Shutdown: provider.Shutdown,
	}, nil
}
