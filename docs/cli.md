# CLI installation and commands

The Go CLI provides local installation/reinstallation, build identity, and a
shared runtime image build/status check, and Codex account login. Ticket
execution and secret-store access remain to be implemented.

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
sdlc auth status
```

Codex uses its device login flow. Follow the displayed browser instructions;
device authentication must be enabled for your account or workspace. Login has a
fifteen-minute limit. API-key authentication and other providers remain future
work. Claude login will use its documented native CLI cache in the next slice;
see [provider usage](provider-usage.md).

Check Codex with `sdlc auth status` or `sdlc auth status --provider codex`.
Status starts a fresh container with networking disabled and a read-only
credential volume. It reports whether the CLI can load stored account credentials,
without printing account details or raw provider output. This does not validate a
token remotely, check model access or make a paid model request. Status exits
nonzero if Codex lacks a usable stored login.

Each installation has one labelled local Docker volume for Codex. Only
`auth.json` is persisted. Provider
homes, settings and logs are disposable. The installation identity lives in
`auth-installation.json` beside the runtime record. Keep the same state directory
and Docker engine to reuse the volumes after reinstalling or rebuilding.

Login containers use the recorded immutable image, a non-root user, an empty
home, a read-only root filesystem and no Linux capabilities. They receive no
project checkout, host credentials, signing key, GitHub token or Docker socket.
Only login has network access. A separate offline initializer sets volume
ownership with limited privileges and never reads credential contents. The
runtime lock prevents image replacement during authentication.

Treat these volumes as passwords. They are file-backed Docker storage, without
encryption supplied by SDLC; anyone with Docker administration access can read
them. This persistence is separate from the planned 1Password integration for
work secrets. SDLC refuses to mount volumes with unexpected ownership labels,
drivers or driver options, and refuses linked or publicly readable credential
files.

Login requires terminal input and output. Keep that terminal private and avoid
terminal recording: login URLs and one-time codes appear there. Docker logging is
disabled for all authentication containers, and SDLC creates no authentication
log files. Host proxy environment and Docker-config proxy injection are excluded;
environments that require a proxy need a future explicit configuration option.

If login fails or is interrupted, run `sdlc auth status` and retry the relevant
login. A fresh login can replace malformed contents in a safe credential file.
Unsafe storage permissions or links require inspection before retrying; SDLC does
not delete credential volumes automatically. Containers are removed after each
operation, including cancellation. If cleanup fails or the host crashes, inspect
leftover `sdlc-auth-*` containers in Docker before rebuilding the image. Login does
not yet provide logout or remote token revocation.

The command contracts follow the pinned provider version: [Codex authentication](https://learn.chatgpt.com/docs/auth)
and [Codex login implementation](https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/cli/src/login.rs).
An incompatible provider upgrade fails the status check rather than displaying
unrecognised provider output.

See [the workflow](workflow.md) for the next capabilities.
