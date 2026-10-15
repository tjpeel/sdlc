# Named runtimes and local skills catalogues

A named runtime is an explicit alternative to the shared default `sdlc:local`
image. It uses the same SDLC installation directory, account caches and build
lock, with a separate Docker tag and private state file.

```sh
sdlc runtime build --name skills-test --source /path/to/sdlc \
  --skills-source /path/to/skills --agents-source /path/to/agents
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

Use `--agents-source` to include a clean, committed local agents catalogue,
with or without `--skills-source`. Both options require a named runtime and
apply the same archive limits and authentication-state exclusions. The agents
catalogue must include committed `scripts/install-agents`; its installer runs
for both Codex and Claude. Runtime state, inventory and new-run provenance
record the exact agents commit.

When rebuilding a local agents variant, pass `--agents-source` again or choose
`--source-pins` to return to remote catalogue pins. To retain local skills while
returning agents to remote pins, pass `--source-pins --skills-source`.
Existing runtime leases, container checks and saved-work guards still apply;
`--force` does not bypass an active controller. Named runtimes preserve the
default selection and retain superseded images for saved work.

## Refresh dependencies while retaining local catalogues

Use `--dependencies` on a named build to resolve current public dependencies
within SDLC's supported release tracks, while keeping the selected local
skills and agents commits:

```sh
sdlc runtime build --name custom --source /path/to/sdlc \
  --skills-source /path/to/skills --agents-source /path/to/agents \
  --dependencies --dry-run

sdlc runtime build --name custom --source /path/to/sdlc \
  --skills-source /path/to/skills --agents-source /path/to/agents \
  --dependencies

sdlc run --runtime custom --reference EXAMPLE --ticket 01-example.md
sdlc adopt --runtime custom --pr https://github.com/OWNER/REPO/pull/123
```

The first build does not require an installed default or named runtime. Each
supplied catalogue must be a clean, committed Git checkout; its bounded archive
is validated before metadata requests. SDLC makes no upstream lookup for a
catalogue selected locally. On a fresh runtime, omit either catalogue option to resolve that
catalogue's public main commit instead. Repeat previously selected local paths
when rebuilding, or reset with `--source-pins` in a separate build first. If a local commit changes between
planning and capture, the build stops before Docker operations.

The preview contacts public release and image registries. It prints exact
versions, catalogue commits and image digests without pulling images, building
containers, saving dependency overrides or selecting a runtime. The real build
refreshes Debian packages and checks the resulting .NET runtimes and Debian
package state before selecting its candidate. Metadata or validation failures
preserve the previously selected runtime. Provider sign-in and connected model
requests are not part of dependency resolution.

“Current” follows the supported tracks already used by runtime updates: Node's
selected major, the Go builder's selected major/minor and the .NET SDK's selected
channel. Other direct tools follow stable releases. Transitive npm packages
remain governed by their parent tools. This does not force every tool onto a
new major release.

`--dependencies` requires `--name` and cannot combine with `--source-pins`.
`--dry-run` on `runtime build` requires `--dependencies`. The source Dockerfile
and default runtime stay unchanged. Exact selections are saved privately for
this named runtime; a later ordinary build retains them for the same source.
Pass local catalogue paths again when rebuilding. Existing saved-work guards
and `--force` retention rules still apply.
