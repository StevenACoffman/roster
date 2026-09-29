package profiling_test

import (
	"net/http"
	"net/http/httptest"
	"runtime/pprof"
	"strings"
	"testing"

	"github.com/StevenACoffman/roster/internal/profiling"
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

func TestConfigEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		endpoint string
		want     bool
	}{
		{name: "an empty endpoint disables profiling", endpoint: "", want: false},
		{name: "whitespace is not an endpoint", endpoint: "   ", want: false},
		{name: "an endpoint enables it", endpoint: "http://pyroscope:4040", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := profiling.Config{Endpoint: tt.endpoint}
			if got := cfg.Enabled(); got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestStartDisabledIsAUsableNoOp pins the property that lets the caller skip a
// branch: a disabled config still returns a callable stop.
func TestStartDisabledIsAUsableNoOp(t *testing.T) {
	t.Parallel()

	stop, err := profiling.Start(profiling.Config{})
	ok(t, err)
	if stop == nil {
		t.Fatal("Start returned a nil stop for a disabled config")
	}
	stop() // must not panic
}

// TestStartRejectsAnUnusableEndpoint checks that a misconfigured profiler
// reports rather than panicking, and still hands back a callable stop so the
// caller's deferred cleanup is safe.
func TestStartRejectsAnUnusableEndpoint(t *testing.T) {
	t.Parallel()

	stop, err := profiling.Start(profiling.Config{
		Endpoint:    "://not a url",
		ServiceName: "roster-test",
	})
	if err == nil {
		stop()
		t.Fatal("Start accepted an unparseable endpoint")
	}
	if stop == nil {
		t.Fatal("Start returned a nil stop alongside its error")
	}
	stop()
}

// TestK6LabelsMiddlewareLabelsFromBaggage is the reason the middleware exists:
// k6 sends a Baggage header and the profiler should be able to filter a flame
// graph to one scenario.
func TestK6LabelsMiddlewareLabelsFromBaggage(t *testing.T) {
	t.Parallel()

	var gotRunID, gotScenario string
	var hadLabels bool

	handler := profiling.K6LabelsMiddleware()(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			// The labels are attached to the request's context, so they are read
			// from there rather than from the request itself.
			pprof.ForLabels(r.Context(), func(key, value string) bool {
				hadLabels = true
				switch key {
				case "k6_test_run_id":
					gotRunID = value
				case "k6_scenario":
					gotScenario = value
				}
				return true
			})
		}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.Header.Set("Baggage", "k6.test_run_id=run-7,k6.scenario=browse")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if !hadLabels {
		t.Fatal("no pprof labels were attached; the Baggage header was not read")
	}
	equals(t, gotRunID, "run-7")
	equals(t, gotScenario, "browse")
}

// TestK6LabelsMiddlewareIgnoresNonK6Baggage guards the filtering: arbitrary
// baggage from a caller must not become an unbounded set of pprof label keys.
func TestK6LabelsMiddlewareIgnoresNonK6Baggage(t *testing.T) {
	t.Parallel()

	var keys []string

	handler := profiling.K6LabelsMiddleware()(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			pprof.ForLabels(r.Context(), func(key, _ string) bool {
				keys = append(keys, key)
				return true
			})
		}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.Header.Set("Baggage", "tenant=acme,k6.scenario=curate,secret=hunter2")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	for _, key := range keys {
		if !strings.HasPrefix(key, "k6_") {
			t.Errorf("label %q was attached; only k6_-prefixed labels should be", key)
		}
	}
}

// TestK6LabelsMiddlewareWithoutBaggageIsHarmless covers the common case: almost
// every real request has no Baggage header, and the middleware is applied to all
// of them.
func TestK6LabelsMiddlewareWithoutBaggageIsHarmless(t *testing.T) {
	t.Parallel()

	served := false
	handler := profiling.K6LabelsMiddleware()(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			served = true
			w.WriteHeader(http.StatusOK)
		}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))

	if !served {
		t.Error("the request was not passed through")
	}
	equals(t, rec.Code, http.StatusOK)
}
