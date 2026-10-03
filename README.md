# SDLC

A Go CLI for a ticket-based engineering workflow using one shared local Docker
image. The workflow starts from the repository where work is requested.

The CLI currently supports installation and reinstallation, version reporting,
and building and inspecting the shared runtime. Provider login, secret retrieval,
ticket execution, progress logs and recovery are still to be implemented.

## Get started

From this clone, with Go 1.24 or later and a local Docker engine running Linux
containers, select an existing directory on your PATH:

```sh
go run ./cmd/sdlc-install --bin-dir /PATH/TO/YOUR_BIN_DIRECTORY
sdlc --version
sdlc runtime build --source .
sdlc runtime status
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

## Documentation

- [CLI installation and commands](docs/cli.md)
- [Agreed workflow and remaining work](docs/workflow.md)
- [Development and validation](docs/development.md)
- [Keeping public commits free of private material](docs/publication-safety.md)
- [Research and earlier prototypes](research/README.md)
