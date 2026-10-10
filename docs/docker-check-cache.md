# Shared Docker check cache

New Docker-enabled ticket and feature runs use one persistent test daemon per
SDLC state directory and local Docker engine. Named runtimes use the same daemon
when their frozen daemon pins agree. The daemon owns its image store exclusively;
SDLC refuses another container using that data volume, a different daemon pin,
unexpected mounts or a changed daemon command.

Rebuild the runtime for new runs so its `sdlc-test-proxy` supports NuGet cache
sharing. SDLC probes capability once per immutable runtime image. Saved runs
with older frozen proxies keep private HOME across commands, while persistent
NuGet reuse and automatic nested cache mounts remain unavailable. Their logs
explain the fallback; upgrading the CLI does not change a saved runtime pin.
Previously saved runs retain their recorded mode: an absent `daemon_mode` means
the original disposable daemon. Resuming those runs does not migrate them.

## Reuse and performance

The persistent daemon retains pulled image layers and classic build cache after
each check session. Before checks start, SDLC examines the selected project's
Compose configuration and ordinary Dockerfile image references in a worker
without network access or host credentials. Required images already cached on
the host are streamed into the test daemon by immutable image ID, once per ID.
Only project references are considered. SDLC does not copy the host Docker data
directory or credential configuration. Missing images and newer images required
by the project's pull policy still use Docker's normal pull path.

Committed source templates are cached by Git tree identity. Each session receives
an independent writable copy, using filesystem reflinks where supported. Frozen
inputs and baseline source substitutions are applied after copying, so `.env`
files and mutable build outputs do not enter the source template. Git blobs are
read through one batch process rather than starting Git for each file. Existing
passing check evidence for an unchanged candidate remains reusable under the
controller's existing verification rules.

Every check phase has a private HOME that lasts across its commands. A restore,
local tool installation and later build therefore see the same package and tool
state. Candidate and baseline phases have separate homes. Legacy disposable
checks receive the same session lifetime, without persistent package reuse.

Shared sessions also receive private NuGet packages and scratch directories.
The controller retains an immutable package seed in its Linux work volume and
copies it into each session using reflinks where supported. It owns the source
ancestors and exposes only package/scratch mount roots to workers. A writable
HOME cannot redirect those sources into another session or the persistent seed.
Nested Docker run containers receive the same package and scratch mounts through
the scoped proxy. Explicit `NUGET_PACKAGES` and `NUGET_SCRATCH` overrides may stay
within the session workspace. Arbitrary cache binds remain unavailable. These
mounts are for running containers; Dockerfile restores reuse ordinary image
layers and do not automatically receive session package mounts.

For the initial seed, SDLC reads committed `.csproj`, `.props` and tool-manifest
blobs to select package IDs and explicit versions. It follows dependencies from
validated cached nuspecs, importing at most 64 changed packages and 128 MiB of
archives per session. The host source is only `~/.nuget/packages`; no wider HOME,
NuGet configuration, feed URLs from cache markers or authentication state is
copied. Fingerprints avoid revalidating unchanged host packages, while a seed
epoch makes host import resume after the persistent volume is reset. Private
check logs record import counts, rejected packages and elapsed time. If host
`python3` is unavailable, host import is skipped with a log message; runtime-side
package reuse, ordinary restores and cleanup promotion continue.

An imported or newly promoted package must have a complete extraction marker,
a matching SHA-512 archive hash, the expected nuspec ID/version and complete,
matching extracted payloads. Links, unsafe archive paths and oversized payloads
are rejected. The controller reconstructs files from the checked archive and
writes a fresh marker without the original feed URL. Signed packages may use a
NuGet content hash that differs from the complete archive SHA-512. The original
marker content hash is preserved as package metadata; the archive sidecar still
checks every archive byte, and extracted payloads and package identity must
match. This does not verify a signature or independently establish the signed
package content hash. The original authorized
package archive remains package data; its own nuspec and signature bytes are
retained. At cleanup, after workers
and nested containers stop, valid new packages are promoted under a kernel
lease. The first valid ID/version wins; conflicting later archive bytes are
reported and skipped, and never overwrite the seed. A protected manifest of
the initial copy records archive inode, size and timestamps; unchanged copies
are skipped during cleanup without rehashing their archives. Crash recovery discards an
orphan session cache rather than promoting it.

This validates package consistency, not publisher signatures or registry
provenance. Use the persistent seed for trusted local projects and package data;
a project can produce its own internally consistent package. Previous `obj`,
`bin` and mutable build directories are never imported.

NuGet's [cache guidance](https://learn.microsoft.com/en-us/nuget/consume-packages/managing-the-global-packages-and-cache-folders)
requires processes sharing global packages to share the scratch location used
for filesystem locks. Direct workers and nested containers in one session use
the same packages and scratch. Concurrent sessions have independent copies and
scratch directories, so they do not write a shared global package tree.

## Parallel sessions

Each session has a scoped Docker API socket. Container, network, volume and local
image names are mapped into its namespace, including explicit Compose project
names and fixed `container_name` entries. Checks can use the same published TCP
or UDP port in concurrent sessions. Ports bind in each session's network
namespace, and `network_mode: host` joins that namespace. Worker `localhost`
therefore reaches only its session's published services.

Only the controller and proxy can access the shared daemon socket. Session
cleanup stops its workers and proxy before removing its test containers,
networks, volumes, writable workspace, HOME and private NuGet cache. Immutable image cache references and
source templates remain. Kernel leases and private session records allow the
next controller to recover an orphan left by a crashed controller while keeping
live sessions running.

## Compatibility and trust

Shared checks use the classic Engine build API. SDLC sets `DOCKER_BUILDKIT=0`,
`COMPOSE_BAKE=false` and `COMPOSE_DOCKER_CLI_BUILD=0`; BuildKit tunnels and remote
build contexts are rejected because they cannot be scoped by this proxy.
Simple global `ARG` image references are supported. Unsupported image-reference
substitution and multiline references fail explicitly.
Dockerfile `ADD` and `ONBUILD` instructions and archive links are rejected;
use `COPY` with regular context files. Cached base images with deferred build
triggers are rejected. Missing public bases still use Engine pulls, so those
externally supplied base images must be trusted.

Session builds can use their own images and approved shared bases. Ordinary
registry references and immutable controller seed markers identify shared
bases; reserved controller repositories cannot be pulled or overwritten.
Dangling private intermediate images remain available to the daemon's build
cache but are hidden from session listings and rejected as direct image inputs.

Shared checks support bridge networks, session host networking, workspace binds,
named and anonymous local volumes, dynamic or fixed TCP/UDP port bindings,
Compose build/run/exec/down and ordinary Docker clients. Static network addresses,
host volume-driver options, privileged test containers, host devices, global
pruning and image publication are unavailable. Testcontainers' Ryuk is disabled;
the SDLC controller performs session cleanup. A check that needs unsupported
Engine behaviour must be adapted before using this mode.

The outer daemon remains privileged. Use it only for trusted repository checks.
Resource scoping prevents normal check operations from affecting other sessions;
it does not make privileged Docker a security boundary against hostile code.
Bind sources are canonicalized within the session workspace, but a hostile
check can still race filesystem changes against daemon mount resolution.

## Cache lifetime

Cache volumes grow until explicitly removed. Daemon upgrades never silently
reuse or downgrade an existing data store. If the frozen daemon pin changes,
stop SDLC Docker checks before removing the corresponding idle test daemon and
its volumes. Identify them through the `io.sdlc.test-owner` label; each daemon
owns `<daemon>-data`, `<daemon>-work` and `<daemon>-socket`. Removing these volumes
also removes retained images, build cache, source templates and the NuGet seed. Do not remove a
daemon while its check sessions are active, or mount its data volume in a second
daemon.

Dockerfiles that copy source or frozen inputs into an image leave those bytes
in retained Docker layers and build cache. Source-template exclusions do not
exclude them from Docker cache. Keep credentials out of build contexts and use
the cache only for trusted projects with compatible data-access requirements.
