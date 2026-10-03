# .NET smoke fixture

Copy this directory into a disposable Git repository before using SDLC. It has
a .NET 10 `.slnx` solution, a minimal API, xUnit unit tests and an integration
check that runs the API, Mongo and tests in disposable Docker containers.
It contains only synthetic item data and has no provider or account setup.

Create a new empty destination and stop if any command fails. Replace
`/PATH/TO/SDLC` with the source checkout location. Export the committed fixture
with Git so ignored local files are excluded; use your configured Git author
and signing settings for the baseline commit:

```sh
mkdir /tmp/dotnet-smoke-disposable
git -C /PATH/TO/SDLC archive HEAD:examples/dotnet-smoke \
  | tar -x -C /tmp/dotnet-smoke-disposable
cd /tmp/dotnet-smoke-disposable
git init -b main
git add .
git commit -m "Add .NET smoke fixture"
```

Run all remaining commands from that new repository. The baseline commit makes
the fixture available to SDLC source capture; do not initialise or execute this
sample as a project from the public SDLC checkout. Connected execution and PR
publication need separate account and remote setup outside this offline fixture.

Run the local build and unit checks with SDK 10.0.100 or a later .NET 10.0 feature
band. `global.json` uses Microsoft's documented
[`latestFeature` policy](https://learn.microsoft.com/en-us/dotnet/core/tools/global-json)
and excludes prerelease SDKs. Container builds use SDK 10.0.401 and runtime 10.0.12:

```sh
dotnet build Smoke.slnx
dotnet test tests/Smoke.UnitTests/Smoke.UnitTests.csproj
```

Review these exact check arrays in the disposable project's `.sdlc/project.json`:

```json
[
  ["dotnet", "build", "Smoke.slnx"],
  ["dotnet", "test", "tests/Smoke.UnitTests/Smoke.UnitTests.csproj"],
  ["python3", "scripts/integration.py"]
]
```

The integration command requires `sdlc run --docker-tests`. Its worker receives
`DOCKER_HOST=unix:///run/sdlc/docker.sock` for a separate disposable daemon.
The script refuses other sockets and pins every Docker command to this socket,
including when `DOCKER_CONTEXT` is set. It gives each run unique resource names and
removes its containers, anonymous Mongo volume, network and image tags. It
publishes no host ports and mounts no host files. NuGet restore and Docker image
pulls require network access to public package/image registries. No provider
request is needed for these checks. Running `dotnet test Smoke.slnx` directly
includes integration tests and fails without the API endpoint configuration.

To prepare the public count-endpoint template as a local ticket, run from the
disposable repository root after `sdlc init`:

```sh
mkdir -p .sdlc/work/dotnet-smoke/tickets
cp docs/specification.md .sdlc/work/dotnet-smoke/specification.md
cp docs/tickets/01-count-items.md .sdlc/work/dotnet-smoke/tickets/01-count-items.md
git check-ignore .sdlc/work/dotnet-smoke/specification.md .sdlc/work/dotnet-smoke/tickets/01-count-items.md
sdlc work --reference dotnet-smoke
```

Keep the copied requirements ignored and untracked. The template's source link
remains valid after copying. The count endpoint is deliberately absent from the
baseline so that the ticket produces an observable API change.

The sample uses Microsoft's documented [VSTest/xUnit workflow](https://learn.microsoft.com/en-us/dotnet/core/testing/unit-testing-csharp-with-xunit)
and [.slnx solution support](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-sln),
the official [MongoDB C# driver](https://www.mongodb.com/docs/drivers/csharp/current/get-started/),
and [Docker CLI container lifecycle commands](https://docs.docker.com/reference/cli/docker/container/run/).
SDK, NuGet package and container image versions are explicit; image tags are
version pins rather than immutable digest pins.

Validation on 4 October 2026 passed the complete solution build with SDK
10.0.401, zero warnings and zero errors, all four unit tests, and one integration
test against the API and Mongo containers. The probe completed in 109
seconds using the dedicated daemon with `overlay2`. Run it from the SDLC checkout:

```sh
SDLC_OFFLINE_DOTNET_TESTS=1 go test -timeout 25m -count=1 -v ./internal/workrun \
  -run '^TestOfflineDotnetSmokeExample$'
```

The probe requires the recorded local SDLC runtime, a Docker engine that can
start its privileged disposable daemon, and network access to public NuGet and
container registries. It copies only 17 explicitly listed public fixture files
and rejects symlinks before reading them. It uses no provider account, account
cache, model request or GitHub publishing credentials. It does not run in CI.

Nine offline runner tests also passed on 4 October 2026:

```sh
python3 -m unittest discover -s tests -p test_dotnet_example.py -v
```

The tests replace Docker subprocesses with fakes. They cover dedicated-socket
rejection, the `DOCKER_CONTEXT` override guard, disjoint resource names for
repeated and parallel runs, reverse cleanup after success, build failure, test
failure and cancellation, cleanup
failure reporting, and absence of host mounts and published ports. They also
cover a network-create timeout: cleanup is registered before creation because
the daemon may create the network before the client times out. The real probe
provides the execution evidence; the offline tests check failure and cleanup
paths. The repository sensitive-content check passed.
