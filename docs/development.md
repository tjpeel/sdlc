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
| `internal/` | Build identity, installation, locking, shared image, provider authentication, instructions, project setup, ticket discovery, interactive sessions and single-ticket execution |
| `runtime/` | Shared local image context and supporting runtime components |
| `docs/` | Current CLI, workflow and development guides |
| `scripts/` | Public-source safeguards and dependency pin tooling |
| `tests/` | Offline safeguards/runtime checks and opt-in container probes |
| `research/` | Earlier notes, examples, Python runner and experiments |

The runtime still contains components exercised by the Python prototype. Their
presence does not define the Go CLI workflow. The current `sdlc run` implementation
has its own Go controller and native client adapter. Prototype usage is documented
only in the research archive.

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

The metrics and Headroom iteration was checked on 6 October 2026 with uncached Go
tests, vet, builds, targeted race checks, all 217 offline Python tests and the
runtime pin check. CLI builds passed for macOS, Linux and Windows on ARM64 and
x86-64. Read-only usage fixtures check unchanged private state, missing coverage
and omission of session identifiers; runner fixtures check durable start/final
records across resume, failure and cancellation. An independent integration
review found no lifecycle or resume defects.

The separate Headroom 0.39.1 image passed network-disabled fake-upstream probes for
Responses HTTP/SSE and WebSockets, Anthropic HTTP/SSE, structured schema, native
OAuth capability-header forwarding and 429 responses. Passthrough preserved tool
logs; conservative optimization compressed timestamped logs and its decoder
reconstructed the exact original. A separate pinned Codex 0.160.0 probe verified
its native WebSocket inference URL and disposable saved-account headers. These
probes use no host mounts or real credentials. They do not establish connected
subscription access, completed engineering work or savings on real tickets.
See the [Headroom guide](headroom.md) for explicit local probe commands.

The interactive shell first pass was checked on 5 October 2026 with uncached
Go tests, vet, CLI builds, race checks for the shell and launch-receipt packages,
all 217 offline Python tests and the runtime pin check. CLI cross-builds cover
macOS, Linux and Windows on ARM64 and x86-64; native Linux/Windows execution
remains unverified.

A disposable macOS/ARM64 pseudo-terminal check passed full-window alternate
screen entry, raw keyboard input, slash lookahead, resize with a retained draft,
running beta identity, native project-init handoff, terminal-mode restoration
on clean exit and no Docker/provider/GitHub command execution. Four screenshots
in ignored `results/interactive-cli-runtime/` show the actual Go renderer at
100 × 28 and 70 × 20 cells with fictional offline callbacks. Their browser font
and ANSI palette illustrate rendering; the native shell inherits the terminal.

The existing Automation grant passed a read-only check using the same
`osascript` sender as iTerm's runner. The installed iTerm bundle failed macOS
code-signature verification, so no native tab or job was launched. Focus,
flicker and independent-controller lifetime remain native acceptance checks;
the first pass provides a manual command when the adapter is unavailable.
No connected provider job, account login, Docker rebuild or CLI installation
was performed for this iteration.

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

CLI selection tests cover Codex as the default for interactive sessions, login and
status, explicit provider overrides and status-only `--all`. Invalid combinations
are rejected before runtime access. The offline authentication probe also checks
that a missing Claude login does not prevent the default Codex status check.

Run the interactive probe locally with:

```sh
python3 scripts/probe_provider_interactive.py --cli /PATH/TO/BUILT_SDLC
```

It is separate from CI. Connected model requests and real interactive account
sessions remain manual checks.

Project initialization is checked with disposable Git repositories for nested
invocation, new and detached checkouts, linked-worktree exclude resolution,
dirty source, sanitized remotes, custom settings preservation and invalid input.
Tests reject tracked private work, symlinked state, input paths and tickets,
conflicting ignore rules, case variants of tracked private paths, and Git
filesystem-monitor and content-filter execution. A concurrent exclude edit is
preserved rather than overwritten. Go tests,
vet, all 179 offline Python tests, sensitive-content checks and CLI/installer
builds for all six targets pass. A temporary macOS/ARM64 installation also passed
initialization and repeat initialization from a nested project directory with
spaces in its path. Native Linux and Windows execution remains unverified.

Runtime dependency status has offline tests for immutable inventories, native
tool versions, npm optional packages and aliases, catalogue ancestry, release
channels, paginated Docker tags, registry digests and Debian version ordering.
They also cover upstream timeouts, partial failures, older images and `--offline`
without network requests. Go tests, race checks for the changed packages, vet,
CLI and installer builds for all six targets, and all 186 Python tests pass.
These tests use disposable fake metadata and no provider accounts. A local
macOS/ARM64 rebuild captured 160 component entries and 423 Debian packages.
Both offline status and a complete online metadata check passed; the online
check found available updates without installing them or accessing provider
storage.

Runtime updates have offline tests for complete public pin resolution, catalogue
ancestry, legacy recipe provenance, stale-plan rejection, candidate version and
.NET runtime matching, package checks, rollback and active controller leases.
Default status summarizes bundled npm packages and Debian updates; `--all`
retains individual results and incomplete metadata remains visible. Go tests,
race checks, vet, six CLI target builds and all 202 Python tests pass.

A local macOS/ARM64 probe passed a fresh dependency rebuild, exact tool and image
pin checks, and all 423 Debian package candidates. The rebuilt image passed the
public .NET fixture's build, four unit tests, and API/Mongo integration checks
through both service networking and localhost. Synthetic `.env` inputs reached
only the check worker. The probe used private temporary state and a separate
runtime tag, cleaned its test resources and preserved the installed runtime.
No provider login, model request, vault access or GitHub write ran. Run it with:

```sh
SDLC_RUNTIME_UPDATE_DOCKER_TESTS=1 SDLC_RUNTIME_UPDATE_DOTNET_TESTS=1 \
  go test -count=1 -v -timeout 75m ./cmd/sdlc -run '^TestRuntimeUpdateDocker$'
```

This probe uses public registries and a dedicated privileged Docker test daemon;
it is opt-in and does not run in CI.

Ticket discovery is checked with disposable Git repositories for numeric order,
opaque references, missing folders, malformed and duplicate numbers, unsafe
links, ignored/untracked private inputs and unchanged checkout metadata. Ticket
bodies are deliberately incomplete or invalid in fixtures to verify they are
not parsed. Go tests, vet, CLI/installer builds and all 186 offline Python tests
pass. A built macOS/ARM64 CLI also passed discovery from a nested directory in a
disposable `.slnx` repository, duplicate failure and help dispatch. No Docker or
provider session is launched by this command.

Single-ticket execution is checked with offline fake-provider tests for streaming,
schema handoffs, opposite-provider selection, human questions, exact session
resume, bounded repairs, publication reconciliation, signing and current CI gates.
Tests cover isolated source capture, check input separation and checkpoint recovery.
Go tests and vet, plus all 193 Python tests, pass. A local macOS/ARM64 Docker probe
verified installed native CLI flags with networking disabled, source inspection
and bundle export, credential-free unit checks, and a nested service with a source
bind through the separate integration daemon. No provider account, model request
or GitHub write ran in these probes. Run the Docker probe explicitly with:

```sh
SDLC_OFFLINE_DOCKER_TESTS=1 go test -count=1 -v ./internal/workrun \
  -run TestOfflineDockerExecutionBoundaries
```

The probe uses the recorded local runtime and a disposable privileged Docker
daemon; it does not run in CI. It also builds successive image layers after
creating a Unix socket, verifies the `overlay2` driver and runs the built image.
VFS fails this class of build; see [BuildKit issue 3965](https://github.com/moby/buildkit/issues/3965).
Docker 29's containerd image store is explicitly disabled for this classic
storage driver; see [Docker daemon feature flags](https://docs.docker.com/reference/cli/dockerd/#enable-feature-in-the-daemon---feature).
Connected implementation, PR publication and cross-provider review remain to be
validated together.

Run reporting is checked with offline fixtures for private atomic registry writes,
controller ownership, independent heartbeats, stopped and stale sessions, corrupt
or missing run state, and retained stop/CI evidence. Reporting starts and finishes
inside the selected run's exclusive controller lock. No provider calls are needed
for reporting tests.

Dashboard tests cover attention ordering, selection, questions and evidence,
sanitized terminal output, bounded log tails, readonly snapshots and cancellation.
Usage fixtures cover chunked native events, optional counters, repeated events,
subagent exclusion, model matching and oversized/malformed input. These remain
offline; no reported metric establishes real model access or connected-run success.

Scoped-concurrency tests cover different-provider overlap, same-cache waits through
cleanup, cancellation, exclusive-build/shared-session exclusion, simultaneous
installation identity creation, runtime changes while queued, process-crash lock
release and surviving Docker cache users. They use fake Docker clients and no
provider account calls. Queue reporting retains heartbeat and optional usage data.

The [disposable .NET example](../examples/dotnet-smoke/README.md) matches the target
repository shape: a .NET 10 `.slnx`, an API, unit tests and Docker integration
tests with Mongo. On 4 October 2026 its credential-free check worker passed the
solution build with zero warnings or errors, four unit tests and the API/Mongo
integration scenario in both bridge and localhost modes. It captures a generated
ignored host-root `.env` as a check-only input, verifies the provider workspace
does not contain it, changes the host file after capture, and verifies the saved
input reaches nested Compose through interpolation and `env_file`. API and test
images exclude the file. The dedicated daemon used `overlay2`; all check containers,
networks and volumes were removed afterwards. No provider account or GitHub write
was used. Run the opt-in probe from the SDLC checkout:

```sh
SDLC_OFFLINE_DOTNET_TESTS=1 go test -timeout 25m -count=1 -v ./internal/workrun \
  -run '^TestOfflineDotnetSmokeExample$'
```

The probe restores public NuGet packages and pulls public container images. Its
explicit public file list excludes local ignored credentials and work material.
Nine offline Python tests cover the sample runner's failure, cancellation,
cleanup, resource naming and fixed Docker endpoint, including a conflicting
`DOCKER_CONTEXT`. The complete offline suite now has 202 Python tests. Connected
implementation, signed publication, CI and opposite-provider review still need a
test with the account owner.

A separate credential-free probe passed on 4 October 2026 with two check jobs
running together. Each used its own Docker daemon, published an HTTP service on
`18080:8080`, and received its own distinct response through `localhost:18080`.
Both services must be ready before either worker can finish, so startup timing
cannot hide a port collision. Both jobs completed cleanup. These ports belong to
the nested daemons; the main engine host receives no published ports. Run it with
the recorded local SDLC runtime and Docker engine:

```sh
SDLC_OFFLINE_DOCKER_TESTS=1 go test -timeout 8m -count=1 -v ./internal/workrun \
  -run '^TestOfflineDockerConcurrentPublishedPorts$'
```

This probe imports a small disposable service from the runtime's existing Python
files and requires no nested image pull, provider account or model request.
