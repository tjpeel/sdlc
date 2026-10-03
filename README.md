# SDLC

A Go CLI for a ticket-based engineering workflow using one shared local Docker
image. The workflow starts from the repository where work is requested.

The CLI supports installation and reinstallation, version reporting, building and
inspecting the shared runtime, Codex/Claude account login, shared instruction
settings, local project initialization and interactive provider sessions. Secret
retrieval, ticket execution, progress logs and recovery are still to be implemented.

## Get started

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
workspace; project setup belongs to the remaining ticket-workflow work.
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
ticket execution remains future work. See the
[project initialization guide](docs/cli.md#initialize-a-project).

## Security boundary and risks

SDLC uses Docker to limit what provider commands can reach. Interactive sessions
run as a non-root user with a read-only root filesystem, dropped Linux
capabilities, Docker's `no-new-privileges` restriction, and CPU, memory and
process limits.
They receive the selected provider's private cache and shared instructions.
The current session starts in an empty disposable workspace, with no host
repository, host home, SSH agent, work secrets or Docker socket mounted. Project
execution and exporting changes are still future work.

This boundary is useful for trusted repositories too: their dependencies,
installation scripts, build tools and tests can be compromised. Keeping that
code away from unrelated host files reduces the damage it can cause. The host,
Docker engine and runtime image remain trusted parts of the system; an attacker
controlling them can inspect or alter sessions and credential storage. See
[Docker's security model](https://docs.docker.com/engine/security/).

The remaining risks are:

- **Credentials inside the session are exposed to its code.** Login and
  interactive sessions can read and update the selected provider's cache.
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
- **Disposable state has operational costs.** Current workspace changes are
  discarded on exit. Provider volumes have no storage quota, so session code
  can exhaust Docker's disk space. Deleting these volumes loses saved login
  state; losing installation metadata or switching engines can leave old
  sensitive volumes behind. Offline auth status cannot prove current account
  or model access.

The next hardening priorities, not yet implemented, are to run dependency
installation and tests in separate workers without provider caches or publishing
credentials, enforce network restrictions outside those workers, and give future
ticket jobs disposable source copies. Signing and publishing should remain
outside workers and operate only on an approved revision and destination.
Separating workers must be enforced by the execution design; a prompt asking the
agent to use a safer container is insufficient. Allowed network destinations
can still carry data, so network restrictions reduce rather than eliminate
disclosure risk. A dedicated execution VM or separate machine can add another
boundary, while its administrator remains trusted.

A carefully configured Claude dev container can provide comparable containment
and can already offer tighter network controls. SDLC uses the same underlying
container boundary. Its current value is a shared recorded runtime, consistent
Codex/Claude launch settings, provider cache separation, shared instructions and
dependency visibility. Disposable ticket work, independent validation and
controlled publication are planned workflow benefits. Full access directly on
the host exposes the files and services available to that host user; running
inside SDLC narrows that exposure, while retaining the risks listed above.

## Documentation

- [CLI installation and commands](docs/cli.md)
- [Agreed workflow and remaining work](docs/workflow.md)
- [Development and validation](docs/development.md)
- [Keeping public commits free of private material](docs/publication-safety.md)
- [Provider usage rules and authentication boundaries](docs/provider-usage.md)
- [Research and earlier prototypes](research/README.md)
