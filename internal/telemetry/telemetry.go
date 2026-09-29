// Package telemetry wires OpenTelemetry tracing and metrics for the service.
//
// Configuration arrives as a Config value rather than being read from the
// environment here: in this CLI every knob is a registered flag, so that
// `roster serve --help` shows the whole configuration surface. Where a Config
// field is left empty this package passes no explicit option to the SDK, which
// leaves the OTel SDK's own OTEL_* environment handling in effect — so the
// ecosystem-standard variables keep working, through the library that defines
// them rather than through an os.Getenv call of ours.
package telemetry

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Exporter names accepted by Config.Exporter.
const (
	// ExporterNone keeps the tracer provider active but sends nothing. Spans are
	// still created and context still propagates, so trace ids reach the logs;
	// only export is off. This is the default because an unconfigured checkout
	// should neither fail nor talk to a collector that is not there.
	ExporterNone = "none"
	// ExporterOTLP sends spans over gRPC to a collector.
	ExporterOTLP = "otlp"
	// ExporterStdout prints spans, for seeing what would be exported.
	ExporterStdout = "stdout"
)

// fullSamplePercent is the upper bound of Config.SamplePercent.
const fullSamplePercent = 100.0

// Config describes the telemetry pipeline.
//
// Every field is optional. An empty ServiceName or Endpoint means "let the SDK
// decide", which is how the standard OTEL_* variables still apply.
type Config struct {
	// ServiceName and ServiceVersion identify this deployment on every span and
	// metric series. Empty defers to OTEL_SERVICE_NAME.
	ServiceName    string
	ServiceVersion string

	// Exporter is one of the Exporter* constants. An unrecognized value is
	// treated as ExporterNone by Init, which reports it rather than failing
	// startup over a telemetry typo.
	Exporter string

	// Endpoint is the OTLP collector address. Empty defers to
	// OTEL_EXPORTER_OTLP_ENDPOINT.
	Endpoint string

	// Insecure sends OTLP without TLS, for a collector on the same host.
	Insecure bool

	// SamplePercent is the share of root spans to record, 0 to 100. A span with a
	// sampled remote parent is always recorded regardless, so a trace started by
	// the frontend stays whole.
	SamplePercent float64
}

// Init installs the global tracer provider and propagators, returning the
// function that flushes and stops it.
//
// Called exactly once, from the serve command, and never from an init()
// function: OTel's API is unavoidably global, but when it is installed and when
// it is torn down should still be decisions the composition root makes. The
// returned shutdown must be called or buffered spans are lost.
//
// Requires: cfg.SamplePercent in [0, 100]; values outside are clamped.
// Ensures:  on error nothing global has been installed.
func Init(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	res, err := NewResource(ctx, cfg)
	if err != nil {
		return nil, err
	}

	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(rootSampler(cfg.SamplePercent))),
	}

	switch cfg.Exporter {
	case ExporterStdout:
		exporter, exporterErr := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if exporterErr != nil {
			return nil, fmt.Errorf("creating stdout trace exporter: %w", exporterErr)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	case ExporterOTLP:
		var grpcOpts []otlptracegrpc.Option
		// Only when set: otherwise the SDK reads OTEL_EXPORTER_OTLP_ENDPOINT.
		if cfg.Endpoint != "" {
			grpcOpts = append(grpcOpts, otlptracegrpc.WithEndpoint(cfg.Endpoint))
		}
		if cfg.Insecure {
			grpcOpts = append(grpcOpts, otlptracegrpc.WithInsecure())
		}
		exporter, exporterErr := otlptracegrpc.New(ctx, grpcOpts...)
		if exporterErr != nil {
			return nil, fmt.Errorf("creating otlp trace exporter: %w", exporterErr)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	case ExporterNone:
		// Provider without an exporter: spans exist, nothing leaves.
	default:
		// Deliberately not an error. A telemetry misconfiguration should not stop
		// the service from serving rosters; the caller logs what it passed.
	}

	provider := sdktrace.NewTracerProvider(opts...)

	// Installed only once everything above has succeeded, so an error leaves no
	// half-configured global behind.
	otel.SetTracerProvider(provider)
	// W3C TraceContext and Baggage, so a trace started in the browser continues
	// through this service, and so k6's baggage reaches the profiler.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return provider.Shutdown, nil
}

// rootSampler maps a percentage onto a sampler for spans with no remote parent.
func rootSampler(percent float64) sdktrace.Sampler {
	switch {
	case percent >= fullSamplePercent:
		return sdktrace.AlwaysSample()
	case percent <= 0:
		return sdktrace.NeverSample()
	default:
		return sdktrace.TraceIDRatioBased(percent / fullSamplePercent)
	}
}

// NewResource describes this service.
//
// Traces and metrics share one resource, so a span and a metric series attribute
// to the same deployment and can be correlated in a dashboard. WithFromEnv is
// included so OTEL_RESOURCE_ATTRIBUTES and OTEL_SERVICE_NAME still apply;
// explicit fields are layered on top and win.
func NewResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	attrs := make([]attribute.KeyValue, 0, 2)
	if cfg.ServiceName != "" {
		attrs = append(attrs, semconv.ServiceNameKey.String(cfg.ServiceName))
	}
	if cfg.ServiceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersionKey.String(cfg.ServiceVersion))
	}

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(attrs...),
	)
	if err != nil {
		return nil, fmt.Errorf("creating otel resource: %w", err)
	}
	return res, nil
}

// NewConnectInterceptor returns the RPC interceptor that produces a span per
// call and the RED metrics that go with it.
//
// WithTrustRemote adopts a caller-supplied traceparent, which is what joins a
// browser's trace to this service's. That trust is appropriate here because the
// listener is not public — a caller who can reach it is already authenticated —
// and a forged trace id costs nothing but a misleading dashboard.
func NewConnectInterceptor() (connect.Interceptor, error) {
	interceptor, err := otelconnect.NewInterceptor(
		otelconnect.WithTrustRemote(),
		otelconnect.WithPropagateResponseHeader(),
	)
	if err != nil {
		return nil, fmt.Errorf("creating otelconnect interceptor: %w", err)
	}
	return interceptor, nil
}
