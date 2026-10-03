# CLI installation and commands

The Go CLI provides local installation/reinstallation, build identity, shared
runtime image build/status checks, Codex/Claude account login, shared instruction
settings and interactive provider sessions. Ticket execution and secret-store
access remain to be implemented.

## Install or reinstall

Clone this repository and run the installer from its root. Local builds require
Go 1.24 or later. Choose an existing directory on your PATH:

```sh
go run ./cmd/sdlc-install --bin-dir /PATH/TO/YOUR_BIN_DIRECTORY
sdlc --version
```

Repeat the same command after updating the clone. The installer builds a native
executable before replacing the previous installation. A failed build leaves the
previous executable intact. It preserves runtime state and unrelated files,
rejects an unmanaged executable or symlink at the destination, and checks for an
earlier `sdlc` on PATH. On Windows, close any running `sdlc` before reinstalling.

The installed executable needs no Go runtime. Its version includes `0.1.0-dev`,
the Git revision, a dirty-source marker when applicable, and the host OS and
architecture. Release archives and package-manager installation are future work.

## Shared agent instructions

Inspect or change the installation's shared instructions from any directory:

```sh
sdlc instructions show
sdlc instructions set --file /PATH/TO/PRIVATE_INSTRUCTIONS.md
sdlc instructions reset
```

The default contains only this rule:

> If you have any question for a human, stop implementation immediately and report it.
> Do not assume an answer or continue implementation until a human has answered.

`set` copies additional Markdown instructions into private installation state.
They follow the human-answer rule, which remains present. Editing the source file
afterwards has no effect until you run `set` again. An empty file adds nothing;
`reset` removes the additions. `show` prints the complete shared body, so keep its
output private. Custom instructions must be UTF-8 text, at most 16 KiB, without
terminal control characters.

Settings live in `instructions.md` beside the runtime record. Reinstalling the CLI
preserves them. Changing them requires no Docker build and affects every project
using this installation. Personal instructions stay outside the shared image and
this public source tree.

No shared instruction file is injected by the current login or status commands.
The image contains catalogue-source `AGENTS.md` files under `/opt/sdlc/catalogues`;
these are outside the worker's instruction-discovery path. The SDLC repository's
root `AGENTS.md` is excluded from the Docker build context.

Interactive sessions receive a read-only snapshot of this shared body at the
providers' native global instruction paths: `$CODEX_HOME/AGENTS.md` for Codex and
`$CLAUDE_CONFIG_DIR/CLAUDE.md` for Claude (normally `~/.claude/CLAUDE.md`).
See [Codex instruction discovery](https://learn.chatgpt.com/docs/agent-configuration/agents-md)
and [Claude memory files](https://code.claude.com/docs/en/memory).
Changes to the settings affect the next session. Ticket workers will need the
same injection alongside project instruction files. An enforced human-input pause
remains part of ticket-execution work; Markdown instructions alone cannot
guarantee a process stops.

## Build the shared runtime

The host needs the Docker CLI and a local Docker engine running Linux containers.
Remote Docker engines and Windows containers are outside the current build path.

From the SDLC clone:

```sh
sdlc runtime build --source .
sdlc runtime status
```

The build uses only `runtime/` as its Docker context and the dependency and
catalogue pins in its Dockerfile. It supplies no account profile, signing key,
GitHub token or provider login. Skills and agents come from the pinned public
GitHub repositories during the image build.

The command checks Codex, Claude Code, GitHub CLI, .NET and Docker/Compose before
selecting the shared `sdlc-codex-spike:local` image. Failed builds or tool checks
leave the previous shared image and recorded state intact. A build lock prevents
overlapping replacements, and existing containers using the image block a
rebuild. After replacement, the command removes the superseded image.

Once the source clone is recorded, rebuild from any directory with:

```sh
sdlc runtime build
```

Use `--source /PATH/TO/SDLC_CLONE` if the clone moves. Builds are local; CI neither
builds nor publishes the Docker image. Updating the CLI executable and rebuilding
the image are separate operations.

## Inspect the runtime

```sh
sdlc runtime status
```

Status verifies that the selected Docker engine and shared image match the
recorded build. It reports the image ID, source revision and tool versions from
the build checks. It does not check provider authentication or upstream updates.

State lives under `sdlc` in the OS user configuration directory:

| Host | Default base directory |
| --- | --- |
| macOS | `~/Library/Application Support` |
| Linux | `$XDG_CONFIG_HOME`, or `~/.config` |
| Windows | `%AppData%` |

`SDLC_STATE_DIR` can select another directory. The state records the source clone,
engine and image identity; reinstalling the executable preserves it.

macOS/ARM64 installation, reinstallation and local image build have been checked.
Both commands cross compile for macOS, Linux and Windows on ARM64 and x86-64.
Native Linux and Windows CLI execution still needs verification.

## Provider login

After building the shared image, run these commands from an interactive terminal:

```sh
sdlc auth login --provider codex
sdlc auth login --provider claude
sdlc auth status
```

Codex uses its device login flow. Follow the displayed browser instructions;
device authentication must be enabled for your account or workspace. Login has a
fifteen-minute limit. API-key authentication and other providers remain future
work. Claude uses its normal browser login through the unmodified CLI. Open the
displayed URL and paste the code into the terminal if the browser cannot reach
the container callback. These commands use your own account; subscription access
is not shared or used through a custom API client. See [provider usage](provider-usage.md).

Check both providers with `sdlc auth status`, or select one with
`sdlc auth status --provider codex` or `--provider claude`.
Status starts a fresh container with networking disabled and a read-only
credential volume. It reports whether the CLI can load stored account credentials,
without printing account details or raw provider output. This does not validate a
token remotely, check model access or make a paid model request. Status exits
nonzero if any requested provider lacks a usable stored login.

Each installation has one labelled local Docker volume per provider. Codex retains
only `auth.json`; its home, settings and logs are disposable. Claude Code manages
its own native configuration/authentication cache directly in its volume, using
the [documented container pattern](https://code.claude.com/docs/en/devcontainer#persist-authentication-and-settings-across-rebuilds).
SDLC checks file safety metadata but does not read, copy or interpret Claude
credential contents. The cache can also retain provider settings, account metadata
and native logs; treat all of it as private. The installation identity lives in
`auth-installation.json` beside the runtime record. Keep the same state directory
and Docker engine to reuse the volumes after reinstalling or rebuilding.

Sign in once per provider for this installation. Container removal, image rebuilds
and CLI reinstalls preserve login state. The official clients handle refresh;
repeat login when a client requires it. Authenticating on every job would add
friction without changing which uses the account permits.

Login containers use the recorded immutable image, a non-root user, an empty
home, a read-only root filesystem and no Linux capabilities. They receive no
project checkout, host credentials, signing key, GitHub token or Docker socket.
Only login has network access. A separate offline initializer sets volume
ownership with limited privileges and never reads credential contents. The
runtime lock prevents image replacement during authentication.

Claude authentication commands use documented [safe and restricted modes](https://code.claude.com/docs/en/cli-reference).
They retain native authentication, disable user customizations such as hooks and
MCP servers, and exclude cached user settings, including custom credential helpers
and environment overrides. Managed provider policy still applies. Updates and
nonessential traffic are disabled for these commands; the pinned image is updated
through a local rebuild.

Treat these volumes as passwords. They are file-backed Docker storage, without
encryption supplied by SDLC; anyone with Docker administration access can read
them. This persistence is separate from the planned 1Password integration for
work secrets. SDLC refuses to mount volumes with unexpected ownership labels,
drivers or driver options, and refuses linked or publicly readable credential
files.

Login requires terminal input and output. Keep that terminal private and avoid
terminal recording: login URLs and one-time codes appear there. Docker logging is
disabled for all authentication containers, and SDLC creates no authentication
log files. Claude's own private cache logs may persist. Host proxy environment and
Docker-config proxy injection are excluded;
environments that require a proxy need a future explicit configuration option.

If login fails or is interrupted, run `sdlc auth status` and retry the relevant
login. Codex login can replace malformed contents in a safe credential file.
Claude Code manages its own recovery and cache writes, including during a failed
login; SDLC makes no promise that its previous cache is unchanged.
Unsafe storage permissions or links require inspection before retrying; SDLC does
not delete credential volumes automatically. Containers are removed after each
operation, including cancellation. If cleanup fails or the host crashes, inspect
leftover `sdlc-auth-*` containers in Docker before rebuilding the image. Login does
not yet provide logout or remote token revocation.

The command contracts follow the pinned provider versions: [Codex authentication](https://learn.chatgpt.com/docs/auth),
[Codex login implementation](https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/cli/src/login.rs),
and [Claude authentication](https://code.claude.com/docs/en/authentication).
An incompatible provider upgrade fails the status check rather than displaying
unrecognised provider output.

See [the workflow](workflow.md) for the next capabilities.

## Interactive provider sessions

After building the shared image and logging in to the selected provider:

```sh
sdlc interactive --provider codex
sdlc interactive --provider claude
```

The command checks the stored login offline, starts the recorded shared image and
attaches your terminal to the unmodified provider CLI. Enter prompts in its native
interface and exit through that CLI. A missing login directs you to `sdlc auth
login --provider ...`. Provider errors and questions appear directly in the
terminal. SDLC does not submit a prompt, restart a failed session or bypass usage
limits.

This first slice opens an empty `/workspace`. It receives the shared instruction
snapshot and the skills and agents already built into the image. It does not
capture the current repository or load work secrets. Workspace files, temporary
homes and session history are discarded on exit. There is no saved progress log,
checkpoint or resumable session yet; do not use it for work you need to keep.

Only the selected provider's private native cache persists. The official client
handles refresh. Codex's scratch `auth.json` points to its persistent cache so
native refresh writes survive interruption; native logout is propagated on exit.
This follows the pinned client's [file-cache implementation](https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/login/src/auth/storage.rs)
and must be rechecked on upgrade. Claude manages its cache directly; it may retain account metadata and
native configuration. Its transcript history and automatic memory are disabled.

Sessions run as a non-root user with a read-only root filesystem, no Linux
capabilities, bounded resources and no host repository or Docker socket. They
have network access and can access their own provider cache, so use trusted
prompts. Codex uses Docker as the isolation boundary with
`--sandbox danger-full-access --ask-for-approval on-request`, following the
[official container guidance](https://learn.chatgpt.com/docs/agent-approvals-security).
This avoids adding privileges for a second Linux sandbox. Claude starts in its
normal manual permission mode, uses fresh read-only settings and an empty strict
MCP configuration, and disables account MCP connectors. Its global instructions,
skills and agents remain available; cached rules, commands, styles, workflows and
plugins are hidden by disposable directories. Documented native work-data
directories, including plans and file history, are also disposable. Recheck this
layout when upgrading the pinned client. Native settings changes cannot persist
through the read-only settings file. Managed provider policy still applies.

The runtime lock prevents login operations or image replacement during a session.
Normal exit and cancellation remove the container and instruction snapshot while
keeping the provider cache. If cleanup fails or the host crashes, inspect leftover
`sdlc-interactive-*` containers before trying again. Reinstall the CLI after this
change; the existing image can be reused.
