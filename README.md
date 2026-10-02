# Roster

An implementation of the **[IMS Global OneRoster v1.2](https://www.1edtech.org/standards/oneroster)** rostering API, built with **[Go 1.27](https://go.dev)**, **[ConnectRPC](https://connectrpc.com)**, **[OpenTelemetry](https://opentelemetry.io)**, **[Buf](https://buf.build)**, **[protovalidate](https://github.com/bufbuild/protovalidate)**, **[FauxRPC](https://github.com/sudorandom/fauxrpc)**, **[sqlc](https://sqlc.dev)** and **[PostgreSQL](https://www.postgresql.org)**.

It serves 20 read RPCs and 18 curation RPCs, because a roster is browsed and edited by hand as well as loaded from a source system. Authorization is enforced by the database rather than by application code, so a caller scoped to one school cannot read the rows of another. A curation web frontend is intended but not built yet.

This shows how you can have static typing and validation for your APIs, your code (because Go) and database queries via `sqlc`.

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
├── .golangci.yml           # Linter configuration; depguard bans testify in tests
├── .mutago.yml             # Mutation-testing configuration
├── .vale.ini               # Prose linting for Markdown and SQL comments
├── justfile                # Task runner commands (just)
├── docker-compose.yml      # Local PostgreSQL service
├── buf.yaml                # Buf module definition with protovalidate dependency
├── buf.gen.yaml            # Buf code generation for Go, OpenAPI, and TypeScript
├── sqlc.yaml               # SQLC configuration with pgx/v5 engine
├── AGENTS.md               # Decisions that look wrong without their reasons
├── main.go                 # Entry point; owns os.Exit and nothing else
├── cmd/
│   ├── cmd.go              # Dispatcher; the only place commands register
│   ├── root/               # Shared Config and ExitError
│   ├── serve/              # API server (CORS, interceptors, OTel, admin listener)
│   ├── migrate/            # Migration CLI: up, down, status, version
│   └── version/            # Build and version reporting
├── internal/
│   ├── oneroster/          # The service: pure core + imperative shell
│   ├── postgres/           # pgx pool and goose migrations
│   ├── db/                 # SQLC generated code (lint-excluded)
│   ├── onerosterjson/      # go-jsonschema output from the OneRoster schemas
│   ├── telemetry/          # OpenTelemetry traces, metrics, Connect interceptor
│   ├── profiling/          # Pyroscope continuous profiling
│   ├── resilience/         # Retry, circuit breaker, rate limit, timeout
│   ├── logging/            # slog handler that adds trace context
│   ├── ptr/                # Pointer helpers
│   └── testutil/           # PostgreSQL Testcontainers helper with goose migrations
├── proto/
│   └── oneroster/v1p2/v1/  # 101 files, one message per file
├── gen/                    # Generated Go stubs, OpenAPI specs, descriptor images
├── sql/
│   ├── schema/             # Versioned goose migrations, embedded in the binary
│   └── queries/            # SQLC queries
├── stubs/
│   ├── normal/             # FauxRPC stubs with CEL dynamic responses
│   └── failures/           # FauxRPC failure stubs for error testing
├── test/
│   └── integration_test.go # Black-box suite through the generated client
├── k6/                     # Load test
└── web/
    └── src/gen/            # Generated TypeScript clients; no UI is built yet
```

`internal/oneroster` is split along the functional-core/imperative-shell line.
`core.go`, `cursor.go`, `curation.go`, `tostorage.go` and `identity.go` touch no
database and read no clock. `handler.go`, `curation_*.go`, `auth.go` and
`associations.go` are the shell.

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
just fuzz FuzzRequireMaskIsTotal 60s

# Mutation-test the pure core (fails below 95% covered MSI)
just mutate
# Benchmark the request path and report allocations
just bench
# Report which code no benchmark reaches
just bench-gaps

# Mutation-test only the lines changed against a base ref
just mutate-diff main

# Verify go.mod/go.sum are tidy
just tidy-check

# Lint the prose in Markdown and SQL comments
just vale
# Run frontend tests; skips while no web/package.json exists
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
- **Connect Service:** `https://localhost:8080/oneroster.v1p2.v1.RosterService/`

Operational endpoints live on a **separate admin listener**, loopback-bound
(`127.0.0.1:9090`) so profiling data is never public:

- `/metrics` exposes RED metrics as histograms (p50/p95/p99 queryable), plus Go saturation.
- `/debug/pprof/` covers CPU, heap, goroutine, and mutex profiles.
- `POST /debug/trace/snapshot` dumps the flight recorder for `go tool trace`.
  Enable with `--trace-snapshot-dir`.

### Continuous Profiling

Set `PYROSCOPE_ENDPOINT` and the service pushes all ten Go profile types to
[Pyroscope](https://grafana.com/oss/pyroscope/); `just up` starts one on
<http://localhost:4040>. Unset, nothing is collected and nothing is sent.

```bash
ROSTER_PYROSCOPE_ENDPOINT=http://localhost:4040 just run
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
ROSTER_OTEL_EXPORTER=otlp ROSTER_OTEL_ENDPOINT=localhost:4317 \
  ROSTER_PYROSCOPE_ENDPOINT=http://localhost:4040 ROSTER_ADMIN_ADDR=0.0.0.0:9090 just run
```

Grafana is on <http://localhost:3000> with Tempo, Prometheus and Pyroscope
provisioned, and the Tempo datasource carries `tracesToProfiles`, so a span links
to its profile. Nothing starts unless you ask: `just up` and a plain
`docker compose up` still bring up only Postgres and Pyroscope.

`ROSTER_ADMIN_ADDR=0.0.0.0:9090` is needed because Alloy runs in Docker and scrapes
`/metrics` through the host gateway; the loopback default is unreachable from a
container. That is a real loosening, because pprof becomes reachable from anything
that can route to the host. Use it locally rather than in a deployment.

Traces reach Tempo through Alloy rather than directly. The service could talk to
Tempo itself, but a collector is the shape a deployment has: one place to add
sampling or a second destination without redeploying.

### 6. The Curation Web Frontend

**Not built yet.** `web/src/` holds only `gen/`, the generated TypeScript
clients, and there is no `package.json`. The rostering API is meant to be
browsed and curated by hand as well as fed by imports, so a UI is intended, but
nothing in this repository serves one.

What is already in place for it:

- generated Connect-Web clients for every RPC, refreshed by `just generate`;
- an OpenAPI 3.1 document under `gen/openapi/`, which `just generate` copies to
  `web/public/openapi.yaml` once a `web/package.json` exists;
- `just test-web` and the CI `web` job, both of which detect the missing
  `package.json` and skip rather than fail.

### 7. Run FauxRPC Standalone Mock Server

To serve fake data for every RPC without starting PostgreSQL:

```bash
# Dynamic responses (celfakeit)
just fauxrpc

# Failure stubs, simulating errors across all RPC methods
just fauxrpc-fail
```

FauxRPC listens on `https://127.0.0.1:8080`, the same port the Go server uses,
with documentation at `/fauxrpc/docs/`. Stub definitions live in
[`stubs/`](stubs/README.md).

This is useful on its own for exercising a client against the schema. The
frontend workflow it was originally paired with needs the UI above.

______________________________________________________________________

## 🧪 Testing

Tests that touch the database run against real PostgreSQL (`postgres:17-alpine`) using **[Testcontainers for Go](https://golang.testcontainers.org)** instead of mocks or SQLite. This even includes top-level handler code, so those tests exercise every layer beneath it without any mocking code. Unit tests built on interfaces and mocks tend to end up asserting against the mocks rather than the real behaviour.

- **Automated migrations**: containers start with the full suite of goose migrations applied via [`postgres.Migrate`](internal/postgres/migrate.go).
- **A database per test**: one container is reused, and each test creates its own database inside it. That is what makes `t.Parallel` safe here, where a shared database with `TRUNCATE` between tests would not be.
- **Docker & Colima**: automatically detects Colima on macOS (`~/.colima/default/docker.sock`). Set `ROSTER_TEST_DATABASE_URL` to point tests at an existing PostgreSQL server instead of starting a container.
- **Pure unit tests**: logic without database dependencies (config, CORS, auth headers, validation helpers) runs in-memory.
- **Build-tagged separation**: container suites sit behind `//go:build integration`,
  so `just test` stays fast and Docker-free; `just test-integration` runs everything.
  Both run `-race`.
- **Fuzzing** over the pure core ([`fuzz_test.go`](internal/oneroster/fuzz_test.go)),
  asserting properties rather than fixed outputs: round-trip, idempotence,
  totality and boundary. One target found a real date-normalisation bug on its
  fourth seed.
- **Mutation testing** with [mutago](https://github.com/quality-gates/mutago) over
  the pure core, reaching **96% covered MSI**. It answers what coverage
  cannot: not "did a test run this line?" but "would any test have noticed if it
  behaved differently?". Gated on *covered* MSI, because the `toProto*` row
  builders in `core.go` are proven by the container suites a mutation run does
  not execute, and counting their mutants as survivors would make the gate
  unreachable. See [`.mutago.yml`](.mutago.yml);
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

Every knob is a registered flag. Nothing reads `os.Getenv`, so
`roster serve --help` is the whole configuration surface and this table is
generated from it.

`ff` derives an environment variable from each flag: prepend `ROSTER_`,
uppercase it, and write each dash as `_`, so `--database-url` becomes
`ROSTER_DATABASE_URL`. A flag given on the command line wins over its variable.

Where an ecosystem standard already exists, an empty flag passes no option at
all, so the OpenTelemetry SDK applies its own handling of `OTEL_*`. That is why
`--otel-endpoint` and `--otel-service-name` document what they defer to rather
than inventing a default.

| Variable                         | Default                                                                 | Purpose                                                                                                                                        |
| -------------------------------- | ----------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| `ROSTER_ADDR`                    | `127.0.0.1:8080`                                                        | listen address for the API                                                                                                                     |
| `ROSTER_DATABASE_URL`            | `postgres://postgres:password@localhost:5432/roster_db?sslmode=disable` | PostgreSQL connection string                                                                                                                   |
| `ROSTER_AUTO_MIGRATE`            | unset                                                                   | apply pending database migrations at startup                                                                                                   |
| `ROSTER_TLS_CERT`                | unset                                                                   | TLS certificate file; serves cleartext when this or `--tls-key` is unset                                                                       |
| `ROSTER_TLS_KEY`                 | unset                                                                   | TLS private key file                                                                                                                           |
| `ROSTER_CORS_ALLOWED_ORIGINS`    | unset                                                                   | comma-separated browser origins permitted to call the API                                                                                      |
| `ROSTER_LOG_LEVEL`               | `info`                                                                  | log level: `debug`, `info`, `warn`, or `error`                                                                                                 |
| `ROSTER_LOG_FORMAT`              | `json`                                                                  | log format: `json` or `text`                                                                                                                   |
| `ROSTER_ADMIN_ADDR`              | `127.0.0.1:9090`                                                        | address for pprof and metrics; "off" disables the admin listener                                                                               |
| `ROSTER_DEV_SUBJECT`             | unset                                                                   | DEVELOPMENT ONLY: authenticate every request as this subject, skipping token checks                                                            |
| `ROSTER_TRUST_PROXY_HEADERS`     | unset                                                                   | trust identity headers from an upstream proxy (Google IAP or oauth2-proxy); only safe when clients cannot reach this service except through it |
| `ROSTER_REQUEST_TIMEOUT`         | `30s`                                                                   | per-request deadline applied to every RPC                                                                                                      |
| `ROSTER_TRACE_SNAPSHOT_DIR`      | unset                                                                   | enable the execution-trace flight recorder and write snapshots here                                                                            |
| `ROSTER_OTEL_EXPORTER`           | `none`                                                                  | trace exporter: `none`, `otlp`, or `stdout`                                                                                                    |
| `ROSTER_OTEL_ENDPOINT`           | unset                                                                   | OTLP collector address; empty defers to OTEL_EXPORTER_OTLP_ENDPOINT                                                                            |
| `ROSTER_OTEL_INSECURE`           | unset                                                                   | send OTLP without TLS, for a collector on the same host                                                                                        |
| `ROSTER_OTEL_SERVICE_NAME`       | `roster`                                                                | service name on every span and metric; empty defers to OTEL_SERVICE_NAME                                                                       |
| `ROSTER_OTEL_SERVICE_VERSION`    | unset                                                                   | service version on every span and metric; empty defers to OTEL_SERVICE_VERSION                                                                 |
| `ROSTER_OTEL_SAMPLE_PERCENT`     | `100`                                                                   | percentage of root spans to record; a sampled remote parent is always recorded                                                                 |
| `ROSTER_RATE_LIMIT_RPS`          | `200`                                                                   | requests per second admitted per instance; 0 disables admission control                                                                        |
| `ROSTER_DB_MAX_RETRIES`          | `3`                                                                     | replays of a transient database failure; reads only, never writes                                                                              |
| `ROSTER_DB_BREAKER_FAILURES`     | `5`                                                                     | consecutive infrastructure failures that open the database circuit breaker                                                                     |
| `ROSTER_DB_BREAKER_OPEN_DELAY`   | `5s`                                                                    | how long the database circuit breaker stays open before a trial request                                                                        |
| `ROSTER_PYROSCOPE_ENDPOINT`      | unset                                                                   | Pyroscope server for continuous profiling; empty disables profiling                                                                            |
| `ROSTER_PYROSCOPE_AUTH_USER`     | unset                                                                   | Pyroscope basic-auth user                                                                                                                      |
| `ROSTER_PYROSCOPE_AUTH_PASSWORD` | unset                                                                   | Pyroscope basic-auth password; prefer ROSTER_PYROSCOPE_AUTH_PASSWORD, as a flag value is visible in `ps`                                       |
| `ROSTER_DEPLOYMENT_ENVIRONMENT`  | unset                                                                   | environment label on profiles, e.g. `production`; empty means "development"                                                                    |

`unset` in the default column means the flag is off, empty, or disabled
until given a value.

### Authentication and authorization posture

Two ways to authenticate: an identity asserted by a trusted upstream proxy, or a
bearer token this service issued and stores hashed in `auth_api_token`.

Proxy headers are ignored unless `--trust-proxy-headers` is set. On a listener a
client can reach without passing through the proxy, a believed header would let
anyone claim any identity. Startup refuses `--dev-subject` on a non-loopback
address, and refuses it alongside `--trust-proxy-headers`.

Roles are never read from a header. They come from `auth_grant` and reach every
query through the `auth_effective_access` view, so `X-Forwarded-Groups` is
ignored.

______________________________________________________________________

## 📄 Pagination

Every `GetAll*` RPC is cursor-paged. Pass `page_size`, read `next_page_token`
from the response, and send it back as `page_token`. An empty token means the
last page. A page may come back shorter than requested, so only an empty
`next_page_token` ends iteration.

Paging is keyset, never `OFFSET`. A roster is rewritten wholesale by nightly
imports, which is exactly the traffic that makes offset pages skip and repeat
rows: a row inserted between two fetches shifts the window, so one record is
served twice and another never at all.

No endpoint reports a total count. Counting the rows a caller is scoped to would
mean a second pass over the same authorization join on every page, and the
number would be stale before the client read it.

______________________________________________________________________

## 🔑 Authorization

Authentication establishes *who* you are. Authorization decides *which rows* you
may see, and it lives in SQL rather than in an interceptor.

An interceptor can answer "may this caller call `GetAllClasses`?" but never
"*which* classes?", and that second question is the one that matters. Roster
defines no procedure-to-role matrix and no policy configuration. Instead:

- every read joins the `auth_effective_access` view, so a row outside the
  caller's scope is never loaded;
- every write is an `INSERT ... WHERE EXISTS` or carries the predicate in its
  `WHERE`, so a refused write changes nothing.

Grants live in `auth_grant`, which pairs a principal with a role and an org.
`org_closure`, a recursive view, expands a grant on a district down to every
school beneath it, so scoping a curator to a district needs one row rather than
one per school. `auth_effective_access` filters `NOT p.disabled` and honours
`expires_at`, which is what makes a revoked or expired grant take effect on the
next request rather than when a cached permission set expires.

A refused write returns `not_found` rather than `permission_denied`, so a write
cannot be used to probe for the existence of a record the caller may not see.
Academic sessions are the one exception: they carry no org reference in the
OneRoster schema, so there is nothing for a scoped grant to check, and their
curation requires a global `roster_admin` grant and answers
`permission_denied`.

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
- **Rate limiting** (`--rate-limit-rps`), smooth rather than bursty so permits
  are spaced evenly instead of handing the database a whole second's allowance at
  once. Per-instance, because a limit spanning every replica needs a shared
  counter that a single process cannot provide.
- **Per-RPC deadlines** (`--request-timeout`): a stricter client deadline is
  honoured, a longer one clamped. A zero or negative value disables the check
  rather than expiring every request, which is the safer reading of a
  misconfigured flag.
- **Explicit pool bounds** rather than pgx's CPU-derived default, because without a
  ceiling an outage just grows the wait queue.

______________________________________________________________________

## 🔐 Authentication

Two ways in, tried in that order, in `internal/oneroster`:

- **Proxy headers** from Google IAP (`X-Goog-Authenticated-User-Id`, falling back to
  `-Email`) or oauth2-proxy (`X-Forwarded-User`, falling back to
  `X-Forwarded-Email`). Read only when `--trust-proxy-headers` is set, and
  ignored rather than refused when it is not, so nobody can tell a proxied
  deployment from a direct one by testing. IAP outranks oauth2-proxy, because its
  assertion is the stronger claim.
- **Bearer tokens**: `Authorization: Bearer <token>`, hashed with SHA-256 and
  matched against `auth_api_token`. The database stores only the hash, so a dump
  of that table cannot be replayed against this service. One message answers an
  unknown, revoked, expired, or disabled token, because distinguishing them would
  tell a caller which of those a guessed token is.

A `TokenValidator` can be supplied to verify a credential rather than trust the
header that carried it, for a deployment that checks an IAP JWT assertion or an
OIDC token. None is built in, so this is an extension point rather than a
feature. Where one is configured it applies to IAP assertions and bearer tokens,
and never to oauth2-proxy, which forwards nothing signed to check.

Local development sets `--dev-subject`, which injects `X-Forwarded-User` rather
than bypassing the authenticator, so a checkout exercises the same resolution a
deployment does. `X-Roster-Dev-Subject` names a different caller per request,
which is how org scoping gets exercised without minting tokens. Startup refuses
`--dev-subject` unless `--addr` is bound to loopback.

The resolved subject reaches handlers through
[`oneroster.SubjectFromContext(ctx)`](internal/oneroster/context.go). It carries
the subject alone, not a permission set, because the permissions live in the
database.
