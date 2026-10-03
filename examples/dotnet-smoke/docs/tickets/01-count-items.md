# Count stored items

- Status: ready-for-agent
- Work reference: dotnet-smoke
- Source specification: [../specification.md](../specification.md)
- Blockers: none

## Scope

Implement `GET /items/count` as specified. Limit changes to the API and its tests.
The sample already supports create/read, name validation and Mongo readiness.
Keep the Docker orchestration and dependency versions unchanged.

## Acceptance criteria

1. An empty Mongo collection returns HTTP 200 with `{"count":0}`.
2. After two successful creates, the route returns `{"count":2}`.
3. A rejected empty-name create leaves the count unchanged.
4. Existing create/read and validation tests still pass.

## Verification

From the sample repository root:

```sh
dotnet build Smoke.slnx
dotnet test tests/Smoke.UnitTests/Smoke.UnitTests.csproj
python3 scripts/integration.py
```

Run the last command only in a credential-free SDLC check worker with
`--docker-tests`, which supplies the dedicated daemon socket. Use isolated
database state for each integration case; do not depend on test order.

## Deferred work

Authentication, pagination, schema migrations and deployed services are outside
this fixture. Stop if an implementation needs a product decision beyond the
linked specification.
