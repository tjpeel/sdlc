# .NET smoke fixture

Copy this directory into a disposable Git repository before using SDLC. It has
a .NET 10 `.slnx` solution, a minimal API, xUnit unit tests and an integration
check that runs the API, Mongo and tests with nested Docker Compose.
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

Create the ignored host-root test environment, then initialise the disposable
project. The example contains only settings for local test services:

```sh
cp test-environment.example .env
chmod 600 .env
git check-ignore .env
sdlc init
```

Review the disposable project's `.sdlc/project.json` and set these values:

```json
{
  "version": 1,
  "checks": [
    ["dotnet", "build", "Smoke.slnx"],
    ["dotnet", "test", "tests/Smoke.UnitTests/Smoke.UnitTests.csproj"],
    ["python3", "scripts/integration.py"]
  ],
  "input_files": [".env"]
}
```

The integration command requires `sdlc run --docker-tests`. Its worker receives
`DOCKER_HOST=unix:///run/sdlc/docker.sock` for a separate disposable daemon.
SDLC captures the configured `.env` separately, then copies it into the check
worker's `/workspace/.env`. It stays out of the provider workspace and Git
commits. Changes to the host file after capture do not change the saved input.
Do not select `.env` with `--input`, which selects provider requirements.

The runner requires a regular root `.env` and pins every Docker command to the
dedicated socket, including when `DOCKER_CONTEXT` is set. Compose reads `.env`
through `--env-file` for interpolation and `env_file` for the test containers'
environment. A file's presence alone does not export its variables into the
outer check process. See Docker's [interpolation guide](https://docs.docker.com/compose/how-tos/environment-variables/variable-interpolation/)
and [container environment guide](https://docs.docker.com/compose/how-tos/environment-variables/set-environment-variables/).

The integration profile runs the same API/Mongo scenario twice: `tests` uses
service DNS (`http://api:8080`), while `host-tests` shares the nested daemon's
network namespace and uses `http://localhost:18080`. Mongo publishes `27017` and
the API publishes `18080` inside that daemon's namespace. SDLC publishes neither
port on the main engine host. Separate check jobs can reuse these ports; two
copies in one daemon would collide.

Each invocation has a unique Compose project. Cleanup removes its containers,
Mongo volumes, network and local build tags on success, failure and cancellation.
The Docker build context excludes `.env`; checks verify the file is absent from
the API and test images. NuGet restore and image pulls need public registry
access. Running `dotnet test Smoke.slnx` directly includes integration tests and
fails without their environment and running services.

Use reviewed settings for disposable test services. Check code and dependencies
can read supplied values and access the network; a copied environment file does
not protect live service credentials from that code.

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

Inspect the selected inputs, checks and provider roles before a connected trial.
Replace `OWNER/REPO` with the intended test repository:

```sh
sdlc run --reference dotnet-smoke --ticket 01-count-items.md \
  --input .sdlc/work/dotnet-smoke/specification.md --repo OWNER/REPO \
  --docker-tests --dry-run
```

For the connected trial, first configure the test repository's GitHub remote,
effective Git author name/email, SDLC GitHub/signing pair and a CI workflow that
reports PR checks. Follow the [user guide](../../docs/user-guide.md); host `gh`
login and a host SSH signing agent do not supply SDLC's publication credentials.
This fixture includes no CI workflow. SDLC pauses if the PR has no reported
checks after its CI startup wait. The account-based SDLC controller runs locally;
CI runs the repository's checks separately. In the original SDLC checkout,
follow the provider-login section of `docs/cli.md` and the account usage rules
in `docs/provider-usage.md` for your account.

When ready, use the same run command without `--dry-run`. Codex implements by
default; Claude reviews when authenticated. Missing Claude login pauses delivery
at `awaiting_reviewer` after draft publication and CI. Watch from another terminal
with `sdlc dashboard`. The run remains attached to its terminal; detached
execution is not implemented.

The sample uses Microsoft's documented [VSTest/xUnit workflow](https://learn.microsoft.com/en-us/dotnet/core/testing/unit-testing-csharp-with-xunit)
and [.slnx solution support](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-sln),
the official [MongoDB C# driver](https://www.mongodb.com/docs/drivers/csharp/current/get-started/),
and [Docker Compose profiles](https://docs.docker.com/compose/how-tos/profiles/).
SDK, NuGet package and container image versions are explicit; image tags are
version pins rather than immutable digest pins.

Validation on 4 October 2026 passed the complete solution build with SDK
10.0.401, zero warnings and zero errors, all four unit tests, and both bridge and
localhost integration runs against the API and Mongo containers. It exercised
a generated ignored host-root `.env`, check-only capture, frozen input after a
host-file change, Compose interpolation and `env_file`, and image exclusion.
The probe completed in 109
seconds using the dedicated daemon with `overlay2`. Run it from the SDLC checkout:

```sh
SDLC_OFFLINE_DOTNET_TESTS=1 go test -timeout 25m -count=1 -v ./internal/workrun \
  -run '^TestOfflineDotnetSmokeExample$'
```

The probe requires the recorded local SDLC runtime, a Docker engine that can
start its privileged disposable daemon, and network access to public NuGet and
container registries. It copies only 19 explicitly listed public fixture files,
rejects symlinks and generates its own disposable `.env` afterwards. It never
copies the source checkout's actual `.env`. It uses no provider account, account
cache, model request or GitHub publishing credentials. It does not run in CI.

Nine offline runner tests also passed on 4 October 2026:

```sh
python3 -m unittest discover -s tests -p test_dotnet_example.py -v
```

The tests replace Docker subprocesses with fakes. They cover dedicated-socket
rejection, missing/directory/symlink `.env` rejection, the `DOCKER_CONTEXT`
override guard, unique Compose project names, cleanup after build/start/image
exclusion/test failures and cancellation, cleanup failure reporting, and absence
of engine-host mounts or sockets. The real probe
provides the execution evidence; the offline tests check failure and cleanup
paths. The repository sensitive-content check passed.
