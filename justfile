# The functional core of internal/oneroster: no database, no clock. These are the
# files a mutation run can judge, because unit tests reach all of their
# behaviour. Kept in one place so the mutate recipes cannot disagree about what
# "the pure core" means.
pure_core := "./internal/oneroster/core.go ./internal/oneroster/cursor.go ./internal/oneroster/curation.go ./internal/oneroster/tostorage.go ./internal/oneroster/identity.go"

default:
    @just --list

# Generate local trusted TLS certificates using mkcert
certs:
    mkdir -p .certs
    mkcert -install
    mkcert -cert-file .certs/cert.pem -key-file .certs/key.pem localhost 127.0.0.1 ::1

# Install all toolchains via mise, generate TLS certs, install frontend deps and git hooks
setup:
    mise install
    just certs
    just hooks
    @if [ -f web/package.json ]; then cd web && pnpm install; \
     else echo "skipping web deps: no web/package.json (the curation UI is not built yet)"; fi

# Install the lefthook git hooks (pre-commit, pre-push, commit-msg)
hooks:
    lefthook install

# Regenerate Protobuf, Connect stubs, OpenAPI specs, TypeScript types, and descriptor image
generate:
    buf generate
    buf build -o gen/image.binpb
    sqlc generate
    @if [ -f web/package.json ]; then mkdir -p web/public && \
       cp gen/openapi/oneroster/v1p2/v1/service.openapi.yaml web/public/openapi.yaml; \
     else echo "skipping openapi copy: no web/package.json"; fi

# Build Go binary and Vite React web frontend
build:
    go build -v ./...
    @if [ -f web/package.json ]; then cd web && pnpm build; \
     else echo "skipping web build: no web/package.json (the curation UI is not built yet)"; fi

# Run the fast unit suite: race detector on, no Docker required
test:
    go test -race ./...

# Run the unit suite with a coverage report (a report for humans, not a gate)
test-cover:
    go test -race -coverprofile=coverage.out -covermode=atomic ./...
    go tool cover -func=coverage.out | tail -1
    @echo "HTML report: go tool cover -html=coverage.out"

# Lint the prose in Markdown and the SQL schema comments.
#
# `vale sync` first: the style packages are gitignored as disposable, so a fresh
# checkout has none and vale would otherwise report every rule as missing rather
# than saying why.
vale:
    vale sync
    vale README.md AGENTS.md sql/schema/ stubs/

# Run golangci-lint over both build configurations
lint:
    golangci-lint run ./...
    golangci-lint run --build-tags=integration ./...

# Auto-fix what the linters can fix, then prove the tree still works.
#
# --fix rewrites source, and some rewrites are wrong: a constant retyped to int32
# has broken this build, and misspell has replaced a SQL literal '' with a smart
# quote inside a comment. Building and testing afterwards is what turns those
# into an immediate failure instead of a committed regression. Both build
# configurations are fixed, because --fix skips tagged files in the default one.
lint-fix:
    golangci-lint run --fix ./... || true
    golangci-lint run --fix --build-tags=integration ./... || true
    @echo "── verifying --fix did not break the tree ──"
    go build ./...
    go vet ./...
    go vet -tags=integration ./...
    go test ./...
    @echo "── remaining issues --fix could not resolve ──"
    golangci-lint run ./...
    golangci-lint run --build-tags=integration ./...

# Mutation testing asks what coverage cannot: not "did a test execute this line?"
# but "would any test have noticed if it behaved differently?". Scoped to the
# pure half of internal/oneroster — the shell's behaviour is proven by the
# container-backed suites, which a mutation run does not execute.
#
# The gate is on COVERED MSI, not MSI, and that distinction is the whole point.
# core.go also holds the toProto* row builders, which only the integration suite
# reaches; counting their mutants as survivors put the raw MSI at 30% and made a
# 90% gate unreachable by any amount of unit testing. --coverage marks that code
# not-covered instead, so the score answers the question actually worth gating:
# of the code these tests do reach, how much would they notice breaking?
#
# Mutation-test the pure core; fails below the threshold (exit code 4)
mutate threshold="95":
    mutago --coverage --per-test --min-covered-msi={{threshold}} --quiet --no-diffs {{pure_core}}

# Mutation-test the core and show the diff for every surviving mutant.
mutate-report:
    mutago --coverage --per-test --html-output {{pure_core}}
    @echo "wrote mutago-report.html"

# This is what makes the technique affordable on a large codebase: seconds per
# pull request rather than minutes over the whole tree.
#
# Mutation-test only the lines changed against a base ref
mutate-diff base="main":
    mutago --git-diff-lines --git-diff-base={{base}} --quiet --no-diffs ./internal/...

# The score is dragged down by handler.go, whose behaviour lives in the
# integration suite rather than the unit tests a mutation run executes.
#
# Whole-package mutation run, informational only; read it per-file
mutate-all:
    mutago --coverage --per-test --quiet --no-diffs ./internal/oneroster/

# Surviving mutants with the enriched JSON report, for working through them one
# at a time. Read mutago-agentic.json, then target a single survivor with
# `mutago --run-mutant-id=<id> <file>`.
mutate-survivors:
    mutago --coverage --per-test --quiet --no-diffs --logger-agentic-json {{pure_core}}
    @echo "wrote mutago-agentic.json"

# Verify go.mod/go.sum are tidy (CI runs this; a dirty tree fails the build)
tidy-check:
    #!/usr/bin/env bash
    set -euo pipefail
    cp go.mod /tmp/go.mod.bak && cp go.sum /tmp/go.sum.bak
    go mod tidy
    if ! diff -q /tmp/go.mod.bak go.mod >/dev/null || ! diff -q /tmp/go.sum.bak go.sum >/dev/null; then
        echo "go.mod/go.sum are not tidy; run 'go mod tidy' and commit the result" >&2
        exit 1
    fi
    echo "go.mod and go.sum are tidy"

# Run govulncheck vulnerability scanner
vulncheck:
    govulncheck ./...

# Lint the protobuf schema
buf-lint:
    buf lint

# CI runs this on every PR; running it locally is what would have caught the
# oneroster.v1 break before it was committed.
#
# Fail if the protobuf schema breaks compatibility with the base branch
buf-breaking base="main":
    buf breaking --against ".git#branch={{base}}"

# Run every quality and security gate the CI pipeline runs
check: tidy-check buf-lint buf-breaking lint vulncheck test test-web

# Run the container-backed suites (needs a running Docker/Colima daemon)
test-integration:
    go test -race -tags=integration -count=1 ./...

# Fuzz one target in the pure core, e.g. `just fuzz FuzzParseDate 60s`
fuzz target="FuzzNewOneRosterInput" duration="30s":
    #!/usr/bin/env bash
    set -euo pipefail
    # `go test -fuzz` exits 0 when the pattern matches nothing, so a renamed or
    # deleted target would silently "pass". Check it exists first.
    if ! go test -list '^Fuzz' ./internal/oneroster/ | grep -qx '{{target}}'; then
        echo "no such fuzz target: {{target}}" >&2
        go test -list '^Fuzz' ./internal/oneroster/ | grep '^Fuzz' >&2
        exit 1
    fi
    go test -run='^$' -fuzz='^{{target}}$' -fuzztime={{duration}} ./internal/oneroster/

# Fuzz every target in the core for a short burst each (what CI runs)
fuzz-all duration="20s":
    #!/usr/bin/env bash
    set -euo pipefail
    # The list is derived from the source rather than hard-coded, so a target added
    # or removed in fuzz_test.go cannot silently drop out of the sweep.
    targets=$(go test -list '^Fuzz' ./internal/oneroster/ | grep '^Fuzz')
    if [ -z "$targets" ]; then
        echo "no fuzz targets found in ./internal/oneroster/" >&2
        exit 1
    fi
    for target in $targets; do
        echo "== $target"
        go test -run='^$' -fuzz="^${target}$" -fuzztime={{duration}} ./internal/oneroster/
    done

# Start PostgreSQL and Pyroscope via docker-compose
up:
    docker compose up -d postgres pyroscope

# Grafana on :3000 with Tempo, Prometheus and Pyroscope wired up. Run the
# service with OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 to feed it.
#
# Start the optional observability stack
observability:
    docker compose --profile observability up -d
    @echo "Grafana http://localhost:3000  Pyroscope http://localhost:4040  Tempo http://localhost:3200"

# Stop every container, including the optional stack
down:
    docker compose --profile observability down

# Run database migrations up
migrate-up:
    go run ./cmd/migrate up

# Roll back the most recent database migration
migrate-down:
    go run ./cmd/migrate down

# Check database migration status
migrate-status:
    go run ./cmd/migrate status

# Profiles are sliced by run and scenario only when the server has
# PYROSCOPE_ENDPOINT set; `just up` starts a Pyroscope for it.
#
# Drive load at a running server with k6
load-test duration="30s" vus="5":
    DURATION={{duration}} VUS={{vus}} k6 run k6/load.js

# Run the Go microservice
run:
    go run ./cmd/server

# Run FauxRPC mock server with HTTPS, protobuf descriptor image, OpenAPI specification, and normal stubs
fauxrpc:
    fauxrpc run --schema=gen/image.binpb,gen/openapi/oneroster/v1p2/v1/service.openapi.yaml --stubs=stubs/normal --addr=127.0.0.1:8080 --https --cert=.certs/cert.pem --cert-key=.certs/key.pem --log-level=debug

# Run FauxRPC mock server configured with failure stubs to test error handling
fauxrpc-fail:
    fauxrpc run --schema=gen/image.binpb,gen/openapi/oneroster/v1p2/v1/service.openapi.yaml --stubs=stubs/failures --addr=127.0.0.1:8080 --https --cert=.certs/cert.pem --cert-key=.certs/key.pem

# Run Vite React frontend dev server against backend (Go server or FauxRPC on :8080)
web-dev:
    @if [ -f web/package.json ]; then cd web && pnpm dev; \
     else echo "no web/package.json: the curation UI is not built yet" && exit 1; fi

# Run frontend tests against FauxRPC mock server
test-web:
    @if [ -f web/package.json ]; then cd web && pnpm test; \
     else echo "skipping web tests: no web/package.json (the curation UI is not built yet)"; fi
