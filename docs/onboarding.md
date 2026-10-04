# Connected onboarding and test plan

Use this guide for the first joint connected SDLC trial, then for onboarding a
real .NET repository. Start with a new private, disposable GitHub repository and
the public .NET fixture. The aim is to prove implementation, isolated checks,
signed draft publication, CI, opposite-provider review and repair together.

Prepared on 4 October 2026. The Go/Python offline suites, real .NET API/Mongo
checks, captured root `.env` and concurrent nested-Docker port reuse have passed.
The connected delivery path has not yet passed a complete trial. This document
prepares that trial; it does not record a connected test result.

The isolated Docker publisher and dedicated 1Password signing route are
implemented. Start with [GitHub profiles and signing](github-docker-test.md).
Steps 1–12 test them under supervision; the host controller stays attached to
its terminal. Terminal-independent execution and restart reconciliation remain
required gates. See the [delivery plan](proposals/unattended-docker-delivery.md).

Replace `/PATH/TO/SDLC`, `/PATH/TO/NEW_TEST_REPOSITORY` and `OWNER/REPO` with the
chosen paths and repository. Run each step separately and resolve failures
before proceeding. Do not replace the contents of an existing project.

## 1 Prepare the host and test accounts

Have these available for the session:

- A running local Docker engine using Linux containers, with enough disk and
  memory for the SDLC runtime, .NET SDK builds, Mongo and two test daemons.
- Host Git, GitHub CLI (`gh`), Python 3 and Go 1.25 or later for installation.
  Go and host Codex/Claude binaries are not required after SDLC is installed;
  the provider clients and .NET SDK run in the shared image.
- Your authorised Codex and Claude accounts, with access to the chosen models.
  The browser may be needed for initial login. Provision a native GitHub profile,
  a dedicated signing key and a read-only 1Password Service Account using the
  signing guide. Desktop approval is not the ticket-publication route.
- A new private GitHub repository with Actions enabled. Create it empty, without
  an initial README, licence or `.gitignore`, so the local baseline can be pushed.
- The intended Git name, verified or actual GitHub noreply email, and a signing
  public key registered with GitHub as a **signing** key.

Keep the computer awake and both controller terminals open during the trial.
Headless means no provider UI; the SDLC controller currently remains attached to
its terminal. The dashboard does not keep a stopped controller running.

Before connected work, review the selected account and local execution mode
against [provider usage](provider-usage.md) and current official
[Codex authentication](https://learn.chatgpt.com/docs/auth),
[Codex non-interactive guidance](https://learn.chatgpt.com/docs/non-interactive-mode)
and [Claude credential rules](https://code.claude.com/docs/en/legal-and-compliance).
Use the unmodified native clients as the account owner. API keys are OpenAI's
default recommendation for automation; the current SDLC account route is a
local single-user workflow and rejects CI execution. Repository CI below runs
builds and tests, without provider login.

## 2 Install the current CLI and build the runtime

From the SDLC source checkout:

```sh
cd /PATH/TO/SDLC
go version
go run ./cmd/sdlc-install --bin-dir /PATH/TO/YOUR_BIN_DIRECTORY
sdlc --version
sdlc runtime build --source .
sdlc runtime status --offline
sdlc runtime status
```

On an Apple Silicon Mac with Homebrew Go outside the current shell's PATH, use
`/opt/homebrew/bin/go` for the two Go commands. `/opt/homebrew/bin` is a suitable
installation directory only if it is on your PATH.

Expected: the new CLI version is installed, the image is `sdlc:local`, the
recorded image passes local inspection, and the online status command reports
dependency/update information or explains unavailable metadata. It does not
upgrade anything or prove that an available update is safe. Login volumes survive
reinstallation and rebuilds. Finish rebuilding before launching any ticket;
resume requires the recorded image identity.

## 3 Verify both provider logins and persistence

```sh
sdlc auth status --all
```

If a provider has no saved login, authenticate it:

```sh
sdlc auth login --provider codex
sdlc auth login --provider claude
```

Complete the official flow yourself. Codex device login can require enabling
device-code authentication in account/workspace settings. Saved-login status is
an offline check; it cannot establish remote validity or model entitlement.

Open each client, complete any native first-launch prompts, and ask a small
connected question such as: `Do not edit files or run commands. Reply exactly
SDLC_AUTH_OK.`

```sh
sdlc interactive
sdlc interactive --provider claude
```

Exit normally without `/logout`, then reopen each client. Expected: no repeated
login, and the small request succeeds. This checks login and persistence, not
access to the selected headless lead or pinned subagent models; those are tested
by the connected ticket runs. The default modes already allow full
access inside Docker; no permission-mode option is needed. Record any account,
model or usage-limit error rather than repeatedly restarting to bypass it.

Inspect the installation's shared rules privately:

```sh
sdlc instructions show
```

Confirm the human-question stop rule is present and that custom instructions do
not conflict with unattended delivery. Shared instructions apply to all projects
using this installation. The runtime contains pinned engineering skills and
agent policies; no host skill directory needs mounting.

## 4 Provision the Docker publication profile

Follow [GitHub profiles and signing](github-docker-test.md) through its signing
verification step. Use `personal` below, or replace it with your selected profile.
The new native login must be authorised for the disposable repository, including
organisation SSO. Existing host SSH access alone does not prove this.

```sh
sdlc auth status --service github --profile personal --verify
sdlc signing verify --profile personal
```

Expected: the intended GitHub login/numeric ID and a disposable local commit
signed and verified with the dedicated key, without a desktop approval prompt.
No token or private-key output should appear. GitHub repository push permission
is checked at run launch; actual PR/CI access remains part of the live trial.
Host `gh` and signing settings below are only for bootstrapping the baseline,
not for ticket publication. Do not wrap `sdlc run` in a host `op run` credential
wrapper; the publisher uses its own profile.

## 5 Export the fixture into the new repository

Create the new destination, then export only committed public files:

```sh
mkdir /PATH/TO/NEW_TEST_REPOSITORY
git -C /PATH/TO/SDLC archive HEAD:examples/dotnet-smoke \
  | tar -x -C /PATH/TO/NEW_TEST_REPOSITORY
cd /PATH/TO/NEW_TEST_REPOSITORY
git init -b main
git remote add origin https://github.com/OWNER/REPO.git
```

Set approved identity locally if the new repository does not inherit the right
values. This avoids changing settings for unrelated repositories:

```sh
git config --local user.name "YOUR NAME"
git config --local user.email "YOUR VERIFIED OR ACTUAL NOREPLY EMAIL"
git config --local commit.gpgsign true
git config --get user.name
git config --get user.email
git config --type=bool --get commit.gpgsign
git config --get gpg.format
git config --get user.signingkey
```

For SSH signing, also check `gpg.ssh.allowedSignersFile`; SDLC verifies locally
after signing. Use absolute host paths for trust/key/signer files. Optional
`gpg.format` may be unset for default OpenPGP signing; optional
`gpg.ssh.program`/`gpg.program` may be unset when the default signer is correct.
Resolve signing for this initial host baseline with your existing approved
setup. Ticket publication uses the separate Docker signing profile. SDLC freezes
the project Git identity and approved signer before provider work and requires
the recorded profile on resume.

## 6 Add CI before the first ticket

The exported fixture has no CI workflow. Create
`.github/workflows/onboarding.yml` in the disposable repository with:

```yaml
name: Onboarding checks

on:
  pull_request:
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  build-and-unit-tests:
    runs-on: ubuntu-24.04
    timeout-minutes: 15
    container: mcr.microsoft.com/dotnet/sdk:10.0.401
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - run: dotnet build Smoke.slnx
      - run: dotnet test tests/Smoke.UnitTests/Smoke.UnitTests.csproj --no-build --no-restore
```

This is a proposed workflow for tomorrow, not a validated hosted CI result. Its
purpose is to exercise PR-triggered checks and SDLC's CI gate; integration checks
run separately in SDLC's dedicated daemon. It needs no provider credentials,
GitHub PAT, signing key or real test secrets. The checkout pin is the
[official v7.0.1 release](https://github.com/actions/checkout/releases/tag/v7.0.1);
see [container jobs](https://docs.github.com/en/actions/how-tos/write-workflows/choose-where-workflows-run/run-jobs-in-a-container).

Use an authorised maintainer credential to bootstrap the workflow. A fine-grained
PAT that creates or edits workflow files needs the corresponding Workflows
permission in addition to repository Contents access. The normal ticket token
can omit that permission when tickets do not change workflow files. See
[workflow-file permissions](https://docs.github.com/en/rest/repos/contents#create-or-update-file-contents).

## 7 Commit and publish the signed baseline

Review the fixture and workflow before staging. No `.env` has been created yet:

```sh
git add .
git diff --cached --stat
git commit -S -m "Add .NET onboarding fixture and CI"
git verify-commit HEAD
git log -1 --format=fuller --show-signature
git -c credential.helper= \
  -c 'credential.https://github.com.helper=!gh auth git-credential' \
  push -u origin main
```

Expected: local signature verification succeeds, GitHub shows the baseline
commit as Verified under the intended identity, and the push CI job passes.
Resolve any signing-agent prompt or organisation approval for this supervised
baseline. Passing this step does not prove unattended signing after desktop
lock or restart. Do not disable signing to work around a failed signing check.

Confirm the same baseline is local and remote:

```sh
git rev-parse main
gh api repos/OWNER/REPO/branches/main --jq .commit.sha
```

The two SHAs must match. SDLC rejects a different remote base at publication.

## 8 Configure ignored test inputs and explicit checks

```sh
cp test-environment.example .env
chmod 600 .env
git check-ignore .env
sdlc init
```

Set `.sdlc/project.json` to:

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

`.sdlc/project.json` is project configuration and can appear as an untracked
Git path. Private `.sdlc/work/` data is locally excluded. For a clean onboarding
baseline, commit only the reviewed settings, then push `main` using the helper
command from step 7 and wait for CI again:

```sh
git add .sdlc/project.json
git diff --cached
git commit -S -m "Configure onboarding checks"
git verify-commit HEAD
```

Use only the synthetic template settings for this trial. `.env` is a check-only
input, captured once and copied to the isolated check workspace. It is not
automatically exported to the outer process. Compose interpolation and service
`env_file` consume it. Do not pass it with `--input`, which makes requirements
visible to providers. Test code can read supplied values; use disposable service
settings in real projects too.

## 9 Select one ticket and inspect the offline plan

```sh
mkdir -p .sdlc/work/dotnet-smoke/tickets
cp docs/specification.md .sdlc/work/dotnet-smoke/specification.md
cp docs/tickets/01-count-items.md .sdlc/work/dotnet-smoke/tickets/01-count-items.md
git check-ignore .sdlc/work/dotnet-smoke/specification.md \
  .sdlc/work/dotnet-smoke/tickets/01-count-items.md
sdlc work --reference dotnet-smoke
sdlc run --reference dotnet-smoke --ticket 01-count-items.md \
  --input .sdlc/work/dotnet-smoke/specification.md --repo OWNER/REPO \
  --github-profile personal \
  --docker-tests --dry-run
```

Expected: one numbered ticket, the intended repository and base, the selected
specification, three check arrays, `.env` as a check input, and Docker test mode.
The plan should show Codex implementation at medium effort and Claude review at
high effort. Check the full requested model IDs against account availability;
use `--model`/`--review-model` overrides on a **new** run if necessary. Lead flags
do not replace pinned subagent policies. Dry-run performs no authentication or
model request and cannot establish either provider's access.

## 10 Run Codex implementation with Claude review

Start the connected trial only after agreeing that this private test repository
and the selected account mode are suitable. The following command makes provider
requests and can push a branch and create/update a draft PR:

```sh
sdlc run --reference dotnet-smoke --ticket 01-count-items.md \
  --input .sdlc/work/dotnet-smoke/specification.md --repo OWNER/REPO \
  --github-profile personal \
  --docker-tests
```

In a second terminal:

```sh
sdlc dashboard
```

Record the run ID and PR URL privately. Expected observations are:

1. Native implementation output streams to the host while the dashboard shows
   a live heartbeat, stage and requested model.
2. The implementation produces the count endpoint and committed candidate.
3. Build, unit and Compose integration checks run in credential-free workers.
   Mongo and API published ports belong to a separate nested daemon.
4. The Docker publisher recreates and signs commits, verifies the tested tree, pushes the
   branch and creates a draft PR. Check its identity and Verified badge.
5. The exact PR head receives successful CI checks.
6. Claude independently reviews that published revision. Actionable findings
   return to the same Codex implementation session; repairs repeat checks,
   publication, CI and a fresh review.
7. A complete review without findings reaches `ready`. The PR remains draft;
   SDLC neither approves nor merges it.

Closing the dashboard leaves the controller working. Native logs and run output
can contain private code or account data; keep them in ignored private storage.
Do not interpret Codex aggregate token usage as context occupancy. Missing model
or context fields are unknown, rather than a pass or a zero reading.

## 11 Test attention and resume deliberately

Use a separate bounded disposable ticket to request a human decision. Confirm
`waiting_for_human` appears and the controller does not continue automatically.
Put the answer in a private text file, then use the recorded run ID:

```sh
sdlc run --reference dotnet-smoke --ticket NUMBERED_FILE \
  --resume RECORDED_RUN_ID --answer-file /PATH/TO/PRIVATE_ANSWER.txt
```

For an operational stop or reviewer login, resume without an answer:

```sh
sdlc run --reference dotnet-smoke --ticket NUMBERED_FILE \
  --resume RECORDED_RUN_ID
```

Resume preserves provider/model, runtime image, instructions, checks and captured
inputs. Do not add `--docker-tests`, `--provider`, model flags or new `--input`
arguments to resume; their original values are retained. Editing the host `.env`
or project settings does not change an existing run. Keep journal, workspace and
native session storage intact. Repair/turn limits persist through resumes.

After the first successful delivery, use a separate synthetic trial to exercise
error handling and confirm a repair round if the reviewer finds an actionable
defect. A clean review does not prove connected repair behavior; record that
coverage as open if no finding occurs. Do not modify the published branch behind
the controller to manufacture feedback; that should trigger its drift guard.
Also test a controlled interruption/resume and an absent reviewer login if it
can be done without logging out an account needed by another run. Missing
reviewer login should pause at `awaiting_reviewer` after publication and CI.
Stop on account limits or access/policy errors; resolve the cause without account
rotation or repeated restarts.

## 12 Reverse providers and observe concurrency

Use a new bounded ticket with an observable change; the count-endpoint ticket
should not be repeated after that endpoint has been delivered. Launch with
`--provider claude` and inspect its dry-run first:

```sh
sdlc run --reference dotnet-smoke --ticket NUMBERED_FILE \
  --input .sdlc/work/dotnet-smoke/specification.md --repo OWNER/REPO \
  --github-profile personal \
  --provider claude --docker-tests --dry-run
```

Remove `--dry-run` when ready. Expected: Claude implements, Codex reviews, and
the same signed-publication and CI gates apply. This is a new run, not a provider
change on an existing run.

Once each direction has passed, two independent tickets may be launched from
the same unchanged baseline in separate terminals. Use non-overlapping changes
and keep PRs draft. Same-provider operations queue until cache cleanup completes;
different providers can overlap. Verify queued and attention states in the
dashboard. Separate check daemons can reuse internal ports, as already shown by
the offline port probe. Concurrency does not resolve conflicting changes,
inter-ticket dependencies or movement of `main`.

## 13 Validate unattended publication and future supervision

The [GitHub/signing test guide](github-docker-test.md) can now prove native cache
persistence, separate accounts, locked-desktop key retrieval, required signing,
and bounded denial without model requests. Complete those checks now.

The controller supervision portion below remains blocked on implementation.
No real credentials belong in this public guide.

After detached controller supervision is implemented, use the
disposable repository to prove:

- The 1Password desktop app can be locked or closed, with no personal agent
  forwarded. The job continues after closing the launcher terminal.
- Implementation, checks, signed draft publication, CI and opposite-provider
  review complete without approval prompts. A real actionable finding exercises
  repair and a second signed publication without interaction.
- Restart at a safe checkpoint retains the intended account, Git identity,
  signer and run, with no duplicated push/PR and no new desktop approval.
- A revoked/expired credential produces a clear attention state, rather than
  hanging for input, falling back to desktop login or changing accounts.
- Provider/check workers cannot see publisher credentials, and retained state
  and output contain no tokens or private signing keys.

See [machine credentials](github-credentials.md#machine-credentials-for-unattended-docker-delivery)
and the [dedicated-vault test](1password-test.md) for supported 1Password access
and signing distinctions. Human ticket questions and provider account restrictions
remain legitimate attention states.

## Remaining work before real tickets

| Priority | Item | Completion evidence |
| --- | --- | --- |
| Required joint test | Supported account mode, saved login persistence and actual requested-model access for both providers | Native login/persistence and selected headless roles succeed; reported model IDs agree where available. Missing model reports remain unknown. |
| Required setup | Exact GitHub repository/account, signed host identity, local/remote base and PR-triggered CI | Signed baseline verifies locally and on GitHub; baseline CI passes. |
| Required joint test | Complete Codex to Claude delivery | `ready` with signed draft PR, green current-head CI and complete independent review. |
| Required joint test | Claude to Codex delivery | A new ticket succeeds with reversed roles, signed publication and current-head CI. |
| Required joint test | Human attention and interruption recovery | A recorded run resumes the correct native session after a private answer or resolved stop. |
| Required repository onboarding | Selected API or consumer checks | Existing baseline checks pass; exact SDK/test-runner commands and disposable Compose settings are configured. The first bounded ticket validates that repository's SDLC worker topology. |
| Required before relying on unattended repair | Connected actionable review feedback | A real finding causes repair, retest and fresh review. Keep this open after a clean trial; offline loop tests alone do not establish connected repair. |
| Required for unattended Docker delivery | Separate publisher, machine credentials and publication preflight | No desktop unlock/approval after provisioning; required signing, repository/account/identity binding and narrow mounts are validated. The Docker publisher and required signing are implemented and tested offline; live OAuth/SSO, attribution and locked-desktop delivery remain to be proven. |
| Required for unattended Docker delivery | Docker controller supervision, bootstrap recovery and expiry handling | Terminal closure and safe restart preserve the job; machine credentials remain available without prompts; invalid credentials stop with attention. Current controller is foreground only. |

The connected gates establish the supervised workflow. Both unattended Docker
gates are also required before relying on the intended unattended solution.
A small supervised real ticket can diagnose repository compatibility sooner.
Run `sdlc init`; explicitly configure the repository's build, unit and integration
commands, using passing existing baseline CI/tests as a starting point.
Initialization does not infer .NET checks.
Use the repository's actual VSTest or Microsoft Testing Platform commands, not
the fixture's test command by assumption.

There is no standalone `sdlc check` command today. Configured SDLC checks start
after the provider commits a candidate. A credential-free check-only command is
a useful next implementation if we need to validate an actual repository's
unchanged baseline inside SDLC before spending provider context. Until then,
the first small real ticket is also that repository's worker-compatibility trial;
address concrete failures as they appear.

For API/consumer repositories, check Mongo, queue/topic emulation, HTTP stubs,
initialization services, readiness conditions and relative bind mounts. Mount
paths must exist in the check worker's copied workspace; external host files are
not supplied automatically. The current public example covers API/Mongo,
Compose environment input, service DNS and localhost. It does not establish
consumer behavior, queue semantics or the real repositories' full integration
setup. Add generic consumer/queue/initializer examples only where they reveal a
gap, and keep private repository material out of public fixtures.

Later items include wider recovery and cleanup controls, stacked
ticket orchestration, a reliable provider context gauge where native metadata
permits it, and other-host validation. Detached Docker supervision and managed
bootstrap recovery remain required above. The explicit plaintext-file resolver
is implemented; OS credential-store integration remains future work. GitHub App
tokens are an optional alternative where installation is permitted. Run capture currently rejects linked worktrees, submodules and
symlinks; use a
compatible ordinary checkout. Broader recovery after external branch/base changes
is not implemented.

Keep a private test record with run IDs, CLI/image versions, requested/reported
models, stage outcomes, signature verification, CI head, review/repair outcome
and any stop reason. Do not commit transcripts, real `.env` files or credentials.

Preparation validation on 4 October checked shell syntax, local documentation
links, project-settings JSON and the proposed workflow's YAML structure. A
disposable offline checkout passed `init`, ignore checks, ordered ticket discovery
and both provider dry-run plans using the documented settings. That validation
used no Docker, account, host signing or GitHub requests; hosted CI and connected
delivery remain the joint tests above.
