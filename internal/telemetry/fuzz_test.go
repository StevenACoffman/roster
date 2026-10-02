package telemetry

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// FuzzRootSamplerIsAlwaysUsable asserts the totality property that the sampling
// flag depends on: whatever float64 reaches rootSampler, the result is a sampler
// that samples a defined share of traces.
//
// --otel-sample-percent is a float64 flag, and strconv.ParseFloat accepts "NaN"
// and "Inf", so every float64 is reachable from the command line rather than
// only from a caller inside this package. NaN is the dangerous one: it compares
// false against everything, so a switch written as upper bound, then lower
// bound, then ratio falls through both guards and builds a ratio sampler from
// NaN. Nothing fails, nothing logs, and the deployment records an undefined
// share of its traces.
//
// The description is the only thing the SDK exposes about a built sampler, so
// that is what gets asserted. A ratio sampler names its fraction, which is both
// halves of the property in one string: that a ratio was chosen, and which one.
func FuzzRootSamplerIsAlwaysUsable(f *testing.F) {
	// The documented boundaries, so `just test` catches a regression here
	// without waiting for a fuzzing session.
	f.Add(100.0)
	f.Add(0.0)
	f.Add(50.0)
	f.Add(-1.0)
	f.Add(101.0)
	f.Add(math.NaN())
	f.Add(math.Inf(1))
	f.Add(math.Inf(-1))
	f.Add(math.SmallestNonzeroFloat64)

	f.Fuzz(func(t *testing.T, percent float64) {
		sampler := rootSampler(percent)
		if sampler == nil {
			t.Fatalf("rootSampler(%v) returned nil", percent)
		}

		description := sampler.Description()
		switch {
		case description == "AlwaysOnSampler", description == "AlwaysOffSampler":
			return
		case strings.HasPrefix(description, "TraceIDRatioBased{"):
			// The fraction the SDK actually stored. A ratio sampler is only
			// meaningful for a fraction in [0, 1]; anything else means the
			// guards above it let a value through that they should have caught.
			raw := strings.TrimSuffix(
				strings.TrimPrefix(description, "TraceIDRatioBased{"), "}")
			fraction, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				t.Fatalf("rootSampler(%v) built %q, whose fraction does not parse: %v",
					percent, description, err)
			}
			if math.IsNaN(fraction) || fraction < 0 || fraction > 1 {
				t.Errorf("rootSampler(%v) samples a fraction of %v, which is not a share of anything",
					percent, fraction)
			}
		default:
			t.Errorf("rootSampler(%v) built %q, which is none of the three known samplers",
				percent, description)
		}
	})
}
