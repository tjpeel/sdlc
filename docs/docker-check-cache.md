# Shared Docker check cache

New Docker-enabled ticket and feature runs use one persistent test daemon per
SDLC state directory and local Docker engine. Named runtimes use the same daemon
when their frozen daemon pins agree. The daemon owns its image store exclusively;
SDLC refuses another container using that data volume, a different daemon pin,
unexpected mounts or a changed daemon command.

Rebuild the runtime after upgrading the CLI so it contains `sdlc-test-proxy`.
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

Host package-manager caches and previous mutable build directories are not
imported. Reusing those safely needs package-specific concurrency and provenance
rules; an old `obj`, `bin` or dependency directory cannot establish that checks
ran against the current candidate.

## Parallel sessions

Each session has a scoped Docker API socket. Container, network, volume and local
image names are mapped into its namespace, including explicit Compose project
names and fixed `container_name` entries. Checks can use the same published TCP
or UDP port in concurrent sessions. Ports bind in each session's network
namespace, and `network_mode: host` joins that namespace. Worker `localhost`
therefore reaches only its session's published services.

Only the controller and proxy can access the shared daemon socket. Session
cleanup stops its workers and proxy before removing its test containers,
networks, volumes and writable workspace. Immutable image cache references and
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
also removes retained images, build cache and source templates. Do not remove a
daemon while its check sessions are active, or mount its data volume in a second
daemon.

Dockerfiles that copy source or frozen inputs into an image leave those bytes
in retained Docker layers and build cache. Source-template exclusions do not
exclude them from Docker cache. Keep credentials out of build contexts and use
the cache only for trusted projects with compatible data-access requirements.
