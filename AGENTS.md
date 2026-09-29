# Agent guide for roster

Read this before changing Go code here.

It records decisions, not structure. What the packages contain is discoverable
from `go doc`; what is not discoverable is why several of them look wrong until
explained. Each rule below has cost someone time, either in this repository or
in the specification it implements.

## Layout

```text
main.go                       entry point; owns os.Exit and nothing else
cmd/cmd.go                    dispatcher; the only place commands register
cmd/{root,serve,migrate,version}/
internal/oneroster/           the service: pure core + imperative shell
internal/postgres/            pgx pool and goose migrations
internal/db/                  sqlc output (generated, lint-excluded)
internal/{telemetry,profiling,resilience,logging}/
internal/onerosterjson/       go-jsonschema output (vendored, lint-excluded)
proto/oneroster/v1p2/v1/      one message per file
sql/schema/                   goose migrations, embedded in the binary
sql/queries/                  sqlc input
```

`internal/oneroster` is split along the functional-core/imperative-shell line.
`core.go`, `cursor.go`, `curation.go` and `tostorage.go` touch no database and
read no clock. `handler.go`, `curation_*.go` and `associations.go` are the shell.
Keep that line: a decision that migrates into the shell stops being testable
without a container.

## Rules that look wrong without the reason

| Rule                                                                                  | Why                                                                                                                                                                                                                                                                                                                                                                                          |
| ------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Authorization lives in SQL, not an interceptor                                        | An interceptor can answer "may this caller call `GetAllClasses`?" but never "*which* classes?". Every read joins `auth_effective_access`; every write is `INSERT ... WHERE EXISTS` or carries the predicate in its `WHERE`. An out-of-scope row is never loaded.                                                                                                                             |
| A refused write returns `not_found`, not `permission_denied`                          | Otherwise a write could be used to probe for the existence of a record the caller may not see. Academic sessions are the one exception, described below.                                                                                                                                                                                                                                     |
| Academic session curation returns `permission_denied` and needs global `roster_admin` | `AcademicSessionDType` has no org reference in the OneRoster schema, so there is nothing for a scoped grant to check; and sessions are readable by any principal with any grant, so their existence is not a secret worth protecting.                                                                                                                                                        |
| `status` and the GUIDRef `type` fields are `string`, not protobuf enums               | A protobuf enum serializes as `ORG_STATUS_ACTIVE`; OneRoster says `active`. Renaming the enum values collides with `buf lint`'s `ENUM_VALUE_PREFIX`. The vocabulary is enforced by `buf.validate` inbound and `checkedStatus` outbound.                                                                                                                                                      |
| `terms` and `roles` are validated in Go, not by a constraint                          | Both are `required` with `minItems: 1` in the JSON Schema, which a junction table cannot express. A user stored without roles is invisible to every query, because visibility is scoped through them.                                                                                                                                                                                        |
| Paging is keyset, never `OFFSET`                                                      | A roster is rewritten by bulk imports, which is exactly the traffic that makes offset pages skip and repeat rows.                                                                                                                                                                                                                                                                            |
| Updates require `expected_date_last_modified`                                         | Nightly imports and human curation write concurrently by design. Without the check the loser of a race is never told.                                                                                                                                                                                                                                                                        |
| Writes take `<Entity>Write`, not `<Entity>`                                           | `date_last_modified` is set by the server, so the write types omit it and reserve its number. Before the split a create demanded a timestamp the server then overwrote. A field added to an entity has to be added to its twin, and `TestWriteTypesMatchTheirEntities` fails when a pair drifts. The version being written against travels in the request, as `expected_date_last_modified`. |
| An absent `update_mask` is refused                                                    | Treating it as "replace everything" means a client bug that dropped the mask blanks every field it did not send.                                                                                                                                                                                                                                                                             |
| Reads retry, writes never do                                                          | A replayed insert duplicates. A replayed version-checked update re-applies against a version that has already moved. `resilience.Read` gets retry plus breaker. `resilience.Write` gets only the breaker.                                                                                                                                                                                    |
| Delete sets `status = 'tobedeleted'`                                                  | That is what OneRoster defines deletion to mean, and a hard delete would remove the classes and enrollments beneath an org.                                                                                                                                                                                                                                                                  |
| Every knob is a registered flag                                                       | `roster serve --help` is the whole configuration surface. Nothing calls `os.Getenv`. `ff` derives a `ROSTER_`-prefixed environment variable from each flag. Where an ecosystem standard exists (`OTEL_*`), an empty flag passes no option, so the OpenTelemetry SDK applies its own handling.                                                                                                |

## The specification is the authority

The OneRoster JSON Schemas are committed in `internal/onerosterjson/` and
`internal/onerosterjson/rosterjson/`. Read them before changing the data model.

The generated Go in those directories has already lost information the schemas
carry. `minItems` is the case that matters. Building the tables from the
generated types instead of the schemas let a bug through: a class could be
created with no terms, and a user with no roles. A user without roles is
unreachable by any query, because visibility is scoped through them.

## Two traps

**`golangci-lint --fix` can break the build.** It has twice, here: once retyping
a constant to `int32` so it no longer compared with `len()`, and once replacing
a SQL literal `''` inside a comment with a typographic quote. `just lint-fix`
runs the build, vet and the tests after fixing, for that reason. Read its diff.

**`golangci-lint run ./...` does not lint `//go:build integration` files.** It
uses the default build context. Both configurations must be run, which
`just lint` and the two CI steps do. A lint error in an integration test is
invisible to the single-configuration run.

**Handler tests bypass the interceptors.** The suites in `internal/oneroster`
call handler methods directly, so protovalidate never runs. A request those
tests accept can still be rejected by a live server. `test/` exists to catch
that: it goes through the generated client and the full interceptor chain. Add a
case there when a request's shape changes.

That gap has cost real time once. Every create RPC refused a request without
`date_last_modified`, and only academic sessions were driven through the client,
so only academic sessions showed it. `TestEveryCreateReachesTheServer` now drives
all six.

## Testing

Stdlib `testing` only. `depguard` bans testify, gomega, ginkgo and both mock
libraries in `*_test.go`. Each package defines its own small `ok`/`equals`
helpers rather than sharing an assertion package.

`forbidigo` also bans `time.Sleep`, `t.Setenv`, `os.Getwd` and `runtime.Caller`.
Poll on a ticker, pass a `getenv` parameter, and use paths relative to the
package directory.

| Command                 | Covers                                                          |
| ----------------------- | --------------------------------------------------------------- |
| `just test`             | unit tests; no Docker needed                                    |
| `just test-integration` | `-tags=integration`; needs Docker or `ROSTER_TEST_DATABASE_URL` |
| `just fuzz-all 20s`     | the pure core's nine property targets                           |
| `just mutate`           | mutation testing, gated on covered MSI                          |
| `just lint`             | both build configurations                                       |
| `just vale`             | prose in Markdown and the schema comments                       |
| `just check`            | the gate CI runs                                                |

Mutation testing is gated on **covered** MSI, not MSI, and the difference is
worth understanding before changing the threshold. `core.go` holds the `toProto*`
row builders alongside the pure helpers, and only the integration suite reaches
those. Counting their mutants as survivors pinned the raw score near 30%, so the
90% gate could never go green no matter how many unit tests were added. Covered
MSI marks unreached code as not-covered and asks the question worth gating: of
the code these tests do reach, how much would they notice breaking?

Eight surviving mutants are equivalent, meaning no test can kill them because
they produce no observable difference. Two return a different value on an error
path no caller reads. Three change a length check in `metadata` that only skips
work, since a short input fails `protojson.Unmarshal` and yields nil either way.
One drops a nil check that the following length check already covers. Two widen
an array whose extra element is never read. Recognise that shape before writing
a test to chase a survivor, because the test you would have to write asserts
something no caller can observe.

A mutation run also finds dead code, which is how the month and day bounds came
out of `protoDate`. `time.Time.Date()` cannot report a month outside 1..12 or a
day outside 1..31, so those checks were unreachable and no test could tell their
bounds apart. Removing them made `gosec` ask for the bound it had been reading
from the dead comparison, which is what the `//nolint:gosec` there records.

Fuzz targets assert properties: round-trip, idempotence, totality, boundary.
Absence of a panic is not enough. One target found a real date-normalisation bug
on its fourth seed. Keep that standard, because a crash-only target finds
crashers and nothing else.

Integration tests get their own database per test, which is what makes
`t.Parallel` safe. Do not share one.

## Changing the schema

1. Add a migration to `sql/schema/`. Never edit an applied one.
2. `just generate`, which runs `buf generate` and `sqlc generate`.
3. `go test -tags=integration ./internal/postgres/ -update` if the table set
   changed, then read the golden diff before committing it.
4. Check the change against the JSON Schemas, including `required` and
   `minItems`.
5. A new field on an entity also belongs on its `<Entity>Write` twin, unless it
   is server-owned, in which case reserve its number there.

## Commits

Scope first, as in `roster: imperative description`. No Conventional Commits
type prefixes.
