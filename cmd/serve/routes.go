package serve

import (
	"io"
	"net/http"
	"os"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sudorandom/protojsonx/protojsonxconnect"

	"github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1/onerosterv1p2v1connect"
	"github.com/StevenACoffman/roster/internal/oneroster"
	"github.com/StevenACoffman/roster/internal/resilience"
)

// addRoutes registers every route in one place.
//
// It never fails: anything that can error is resolved in newServerHandler before
// this is called, so there is no half-registered mux to reason about.
func addRoutes(
	mux *http.ServeMux,
	pool *pgxpool.Pool,
	resilientDB *resilience.DB,
	interceptors []connect.Interceptor,
) {
	rosterPath, rosterHandler := onerosterv1p2v1connect.NewRosterServiceHandler(
		oneroster.NewHandler(pool).WithResilience(resilientDB),
		connect.WithCodec(&protojsonxconnect.Codec{}),
		connect.WithInterceptors(interceptors...),
	)
	mux.Handle(rosterPath, rosterHandler)

	mux.Handle("GET /openapi.yaml", handleOpenAPISpec())
	mux.Handle("GET /docs", handleDocs())
	mux.Handle("GET /healthz", handleLiveness())
	mux.Handle("GET /readyz", handleReadiness(pool, resilientDB))

	mux.Handle("/", http.NotFoundHandler())
}

// handleLiveness answers whether the process itself is running.
//
// It deliberately touches no dependency: an orchestrator uses liveness to decide
// whether to kill the container, and a database blip must not cause healthy
// processes to be restarted. Dependency health belongs on /readyz.
func handleLiveness() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"status":"ok"}`)
	})
}

// handleReadiness answers whether the service can serve traffic right now.
//
// Two conditions, not one. The database must be reachable, and the circuit
// breaker must be admitting calls: an instance whose breaker is open is shedding
// every request, and a readiness probe that only pinged the pool would keep
// reporting healthy while the instance served nothing. Taking it out of rotation
// is safely reversible, unlike the restart liveness triggers.
func handleReadiness(pool *pgxpool.Pool, resilientDB *resilience.DB) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case pool == nil || pool.Ping(r.Context()) != nil:
			writeJSON(w, http.StatusServiceUnavailable,
				`{"status":"unavailable","database":"disconnected"}`)
		case !resilientDB.Healthy():
			writeJSON(w, http.StatusServiceUnavailable,
				`{"status":"unavailable","database":"connected","breaker":"open"}`)
		default:
			writeJSON(w, http.StatusOK, `{"status":"ok","database":"connected"}`)
		}
	})
}

// handleOpenAPISpec serves the generated specification.
//
// The candidate paths let the binary find the spec whether it is run from the
// repository root or from a package directory under `go test`.
func handleOpenAPISpec() http.Handler {
	candidates := []string{
		"gen/openapi/oneroster/v1p2/v1/service.openapi.yaml",
		"../../gen/openapi/oneroster/v1p2/v1/service.openapi.yaml",
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				http.ServeFile(w, r, candidate)
				return
			}
		}
		http.NotFound(w, r)
	})
}

// handleDocs serves the Scalar API reference page, which loads the spec from
// /openapi.yaml in the browser.
func handleDocs() http.Handler {
	const page = `<!doctype html>
<html>
  <head>
    <title>OneRoster API Reference</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
  </head>
  <body>
    <script id="api-reference" data-url="/openapi.yaml"></script>
    <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
  </body>
</html>`
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, page)
	})
}

// writeJSON writes a pre-rendered JSON body with the given status. The bodies
// are constants, so there is nothing to marshal and nothing that can fail
// halfway through a response.
func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
