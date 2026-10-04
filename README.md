# SDLC

A Go CLI for a ticket-based engineering workflow using one shared local Docker
image. The workflow starts from the repository where work is requested.

The CLI supports installation and reinstallation, version reporting, building and
inspecting the shared runtime, Codex/Claude account login, shared instruction
settings, local project initialization, ordered ticket discovery and interactive
provider sessions, and single-ticket execution through checks, a draft PR, CI and
independent review, and a live dashboard of local runs. Dedicated 1Password
signing-key retrieval is implemented. Detached controller supervision and
stacked-ticket orchestration remain future work.

## Get started

For a joint connected trial, follow the [onboarding runbook](docs/onboarding.md).
It covers a disposable .NET repository, both providers, CI and review/repair.
Start with the [GitHub profile and signing test guide](docs/github-docker-test.md)
to provision separate accounts. Use the [1Password signing setup guide](docs/1password-signing-setup.md)
to create the dedicated vault/account/key and run `sdlc signing setup --profile personal`.

From this clone, with Go 1.24 or later and a local Docker engine running Linux
containers, select an existing directory on your PATH:

```sh
go run ./cmd/sdlc-install --bin-dir /PATH/TO/YOUR_BIN_DIRECTORY
sdlc --version
sdlc runtime build --source .
sdlc runtime status
sdlc auth login
sdlc auth login --provider claude
sdlc auth status --all
sdlc instructions show
sdlc interactive
```

`/PATH/TO/YOUR_BIN_DIRECTORY` means a directory your shell searches for commands.
For a Mac where Homebrew's `/opt/homebrew/bin` is on PATH, install with:

```sh
go run ./cmd/sdlc-install --bin-dir /opt/homebrew/bin
```

Repeat the install command to reinstall. After the first build,
`sdlc runtime build` uses the saved clone location and can run from another
directory. Docker image builds and container checks run locally; CI validates
source only.

`sdlc runtime status` checks the installed runtime dependencies for updates,
including the skills and agents catalogues. Use `sdlc runtime status --offline`
for local validation without network access. Older images need one rebuild with
the updated CLI to record their dependency inventory. See the
[runtime status guide](docs/cli.md#inspect-the-runtime) for coverage and failure
behaviour.

Login opens a browser flow through the terminal. Credentials stay in separate
local Docker storage and survive CLI reinstallation and image rebuilds. These
volumes are readable by Docker administrators; see the [authentication guide](docs/cli.md#provider-login).

Codex is the default provider for login, status and interactive sessions. Use
`--provider claude` to select Claude, or `sdlc auth status --all` to check both.

Use `sdlc interactive` or `sdlc interactive --provider claude` to open the native
provider CLI inside a disposable container. This first command starts in an empty
workspace. Use `sdlc run` for a captured project checkout.
Both default to full access inside Docker. Use `--approval on-request` for Codex
or `--permission-mode manual` for Claude to change their native approval modes;
see the [interactive command guide](docs/cli.md#interactive-provider-sessions).

From the project repository, prepare local settings with:

```sh
sdlc init
```

This discovers Git state, project files and ticket paths, protects private work
with a local Git exclude rule, and creates `.sdlc/project.json`. Review the
suggested checks before using them. It works without Docker or provider login;
execution starts with `sdlc run`. See the
[project initialization guide](docs/cli.md#initialize-a-project).

For a disposable .NET 10 `.slnx` project with an API, unit tests and Docker/Mongo
integration tests, use the [smoke-test example](examples/dotnet-smoke/README.md).
Its public ticket template adds a count endpoint. The build and tests have passed
in credential-free SDLC workers; connected provider execution remains to be tested.

List a work folder's tickets in numeric filename order:

```sh
sdlc work --reference YOUR_WORK_REFERENCE
```

This inspects `.sdlc/work/YOUR_WORK_REFERENCE/tickets/` without reading ticket
bodies or starting work. Content and dependency checks belong to execution.
See the [ticket discovery guide](docs/cli.md#inspect-a-ticket-stream).

Launch exactly one selected ticket:

```sh
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-add-api.md --dry-run
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-add-api.md \
  --input .sdlc/work/YOUR_WORK_REFERENCE/specification.md
```

The default implementer is Codex at medium effort; Claude reviews at high effort.
The implementation account must be logged in. If the opposite reviewer has no
login, delivery proceeds through the draft PR and CI, then pauses at
`awaiting_reviewer`. Authenticate that provider and resume the recorded run.
Select a GitHub account with `--github-profile personal` or `work`. Each has a
separate native login and matching signing profile on the shared image. The Docker
publisher signs every delivered commit with the dedicated approved key, uses the
captured project Git name/email, and publishes through native `gh`. It leaves the
PR draft and never merges. Read the [run guide](docs/cli.md#run-one-ticket)
and [provider rules](docs/provider-usage.md) before a connected run. No complete live
provider/publication trial has been validated for this implementation.

Watch registered runs across repositories from another terminal:

```sh
sdlc dashboard
sdlc dashboard --run RECORDED_RUN_ID --logs
sdlc dashboard --json
```

The dashboard shows heartbeat status, current stage and model, questions, check
results and PR links. It puts attention items first and reads private run state
without contacting providers. Closing it leaves controllers working. Headless
controllers remain attached to their original terminal; detached execution is
future work. See the [dashboard guide](docs/cli.md#watch-local-runs).

Concurrent controllers use separate workspaces. Codex and Claude can overlap;
operations using the same provider cache wait until its prior operation and cleanup
finish. Waiting runs remain visible in the dashboard. See the
[concurrency guide](docs/cli.md#concurrent-runs-and-account-caches).

## Security boundary and risks

SDLC uses Docker to limit what provider commands can reach. Interactive sessions
run as a non-root user with a read-only root filesystem, dropped Linux
capabilities, Docker's `no-new-privileges` restriction, and CPU, memory and
process limits.
They receive the selected provider's private cache and shared instructions.
The current session starts in an empty disposable workspace, with no host
repository, host home, SSH agent, work secrets or Docker socket mounted. Ticket
runs instead mount a captured checkout and the explicitly selected requirements.
Their configured checks run in separate workers without provider caches, host
signing material or publishing credentials. The controller publishes only a
verified committed tree.

This boundary is useful for trusted repositories too: their dependencies,
installation scripts, build tools and tests can be compromised. Keeping that
code away from unrelated host files reduces the damage it can cause. The host,
Docker engine and runtime image remain trusted parts of the system; an attacker
controlling them can inspect or alter sessions and credential storage. See
[Docker's security model](https://docs.docker.com/engine/security/).

The remaining risks are:

- **Credentials inside the session are exposed to its code.** Login,
  interactive and ticket provider sessions can read and update the selected
  provider's cache.
  Default full access removes native permission prompts, and network access
  currently has no destination allowlist. Malicious instructions or executable
  dependencies could steal credentials or transmit other accessible data.
  Manual approval is additional supervision; approving an installation command
  does not constrain its package scripts. Anthropic documents the same
  [credential exposure in dev containers](https://code.claude.com/docs/en/devcontainer).
- **Persistent storage remains sensitive.** SDLC supplies no credential
  encryption. Docker administrators and an attacker with sufficient host access
  can read provider volumes. Claude's native cache may also retain account
  metadata and logs. Reinstalling or rebuilding preserves these volumes;
  deleting local storage does not establish provider-side revocation. Follow
  the [login storage guidance](docs/cli.md#provider-login).
- **Container isolation can fail.** Runtime or kernel vulnerabilities and unsafe
  host configuration can weaken the boundary. Docker bridge networking can
  reach services allowed by the host network; filesystem isolation does not
  isolate those services. Keep the host and container engine maintained.
- **Pins and update checks do not establish safety.** A pinned release or skill
  can contain malicious code. `sdlc runtime status` reports version availability,
  not a vulnerability scan, malware check or endorsement of an update.
- **Unattended signing grants machine authority.** A publisher that signs without
  desktop approval must hold usable signing capability and GitHub access.
  Compromising that publisher, its credential resolver or the Docker host can
  misuse them. Use a dedicated signing key and scoped credentials, and keep both
  away from repository code and test dependencies. The publisher is implemented
  with a read-only native GitHub login volume and tmpfs signing key; neither is
  mounted in provider or check workers. Native GitHub OAuth access can cover
  several repositories: a frozen destination limits intended operations, not
  what a stolen token could access. The explicit Service Account bootstrap file
  is plaintext, private host storage; OS credential-store integration and
  detached controller supervision remain future work. See the
  [credential boundaries](docs/github-credentials.md).
- **Disposable state has operational costs.** Current workspace changes are
  discarded on interactive-session exit; ticket checkpoints persist privately.
  Provider volumes have no storage quota, so session code
  can exhaust Docker's disk space. Deleting these volumes loses saved login
  state; losing installation metadata or switching engines can leave old
  sensitive volumes behind. Offline auth status cannot prove current account
  or model access.

Ticket checks receive their own disposable source copy. With `--docker-tests`,
they also receive a separate privileged Docker-in-Docker daemon, never the host
socket. Privileged Docker is suitable only for trusted integration checks and
is not a boundary against hostile code. The authenticated provider still has a
shell: instructions to leave dependency installation and tests to the check
worker cannot prevent it from running repository code and exposing its cache.
Network destination restrictions and credential encryption remain unimplemented.
A dedicated execution VM or separate machine adds a boundary whose administrator
still remains trusted.

A carefully configured Claude dev container can provide comparable containment
and can already offer tighter network controls. SDLC uses the same underlying
container boundary. Its current value is a shared recorded runtime, consistent
Codex/Claude launch settings, provider cache separation, shared instructions and
dependency visibility. Disposable ticket work, independent validation and
controlled publication are implemented for one selected ticket. Full access
directly on the host exposes the files and services available to that host user.
Running inside SDLC narrows that exposure, while retaining the risks listed above.

## Documentation

- [CLI installation and commands](docs/cli.md)
- [1Password signing provisioning and setup](docs/1password-signing-setup.md)
- [GitHub profiles and unattended signing tests](docs/github-docker-test.md)
- [Agreed workflow and remaining work](docs/workflow.md)
- [Development and validation](docs/development.md)
- [Keeping public commits free of private material](docs/publication-safety.md)
- [Provider usage rules and authentication boundaries](docs/provider-usage.md)
- [Research and earlier prototypes](research/README.md)
