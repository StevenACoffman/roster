package serve

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/cors"

	"github.com/StevenACoffman/roster/internal/oneroster"
	"github.com/StevenACoffman/roster/internal/profiling"
	"github.com/StevenACoffman/roster/internal/resilience"
	"github.com/StevenACoffman/roster/internal/telemetry"
)

// newServerHandler wires every route and the global middleware chain, returning
// the handler the listener will serve.
//
// Anything that can fail resolves here, so addRoutes cannot: by the time routes
// are registered every dependency already exists.
//
// A nil pool is accepted, and the health endpoints then report the database as
// unreachable rather than panicking — which is also what makes the routing table
// testable without a container.
func (cfg *Config) newServerHandler(
	pool *pgxpool.Pool, logger *slog.Logger,
) (http.Handler, error) {
	otelInterceptor, err := telemetry.NewConnectInterceptor()
	if err != nil {
		return nil, fmt.Errorf("building the opentelemetry interceptor: %w", err)
	}

	resilientDB := resilience.NewDB(resilience.Config{
		MaxRetries:       cfg.MaxRetries,
		BaseDelay:        resilience.DefaultConfig().BaseDelay,
		MaxDelay:         resilience.DefaultConfig().MaxDelay,
		JitterFactor:     resilience.DefaultConfig().JitterFactor,
		FailureThreshold: cfg.BreakerFailureThreshold,
		SuccessThreshold: resilience.DefaultConfig().SuccessThreshold,
		OpenDelay:        cfg.BreakerOpenDelay,
	}, logger)

	authenticator := oneroster.NewAuthenticator(pool, cfg.DevSubject)

	// Interceptor order is the request's path inwards: tracing outermost so that
	// even a rejected request produces a span, then the deadline so it covers
	// everything after it, then authentication, then validation last — there is
	// no point parsing a body from a caller who has not identified themselves.
	//
	// There is no authorization interceptor. Scoping happens in SQL; see
	// sql/queries/rostering.sql.
	interceptors := []connect.Interceptor{
		otelInterceptor,
		newTimeoutInterceptor(cfg.RequestTimeout),
	}

	// Admission control before authentication, not after: authenticating a
	// request that is about to be shed spends a token lookup — a database round
	// trip — on load the service has already decided to refuse.
	rateLimit := resilience.RateLimitConfig{
		RequestsPerSecond: cfg.RateLimitRPS,
		MaxWait:           resilience.DefaultRateLimitConfig().MaxWait,
	}
	if rateLimit.Enabled() {
		interceptors = append(interceptors, resilience.NewRateLimitInterceptor(rateLimit))
	}

	interceptors = append(interceptors,
		authenticator.NewInterceptor(),
		validate.NewInterceptor(),
	)

	mux := http.NewServeMux()
	addRoutes(mux, pool, resilientDB, interceptors)

	var handler http.Handler = mux
	// Outermost of the HTTP middleware, so the pprof labels cover the whole
	// request. Applied unconditionally: it costs a few nanoseconds when no
	// Baggage header is present, and the labels are useful to anything reading
	// pprof on the admin listener, not only to a Pyroscope push.
	handler = profiling.K6LabelsMiddleware()(handler)
	return cors.New(cfg.corsOptions()).Handler(handler), nil
}

// corsOptions permits the browser origins the operator named.
//
// No origin is invented when the list is empty: a browser then refuses
// cross-origin calls, which is the correct default for a service holding
// student records. Credentialed CORS also forbids "*", so it is dropped rather
// than emitted for the browser to reject.
func (cfg *Config) corsOptions() cors.Options {
	origins := make([]string, 0)
	for origin := range strings.SplitSeq(cfg.AllowedOrigins, ",") {
		if origin = strings.TrimSpace(origin); origin != "" && origin != "*" {
			origins = append(origins, origin)
		}
	}

	return cors.Options{
		AllowedOrigins:   origins,
		AllowCredentials: true,
		AllowedMethods: []string{
			http.MethodGet,
			http.MethodPost,
			http.MethodOptions,
		},
		AllowedHeaders: []string{
			"Accept",
			"Authorization",
			"Content-Type",
			"Connect-Protocol-Version",
			"Connect-Timeout-Ms",
			"Grpc-Timeout",
			"Traceparent",
			"Tracestate",
		},
		ExposedHeaders: []string{
			"Content-Encoding",
			"Connect-Content-Encoding",
			"Grpc-Status",
			"Grpc-Message",
			"traceparent",
			"tracestate",
		},
		MaxAge: 300,
	}
}
