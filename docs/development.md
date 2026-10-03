# Development

Work directly on `main`. Complete a bounded iteration, run its checks, commit and
push before starting the next iteration. Use plain commit messages.

## Repository layout

| Path | Purpose |
| --- | --- |
| `cmd/` | Installed Go CLI and local installer |
| `internal/` | Build identity, installation, locking, shared image and provider authentication |
| `runtime/` | Shared local image context and supporting runtime components |
| `docs/` | Current CLI, workflow and development guides |
| `scripts/` | Public-source safeguards and dependency pin tooling |
| `tests/` | Offline safeguards/runtime checks and opt-in container probes |
| `research/` | Earlier notes, examples, Python runner and experiments |

The runtime still contains components exercised by the Python prototype. Their
presence does not make ticket execution or recovery available through the Go
CLI. Prototype usage is documented only in the research archive.

## Validate an iteration

Run the checks appropriate to the change. For CLI changes:

```sh
go test -count=1 ./...
go vet ./...
go build ./cmd/...
```

Use uncached tests because installer checks build and run an external executable.
For Python, runtime helpers or safeguards:

```sh
python3 -m unittest discover -s tests -v
python3 scripts/update_runtime_pins.py --check
```

The source-validation workflow runs these checks. Docker image builds and
container probes run locally only. Build through `sdlc runtime build` to record
and verify the shared image; CI must not build, run or publish it.

See [publication safety](publication-safety.md) for hook installation, staged
review and history checks before pushing. Real credentials, tickets and job
output stay out of this public repository.

## Current validation

The bootstrap iteration was checked on 3 October 2026:

- Installation and reinstallation on macOS/ARM64, including execution outside
  the clone and a clean Git revision in `sdlc --version`.
- Shared local Docker image build, tool startup checks, saved-source rebuild and
  image/engine identity checks.
- Go tests and vet, plus builds for macOS, Linux and Windows on ARM64 and x86-64.
- 145 offline Python tests and public-source sensitive-content checks.

Native Linux/Windows CLI execution and connected provider/ticket jobs remain to
be verified. Earlier container and experiment results are preserved in the
[historical validation note](../research/notes/historical-runtime-validation.md).

Codex authentication was checked with offline boundary tests and local
containers using disposable fake OAuth data. These cover fresh-container reads,
repeat volume initialization, fixed status output, credential-file permissions,
failed-login preservation and malformed-file recovery. The local probe runs
explicitly with `python3 scripts/probe_provider_auth.py --cli /PATH/TO/SDLC_BINARY`;
it does not run in CI. Real browser login and remote token validity remain manual
checks. The helper is embedded in the CLI and runs against the existing shared
image; this iteration requires reinstalling the CLI, without rebuilding the image.
