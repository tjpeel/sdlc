# SDLC

A Go CLI for a ticket-based engineering workflow using one shared local Docker
image. The workflow starts from the repository where work is requested.

The CLI supports installation and reinstallation, version reporting, building and
inspecting the shared runtime, Codex/Claude account login, shared instruction
settings and interactive provider sessions. Secret retrieval, ticket execution,
progress logs and recovery are still to be implemented.

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

## Documentation

- [CLI installation and commands](docs/cli.md)
- [Agreed workflow and remaining work](docs/workflow.md)
- [Development and validation](docs/development.md)
- [Keeping public commits free of private material](docs/publication-safety.md)
- [Provider usage rules and authentication boundaries](docs/provider-usage.md)
- [Research and earlier prototypes](research/README.md)
