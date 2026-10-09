# SDLC

A Go CLI for a ticket-based engineering workflow using one shared local Docker
image. The workflow starts from the repository where work is requested.

The CLI supports installation and reinstallation, version reporting, building and
inspecting the shared runtime, Codex/Claude account login, shared instruction
settings, local project initialization, ordered ticket discovery and interactive
provider sessions, and single-ticket or feature execution through checks, draft
PRs, CI and independent review, with a live dashboard of local runs. Dedicated
1Password signing-key retrieval is implemented. Feature runs schedule dependencies
and independent tickets and reconcile owned PRs after human merges. Detached
controller supervision remains future work.

The first interactive frontend is available with `sdlc shell`. It keeps the
existing CLI commands, adds slash-command help and local ticket completion, and
uses the terminal's font and colours with a Robby Russell prompt. Read the
[shell guide](docs/interactive-shell.md) for project scope, onboarding, plan
review and independent terminal launches. The current beta baseline is
`0.1.0-beta.9`; changes are recorded in the [changelog](CHANGELOG.md).

Ticket and feature runs retain [usage metrics](docs/usage-metrics.md) across
attempts and resumes. Read them with `sdlc usage --since 7d --json`.
An [opt-in Headroom variant](docs/headroom.md) adds a local proxy for passthrough
comparison or conservative tool-output compression. Direct execution is the default.

## Get started

Follow the [user guide](docs/user-guide.md) for the full route: install, build,
sign into Codex/Claude/GitHub, provision a dedicated 1Password signing authority,
pair accounts and keys, configure a project, and start and monitor a ticket.
Account setup is done once; each project needs reviewed checks and an account
selection. The guide includes replacement, resume and history retention steps.

For a supervised trial on a disposable .NET repository, use the
[test runbook](docs/onboarding.md). The [proposed host-harness skill](docs/skills/sdlc-drive/SKILL.md)
defines how a harness can drive the current CLI while keeping its context small.
It is not installed automatically. A [single guided setup command](docs/proposals/unified-onboarding.md)
is proposed follow-up work; current onboarding uses the documented commands.

From this clone, with Go 1.25 or later and a local Docker engine running Linux
containers, select an existing directory on your PATH:

```sh
go run ./cmd/sdlc-install --bin-dir /PATH/TO/YOUR_BIN_DIRECTORY
sdlc --version
sdlc runtime status
sdlc auth login
sdlc auth login --provider claude
sdlc auth status --all
sdlc instructions show
sdlc interactive
```

`/PATH/TO/YOUR_BIN_DIRECTORY` means a directory your shell searches for commands.
On macOS or Linux with Homebrew, use the local tap from a clean, committed clone:

```sh
python3 scripts/install_homebrew.py
```

This installs `local/sdlc/sdlc` through Homebrew and prepares its runtime in one
command. Homebrew builds the CLI with its Go dependency and keeps the public
runtime sources in the Cellar. Python 3 and Git are required for the bootstrap.
Use `--cli-only` to migrate an existing receipt-verified native installation while
keeping its runtime. The previous executable is backed up in private state.
See [Homebrew installation](docs/cli.md#local-homebrew-installation) for updates
and recovery.

The installer builds the CLI and runtime in one command. It prepares the runtime
with the new CLI before replacing the installed executable. Use `--cli-only` to
install the host command while keeping the current runtime.

After installation, update from any directory or use `/update` in the shell:

```sh
sdlc update --dry-run
sdlc update
sdlc update --pull
```

The first command previews the local steps without building, fetching or writing.
`update` installs the current checkout; `--pull` first fast-forwards a clean
checkout from its configured upstream. Homebrew updates refresh the committed
snapshot, formula and checksum before upgrading; commit source changes first.
Native source installation also accepts dirty source without `--pull`.
The source and installation method are remembered in private installation
state. Default updates use the checkout's runtime pins, replacing any previous
local overrides. Use `sdlc update --dependencies` to select newer public
dependencies instead, or `--cli-only` to preserve the runtime. Reopen existing
shells after installation. Docker builds remain local; CI validates source.

Resumable saved runs and incomplete features in known projects block runtime
replacement and identify the work to finish. `sdlc update --cli-only` remains
available while that work is paused. Keep external projects registered so updates
can find their saved work.

`sdlc runtime status` checks the installed runtime dependencies for updates,
including the skills and agents catalogues. Use `sdlc runtime status --offline`
for local validation without network access. Older images need one rebuild with
the updated CLI to record their dependency inventory. See the
[runtime status guide](docs/cli.md#inspect-the-runtime) for coverage and failure
behaviour.
The default report lists major tools and image/catalogue pins, with summaries for
supporting packages. Use `sdlc runtime status --all` for individual bundled npm
dependencies and Debian updates.

After checking status, preview and apply dependency updates with:

```sh
sdlc runtime update --dry-run
sdlc runtime update
```

Runtime update resolves exact public releases, refreshes the installed packages and
rebuilds `sdlc:local`. It selects the candidate after tool, inventory and package
checks pass. Selected pins stay in private installation state; source pins and
login storage are preserved. Ordinary `runtime build` retains these local pins;
`runtime build --source-pins` explicitly returns to the checkout's pins. See [runtime updates](docs/cli.md#update-the-runtime)
for release tracks and dependencies controlled by their parent packages.

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
sdlc github use --profile personal
sdlc github status
```

This discovers Git state, project files and ticket paths, protects private work
with a local Git exclude rule, and creates `.sdlc/project.json`. Review the
suggested checks before using them. `init` takes no arguments and works without Docker or provider login;
`github use` saves the checked pair for this checkout in private external state.
Execution starts with `sdlc run`. See the
[project initialization guide](docs/cli.md#initialize-a-project).

For a disposable .NET 10 `.slnx` project with an API, unit tests and Docker/Mongo
integration tests, use the [smoke-test example](examples/dotnet-smoke/README.md).
Its public ticket template adds a count endpoint. The build and tests have passed
in credential-free SDLC workers; supervised private trials using this style of
project have also passed connected delivery and review/repair.

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
A fresh run uses the saved repository pair, or the unique pair whose login owns
the repository. For an organisation or ambiguous account selection, run
`github use --profile personal` first. `--github-profile` can select a registered
pair for an unbound checkout; it cannot override a conflicting saved selection.
Each account has a separate native login and explicitly paired signing profile
on the shared image. The Docker
publisher signs every delivered commit with the dedicated approved key, uses the
captured project Git name/email, and publishes through native `gh`. It leaves the
PR draft and never merges. Read the [run guide](docs/cli.md#run-one-ticket)
and [provider rules](docs/provider-usage.md) before a connected run. Supervised
private trials have passed delivery/review/repair with both implementation
providers. Later account/key pairing, history/dashboard changes and desktop
notification delivery still need separate validation.

Run a whole feature from a clean checkout of the published integration branch:

```sh
sdlc run --reference YOUR_WORK_REFERENCE --all --parallel 2 --dry-run
sdlc run --reference YOUR_WORK_REFERENCE --all --parallel 2 --watch
```

An optional private `plan.json` declares ticket priorities, dependencies and path
prefixes that must run serially. Independent tickets can overlap; a ticket with
one dependency can start from its parent's signed, tested and reviewed draft PR.
Tickets with multiple dependencies wait until all parents merge into the
integration branch. `--watch` keeps the foreground controller monitoring human
merges; without it, SDLC runs currently unblocked work and exits. Repeat the
command to continue the saved feature. `--alternate-providers` alternates
implementers in planned ticket order, with the opposite provider reviewing each.
The dashboard still reports individual ticket runs. See the
[feature run guide](docs/cli.md#run-a-feature) for plan format and restacking.
Feature orchestration has offline test coverage; a connected feature trial still
needs validation in an authorised disposable repository.

Watch registered runs across repositories from another terminal:

```sh
sdlc dashboard
sdlc dashboard --run RECORDED_RUN_ID --logs
sdlc progress --run RECORDED_RUN_ID
sdlc dashboard --json
sdlc dashboard --page 2
sdlc dashboard --notify desktop --sound
```

`sdlc progress --run ID` tails labelled SDLC steps, checks, and provider agent
output. The shell follows this CLI feed after starting or resuming a job;
`/progress --run ID` attaches to existing work. Page Up pauses scrolling and End
returns to the newest output. Closing the view leaves the controller running.

The dashboard shows heartbeat status, current stage and model, questions, check
results and PR links. It puts attention items first and reads private run state
without contacting providers. Ten runs appear per page; completed work follows
questions, problems and active work. `dashboard forget --run RUN_ID` hides a
stopped run while preserving all saved work. Private report export and retention
are described in [run history](docs/run-history.md). Optional local macOS alerts
use `--notify desktop --sound`; add this to `sdlc run` for alerts without an open
dashboard. Closing the dashboard leaves controllers working. Headless
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
controlled publication are implemented for selected tickets and feature runs.
Full access directly on the host exposes the files and services available to that host user.
Running inside SDLC narrows that exposure, while retaining the risks listed above.

## Documentation

- [User guide: full onboarding, ticket execution and recovery](docs/user-guide.md)
- [CLI installation and commands](docs/cli.md)
- [1Password signing provisioning and setup](docs/1password-signing-setup.md)
- [GitHub profiles and unattended signing tests](docs/github-docker-test.md)
- [Agreed workflow and remaining work](docs/workflow.md)
- [Development and validation](docs/development.md)
- [Keeping public commits free of private material](docs/publication-safety.md)
- [Provider usage rules and authentication boundaries](docs/provider-usage.md)
- [Run history, private reports and dashboard removal](docs/run-history.md)
- [Connected validation runbook](docs/onboarding.md)
- [Proposed host-harness SDLC skill](docs/skills/sdlc-drive/SKILL.md)
- [Proposed unified onboarding command](docs/proposals/unified-onboarding.md)
- [Research and earlier prototypes](research/README.md)
