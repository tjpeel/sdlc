# Development

Work directly on `main`. Complete a bounded iteration, run its checks, commit and
push before starting the next iteration. Use plain commit messages.

If remote access is unavailable, keep validated iterations as local commits and
push when access returns. If the account owner explicitly authorises unsigned
commits while signing is unavailable, record those commit IDs for re-signing
before publication. Preserve the repository's signing configuration.

## Repository layout

| Path | Purpose |
| --- | --- |
| `cmd/` | Installed Go CLI and local installer |
| `internal/` | Build identity, installation, locking, shared image, provider authentication, instruction settings and interactive sessions |
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

Provider authentication was checked with offline boundary tests and local
containers using disposable fake OAuth data. These cover fresh-container reads,
repeat volume initialization, fixed status output, credential-file permissions,
Codex failed-login preservation and malformed-file recovery, and Claude's native
cache handling. The Claude probe also checks that cached settings cannot change
the authentication route or run a credential helper or hook. All 159 Python tests,
Go tests and vet, and six CLI target builds pass for the provider-login iteration.
The local probe runs
explicitly with `python3 scripts/probe_provider_auth.py --cli /PATH/TO/SDLC_BINARY`;
it does not run in CI. Real browser login and remote token validity remain manual
checks. The helper is embedded in the CLI and runs against the existing shared
image; this iteration requires reinstalling the CLI, without rebuilding the image.

Shared instruction settings were checked with Go tests for snapshots, reset,
invalid input, file safety and concurrent updates. CLI show/set/reset also passed
outside the clone with disposable settings on macOS/ARM64. Go vet and all six
CLI target builds pass. This iteration changes no Docker or provider execution.

Interactive sessions are checked with offline tests for selected-cache mounts,
instruction snapshots, startup failures, cancellation, native cache updates and
logout. All 179 Python tests, Go tests, vet and six CLI and installer target builds
pass. The local native-client probe uses disposable fake credentials and disables
networking on every test container; it verifies Claude's shared instructions and image skills,
cached customization isolation and Codex's native file-cache behavior. Attached
CLI cancellation also restores the terminal and removes containers and snapshots,
including under umask `077` with a state path containing spaces and a comma.
Native permission probes cover Codex's `never` and `on-request` approvals and
Claude's full-access default, manual and plan modes. Claude's documented
skip-warning setting avoids repeated acknowledgement in disposable sessions;
managed policy still disables bypass permissions and the pinned client visibly
selects its permitted `auto` mode. SDLC does not change modes or retry. These
checks use fake account data without submitting any model prompts.
Run the probe locally with:

```sh
python3 scripts/probe_provider_interactive.py --cli /PATH/TO/BUILT_SDLC
```

It is separate from CI. Connected model requests and real interactive account
sessions remain manual checks.
