# FauxRPC stubs

Canned responses so a client can be built against the schema before the service
is deployed, and so error handling can be exercised on demand.

```sh
just fauxrpc        # serve stubs/normal
just fauxrpc-fail   # serve stubs/failures
```

`normal/` returns plausible rosters. `failures/` returns the status codes the
real service returns, which is the point: a UI that handles these handles
production. `Aborted` on `UpdateOrg` is the one worth wiring up deliberately.
It means a curator's edit lost a race, so the form needs reloading rather than a
generic error banner.

These are **not** a test double for the Go code. Correctness is asserted by the
integration suite (`just test-integration`), which runs against a real
PostgreSQL. A stub asserts nothing. It only answers.
