# Unattended Docker ticket jobs

> Research archive. See the [current CLI](../../docs/cli.md) and
> [agreed workflow](../../docs/workflow.md) for current status.

The `run` action captures one local ticket, clones the configured remote base branch into a
new Docker workspace, implements and reviews the change in separate agent
sessions, runs configured checks, and publishes the worker's signed commits as
one draft PR. Codex and Claude Code can fill either role. Both CLIs, their skills
and custom agents, .NET 10, Docker CLI, Buildx and Compose are included in the
runtime image.

The current runner stops on a blocked implementation and retains diagnostics; it
has no supported human-answer/continuation path. The
[decision-loop guide](runner-options-and-feedback.md) describes that proposed
lifecycle and links an offline CLI spike. It does not change this runner.

This is a compatibility spike. The optional Docker daemon is privileged, and
the worker can read supplied GitHub, signing and provider credentials. The
[protected service trial](protected-runner-handoff.md) remains separate. Use
disposable automation credentials until that trial establishes protection.

## Bootstrap

For a fresh build, account setup, provider login/status checks and interactive
Codex or Claude sessions, follow [container onboarding](container-onboarding.md).

Start with an ignored `profiles.local.json` copied from
[the profile example](../container-spike/examples/profiles.json). Set the account, verified
email, repository allowlist and dedicated signing-key/token paths as described
in [the README](../container-spike/README.md). Add the following fields to the chosen profile:

```json
{
  "implementation": { "provider": "codex" },
  "review": { "provider": "claude" },
  "checks": [
    "dotnet tool restore",
    "dotnet restore",
    "docker compose up --build -d --wait",
    "dotnet build --no-restore",
    "YOUR_UNIT_TEST_COMMAND",
    "YOUR_INTEGRATION_TEST_COMMAND"
  ],
  "cleanup": ["docker compose down -v --remove-orphans"],
  "max_review_rounds": 2,
  "agent_timeout_seconds": 3600
}
```

Replace the test placeholders and other commands with that repository's
actual checks. `checks` must be nonempty; a model's report does not replace
running them. Commands run in separate shell processes, from the cloned
repository root. Put dependent shell operations in one command when they need
to share shell state. Cleanup runs on both success and failure, before
publication. The agent timeout also bounds each check; cleanup has a shorter
limit.

An optional `"model"` in either role selects that provider's model. Omit it to
use the authenticated CLI's default. The existing profile-level `model` and
`--model` belong to the older Codex commands; use the role-specific fields or
flags for `run`.

If checks need environment values, an optional `test_env_file` names a private
file relative to the profile file. Keep it ignored and mode `0600`, for example
`.secrets/test.env`. It uses raw `NAME=value` lines. The values are supplied to
the worker at launch and are not copied into the image or job descriptor.
Project tools and their private diagnostics can still read those values.

Build and authenticate both providers once for this account/repository:

```sh
python3 /ABSOLUTE/PATH/TO/sdlc/research/container-spike/sdlc.py build \
  --profiles /ABSOLUTE/PATH/TO/profiles.local.json \
  --profile work --repo YOUR_ORG/example-repo
python3 /ABSOLUTE/PATH/TO/sdlc/research/container-spike/sdlc.py login \
  --profiles /ABSOLUTE/PATH/TO/profiles.local.json \
  --profile work --repo YOUR_ORG/example-repo --provider codex
python3 /ABSOLUTE/PATH/TO/sdlc/research/container-spike/sdlc.py login \
  --profiles /ABSOLUTE/PATH/TO/profiles.local.json \
  --profile work --repo YOUR_ORG/example-repo --provider claude
```

These commands use container-owned login volumes and do not supply GitHub or
signing credentials. They do not copy host authentication state. After login,
agent stages use saved authentication while it remains valid; expired login,
usage limits or provider failure stop the job. See the official
[Codex unattended execution](https://learn.chatgpt.com/docs/non-interactive-mode)
and [Claude CLI reference](https://code.claude.com/docs/en/cli-reference).

## Pick up a ticket

Run from the target repository root, or pass `--repo-root /PATH/TO/REPOSITORY`:

```sh
python3 /ABSOLUTE/PATH/TO/sdlc/research/container-spike/sdlc.py run \
  --profiles /ABSOLUTE/PATH/TO/profiles.local.json \
  --profile work --repo YOUR_ORG/example-repo \
  --ticket .sdlc/work/tickets/123/ticket-1.md \
  --branch 123-short-description \
  --implementer codex --reviewer claude --docker-tests
```

This prints the concrete launch and cleanup commands. Add `--execute` to
start the unattended job and authorise its signed push and draft PR. The branch
must be explicit, different from the base and absent on the remote. It has no
added prefix. To reverse the roles, use `--implementer claude --reviewer codex`.
Use `--implementation-model` and `--review-model` to override profile models.

The ticket must be a repository-relative path of the form
`.sdlc/work/tickets/<ticket number>/ticket-*.md`. The local checkout supplies
only that ticket and bounded linked Markdown under `.sdlc/work`; local source
edits are excluded. Relative links to repository documentation resolve against
the fresh clone. Input capture rejects escapes, symlinks, hard links,
nonregular files, invalid UTF-8 and excessive input. It captures inline Markdown
links and reference definitions, with a maximum of 32 files, 256 KiB per file
and 1 MiB in total. This is one explicitly selected ticket, with no dependency
scheduler.

The worker verifies the selected GitHub account before model work. It asks the
implementation provider to create signed commits, runs the configured checks,
and asks the review provider to inspect the candidate in a fresh session.
Findings return to the implementer; checks and review repeat within the round
limit. A passing review must have no findings. Missing or malformed structured
results, failed checks, unsigned commits, wrong identities or reviewer changes
to the candidate stop publication. Claude review exposes read and skill tools;
Codex review uses the ordinary container session. The reviewer is a separate
session in the same worker, without a separate credential boundary.

After cleanup, the worker verifies the reviewed head again and pushes that
exact SHA. An atomic absent-ref lease refuses a branch that appeared during
the job; existing remote branches are never overwritten. It then creates one
draft PR and verifies the remote SHA and PR account, head and base. A failure
between pushing and verification can leave a branch or PR; inspect the saved
result and GitHub before submitting another job. No automatic merge or
application deployment is included.

## Docker-backed checks

`--docker-tests` starts a separate Docker-in-Docker daemon for this job. The
ordinary worker shares its network namespace, so inner Compose-published ports
are reachable through worker `localhost`. Both mount the job workspace at
`/workspace`, so relative bind mounts resolve to the same files. The daemon
listens only on the shared Unix socket. No host Docker socket, host home or
provider credential volume is mounted into it.

The host launcher stops this job's containers and removes its daemon data and
socket volumes after success, failure or handled cancellation. Workspace and
results remain available. SIGKILL, host shutdown and Docker failure can prevent
cleanup; use the printed Compose project and job ID to inspect those resources.
Do not run a broad Docker prune to clean up one job. Launcher actions sharing a
Docker project share a lock, including commands using different profile files,
to avoid overlapping access to provider state.

## Results and verification

The launcher prints a job ID and a ready-to-run inspection command:

```sh
python3 /ABSOLUTE/PATH/TO/sdlc/research/container-spike/sdlc.py results \
  --profiles /ABSOLUTE/PATH/TO/profiles.local.json \
  --profile work --repo YOUR_ORG/example-repo --job-id JOB_ID
```

The named `sdlc-JOB_ID-workspace` volume contains the cloned repository and
`/workspace/results/JOB_ID/`: the validated input snapshot and descriptor,
provider results and diagnostics, check/cleanup logs, reviewed diff and final
status. Raw logs stay private. Failed work is retained for inspection; there
is no automatic resume or retry after ambiguous publication.

Run offline checks and the opt-in container probes:

```sh
python3 -m unittest discover -s tests -v
python3 tests/docker_smoke.py
python3 tests/nested_docker_smoke.py --execute
python3 tests/container_ticket_smoke.py --execute
```

The nested probe starts two concurrent disposable jobs with identical ports
and inner Compose project names. It checks nested builds, relative mounts,
worker-local HTTP, .NET 10, catalogue availability and fixture cleanup after a
failing check. The ticket probe uses disposable signing keys, real local Git
operations and CLI stubs for provider/GitHub boundaries; it makes no paid model
calls or GitHub writes.

The existing updater manages Codex, GitHub CLI and catalogue pins. Claude and
.NET pins currently require manual updates. Docker client image updates use
the Dockerfile; keep the daemon image in the optional Compose overlay aligned
with the client. Live provider authentication, a real ticket and GitHub's
Verified signature display still need a connected trial.
