// Package profiling pushes continuous CPU and memory profiles to Pyroscope.
//
// Continuous profiling answers the question an on-demand pprof dump cannot: why
// was the service slow twenty minutes ago, when nobody was holding a profiler
// open. Profiles carry the same service name and version the traces do, so a
// slow span can be opened as the flame graph recorded while it ran.
//
// Configuration arrives as a Config value rather than being read from the
// environment here, for the same reason as internal/telemetry: in this CLI every
// knob is a registered flag, so `roster serve --help` shows the whole surface.
package profiling

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"github.com/grafana/pyroscope-go"
	// x/k6 is an experimental module of pyroscope-go and carries no compatibility
	// promise. That is the trade for not reimplementing its baggage parsing.
	k6 "github.com/grafana/pyroscope-go/x/k6"
)

// Sampling rates for the two profiles Go collects only when asked.
//
// Both default to zero, which means the mutex and block profiles exist but are
// always empty. One contention event in five is Pyroscope's own recommendation:
// enough signal to find a hot lock, little enough overhead to leave on in
// production.
const (
	mutexProfileFraction = 5
	blockProfileRate     = 5
)

// defaultEnvironment labels profiles from a checkout nobody has configured, so
// they cannot be mistaken for production ones.
const defaultEnvironment = "development"

// Config describes where profiles go and how they are labelled.
type Config struct {
	// Endpoint is the Pyroscope server. Empty disables profiling entirely, which
	// is the default: a checkout with no Pyroscope should not spend CPU
	// collecting profiles nothing will read.
	Endpoint string

	// ServiceName and ServiceVersion come from the telemetry settings, so a
	// profile and a trace agree on which deployment they came from. One name for
	// one concept rather than a second set of flags.
	ServiceName    string
	ServiceVersion string

	// Environment keeps production and staging profiles distinguishable. Empty
	// becomes "development".
	Environment string

	// BasicAuthUser and BasicAuthPassword authenticate to Grafana Cloud.
	BasicAuthUser     string
	BasicAuthPassword string
}

// Enabled reports whether profiles should be pushed.
func (c Config) Enabled() bool { return strings.TrimSpace(c.Endpoint) != "" }

// environment returns the label to tag profiles with.
func (c Config) environment() string {
	if env := strings.TrimSpace(c.Environment); env != "" {
		return env
	}
	return defaultEnvironment
}

// profileTypes is everything Pyroscope can collect from a Go process: the five
// it gathers by default, plus the goroutine, mutex and block profiles that need
// the runtime rates set in Start.
func profileTypes() []pyroscope.ProfileType {
	return []pyroscope.ProfileType{
		pyroscope.ProfileCPU,
		pyroscope.ProfileAllocObjects,
		pyroscope.ProfileAllocSpace,
		pyroscope.ProfileInuseObjects,
		pyroscope.ProfileInuseSpace,
		pyroscope.ProfileGoroutines,
		pyroscope.ProfileMutexCount,
		pyroscope.ProfileMutexDuration,
		pyroscope.ProfileBlockCount,
		pyroscope.ProfileBlockDuration,
	}
}

// Start begins pushing profiles and returns a function that stops them.
//
// A disabled config is not an error: the returned stop is a usable no-op, so the
// caller needs no branch and the runtime sampling rates stay at zero.
//
// Mutates process-global runtime state, so it is called from the serve command's
// wiring and never from a library path or an init function — when profiling
// starts and stops should be a decision the composition root makes.
//
// Ensures: stop is always non-nil and safe to call; on failure the sampling
// rates are left as they were found.
func Start(cfg Config) (stop func(), err error) {
	if !cfg.Enabled() {
		return func() {}, nil
	}

	// Set before Start: the profiler reads these rates when it first collects.
	runtime.SetMutexProfileFraction(mutexProfileFraction)
	runtime.SetBlockProfileRate(blockProfileRate)

	profiler, err := pyroscope.Start(pyroscope.Config{
		ApplicationName:   cfg.ServiceName,
		ServerAddress:     cfg.Endpoint,
		BasicAuthUser:     cfg.BasicAuthUser,
		BasicAuthPassword: cfg.BasicAuthPassword,
		ProfileTypes:      profileTypes(),
		Tags: map[string]string{
			"service_version": cfg.ServiceVersion,
			"environment":     cfg.environment(),
		},
	})
	if err != nil {
		// Put the rates back: nothing is collecting them now, and leaving them
		// raised would cost contention tracking for no benefit.
		runtime.SetMutexProfileFraction(0)
		runtime.SetBlockProfileRate(0)
		return func() {}, fmt.Errorf("starting the pyroscope profiler: %w", err)
	}

	return func() {
		_ = profiler.Stop()
		runtime.SetMutexProfileFraction(0)
		runtime.SetBlockProfileRate(0)
	}, nil
}

// K6LabelsMiddleware tags profile samples with the k6 test run and scenario that
// produced them, by reading the Baggage header.
//
// k6 does not send that header on its own — the load script sets it, and without
// it this middleware is present but inert. Only `k6.`-prefixed keys are kept,
// with dots rewritten to underscores, so `k6.test_run_id` becomes the pprof
// label `k6_test_run_id` and a flame graph can be filtered to one scenario.
//
// Costs a few nanoseconds per request when no Baggage header is present, so it
// is applied unconditionally rather than behind a flag; the labels also reach
// anything reading pprof on the admin listener, not only a Pyroscope push.
func K6LabelsMiddleware() func(http.Handler) http.Handler {
	return k6.LabelsFromBaggageHandler
}
