# CLI installation and commands

The Go CLI provides local installation/reinstallation, build identity, and a
shared runtime image build/status check. It does not yet start ticket work or
authenticate Codex, Claude or a secret store.

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

See [the workflow](workflow.md) for the next capabilities.
