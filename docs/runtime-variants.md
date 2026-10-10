# Named runtimes and local skills catalogues

A named runtime is an explicit alternative to the shared default `sdlc:local`
image. It uses the same SDLC installation directory, account caches and build
lock, with a separate Docker tag and private state file.

```sh
sdlc runtime build --name skills-test --source /path/to/sdlc \
  --skills-source /path/to/skills
sdlc runtime status --name skills-test --offline
sdlc run --runtime skills-test --reference EXAMPLE --ticket 01-example.md
```

The skills checkout must be its Git repository root, have a committed HEAD, and
have no tracked changes or untracked files. Ignored files may remain: they are
excluded from the archive. Commit the intended catalogue change before building.

SDLC puts only a bounded archive of the committed catalogue into its existing
private Docker build context. It does not copy the checkout, Git state, ignored
credentials or host provider caches. Catalogue entries must have safe relative paths. Committed relative symlink
references are materialized only from regular files within the archive. Links
that escape the root, cycle, traverse parent symlinks or point to authentication
state are rejected, as are hardlinks and special files. The archive is limited to 16 MiB and 20,000 entries;
individual files are limited to 2 MiB. `scripts/install-skills` must be committed.
Validation runs before any Docker operation.

The Dockerfile installs from this archive rather than trying to download an
unpublished commit from GitHub. It records the actual catalogue commit in the
build recipe and dependency inventory. The private named state also records the
selected source path and commit. An inventory mismatch prevents selection of the
candidate image.

The example selects `sdlc:skills-test` and `runtime.skills-test.json`. It preserves
`runtime.json`, the `sdlc:local` tag, and saved run checkpoints. Named builds do not
remove superseded immutable image IDs. A run freezes its selected runtime and
image; a paused run keeps its recorded selection when resumed.

Names accept 1–48 lowercase letters, digits and hyphens and must begin with a
letter. `local` and names beginning `build-` are reserved. Omit `--name` and
`--runtime` to keep the existing default behavior. Local skills require a named
runtime. Named runtimes currently support single-ticket runs; `--all` uses the
default runtime.

To rebuild a local variant, pass `--skills-source` explicitly again. To switch
that named variant back to the source Dockerfile's remote catalogue pins, use
`runtime build --name NAME --source-pins`. `runtime update` manages the default
runtime; named variants use `runtime build`. Existing container and saved-work
build guards still apply. `runtime build --force` permits replacement despite
stopped saved work, retaining its files and previous image. It does not migrate
checkpoints or bypass active controllers and container checks.
