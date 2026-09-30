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

	// Dev mode implies proxy trust, because that is how the dev identity reaches
	// the authenticator: as headers, through the same code a deployment uses.
	// serve.validate() already refuses --dev-subject on a non-loopback address.
	authOpts := []oneroster.AuthenticatorOption{}
	if cfg.TrustProxyHeaders || cfg.DevSubject != "" {
		authOpts = append(authOpts, oneroster.WithTrustedProxyHeaders())
	}
	authenticator := oneroster.NewAuthenticator(pool, authOpts...)

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
	if cfg.DevSubject != "" {
		handler = devIdentityMiddleware(cfg.DevSubject)(handler)
	}
	handler = profiling.K6LabelsMiddleware()(handler)
	return cors.New(cfg.corsOptions()).Handler(handler), nil
}

// devSubjectHeader lets a developer name a different caller per request, which
// is how org scoping gets exercised locally without minting tokens.
const devSubjectHeader = "X-Roster-Dev-Subject"

// devIdentityMiddleware supplies a local identity by writing the headers an
// oauth2-proxy would.
//
// Injecting headers rather than short-circuiting the authenticator is the whole
// point: local development then runs the same resolution a deployment behind a
// proxy runs, so that path cannot rot untested while the dev path stays green.
//
// It defers to any credential the request already carries, so a local checkout
// can still exercise a real bearer token.
func devIdentityMiddleware(devSubject string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			subject := devSubject
			if named := r.Header.Get(devSubjectHeader); named != "" {
				subject = named
			}

			if r.Header.Get("Authorization") == "" &&
				r.Header.Get("X-Goog-Authenticated-User-Email") == "" &&
				r.Header.Get("X-Goog-Authenticated-User-Id") == "" &&
				r.Header.Get("X-Forwarded-User") == "" &&
				r.Header.Get("X-Forwarded-Email") == "" {
				r.Header.Set("X-Forwarded-User", subject)
			}

			next.ServeHTTP(w, r)
		})
	}
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
