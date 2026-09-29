package resilience

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/failsafe-go/failsafe-go/ratelimiter"
)

// RateLimitConfig tunes server-side admission control.
type RateLimitConfig struct {
	// RequestsPerSecond admitted; zero disables rate limiting entirely.
	RequestsPerSecond uint
	// MaxWait for a permit. Short on purpose: a caller would rather be told to
	// back off than sit in a queue for longer than its own timeout, then be
	// served a response nobody is waiting for any more.
	MaxWait time.Duration
}

// DefaultRateLimitConfig returns a limit suited to one service instance.
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		RequestsPerSecond: 200,
		MaxWait:           250 * time.Millisecond,
	}
}

// Enabled reports whether a rate limit should be installed.
func (c RateLimitConfig) Enabled() bool { return c.RequestsPerSecond > 0 }

// NewRateLimitInterceptor admits at most the configured rate, shedding the
// excess with CodeResourceExhausted.
//
// This is admission control, not client throttling: its job is to stop an
// overload becoming an outage, by refusing work the instance cannot do rather
// than accepting all of it and doing none of it well.
//
// Per-instance. A fleet-wide limit needs a shared counter, which one process
// cannot honestly provide — so the limit is documented as per-instance rather
// than presented as something it is not.
//
// The limiter is smooth rather than bursty, so permits are spaced evenly instead
// of handing the database a whole second's allowance in one go.
func NewRateLimitInterceptor(cfg RateLimitConfig) connect.UnaryInterceptorFunc {
	limiter := ratelimiter.NewSmoothBuilder[any](cfg.RequestsPerSecond, time.Second).
		WithMaxWaitTime(cfg.MaxWait).
		Build()

	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if err := limiter.AcquirePermitWithMaxWait(ctx, cfg.MaxWait); err != nil {
				if errors.Is(err, ratelimiter.ErrExceeded) {
					return nil, connect.NewError(
						connect.CodeResourceExhausted,
						fmt.Errorf("rate limit of %d requests per second exceeded",
							cfg.RequestsPerSecond),
					)
				}
				// The only other outcome is the caller's own context ending.
				return nil, connect.NewError(connect.CodeCanceled, err)
			}
			return next(ctx, req)
		}
	}
}
