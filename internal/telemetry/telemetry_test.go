package telemetry

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func ok(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestRootSampler covers the pure mapping from a percentage onto a sampler. The
// boundaries matter: 0 and 100 must be exactly off and exactly on rather than
// ratios that round.
func TestRootSampler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		percent     float64
		description string
	}{
		{name: "zero never samples", percent: 0, description: sdktrace.NeverSample().Description()},
		{name: "negative never samples", percent: -10, description: sdktrace.NeverSample().Description()},
		{name: "one hundred always samples", percent: 100, description: sdktrace.AlwaysSample().Description()},
		{name: "above one hundred always samples", percent: 250, description: sdktrace.AlwaysSample().Description()},
		{
			name:        "a fraction becomes a ratio of that fraction",
			percent:     25,
			description: sdktrace.TraceIDRatioBased(0.25).Description(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := rootSampler(tt.percent).Description(); got != tt.description {
				t.Errorf("rootSampler(%v) = %s, want %s", tt.percent, got, tt.description)
			}
		})
	}
}

func TestNewResourceCarriesTheServiceIdentity(t *testing.T) {
	t.Parallel()

	res, err := NewResource(t.Context(), Config{
		ServiceName:    "roster-test",
		ServiceVersion: "v9.9.9",
	})
	ok(t, err)

	var gotName, gotVersion string
	for _, attr := range res.Attributes() {
		switch attr.Key {
		case "service.name":
			gotName = attr.Value.AsString()
		case "service.version":
			gotVersion = attr.Value.AsString()
		}
	}

	if gotName != "roster-test" {
		t.Errorf("service.name = %q, want %q", gotName, "roster-test")
	}
	if gotVersion != "v9.9.9" {
		t.Errorf("service.version = %q, want %q", gotVersion, "v9.9.9")
	}
}

// TestNewResourceOmitsEmptyFields pins the behaviour that makes the standard
// OTEL_* variables still work: an empty Config field must not be written as an
// empty attribute, or it would override what the SDK read from the environment.
func TestNewResourceOmitsEmptyFields(t *testing.T) {
	t.Parallel()

	res, err := NewResource(t.Context(), Config{})
	ok(t, err)

	for _, attr := range res.Attributes() {
		if attr.Key == "service.version" && attr.Value.AsString() == "" {
			t.Error("an empty ServiceVersion was written as an empty attribute")
		}
	}
}

// TestInitShutsDownCleanly exercises the lifecycle every exporter shares: the
// provider comes up, the returned shutdown flushes it, and a second shutdown is
// not an error — a caller that shuts down twice during a messy exit should not
// be punished for it.
// Not parallel, here or in the subtests: Init installs a global TracerProvider,
// which is mutable state shared by the whole test binary. Running these
// concurrently would make a failure ambiguous between a logic bug and a race on
// that global.
//
//nolint:paralleltest // Init installs the global TracerProvider.
func TestInitShutsDownCleanly(t *testing.T) {
	tests := []struct {
		name     string
		exporter string
	}{
		{name: "none keeps spans in memory", exporter: ExporterNone},
		{name: "stdout exports to the writer", exporter: ExporterStdout},
		{name: "an unknown exporter degrades rather than failing", exporter: "nonsense"},
	}

	//nolint:paralleltest // see the note on this test: the provider is global.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shutdown, err := Init(t.Context(), Config{
				ServiceName:   "roster-test",
				Exporter:      tt.exporter,
				SamplePercent: 100,
			})
			ok(t, err)

			if shutdown == nil {
				t.Fatal("Init returned a nil shutdown")
			}
			ok(t, shutdown(context.Background()))
			ok(t, shutdown(context.Background()))
		})
	}
}

func TestNewConnectInterceptorIsConstructible(t *testing.T) {
	t.Parallel()

	interceptor, err := NewConnectInterceptor()
	ok(t, err)
	if interceptor == nil {
		t.Error("NewConnectInterceptor returned nil")
	}
}

// Not parallel for the same reason as TestInitShutsDownCleanly: InitMetrics
// installs the global MeterProvider.
//
//nolint:paralleltest // InitMetrics installs the global MeterProvider.
func TestInitMetricsServesAScrapeEndpoint(t *testing.T) {
	res, err := NewResource(t.Context(), Config{ServiceName: "roster-test"})
	ok(t, err)

	metrics, err := InitMetrics(res)
	ok(t, err)
	t.Cleanup(func() { _ = metrics.Shutdown(context.Background()) })

	if metrics.Handler == nil {
		t.Fatal("InitMetrics returned no handler")
	}
}
