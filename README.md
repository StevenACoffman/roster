# Roster Reference Architecture (`roster-reference`)

A modern, production-grade reference microservice modeled after the OneRoster domain, built with **[Go 1.27](https://go.dev)**, **[ConnectRPC](https://connectrpc.com)**, **[OpenTelemetry](https://opentelemetry.io)**, **[Buf](https://buf.build)**, **[protovalidate](https://github.com/bufbuild/protovalidate)**, **[FauxRPC](https://github.com/sudorandom/fauxrpc)**, **[sqlc](https://sqlc.dev)**, **[PostgreSQL](https://www.postgresql.org)**, and a **[React](https://react.dev)** + **[Vite](https://vite.dev)** frontend using **[TanStack Query](https://tanstack.com/query)** and **[Connect-Web](https://connectrpc.com/docs/web/getting-started)**.

This was put together [by request](https://github.com/sudorandom/kmcd.dev/issues/11). This shows how you can have static typing and validation for your APIs, your code (because Go) and database queries via `sqlc`.

______________________________________________________________________

## 🛠️ Tech Stack & Tooling

| Component                 | Tool / Library                                                                                       | Description                                                                            |
| :------------------------ | :--------------------------------------------------------------------------------------------------- | :------------------------------------------------------------------------------------- |
| **Tooling Manager**       | [mise](https://mise.jdx.dev)                                                                         | Installs and manages `go`, `buf`, `sqlc`, `node`, `pnpm`, `fauxrpc`, etc.              |
| **Language**              | Go 1.27                                                                                              | High-performance backend runtime                                                       |
| **RPC & API**             | [ConnectRPC](https://connectrpc.com)                                                                 | Multi-protocol RPC (Connect, gRPC, gRPC-Web) over HTTP/1.1 and HTTP/2                  |
| **Observability**         | [OpenTelemetry](https://opentelemetry.io)                                                            | Distributed tracing with W3C `traceparent` adoption via `otelconnect` and `otelpgx`    |
| **Protobuf Management**   | [Buf CLI](https://buf.build)                                                                         | Linting, breaking change detection, and multi-language code generation                 |
| **Validation**            | [protovalidate](https://buf.build/bufbuild/protovalidate)                                            | Schema-level validation rules compiled into Protobuf definitions                       |
| **OpenAPI Generation**    | [protoc-gen-connect-openapi](https://github.com/sudorandom/protoc-gen-connect-openapi)               | Generates OpenAPI 3.1 specifications directly from Connect Protobuf definitions        |
| **Testing & Mocking**     | [FauxRPC](https://github.com/sudorandom/fauxrpc)                                                     | Fake Connect/gRPC/REST server with CEL-driven stubs and failure simulation             |
| **Integration Testing**   | [Testcontainers for Go](https://golang.testcontainers.org)                                           | Ephemeral PostgreSQL containers with automated goose migrations and TRUNCATE isolation |
| **Database & ORM**        | [sqlc](https://sqlc.dev) + [pgx/v5](https://github.com/jackc/pgx/v5)                                 | Compile-time type-safe Go code generated from raw SQL queries                          |
| **Local Database**        | Docker Compose                                                                                       | Local PostgreSQL container with automated schema migrations via goose                  |
| **Linter & Security**     | [golangci-lint](https://golangci-lint.run) + [gosec](https://github.com/securego/gosec)              | Static analysis and security vulnerability scanner                                     |
| **Vulnerability Scanner** | [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck)                                  | Official Go vulnerability scanner for known CVEs                                       |
| **Frontend**              | [React](https://react.dev) + [Vite](https://vite.dev) + [TanStack Query](https://tanstack.com/query) | Responsive SPA with Connect-Web, black/white dark mode toggle, and photo URLs          |

______________________________________________________________________

## 📂 Project Structure

```text
├── .mise.toml              # Toolchain versions (Go 1.27, Buf, sqlc, node, pnpm, fauxrpc)
├── .golangci.yml           # Linter configuration with gosec enabled
├── justfile                # Task runner commands (just)
├── docker-compose.yml      # Local PostgreSQL service
├── buf.yaml                # Buf module definition with protovalidate dependency
├── buf.gen.yaml            # Buf code generation for Go, OpenAPI, and TypeScript
├── sqlc.yaml               # SQLC configuration with pgx/v5 engine
├── cmd/
│   ├── migrate/            # Database migration CLI tool
│   └── server/             # Microservice entry point (CORS, h2c, interceptors, OTel, OpenAPI)
├── internal/
│   ├── auth/               # ConnectRPC Bearer Token authentication interceptor & context claims
│   ├── config/             # Environment variable configuration
│   ├── db/                 # SQLC generated database code & pgxpool with otelpgx
│   ├── pet/                # OneRosterService: pure core (core.go) + I/O shell (handler.go)
│   ├── telemetry/          # OpenTelemetry TracerProvider & Connect interceptor setup
│   └── testutil/           # PostgreSQL Testcontainers helper with goose migrations & TRUNCATE
├── proto/
│   ├── pet/v2/pet.proto    # Protobuf schema with validation rules
│   └── pet/v1/pet.proto    # withdrawn; retained for buf compatibility
├── gen/                    # Generated Go stubs, OpenAPI specs, and binary descriptor images
├── sql/
│   ├── schema/             # Versioned goose migrations
│   └── queries/            # SQLC queries for pets
├── stubs/
│   ├── normal/             # FauxRPC stubs with CEL dynamic responses
│   └── failures/           # FauxRPC failure stubs for error testing
├── test/
│   └── integration_test.go # End-to-end ConnectRPC & PostgreSQL integration tests
└── web/                    # React + Vite frontend with TanStack Query and Connect-Web
    ├── src/
    │   ├── components/     # Layout, ThemeSwitcher, etc.
    │   ├── lib/            # Connect client, date utilities
    │   ├── pages/          # OneRosterList, OneRosterDetails, CreateOneRoster, EditOneRoster, Docs
    │   └── test/           # Vitest tests with ephemeral FauxRPC server
    └── package.json
```

______________________________________________________________________

## 🚀 Getting Started

### 1. Install Tooling with `mise`

```bash
just setup
```

### 2. Generate Code

Regenerate Protobuf, Connect stubs, OpenAPI specs, `sqlc` database code, and TypeScript types:

```bash
just generate
```

### 3. Start Local PostgreSQL with Docker Compose

```bash
just up
```

### 4. Code Quality, Security & Tests

```bash
# Run golangci-lint over both build configurations (default and integration)
just lint

# Apply every fix the linters can make automatically
just lint-fix

# Run Go vulnerability check
just vulncheck

# Fast unit suite: race detector on, no Docker required (~5s)
just test

# The same suite with a coverage summary
just test-cover

# Container-backed suites (needs a running Docker/Colima daemon)
just test-integration

# Fuzz the pure core; a short burst per target
just fuzz-all 20s
# ...or one target for longer
just fuzz FuzzNewOneRosterInput 60s

# Mutation-test the pure core (fails below 90% MSI)
just mutate

# Mutation-test only the lines changed against a base ref
just mutate-diff main

# Verify go.mod/go.sum are tidy
just tidy-check

# Run frontend tests (Vitest + ephemeral FauxRPC mock server)
just test-web

# Every gate CI runs
just check

# Install the git hooks (pre-commit, pre-push, commit-msg)
just hooks
```

[CI](.github/workflows/ci.yml) runs the same gates on every push and PR: lint, tidy,
generated-code freshness, `buf lint` and breaking-change detection, unit and
integration tests, a fuzz smoke run, `govulncheck`, mutation testing, and the
frontend build and tests.

### 5. Run the Go Microservice

```bash
just run
```

The service will be listening on `https://localhost:8080` (TLS enabled via `mkcert`).

- **Interactive OpenAPI Documentation:** `https://localhost:8080/docs`
- **OpenAPI 3.1 Spec (YAML):** `https://localhost:8080/openapi.yaml`
- **Liveness:** `/healthz` reports that the process is up. It touches no dependency,
  so a database blip never gets a healthy pod killed.
- **Readiness:** `/readyz` reports that PostgreSQL is reachable. Poll this one from a
  balancer.
- **Connect Service:** `https://localhost:8080/pet.v2.OneRosterService/`

Operational endpoints live on a **separate admin listener**, loopback-bound
(`127.0.0.1:9090`) so profiling data is never public:

- `/metrics` exposes RED metrics as histograms (p50/p95/p99 queryable), plus Go saturation.
- `/debug/pprof/` covers CPU, heap, goroutine, and mutex profiles.
- `POST /debug/trace/snapshot` dumps the flight recorder for `go tool trace`.
  Enable with `TRACE_SNAPSHOT_DIR`.

### Continuous Profiling

Set `PYROSCOPE_ENDPOINT` and the service pushes all ten Go profile types to
[Pyroscope](https://grafana.com/oss/pyroscope/); `just up` starts one on
<http://localhost:4040>. Unset, nothing is collected and nothing is sent.

```bash
PYROSCOPE_ENDPOINT=http://localhost:4040 just run
```

Two details carry most of the value:

- Mutex and block profiles are empty unless `SetMutexProfileFraction` and
  `SetBlockProfileRate` are non-zero. The profiler sets both, so lock contention
  is visible rather than silently absent.
- The TracerProvider is wrapped so profile samples carry `trace_id` and
  `span_name`. A slow span opens as the flame graph recorded while it ran.

pprof stays on the admin listener, so a Grafana Alloy `pyroscope.scrape` can pull
instead of the service pushing.

### Load Testing

```bash
just up && just run
just load-test               # or: just load-test 2m 20
```

[`k6/load.js`](k6/load.js) drives a browse and a write scenario and sends a
`Baggage` header carrying `k6.test_run_id` and `k6.scenario`. The server turns
`k6.*` baggage into pprof labels, so a flame graph narrows to one run or one
scenario. `k6_scenario="write"` shows only the create/update path. k6 does not
send that header on its own, so the script sets it.

### Optional Observability Stack

Profiling, metrics and tracing all work without this. It exists for one thing:
clicking from a slow trace to the flame graph recorded while it ran.

```bash
just observability     # Alloy, Tempo, Prometheus, Grafana — opt-in
OTEL_TRACES_EXPORTER=otlp OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 \
  PYROSCOPE_ENDPOINT=http://localhost:4040 ADMIN_ADDR=0.0.0.0:9090 just run
```

Grafana is on <http://localhost:3000> with Tempo, Prometheus and Pyroscope
provisioned, and the Tempo datasource carries `tracesToProfiles`, so a span links
to its profile. Nothing starts unless you ask: `just up` and a plain
`docker compose up` still bring up only Postgres and Pyroscope.

`ADMIN_ADDR=0.0.0.0:9090` is needed because Alloy runs in Docker and scrapes
`/metrics` through the host gateway; the loopback default is unreachable from a
container. That is a real loosening, because pprof becomes reachable from anything
that can route to the host. Use it locally rather than in a deployment.

Traces reach Tempo through Alloy rather than directly. The service could talk to
Tempo itself, but a collector is the shape a deployment has: one place to add
sampling or a second destination without redeploying.

### 6. Run the Web Frontend

```bash
just web-dev
```

Open `https://localhost:4321` in your browser (TLS enabled via `mkcert`).

- **Web Interface:** `https://localhost:4321/`
- **Embedded API Documentation (Scalar):** `https://localhost:4321/docs`
- **OpenAPI 3.1 Spec (YAML):** `https://localhost:4321/openapi.yaml`

### 7. Run FauxRPC Standalone Mock Server

To run a mock server with fake data without starting PostgreSQL:

```bash
# Run with normal dynamic stubs (celfakeit)
just fauxrpc

# Run with failure stubs (simulating errors across all RPC methods)
just fauxrpc-fail
```

- **Mock Documentation:** `https://127.0.0.1:8080/fauxrpc/docs/`
FauxRPC will be available over HTTPS at `https://127.0.0.1:8080` with built-in documentation at `/fauxrpc/docs/`.

### 8. Testing the Frontend with FauxRPC

You can test the frontend against FauxRPC both interactively in the browser and via automated tests:

#### Interactive Development / Manual Testing

1. In one terminal, start the FauxRPC mock server (listening on `:8080`, the same port as the Go server):

   ```bash
   just fauxrpc
   # or test failure states:
   just fauxrpc-fail
   ```

2. In another terminal, start the web dev server:

   ```bash
   just web-dev
   ```

   The frontend at `https://localhost:4321` proxies all requests to `https://localhost:8080` (FauxRPC).

#### Automated Frontend Tests

Run the Vitest test suite, which automatically spawns ephemeral FauxRPC mock servers and verifies frontend pages, components, and error states:

```bash
just test-web
```

Or from the `web` directory:

```bash
pnpm test
```

______________________________________________________________________

## 🧪 Testing

Tests that touch the database run against real PostgreSQL (`postgres:17-alpine`) using **[Testcontainers for Go](https://golang.testcontainers.org)** instead of mocks or SQLite. This even includes top-level handler code, so those tests exercise every layer beneath it without any mocking code. Unit tests built on interfaces and mocks tend to end up asserting against the mocks rather than the real behaviour.

- **Automated migrations**: containers start with the full suite of goose migrations applied via [`postgres.Migrate`](internal/postgres/migrate.go).
- **Fast isolation via `TRUNCATE`**: to keep test suites fast (<3s), suites reuse the container and run `TRUNCATE TABLE pets RESTART IDENTITY CASCADE;` between tests instead of recreating containers.
- **Docker & Colima**: automatically detects Colima on macOS (`~/.colima/default/docker.sock`). Set `DATABASE_URL` to point tests at an existing database instead.
- **Pure unit tests**: logic without database dependencies (config, CORS, auth headers, validation helpers) runs in-memory.
- **Build-tagged separation**: container suites sit behind `//go:build integration`,
  so `just test` stays fast and Docker-free; `just test-integration` runs everything.
  Both run `-race`.
- **Fuzzing** over the pure core ([`fuzz_test.go`](internal/oneroster/fuzz_test.go)),
  asserting invariants rather than fixed outputs: an accepted pet always has
  trimmed, non-blank fields and non-nil slices, whatever arrived on the wire.
- **Mutation testing** with [mutago](https://github.com/quality-gates/mutago) over
  `internal/pet/core.go`, reaching **95% MSI** in ~30s. It answers what coverage
  cannot: not "did a test run this line?" but "would any test have noticed if it
  behaved differently?". Scoped to the pure core, because `handler.go` is proven by the
  container suites a mutation run does not execute. See [`.mutago.yml`](.mutago.yml);
  CI also reports surviving mutants on the lines a PR changed.
- **Golden schema snapshot** ([`schema_tables.golden`](internal/postgres/testdata/schema_tables.golden)):
  a migration that drops a column or loosens a constraint shows up as a reviewable
  diff. Refresh with
  `go test -tags=integration -run TestSchemaGolden ./internal/db/ -update`.
- **End-to-end in-process**: [`serve_integration_test.go`](cmd/serve/serve_integration_test.go)
  drives the wired `run()` on an OS-assigned port as a real HTTP client.
- **Frontend mocks**: web tests in `web/` use [FauxRPC](https://github.com/sudorandom/fauxrpc) stubs to test UI states without a running backend.

______________________________________________________________________

## ⚙️ Configuration

Read from the environment at startup. Development is the default so an unconfigured
checkout runs; `APP_ENV=production` (or `DEV_MODE=false`) invents no credential,
token or CORS origin for you.

| Variable                                  | Default                                   | Purpose                                                                 |
| ----------------------------------------- | ----------------------------------------- | ----------------------------------------------------------------------- |
| `PORT`                                    | `8080`                                    | Public listen port                                                      |
| `DATABASE_URL`                            | local postgres                            | PostgreSQL connection string                                            |
| `APP_ENV`                                 | `development`                             | `production` switches to the strict posture                             |
| `DEV_MODE`                                | derived from `APP_ENV`                    | Explicit override                                                       |
| `AUTO_MIGRATE`                            | `true` in dev                             | Apply migrations on startup                                             |
| `AUTH_ENABLED`                            | `true`                                    | Enforce authentication                                                  |
| `AUTH_TOKENS`                             | dev token in dev                          | Comma-separated static bearer tokens                                    |
| `TRUST_PROXY_HEADERS`                     | `false`                                   | Accept upstream IAP / OAuth2-Proxy identity headers                     |
| `DEV_EMAIL`                               | `developer@local.test`                    | Identity injected in dev mode                                           |
| `DEV_ROLES`                               | `user,admin`                              | Roles for the dev identity; narrow it to test as a non-admin            |
| `CORS_ALLOWED_ORIGINS`                    | localhost in dev                          | Comma-separated; `*` is dropped, as credentialed CORS forbids it        |
| `TLS_CERT_FILE` / `TLS_KEY_FILE`          | `.certs/*.pem`                            | Serve TLS when both exist, otherwise cleartext                          |
| `LOG_LEVEL`                               | `debug` in dev, `info` otherwise          | `debug`, `info`, `warn`, `error`                                        |
| `LOG_FORMAT`                              | `text` in dev, `json` otherwise           | `json` for production collectors                                        |
| `ADMIN_ADDR`                              | `127.0.0.1:9090`                          | Admin listener; `off` disables it                                       |
| `TRACE_SNAPSHOT_DIR`                      | unset                                     | Enables the flight recorder and names the snapshot directory            |
| `RATE_LIMIT_RPS`                          | `200`                                     | Per-instance admission rate; `0` disables it                            |
| `AUTHZ_POLICY`                            | empty (admin only)                        | Role matrix: `procedure=role,role` entries separated by `;` or newlines |
| `PYROSCOPE_ENDPOINT`                      | unset                                     | Pyroscope server; empty disables continuous profiling                   |
| `PYROSCOPE_BASIC_AUTH_USER` / `_PASSWORD` | unset                                     | Grafana Cloud credentials                                               |
| `DEPLOYMENT_ENVIRONMENT`                  | `development`                             | Tags profiles so environments stay distinct                             |
| `OTEL_SERVICE_NAME`                       | `pets-service`                            | Resource attribute shared by traces and metrics                         |
| `OTEL_TRACES_EXPORTER`                    | `otlp` if an endpoint is set, else `none` | `otlp`, `stdout`, `none`                                                |
| `OTEL_EXPORTER_OTLP_ENDPOINT`             | unset                                     | OTLP gRPC collector address                                             |
| `OTEL_SAMPLE_PERCENTAGE`                  | `100`                                     | Root-span sampling; accepts a trailing `%`                              |
| `OTEL_CONFIG_FILE`                        | unset                                     | Optional YAML/JSON/TOML telemetry config, overlaid by the environment   |

Startup refuses to proceed if authentication is enabled in production with neither
`TRUST_PROXY_HEADERS` nor `AUTH_TOKENS` set.

______________________________________________________________________

## 📄 Pagination

`ListRosters` is cursor-paged. Pass `page_size`, read `next_page_token` from the
response, and send it back as `page_token`. An empty token means the last page.

Offset paging was removed because it is not consistent under concurrent writes: a
row inserted between two fetches shifts the window, so one pet is served twice and
another never at all. That was reproduced against a real database before the
change, and the regression test replays it. Sending the deprecated `page` field is
now rejected rather than silently honoured.

The page and its `total_count` come from one query, a `COUNT(*) OVER()` inside a CTE
that carries the filters. The total counts everything matching the filter
rather than the remainder after the cursor, and page and count can no longer disagree
the way two round trips could.

______________________________________________________________________

## 🔑 Authorization

Authentication establishes *who* you are. Authorization decides *what you may do*.
This follows the conventional OneRoster model: a declarative role × action matrix,
**deny by default**, with an `admin` bypass so an operator cannot lock themselves out.

```bash
AUTHZ_POLICY="
/roster.v1.RosterService/ListRoster=viewer,editor
/roster.v1.RosterService/GetPet=viewer,editor
/roster.v1.RosterService/CreatePet=editor
/roster.v1.RosterService/UpdatePet=editor
/roster.v1.RosterService/DeletePet=editor
"
```

Roles come from the caller's claims, either `X-Forwarded-Groups` behind oauth2-proxy
or `DEV_ROLES` locally. A procedure the matrix does not name is refused, so adding an
RPC cannot silently open it, and a malformed policy fails startup rather than
falling back to something permissive.

The matrix covers *action* authorization: may this role call `DeletePet` at all?
Row-level rules belong in SQL `WHERE` clauses, where the database enforces them, and
the two compose. The design deliberately omits an **ownership check**, because
shelter staff edit each other's records, so the reference OneRoster models
[RBAC](https://github.com/permitio/opal-example-policy-repo) as role × action
rather than per-record ownership.

> **Upgrading an existing deployment:** the default denies everything to
> everyone but `admin`, so the service will refuse traffic until `AUTHZ_POLICY` is
> set. That is deliberate, since the safe posture is closed, but it is a breaking
> change. Local development is unaffected: the dev identity is an admin.

______________________________________________________________________

## 🛡️ Resilience

Backed by [failsafe-go](https://failsafe-go.dev), scoped to failures this service
actually has:

- **Retry with backoff and jitter**, reads only, and only for SQLSTATEs known to be
  both transient and certain not to have applied. Replaying a write that may
  already have committed is worse than surfacing the error.
- **Circuit breaker** over reads and writes alike, sharing one breaker, so an outage
  fails fast instead of parking requests on a pool wait. Its predicate ignores caller
  errors: constraint violations mean bad requests, not an unhealthy database.
- **Rate limiting** (`RATE_LIMIT_RPS`), smooth rather than bursty so permits are
  spaced evenly.
- **Per-RPC deadlines**: a stricter client deadline is honoured, a longer one clamped.
- **Explicit pool bounds** rather than pgx's CPU-derived default, because without a
  ceiling an outage just grows the wait queue.

______________________________________________________________________

## 🔐 Authentication

In production, user login is handled by an upstream reverse proxy (like Google Cloud IAP or OAuth2 Proxy), which forwards user identity via headers. The service also supports static bearer tokens for machine-to-machine calls.

- **Proxy headers**: reads identity from IAP (`X-Goog-Authenticated-User-*`) or OAuth2 Proxy (`X-Forwarded-*`) when `TRUST_PROXY_HEADERS=true`. Only enable this behind a proxy that strips untrusted client headers.
- **Bearer tokens**: set `AUTH_TOKENS=token1,token2` for service-to-service or CLI access (`Authorization: Bearer <token>`).
- **Local dev**: with `DEV_MODE=true` (the default) a request without credentials gets a developer identity (`developer@local.test`, roles `user,admin`). The middleware simulates oauth2-proxy rather than IAP, because IAP carries no group membership and so could never exercise a role. Set `DEV_ROLES=user` to test as a non-admin.
- **Context**: parsed claims are accessible in Go handlers via [`oneroster.SubjectFromContext(ctx)`](internal/oneroster/context.go).
