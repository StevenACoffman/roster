package serve

import (
	"context"
	"time"

	"connectrpc.com/connect"
)

// newTimeoutInterceptor bounds every RPC with a deadline.
//
// A caller's own deadline wins when it is shorter: the point is to stop a
// request running unbounded when nobody set one, not to override a client that
// asked for less. A client asking for more is capped, because an unbounded
// request holds a database connection for as long as it runs.
//
// A zero or negative timeout disables the interceptor rather than expiring every
// request immediately, which is the safer reading of a misconfigured flag.
func newTimeoutInterceptor(timeout time.Duration) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if timeout <= 0 {
				return next(ctx, req)
			}
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= timeout {
				return next(ctx, req)
			}

			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return next(ctx, req)
		}
	}
}
