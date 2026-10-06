# CLI installation and commands

The Go CLI provides local installation/reinstallation, build identity, shared
runtime image build/status checks, Codex/Claude account login, shared instruction
settings, local project initialization, ordered ticket discovery and interactive
provider sessions and single-ticket or feature runs through publication, CI and
independent review, plus a live dashboard of local runs. GitHub profiles and mandatory SSH signing
use separate Docker containers. Feature runs schedule dependencies and reconcile
owned ticket PRs after human merges.

## Install or reinstall

Clone this repository and run the installer from its root. Local builds require
Go 1.25 or later. Choose an existing directory on your PATH:

```sh
go run ./cmd/sdlc-install --bin-dir /PATH/TO/YOUR_BIN_DIRECTORY
sdlc --version
```

Repeat the same command after updating the clone. The installer builds a native
executable before replacing the previous installation. A failed build leaves the
previous executable intact. It preserves runtime state and unrelated files,
rejects an unmanaged executable or symlink at the destination, and checks for an
earlier `sdlc` on PATH. On Windows, close any running `sdlc` before reinstalling.

The installed executable needs no Go runtime. Its version includes the beta release,
the Git revision, a dirty-source marker when applicable, and the host OS and
architecture. Local Homebrew installation is supported below; public release
archives and a published tap remain future work.

Use `sdlc version --details` for running, installed, selected-source and recorded
runtime evidence. `sdlc onboard status` describes configuration needed by the
current project without executing checks or connecting accounts. The opt-in
`sdlc shell` provides slash commands over these operations; see the
[interactive shell guide](interactive-shell.md).

Onboarding starts with observed problems, missing setup and the next useful
action, followed by the same eight-step walkthrough on each run. Local settings
are validated, and work references count only when numbered tickets are found.
Invalid or unreadable evidence is shown separately from missing evidence.
Configured settings and recorded images do not establish execution readiness:
provider access, GitHub access, signing, checks and Docker availability are not
checked by onboarding. The terminal bridge is optional; an external controller
terminal remains supported.

Onboarding, dashboard, version details and usage share plain section headings
and explicit status labels. Dashboard separates human input, stopped problems
and review while keeping its attention order and paging. Version details flag
only confirmed identity differences. Usage separates recorded measurements
from missing telemetry and capacity observations; unknown values are not zero.
These cues also work in redirected output without colour.

### Local Homebrew installation

On macOS or Linux, Homebrew can own the CLI through a local tap. No GitHub tap
repository or public release is needed. Install Homebrew, Python 3 and Git, put
Homebrew's `bin` directory first on PATH, then run from a clean, committed clone:

```sh
python3 scripts/install_homebrew.py --dry-run
python3 scripts/install_homebrew.py
```

The command creates `local/sdlc`, packages `HEAD` with `git archive`, records its
checksum in the formula, and runs `brew install local/sdlc/sdlc`. Homebrew installs
Go as a build dependency, the native CLI into its Cellar, and public source under
the keg's `libexec/source`. The new CLI then prepares Docker with the snapshot's
pins. No account login occurs during installation. `--cli-only` keeps the runtime;
`--dependencies` refreshes public runtime dependencies.

An existing native CLI can be migrated only when its private installation receipt
matches the executable. The new package is built and checked before replacing the
old command with Homebrew's link. The previous binary is retained under private
state in `homebrew/backups`. A failed build or link leaves the native command in
place. A matching keg left by a failed link is reused on retry; a different keg
requires `brew uninstall local/sdlc/sdlc` before rerunning the installer.

The local formula and archives remain on this host. Generated formula paths stay
outside this repository. Archives and the source checkout locator live in private
SDLC state; preserve it while using the tap. Homebrew owns the CLI and bundled
sources; account volumes, instructions, run history and the selected Docker image
remain in their existing locations. `brew uninstall local/sdlc/sdlc` removes the
package without deleting that SDLC state.

After committing new source, update from any directory:

```sh
sdlc update --dry-run
sdlc update
sdlc update --cli-only
```

`sdlc update` refreshes the local snapshot and formula, runs
`brew upgrade local/sdlc/sdlc`, then prepares the runtime with the installed bundle.
`--source` changes the saved checkout; `--pull` first fast-forwards a clean checkout.
`--bin-dir` is unavailable because Homebrew controls the destination. The shell's
`/update` uses the same flow. Reopen existing SDLC shells after upgrading.

Homebrew compares the application version and formula revision. SDLC advances
the formula revision when the snapshot changes, including an amended or signed
commit with the same application version. Editing a checkout alone does not
refresh the formula. Direct `brew upgrade local/sdlc/sdlc` installs the last
prepared snapshot and keeps Docker unchanged; `brew reinstall local/sdlc/sdlc`
rebuilds that snapshot. Use `sdlc update` for the full checkout-to-runtime flow.

CLI upgrade and runtime selection are separate operations. A runtime failure
leaves the upgraded CLI installed and reports the partial result; fix the reported
problem and retry `sdlc update`. Saved work continues to block runtime replacement.
`sdlc update` prints one source/snapshot header and ends with an update outcome.
If saved work blocks replacement after Homebrew installs the CLI, the outcome
shows the CLI installed, the runtime kept, the blocking run and
`Next: sdlc update --cli-only`. The command still exits with a failure status;
complete the saved work before retrying a full update. Terminal failures use a
red heading and an accented next action. Redirected output and `NO_COLOR` keep
plain text.

`--cli-only` remains available while work is paused. Runtime build/update commands
discover the running package's current bundle, so Homebrew cleanup of older kegs
does not leave them dependent on a removed source path. `version --details` checks
the package checksum and reports its source revision outside a Git checkout.

## Initialize a project

Run from a project repository or any directory inside it:

```sh
cd /PATH/TO/YOUR_PROJECT
sdlc init
```

Initialization discovers the Git root, current branch and HEAD, local changes,
sanitized remote identities, project manifests and ticket paths under
`.sdlc/work/<reference>/tickets/`. It supports new repositories without commits
and detached HEAD. It reports missing inputs without reading ticket bodies or
deciding ticket readiness and blockers.
Source-state inspection avoids Git content filters. It reports possible changes
when file metadata differs or cannot be compared, even if contents are unchanged.

The command creates `.sdlc/work/` and, when needed, adds `/.sdlc/work/` to Git's
local `info/exclude` file. It resolves that file through Git, including in linked
worktrees, and preserves existing exclude rules. Private process inputs and run
output under this directory must be ignored and untracked. Initialization fails
if they or case variants of their paths are tracked or staged, or if repository
ignore rules prevent this protection. Remove private files from tracking deliberately before retrying;
initialization does not change the index or erase Git history.

Portable settings live in `.sdlc/project.json`:

```json
{
  "version": 1,
  "checks": [["go", "test", "./..."], ["go", "vet", "./..."]],
  "input_files": []
}
```

Each check is an argument array. The initial checks are suggestions based on
detected project files; review and edit them for the project's requirements.
`input_files` starts empty. During a run, these exact relative files are copied
only into the separate credential-free check worker. Use this for reviewed test
configuration; it does not supply requirements to the provider. Files named
explicitly by `--input` are provider-visible, even if also in `input_files`.
Files already tracked in source remain visible as normal source. Keep credentials, account details, vault references
and host paths out of these settings. This file can be committed if its contents
are suitable for the project repository.

For Compose checks that require an ignored root `.env`, explicitly configure
`"input_files": [".env"]` and use `sdlc run --docker-tests`. SDLC freezes the file
at capture and copies it to the check workspace; it does not export its contents
as process environment variables. Compose's `--env-file` selects interpolation
input, while service `env_file` supplies container environment. See the
[tested .NET Compose example](../examples/dotnet-smoke/README.md). Keep the `.env`
itself ignored and untracked and use settings for disposable test services.

Repeated initialization validates and preserves existing settings, including
custom checks, rather than replacing them with newly detected defaults. It
rejects unsupported settings versions, invalid paths and unsafe filesystem links.
No project checks run during initialization. It needs only local Git access;
it does not fetch source, contact providers, bind an account profile or require
Docker. `init` takes no arguments and remembers a unique GitHub origin identity locally,
including SSH remote metadata. After initialization, use `sdlc github list` to inspect
configured profiles and `sdlc github use` to check access and save repository selection outside the
checkout; account setup does not write Git configuration. `sdlc run` captures source and starts ticket execution after GitHub and
signing preflight. `sdlc interactive` still opens an empty workspace after initialization.

If a later setup step fails, earlier completed steps can remain. Fix the reported
problem and rerun initialization; existing settings and exclude rules are retained.

## Inspect a ticket stream

List local references, then the ticket filenames for a chosen reference:

```sh
sdlc references
sdlc tickets "Example stream"
```

Both commands accept `--json`. They use the same metadata-only discovery and
filesystem checks as `sdlc work --references` and `sdlc work --reference`.
For a reference beginning with `-`, use `sdlc tickets -- -draft`; place any
`--json` option before the `--` delimiter.

Run from the project repository or a directory inside it:

```sh
sdlc work --reference YOUR_WORK_REFERENCE
```

The command lists numbered Markdown files directly inside
`.sdlc/work/YOUR_WORK_REFERENCE/tickets/`, ordered by their numeric prefix.
Use names such as `01-add-api.md` and `02-add-consumer.md`. Padding is recommended;
the command also accepts unpadded numbers and does not require consecutive numbers.
Other files, including unnumbered Markdown notes, are ignored.

The reference is the exact work folder name, including its case. Quote it if it
contains spaces. It must be a single directory name, without path separators or
control characters. Other characters, including a colon where the host filesystem
permits it, are treated literally. A missing folder, no numbered tickets,
malformed numbered Markdown filenames or duplicate numeric prefixes cause a
nonzero exit. For example, `01-add-api.md` and `001-add-consumer.md` have the same
prefix value.

Private work must already be ignored and untracked; run `sdlc init` to establish
the local exclude rule. Unsafe filesystem links, tracked private work and
selected tickets that Git does not ignore also cause a failure.

Discovery reads filenames and local Git metadata only. It does not read ticket
bodies, project settings or specifications, check statuses or blockers, or run
project commands. It writes no state and needs no Docker or provider login.
The listing describes filename order; it does not approve or launch tickets.
Use `sdlc run` to select a ticket; its implementation skill checks ticket
eligibility, dependencies and missing requirements when encountered.

## Run a feature

Run every pending numbered ticket under one work reference:

```sh
sdlc run --reference YOUR_WORK_REFERENCE --all --parallel 2 --dry-run
sdlc run --reference YOUR_WORK_REFERENCE --all --parallel 2 --watch
```

`--all` replaces `--ticket`. It owns ticket selection and generated branches, so
it cannot be combined with `--ticket`, `--branch`, `--resume` or `--answer-file`.
The existing provider, input, base, repository, model, check and notification
options apply to feature runs. Review project checks and account pairing before
execution. A new feature requires a clean checkout at the current published
integration revision (`main` by default, or the selected `--base`).

| Feature flag | Default and purpose |
| --- | --- |
| `--all` | Schedule the work folder's pending tickets. |
| `--parallel` | `1`; maximum concurrent ticket controllers, from `1` to `8`. |
| `--watch` | Keep the foreground controller observing human merges and scheduling or reconciling remaining work. |
| `--alternate-providers` | Alternate implementation providers in planned ticket order, starting with `--provider` (default `codex`); each ticket uses the opposite reviewer. Explicit model overrides cannot be combined with this flag. |
| `--dry-run` | Show the offline schedule and validate linked requirements without reading credentials or making model calls. |

Without `--watch`, the controller runs all currently unblocked work and exits;
repeat the command after merging or resolving a stop. `--watch` requires a live
host process and does not provide detached supervision. The parallel limit
bounds ticket controllers: operations sharing a provider's authentication cache
can still queue. Host stream lines carry ticket labels; each private native event
file retains its original bytes. The dashboard continues to show individual ticket runs.

### Private feature plan

Numeric filename discovery remains the same as `sdlc work`. Optional scheduling
metadata lives in the ignored `.sdlc/work/YOUR_WORK_REFERENCE/plan.json`:

```json
{
  "version": 1,
  "tickets": {
    "01-add-api.md": {"priority": 0, "depends_on": [], "touches": ["src/api"]},
    "02-add-tests.md": {"priority": 1, "depends_on": ["01-add-api.md"], "touches": ["tests"]}
  }
}
```

Keys and `depends_on` entries are exact discovered filenames. Lower priority
numbers run first among tickets whose dependencies allow them to start; equal or
omitted priorities use numeric filename order.
Omitted dependencies and touches are empty. `touches` contains relative path
prefixes: overlapping prefixes serialize those tickets. Declare known overlap;
the controller does not infer it from ticket prose. Missing dependencies,
cycles, unknown tickets and unsafe paths stop planning. Without a plan, tickets
are independent and follow numeric filename order.

A ticket with one dependency can start when its parent has a signed, tested and
independently reviewed draft PR in `ready`. The child's source and PR base use
that parent's recorded branch/head. A ticket with multiple parents waits until
all parents merge into the integration branch, then starts from that branch.
SDLC never merges or approves PRs automatically.

### Continue and reconcile a feature

Each work reference has one durable private feature checkpoint. Repeating the
feature command continues recorded work and adopts compatible existing ticket
runs instead of creating duplicates; ambiguous or incompatible runs stop for
attention. The feature freezes its plan, models, accounts, runtime, common
inputs and checks. Restore changed inputs or use a new reference; rerunning is
not a way to replace those choices.

After a parent squash merge or integration-base movement, the controller
restacks only the ticket's own commits. It checks the recorded PR head and owned
branch and uses an exact push lease. It re-signs, reruns isolated checks and CI,
and obtains a fresh opposite-provider review. Conflicts return to the original
native implementation session; product decisions require a human answer.
Unexpected remote changes stop reconciliation.

Human attention stops feature execution. Use the recorded individual ticket
resume command, adding `--answer-file` for a human question, then repeat the
feature command. Keep feature checkpoints and native sessions private and
intact. Offline tests cover scheduling and reconciliation; live provider,
GitHub and signing behaviour for a complete feature still needs a connected
trial in an authorised disposable repository.

## Run one ticket

Run from an initialized project with an existing HEAD commit and a local base
branch. Review `.sdlc/project.json` checks first. The command captures current
source, including local changes, in private run storage without changing the
initiating checkout. Capture currently requires a real `.git` directory and
rejects symlinks, submodules and unsafe Git metadata.

```sh
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-add-api.md --dry-run
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-add-api.md \
  --input .sdlc/work/YOUR_WORK_REFERENCE/specification.md \
  --input .sdlc/work/YOUR_WORK_REFERENCE/decisions.md
```

The ticket is an exact numbered filename from `sdlc work`. Run preflight includes
Markdown requirements linked from the selected ticket and documents within its
`.sdlc/work/REFERENCE/` folder, including specifications, decisions and related
tickets. It follows their local links, removes duplicates and rejects missing or
unsafe targets before launching. External URLs, images and code examples do not
select files. It does not copy the whole work folder or launch linked tickets.
Use repeatable `--input` for additional exact project-relative requirements.
`--dry-run` reads the selected Markdown to validate links and prints paths without
document bodies, Docker, authentication checks or execution; their
content hashes remain empty until source capture. A resumed run's plan shows its
recorded paths and hashes. The offline plan cannot prove account or model access. A missing pair produces
a warning with the requested or unresolved profile; malformed or changed pair
metadata and ambiguous selection fail even during dry-run.

| Flag | Default and purpose |
| --- | --- |
| `--reference`, `--ticket` | Required exact work folder and numbered ticket. |
| `--provider` | `codex`; use `claude` to reverse the implementation/review roles. |
| `--input` | Repeatable exact path relative to the project; visible to both providers. |
| `--base` | `main`; must exist locally, be included in source HEAD, and match the GitHub base at publication. |
| `--branch` | Unique `work/<reference>/<ticket-stem>-<run-prefix>` branch. |
| `--repo` | GitHub.com `OWNER/REPO`; defaults to saved selection, then initialized identity, then legacy origin discovery. Supply it when no identity can be inferred. |
| `--github-profile` | Select a registered account/key pair for an unbound repository; omission uses saved repository selection or a unique owner pair or sole configured pair. A conflicting saved selection fails. |
| `--model`, `--effort` | Override the implementation lead's model and effort. |
| `--review-model`, `--review-effort` | Override the opposite provider's review lead. |
| `--docker-tests` | Enable the separate privileged integration-test daemon. |
| `--timeout` | `2h` per controller invocation; accepts `1m` through `24h`. |
| `--resume` | Continue a recorded run ID with its original settings. |
| `--answer-file` | Bounded nonempty UTF-8 human answer, only with `--resume`. |
| `--dry-run` | Print an offline plan. |

Before execution, configure the selected [GitHub and signing profile](#github-login-and-signing).
SDLC freezes its account ID, repository ID/canonical name, effective project Git
name/email, signing public key and runtime image. A changed identity or missing
push permission or public signing-key registration stops the run. It uses no host GitHub credential fallback.
Use `github use` before launch; omit `--profile` for a saved selection or sole configured
profile. Multiple profiles require a choice. Saved selection binds the canonical
checkout root and GitHub repository independently of later origin changes. Changed pair
metadata requires a deliberate new `github use --profile NAME` selection.

The implementation login is required before launch. A missing opposite-provider
login allows implementation, local checks, draft PR publication and CI to finish,
then retains `awaiting_reviewer`. Login and resume to complete independent review.
Account-authenticated runs support the account owner's local single-user job;
CI execution is rejected. Review the [provider rules](provider-usage.md) first.

### Models and pinned agents

These are requested lead settings, not a guarantee of account entitlement:

| Selected implementer | Implementation | Independent review |
| --- | --- | --- |
| Codex (default) | `gpt-6.1-sol`, `medium` | Claude `claude-opus-5-5`, `high` |
| Claude | `claude-sonnet-5-5`, `medium` | Codex `gpt-6.1-sol`, `high` |

For example:

```sh
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-add-api.md \
  --provider codex --model gpt-6.1-sol --effort medium \
  --review-model claude-opus-5-5 --review-effort high
```

An optional private `models.json` beside the installation's runtime record can
replace defaults. The state directory is `SDLC_STATE_DIR` when set, otherwise
the OS user configuration directory followed by `sdlc` (on macOS,
`~/Library/Application Support/sdlc`). Use full model identifiers, with selected implementer and
opposite reviewer providers:

```json
{
  "version": 1,
  "codex": {
    "implementation": {"provider": "codex", "model": "gpt-6.1-sol", "effort": "medium"},
    "review": {"provider": "claude", "model": "claude-opus-5-5", "effort": "high"}
  },
  "claude": {
    "implementation": {"provider": "claude", "model": "claude-sonnet-5-5", "effort": "medium"},
    "review": {"provider": "codex", "model": "gpt-6.1-sol", "effort": "high"}
  }
}
```

Flags override that file for new runs. Resume preserves the recorded models,
effort, runtime image, inputs, checks and shared instructions. Accepted Codex
efforts are `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, `ultra`;
Claude accepts `low`, `medium`, `high`, `xhigh`, `max`. Acceptance by SDLC does not
establish support by a model or account. A reported model mismatch or change
stops the run; if the client reports no model, SDLC cannot verify it. Providers
may cap or interpret effort differently.

The lead follows the pinned engineering and PR skills. The native clients load
the image's pinned agent definitions and model/effort policies for
`read_low`, `read_medium`, `read_high` and `write_medium`; lead flags do not
replace those agent policies. See [Codex subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents),
[Claude subagents](https://code.claude.com/docs/en/sub-agents) and
[Claude model configuration](https://code.claude.com/docs/en/model-config).

### Context use

Each run covers one ticket. Fresh sessions receive the launch brief; implementation
repairs receive only the ticket identity, current feedback and execution gates.
Shared instructions are supplied through native instruction files without
duplicating their body in prompts. Leads are told to delegate narrow investigations,
request concise evidence and leave large logs and inventories on disk. Reviewers
start fresh each round. Native compaction retains the implementation session.

Native usage events are retained in private `events-*.jsonl` files and streamed
to the host. They are not a normalized context gauge. The pinned
[Codex JSON event processor](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/exec/src/event_processor_with_jsonl_output.rs)
reports aggregate usage without current context occupancy or its window size;
do not sum those counters across resumes or turn them into a context percentage.
Claude may report per-request assistant usage: its latest main-session input
size includes input, cache-read and cache-creation tokens, following the
[documented context calculation](https://code.claude.com/docs/en/statusline).
Aggregate result usage and subagent usage are separate. Missing metadata is
unknown. A context percentage requires an actual matching model window size;
SDLC does not currently display such a gauge or trigger compaction thresholds.

### Checks, publication and review

The implementation requests checks on a clean committed candidate. SDLC executes
`.sdlc/project.json` argument arrays in order in a separate disposable worker,
without a host shell. Configure dependency installation as a check if required.
No commands are guessed at run time. For .NET 10 `.slnx` repositories, choose
commands that match the repository's `global.json`, SDK and VSTest or Microsoft
Testing Platform configuration. Initialization does not generate .NET checks.

Configured `input_files` receive a separate copy in the check workspace. Their
untracked files stay out of the provider workspace unless selected with
`--input`; files already tracked in source remain visible as normal source. Keep credentials
out of these files; SDLC's path checks do not identify every secret in file
contents. Check workers receive no provider cache, signing keys, GitHub token,
SSH agent or host Docker socket. They have network access for dependencies.
`--docker-tests` explicitly starts a separate privileged Docker-in-Docker daemon,
job network and disposable volumes. This mode is for trusted integration checks;
privileged containers do not isolate hostile code from the engine host. Cleanup
errors stop delivery.

The provider has shell access to its authenticated workspace. Its instructions
prohibit dependency installation, scripts and tests there, but that is not an
enforced execution boundary: repository code can still read its provider cache
or send accessible data over the network. Review the [remaining risks](../README.md#security-boundary-and-risks).

After passing checks and the implementation's whole-ticket local review, the
host controller starts the trusted publisher baked into the runtime image. It
imports the candidate bundle into controlled Git metadata without running project
code. It requires check evidence for the recorded candidate head and tree,
recreates every delivered commit with the frozen Git identity and dedicated
Ed25519 key, verifies signatures against the approved public key, and confirms
the signed tree matches the checked tree. Account/repository IDs, canonical
repository name, push permission, base and expected remote head are checked
before publication; an exact push lease rejects competing branch changes.

The publisher receives the selected GitHub profile volume read-only and the
resolved signing key through stdin into private tmpfs. It removes the key file
before push. The separate official 1Password CLI container receives its bootstrap
token over stdin, has a 35-second internal timeout and is removed after use.
Neither container receives provider caches, host GitHub configuration, a desktop
SSH agent or a Docker socket. No GitHub App installation is required for this
native CLI route. See the [Docker GitHub test guide](github-docker-test.md).
Supervised private trials have passed provider execution, vault retrieval,
signed GitHub draft publication, CI and opposite-provider review/repair. New
account/key pairing and later dashboard changes need separate validation.

Every reported PR check must pass on the recorded current base/head. Pending
checks are polled; failures, cancellation and skipping require repair. When no
checks are reported, SDLC waits for up to two minutes for CI to start, then blocks
the run with its checkpoint retained. Configure CI and resume the run. SDLC
rechecks the PR boundary around CI inspection and independent review. External base/head changes stop for reconciliation.

A fresh opposite-provider session reviews the exact published revision and
selected requirements, without the implementation transcript. Actionable findings
return to the original implementation session for bounded repairs, checks,
publication, CI and another fresh review. The normal repair bound is three rounds.
A complete review with no findings reaches `ready` and prints the PR URL. The PR
remains draft; SDLC never approves, marks it ready or merges automatically.

### Logs, questions and resume

Every executing controller registers its private run directory in the installation's
`runs/` registry. The registry contains paths and run identities, without ticket
bodies or transcripts. Each run retains `activity.json` alongside its journal:
a controller heartbeat every five seconds, last streamed activity, current role
and requested model, and a stopped marker. Heartbeats are separate from journal
updates; a quiet provider session can still be alive. Activity older than twenty
seconds without a stopped marker is stale, rather than proof of a failed ticket.
Stop reasons and the latest CI result are saved in the journal for later inspection.
Resuming an older run registers it when the controller starts. Dry runs create
neither registry entries nor heartbeats. These records are private local state.

Native structured JSONL streams to the terminal and is retained privately under:

```text
.sdlc/work/<reference>/runs/<ticket-stem>/<run-id>/
  journal.json
  events-<attempt>.jsonl
  diagnostics-<attempt>.log
  checks-<attempt>.log
  native-implementation/
  native-review-<attempt>/
  workspace/
```

Logs, snapshots and native sessions may contain private source, prompts and
account data. Keep the whole work directory ignored and untracked; do not publish
terminal recordings. Supervised trials have exercised original-session resume
with both implementation providers; that does not guarantee recovery from every
host, client or container failure.

A structured human question pauses at `waiting_for_human`. Supply an answer:

```sh
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-add-api.md \
  --resume RECORDED_RUN_ID --answer-file /PATH/TO/PRIVATE_ANSWER.txt
```

For reviewer login or an operational stop, resume without an answer file:

```sh
sdlc auth login --provider claude
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-add-api.md --resume RECORDED_RUN_ID
```

`run --resume` resolves a prefix within the selected reference and ticket's
retained checkpoint directory, including runs forgotten from the dashboard.

Resume retains the recorded GitHub profile, account/repository IDs and signing
identity; it does not switch accounts or signing configurations. Existing frozen
journals retain their legacy same-name signing route.

Resume accepts only `--reference`, `--ticket`, `--resume`, `--answer-file`,
`--timeout` and `--dry-run`. Implementation turns and repairs resume the exact
recorded native session, never the latest unrelated session. Each independent
review starts fresh. Cancellation, timeout, invalid handoffs, missing inputs,
usage/access limits and policy refusals retain a stopped checkpoint. Fix the
reported cause before resuming; never restart or change identities to evade
provider restrictions. Journals and native storage must remain intact for resume.

### Missing requirement files

An older run may lack a linked requirement that was present on the host. A text
answer cannot copy that file into its private Docker workspace. Inspect and
attach requirements without starting a provider:

```sh
sdlc inputs --run RUN_ID
sdlc inputs --run RUN_ID --add .sdlc/work/REFERENCE/specification.md --dry-run
sdlc inputs --run RUN_ID --add .sdlc/work/REFERENCE/specification.md
sdlc answer --run RUN_ID
```

`--add` is repeatable. The preview shows selected paths and hashes; attachment
copies the validated files, records an audit entry and leaves the run paused.
Existing captured bytes, native session, account, models and branch stay fixed.
Linked requirements from the added document are included through the
same preflight as a new launch. Conflicting destinations, unsafe paths and
changes to existing inputs are refused. Only stopped, unpublished implementation
questions support attachment; review or published work requires a separate
reimplementation decision. Current check evidence is invalidated when requirements
are added. Other tickets in a feature do not receive the attachment.

If attachment is interrupted, `inputs` shows the pending operation. Complete it
before answering or resuming; the controller refuses a partial input manifest.
Use `--json` for structured input metadata without file bodies.

## Shared agent instructions

Inspect or change the installation's shared instructions from any directory:

```sh
sdlc instructions show
sdlc instructions set --file /PATH/TO/PRIVATE_INSTRUCTIONS.md
sdlc instructions reset
```

The default contains only this rule:

> If you have any question for a human, stop implementation immediately and report it.
> Do not assume an answer or continue implementation until a human has answered.

`set` copies additional Markdown instructions into private installation state.
They follow the human-answer rule, which remains present. Editing the source file
afterwards has no effect until you run `set` again. An empty file adds nothing;
`reset` removes the additions. `show` prints the complete shared body, so keep its
output private. Custom instructions must be UTF-8 text, at most 16 KiB, without
terminal control characters.

Settings live in `instructions.md` beside the runtime record. Reinstalling the CLI
preserves them. Changing them requires no Docker build and affects every project
using this installation. Personal instructions stay outside the shared image and
this public source tree.

No shared instruction file is injected by the current login or status commands.
The image contains catalogue-source `AGENTS.md` files under `/opt/sdlc/catalogues`;
these are outside the worker's instruction-discovery path. The SDLC repository's
root `AGENTS.md` is excluded from the Docker build context.

Interactive sessions receive a read-only snapshot of this shared body at the
providers' native global instruction paths: `$CODEX_HOME/AGENTS.md` for Codex and
`$CLAUDE_CONFIG_DIR/CLAUDE.md` for Claude (normally `~/.claude/CLAUDE.md`).
See [Codex instruction discovery](https://learn.chatgpt.com/docs/agent-configuration/agents-md)
and [Claude memory files](https://code.claude.com/docs/en/memory).
Changes to the settings affect the next session or new run. Ticket runs capture
the shared body once and inject it alongside project instructions on every turn.
The controller pauses on a structured human question; Markdown instructions alone
cannot guarantee that a provider stops before returning its handoff.

## Watch local runs

Run selectors accept a full ID or a unique lowercase hexadecimal prefix of at
least three characters. This applies to dashboard, answer, resume, inputs, usage
and progress commands. Ambiguous prefixes list the matching full IDs; use more
characters to select one. Commands retain the full ID after selection and keep
their existing project or installation scope. Explicit `usage --run` selection
ignores the age filter.

```sh
sdlc dashboard
sdlc dashboard --once
sdlc dashboard --json
sdlc dashboard --page 2
sdlc dashboard --notify desktop --sound
sdlc dashboard --run RECORDED_RUN_ID --logs
sdlc progress --run RECORDED_RUN_ID
sdlc progress --run RECORDED_RUN_ID --follow
sdlc progress --launch-id RECORDED_LAUNCH_ID --follow
sdlc progress --run RECORDED_RUN_ID --once --json
sdlc dashboard forget --run RECORDED_RUN_ID
```

`progress` appends labelled SDLC steps, check output and native agent output as
it arrives. Labels include the run, provider and role. In a terminal it follows
until the controller stops; redirected output takes one recent snapshot unless
`--follow` is supplied. Ctrl+C closes the view and leaves the controller running.
`--launch-id` waits for a dispatched terminal and follows its registered ticket
runs, including resumed controllers. `--scope project` restricts selection to
the current checkout; the default is installation-wide.

For launch receipts, `launch status --id` and `progress --launch-id` accept a
full UUID or a unique prefix of at least three characters. Progress cursors bind
to the resolved full ID. Internal launch execution and receipt updates require
the full UUID.

JSON output consists of batches with `events`, `cursor` and `done`. Pass the
opaque cursor back through `--cursor` to read only new output. `--follow --json`
emits newline-delimited batches; `--once --json` returns one batch. The CLI
bounds aggregate batches across feature runs and retains unread records for the
next request. The initial tail is bounded to recent output. Provider events and
full check logs remain private and unchanged. Older runs use their existing
native/check logs with the same producer labels. Pending questions include the
answer command. A completed feed means its controller has stopped; check the
reported run state for ready, failed or waiting for human input.

The shell consumes these same CLI batches after Start or resume, and through
`/progress --run ID`. `/dashboard --run ID --logs` selects the feed in the shell;
the external dashboard retains its bounded diagnostic snapshot.

The dashboard reads the installation's private run registry across repositories.
Viewing needs neither Docker nor provider login and does not resume or stop a
run. Human questions appear first, then failures, unavailable state, stale or
interrupted controllers, missing reviewer login, queued/running work and finally
completed draft PRs ready for human review. Within each group, newer updates come
first. Every overview, including JSON, contains at most ten runs; `--page N` pages
through the complete ordered history. JSON also reports page, page size, total
and page count. Page numbers clamp when the history shrinks. A fresh heartbeat
means the controller is live; elapsed time and
stage do not estimate percentage completion. Long sessions can be quiet without
being stale. Missing or corrupt records remain visible as unavailable.

In a terminal, the view refreshes every two seconds. Redirected output defaults to
one snapshot. Use `--watch` to append snapshots to redirected output, `--once` for
one terminal snapshot, or `--json` for one structured snapshot. In a live terminal,
press `n` for the next page, `p` for the previous page, or `q` to close.
Scroll using the wheel, arrows or Page Up/Down. Click the output (or press F2/Space)
to freeze the visible text, then drag and use your terminal copy shortcut. Escape
or F2 resumes with the latest status. Set `--interval` between `250ms` and `1m`. Details include
questions, findings, check evidence, PR links and the existing resume command. Optional `--logs` reads a
bounded tail from that selected run's private output; it requires `--run` and
cannot combine with JSON. Terminal control characters in displayed content are
removed. JSON contains status and usage projections, without journal prompts,
instructions or transcripts.

Native usage is optional. Codex reports aggregate counters; these cannot establish
current context occupancy. Claude main-message input, cache-read and cache-creation
counters can describe the latest reported request context. The dashboard shows a
percentage only when that client actually reports a positive context window for
the matching model. Missing metrics stay unknown. It ignores subagent messages,
replaces repeated counters and resets measurements at a new session attempt; it
never sums those live aggregate snapshots across resumes. Separate
[durable metrics](usage-metrics.md) retain attempt coverage and reconcile cumulative
native totals. Read them with `sdlc usage --since 7d --json`; dashboard details and
JSON also expose them. The pinned clients may omit fields.

Ticket and feature runs can select `--headroom passthrough` or `--headroom optimize`
after `sdlc runtime headroom build`. The [Headroom guide](headroom.md) explains its
separate image, native forwarding route, frozen resume settings and comparison
criteria. Direct execution is the default.

Closing the dashboard leaves controllers working. `sdlc run` remains a foreground
process; its original terminal must stay open. Headless means no provider terminal
UI, rather than detached execution. Resume older runs to register them; dry runs
do not register. The registry and logs can contain private repository paths and
work details, so keep dashboard output private too.

Runs have no automatic expiry. `dashboard forget --run RUN_ID` (also `remove`)
removes one stopped run's registry entry while keeping its journal, logs, inputs,
workspace and lock files. It checks the actual controller lock; a stale heartbeat
alone is insufficient. Resuming re-registers the retained run. Corrupt records or
missing run directories must be repaired before removal. Removal and report
export currently fail closed on Windows because Unix ownership checks do not
establish private Windows file ownership.

Use `sdlc dashboard export --run RUN_ID --to PRIVATE_DIRECTORY` before forgetting
a run to keep a portable checkpoint report outside Git checkouts. The destination
must already exist with mode `0700`; reports have mode `0600` and cannot overwrite
an existing export. See [run history and retention](run-history.md) for the report
contents, full artifact inventory and backup guidance.

Optional `--notify desktop [--sound]` sends fixed local macOS notifications for
human questions, blocked/failed/interrupted runs, missing reviewer login and new
completions. A watching dashboard also detects unavailable records and stale
heartbeats, including runs on another page or outside a selected detail view.
`--notify bell` rings the terminal bell and requires a terminal. Notifications
default to `off`; desktop delivery can be suppressed by macOS notification or
Focus settings. Sound requires desktop mode. No repository names, ticket text,
questions, paths or logs enter notification messages.

For alerts while the dashboard is closed, add the same `--notify desktop --sound`
flags to `sdlc run`, including resume. This host preference does not change frozen
execution settings. Each controller/watcher deduplicates unchanged attention
states and keeps existing completed history quiet at startup. Starting another
watcher can repeat current attention alerts; enabling both run and dashboard
alerts can produce duplicates. Notification delivery failures do not stop runs.
A controller cannot report its own sudden death; leave a watching dashboard open
for stale-heartbeat alerts. No email, webhook or remote notification service is
contacted.

## Concurrent runs and account caches

Multiple controllers can operate in the same repository using separate captured
workspaces and branches. Codex and Claude operations can overlap. Operations
using the same provider's account cache wait for one another, including login and
offline status checks; native refresh and cleanup finish before the next user of
that cache starts. This permits at most one native operation per provider per
installation. Waiting is cancellable and makes no FIFO or fairness guarantee.
`sdlc run --timeout` includes setup and queued waits. Authentication is checked
inside the registered controller before implementation, so queued runs remain
visible in the dashboard. Reviewer authentication is checked at the review stage.

GitHub profiles such as `personal` and `work` use separate native caches and
leases while sharing the runtime image. Authentication and publication serialize
within one profile; separate profiles can coexist. Codex and Claude still each
have one account cache per installation. Profile selection does not authorise
switching identities to evade limits or access denials.

Provider and GitHub operations hold a shared runtime lease. Runtime builds need exclusive
ownership and retain their existing immediate failure when the runtime is in use.
A provider waiting behind a build checks runtime identity after acquiring its
lease; an existing run still rejects an image that differs from its recorded image.
Installation identity creation has a separate short lock. If Docker reports any
surviving container using the selected provider volume, SDLC blocks reuse. Inspect
and stop/remove that container deliberately before retrying; lock files alone
cannot establish whether a Docker container survived a controller interruption.
External Docker operations remain outside SDLC's coordination.

Each `--docker-tests` check invocation receives a distinct daemon and network
namespace without publishing ports on the host. Separate invocations can use the
same API or database ports inside their own daemons. Commands within one invocation
share its daemon and can still collide. Shared external services, host resources,
provider usage limits and Git merge conflicts remain shared concerns.

## Install and update the CLI and runtime

The source installer prepares the CLI and runtime together:

```sh
go run ./cmd/sdlc-install --bin-dir /PATH/TO/YOUR_BIN_DIRECTORY
```

It validates a native CLI candidate, runs that candidate to build the runtime with
the checkout's pins, then atomically replaces the managed executable on PATH.
Build or runtime preparation failure keeps the old host executable. Runtime and
host installation are separate filesystem/Docker operations; if replacement fails
after runtime selection, the error reports the partial outcome. `--cli-only`
skips runtime preparation; `--dependencies` selects newer public dependencies.

Subsequent updates can run from any directory:

```sh
sdlc update --dry-run
sdlc update
sdlc update --pull
sdlc update --dependencies
sdlc update --cli-only
```

For native installations, `update` runs the selected checkout's installer, using
its current code to build both components. The private installation receipt remembers canonical source and
destination paths and records the installed executable's identity and SHA-256.
Older installations fall back to the source in `runtime.json`. `--source` and
`--bin-dir` select replacements when those locations move. An arbitrary PATH
executable is never run to discover its version or install location.

`--pull` requires a clean checkout on a branch and uses `git pull --ff-only`.
Without it, current local changes are built. The dry-run validates local source
and destination, makes no builds or writes, and contacts no remote. A `--pull`
preview describes current local source and prints the pending fast-forward step.
Homebrew packages committed snapshots; its installation and update flow is
described [above](#local-homebrew-installation).

The default uses source pins and clears previously selected private dependency
overrides after a successful runtime build. `--dependencies` instead selects the
newer exact public versions supported by `runtime update`. `--cli-only` updates
the host command while retaining the runtime. These options also work with
`/update` in the shell. Reopen existing shells after installation.

Resumable runs and incomplete feature series in known project roots block runtime
replacement under the build lock. Ready standalone tickets and fully merged
features permit it. The error identifies the saved work and offers `--cli-only`.
Checks cover registered, catalogued, current and source roots, including forgotten
runs in those roots. An external root removed from every locator cannot be found;
keep projects registered while they contain unfinished work.

### Where version pins live

| Selection | Stored in | How it changes |
| --- | --- | --- |
| SDLC application version | `internal/buildinfo/version.go` and changelog | A delivered application iteration increments the beta; install builds that source version. |
| Repository runtime baseline | `runtime/Dockerfile`, plus managed image defaults | CI updates Codex, Claude Code, GitHub CLI and catalogue pins weekly or manually; other source pins are maintained explicitly or by their configured dependency automation. |
| Local selected runtime | Private `runtime.json`, immutable image ID and inventory | `runtime update` or `update --dependencies` saves exact private selections. Default `update` or `runtime build --source-pins` returns to the source baseline. |
| Saved run and feature settings | Private checkpoints | Frozen for their work; installation does not migrate them. |

Local updates intentionally leave tracked pipeline pins untouched. They do not
create source edits, commits or pull requests. CI pin changes reach a machine
after updating its source and rebuilding the runtime; reinstalling only the CLI
does not change provider versions. `runtime build` retains private pins for an
ordinary rebuild; `--source-pins` explicitly applies the checkout's defaults.

## Build the shared runtime

The host needs the Docker CLI and a local Docker engine running Linux containers.
Remote Docker engines and Windows containers are outside the current build path.

From the SDLC clone:

```sh
sdlc runtime build --source .
sdlc runtime status
```

The build prepares a private temporary context containing only the allowlisted
runtime assets and the publisher's Go source/import closure and approved embedded
helpers. Symlinks, special files and oversized inputs are rejected. The pinned Go
builder produces a static publisher; only its binary enters the final image.
The Dockerfile retains the dependency and catalogue pins. It supplies no account profile, signing key,
GitHub token or provider login. Skills and agents come from the pinned public
GitHub repositories during the image build.

The command checks Codex, Claude Code, GitHub CLI, .NET and Docker/Compose before
selecting the shared `sdlc:local` image. Failed builds or tool checks
leave the previous shared image and recorded state intact. A build lock prevents
overlapping replacements, and existing containers using the image block a
rebuild. After replacement, the command removes the superseded image.

Once the source clone is recorded, rebuild from any directory with:

```sh
sdlc runtime build
```

Use `--source /PATH/TO/SDLC_CLONE` if the clone moves. Builds are local; CI neither
builds nor publishes the Docker image. The one-shot installer/update performs
both operations; `--cli-only` performs only executable replacement.

If an existing installation uses `sdlc-codex-spike:local`, check that
`sdlc runtime status` succeeds with the old CLI, then reinstall the CLI and rename
the local image before running the updated CLI:

```sh
docker image tag sdlc-codex-spike:local sdlc:local
sdlc runtime status --offline
docker image rm sdlc-codex-spike:local
```

The new tag points to the same image ID, so the runtime record and provider login
volumes remain valid. Remove the old tag only after status succeeds; this keeps
superseded-image cleanup working on the next build. The tag rename itself preserves provider login. Rebuild from the current SDLC
source to add the baked publisher before using the new publication route; the
credential volumes remain separate. See Docker's [image tagging](https://docs.docker.com/reference/cli/docker/image/tag/)
and [image removal](https://docs.docker.com/reference/cli/docker/image/rm/) documentation.

## Inspect the runtime

```sh
sdlc runtime status
sdlc runtime status --all
sdlc runtime status --offline
sdlc runtime status --offline --github-profile personal
```

| Option | Purpose |
| --- | --- |
| `--offline` | Validate the local runtime and show its inventory without upstream update requests. |
| `--all` | List bundled npm dependencies and individual Debian updates instead of summarizing them. |
| `--github-profile NAME` | Inspect this account's registered pair and signing readiness. Omission uses the current checkout's account selection; outside a checkout, choose a profile explicitly. Unpaired or invalid selections report their actual issue. |

Status verifies that the selected Docker engine and shared image match the
recorded build, then checks public upstream metadata for updates. Each image
build saves an inventory of the versions and catalogue commits actually installed
in that image. Status compares this inventory, even if the source clone has since
changed.

The report covers:

- Codex, Claude Code, npm, Yarn if present, and all installed global npm dependencies;
- GitHub CLI, Docker CLI, Compose and Buildx;
- the skills and agents catalogue commits against their `main` branches;
- Node.js within its installed major version, and .NET SDK and runtimes within
  their installed major/minor release channels;
- the pinned Node base image digest against its current tag;
- the Go publisher builder, Docker CLI image, isolated Docker test daemon and
  1Password resolver against their public releases and image digests;
- every installed Debian package against signed Bookworm, Bookworm updates and
  Bookworm security repository candidates.

The default display lists the main tools, catalogue commits and image pins.
Bundled npm packages and Debian updates appear as counts; use `--all` for their
individual results. Most of those supporting packages come from the base image
or the tools' published dependency trees. Their latest versions are diagnostic
information, rather than independent pins that SDLC should force into a tool.

Catalogue ancestry distinguishes newer commits from divergent revisions. A changed
base image digest means its tag changed; it does not establish release ordering.
Unchanged Debian packages are counted; `--all` lists available updates and missing
candidates. These checks cover the runtime and its build and sidecar
images. They do not check
host tools, project dependencies or provider authentication.

The online check has a 45-second limit and uses public metadata from npm, GitHub,
Node.js, .NET, Docker Hub and Debian. The Debian check runs in a disposable
container without provider storage or project mounts. A failed or unavailable
check retains the other results, marks the report incomplete and returns a
nonzero exit code. Updates alone do not cause a nonzero exit code.

Use `--offline` to verify the local runtime and list its inventory without
contacting upstream services. Login and interactive commands also retain their
local runtime checks. Status does not install updates, change pins or rebuild
the image. Preview and apply updates with `sdlc runtime update --dry-run` and
`sdlc runtime update`.

Runtime status also follows the selected GitHub account's registered pair to
show an offline signing summary, including whether configuration and the private bootstrap are present
and safe. This summary hides vault/item references and storage paths and never
retrieves a 1Password key. An unpaired account reports attention. Missing or unsafe signing setup is reported without
changing the runtime image/dependency check's exit result. Use `sdlc signing
status --profile NAME` for a dedicated signing-readiness exit status, or add
`--verify` there when you intend to contact 1Password and test a real signature.

## Update the runtime

After inspecting status, run from any directory:

```sh
sdlc runtime update --dry-run
sdlc runtime update
```

`--dry-run` fetches public release metadata and checks Debian candidates in a
disposable container. It prints the planned versions and build actions without
pulling new images, saving pins or rebuilding. It makes no provider model request
or vault connection. The executing command repeats planning before rebuilding;
it does not apply an earlier preview whose metadata may have changed.

The update covers Codex, Claude Code, GitHub CLI, npm, Yarn, Compose, Buildx, the
skills and agents catalogues, Node, .NET SDK/runtime packs and installed Debian
packages. It also refreshes the Go publisher builder, Docker CLI image, isolated
Docker test daemon and 1Password resolver. Numeric versions never move backwards;
catalogues advance only through a same or descendant `main` revision. Node keeps
its installed major, .NET its installed major/minor, Go its builder major/minor,
and 1Password its v2 channel. Debian stays on Bookworm and its signed update and
security repositories. Docker CLI and plugins use stable releases.

Every selected image has an exact version and verified manifest digest. Compose
and Buildx can advance independently of the versions bundled in Docker's CLI
image; their official release binaries are checked against the published
checksums before installation. Update uses a clean build with `--pull` and
`--no-cache`, upgrades installed Debian packages, and reinstalls the selected
native clients. These build flags refresh different parts of the image, as
described in [Docker's build guidance](https://docs.docker.com/build/building/best-practices/).
With npm 12 or later, the build permits installation scripts only for the exact
pinned Codex and Claude packages, as supported by [npm's global install policy](https://docs.npmjs.com/cli/install/).
It does not enable npm's unrestricted script bypass.
Public image pulls and update builds use a temporary Docker configuration without
host registry credentials or credential helpers. Version and checksum checks
verify the selected artifacts; they cannot establish that a release is free of
malicious code. The host Docker client and Buildx installed alongside it or in
system plugin directories remain trusted host software. Configured plugins from
the host's Docker settings are not inherited by the update build.

The source clone is used to construct the existing allowlisted private build
context. Selected pins alter only that context's Dockerfile. Successful selection
records the pins and exact build recipe in private `runtime.json`; the tracked
source checkout is not edited or committed by this command. Subsequent builds
from the same saved source retain these pins, even if source defaults changed.
Use `runtime build --source-pins` or default `sdlc update` to apply the checkout's
pins. An explicit different source uses its own defaults. Use
`--source /PATH/TO/SDLC_CLONE` if the clone moves.

Missing managed metadata, divergent catalogue history, a failed build, mismatched
installed tool/runtime versions or incomplete/stale Debian candidates stop
selection. The previous shared image and runtime record remain selected.
Existing running controllers hold a shared lease; finish them before updating.
Credential-free version probes do not start the privileged test daemon or log in
to any account. Login volumes and signing profiles keep their existing storage.

Published parent packages govern transitive npm dependencies. Update refreshes
those parents and their dependency resolution, and reports children that remain
behind or unavailable. It does not force arbitrary child versions outside the
parents' declared requirements. The final status report lists remaining updates;
an unavailable post-build metadata check returns a nonzero result even when the
validated new runtime was already selected. Host tools, project packages, model
identifiers and account permissions are outside runtime dependency updates.

Run dependency updates between ticket runs. Saved work still requires its
recorded runtime image; rebuilding is not a migration of a paused session.
New Docker test runs record an exact daemon digest and require that image to be
cached before starting checks. Older saved runs without a daemon reference keep
their legacy default tag and may pull it if absent. That tag can change upstream;
start a new run to use the digest-pinned path.

Update transitive npm dependencies through their parent package or base image;
they are not separate Dockerfile pins. The available versions can include major
releases, so review compatibility when choosing updates.

Images built before dependency inventories were added need one rebuild with the
updated CLI. Online status reports this as incomplete until that rebuild;
`--offline` still verifies an older image. Rebuilding preserves provider login
volumes.

The checks use npm's [package metadata](https://docs.npmjs.com/cli/v11/commands/npm-view/),
GitHub's [stable releases](https://docs.github.com/en/rest/releases/releases#get-the-latest-release)
and [commit comparison](https://docs.github.com/en/rest/commits/commits#compare-two-commits),
Docker's [registry metadata](https://distribution.github.io/distribution/spec/api/),
and Debian's [APT metadata refresh](https://manpages.debian.org/bookworm/apt/apt-get.8.en.html).

State lives under `sdlc` in the OS user configuration directory:

| Host | Default base directory |
| --- | --- |
| macOS | `~/Library/Application Support` |
| Linux | `$XDG_CONFIG_HOME`, or `~/.config` |
| Windows | `%AppData%` |

`SDLC_STATE_DIR` can select another directory. The state records the source clone,
engine and image identity; reinstalling the executable preserves it.

macOS/ARM64 installation, reinstallation and local image build have been checked.
Both commands cross compile for macOS, Linux and Windows on ARM64 and x86-64.
Native Linux and Windows CLI execution still needs verification.

## GitHub login and signing

Authenticate each intended GitHub account through the official CLI's browser/device
flow in an isolated container. Keep the login terminal private:

```sh
sdlc auth login --service github --profile personal
sdlc auth status --service github --profile personal
sdlc auth status --service github --profile personal --verify
sdlc auth logout --service github --profile personal
```

Omit `--profile` for `default`. Names use 1–48 lowercase letters, digits, hyphens
or underscores and start with a letter or digit. Use a separate profile for each
account, for example `personal` and `work`; both share the same runtime image.
The native login uses HTTPS and stores its own `gh` file-backed credentials in a
private managed volume. These are persistent plaintext credentials: SDLC does
not encrypt Docker volumes. Rebuilds and reinstalls preserve them. Host tokens,
host `gh` configuration and another profile's active account cannot override the
selected cache. A profile with stored login refuses another login. Verify it
with `status --verify`; log out before reauthorizing or changing accounts, or
create a separate profile for another account. Logout removes the local native login; it does not revoke remote
authority or delete the volume.

Default status checks private cache storage offline, without proving token
validity or account access. `--verify` explicitly contacts GitHub through native
`gh auth status`, then prints the selected login and numeric account ID. It
never prints a token or raw native diagnostics.
Publication verifies the numeric account and repository IDs and push permission.
It stops on a mismatch rather than switching accounts.

Provision the custom 1Password vault, Read Items Service Account and dedicated
Ed25519 SSH Key item using [1Password signing setup](1password-signing-setup.md).
Then run the wizard in an interactive terminal:

```sh
sdlc signing setup --profile personal-key --provider 1password
sdlc signing status --profile personal-key
sdlc signing status --profile personal-key --verify
sdlc github pair --profile personal --signing-profile personal-key
# From the initialized project:
sdlc github use --profile personal
sdlc github status --verify
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-add-api.md
```

| Setting or flag context | Current meaning |
| --- | --- |
| Signing profile JSON `provider` | `"1password"`; an omitted field in an older profile defaults to 1Password. |
| `signing setup --provider` | Defaults to `1password`, the only implemented secret provider. Other values fail before token reads or provider requests. |
| `signing configure`, `status`, `verify` | Use the secret provider saved in the profile; no provider override. Status identifies that provider. |
| Execution/provider-auth `--provider` | Selects `codex` or `claude` as the engineering client; separate from signing-secret selection. |

One open source signing-secret alternative is [planned](proposals/signing-secret-providers.md).
No alternative backend or general plugin interface is implemented.

`signing setup` asks for vault/item names or IDs, the public key and an optional
expected SHA256 fingerprint. A blank fingerprint is calculated from the public
key. It reads the Service Account token through a hidden prompt, or uses an
existing private file selected by `--bootstrap-file /PATH/TO/YOUR_PRIVATE_BOOTSTRAP`.
The wizard requires confirmation before saving private local configuration and
prints its exact storage paths. Without `--replace`, it refuses to overwrite an
existing profile. It creates no vault, account or SSH key, makes no network
request and cannot certify account grants.

Use the wizard to change an existing profile:

```sh
sdlc signing setup --profile personal-key --provider 1password --replace
```

`--replace` requires a valid private saved profile. Any token file selected for
reuse must pass the safety checks; a new token can replace a missing or unsafe
bootstrap.
Press Enter at the vault, item and public-key prompts to keep their current
values. A blank fingerprint is recalculated from the accepted public key. The
token choice defaults to keeping the current Service Account token; choose a
new token to enter it at a hidden prompt, or select a safe existing token file
with `--bootstrap-file`. A new token is saved in a unique private file. Setup
never overwrites or deletes an existing bootstrap, which may be shared by other
profiles. After confirmation, it replaces the profile atomically. Cancellation
or a save failure preserves the existing profile. Connected verification remains
a separate `signing status --verify` command.

Default installation state is `~/Library/Application Support/sdlc` on macOS and
`~/.config/sdlc` on Linux (respecting XDG configuration). A named profile uses
`profiles.personal-key.local.json`; first setup through the hidden prompt creates
`signing-personal-key-bootstrap` there. Keep state outside repositories. The bearer
token remains plaintext in a private owned mode-`0600` file.

Plain `signing status` checks saved configuration, public identity and bootstrap
safety offline. It prints neither references nor private paths. `--verify`
contacts the configured vault through official `op`, then checks the key identity
and signs/verifies a disposable Git commit in a separate network-disabled
container. It makes no GitHub or model request and does not establish GitHub
Verified attribution. Success belongs to that invocation; it is not cached.

```sh
sdlc signing status --profile personal-key --show-config
sdlc signing verify --profile personal-key
sdlc signing configure --profile personal-key --file /PATH/TO/YOUR_PRIVATE_SIGNING_PROFILE.json
```

`--show-config` is explicit private inspection: it displays the reference,
bootstrap path and saved configuration location, never the token or private key.
Keep that output private. `signing verify` remains an alias for `status --verify`.
`configure --file` is an advanced import of an external JSON profile owned by
your user with mode `0600`. The JSON contains `provider`, `version`, `id`,
`reference`, `public_key`, `fingerprint` and `bootstrap_file` metadata, with no
Service Account token or private key. The wizard creates this file; routine
changes through `setup --replace` do not require a hand-written JSON file.
`--file` belongs to `configure`. These commands default to the `default` profile
when `--profile` is omitted.

Signing profile names are independent of GitHub profile names. Register the
public key as a GitHub signing key for the intended account and Git email, then
create an explicit pair as described below.
Signing is mandatory; missing vault access, bootstrap or the approved key stops
publication. The bootstrap file is itself a persistent plaintext bearer secret.
Restrict its permissions, keep it out of source/build contexts and use host disk
protection. OS credential-store integration is not implemented. No desktop
1Password approval or forwarded personal SSH agent is used by this route.

### Pair accounts and select a repository

```sh
sdlc github pair --profile personal --signing-profile personal-key
sdlc github list
sdlc github status --profile personal
sdlc github status --profile personal --verify
# From the initialized project:
sdlc github use --profile personal
sdlc github status
sdlc github status --verify
```

`pair` uses official native `gh` to prove the selected account and check that
its configured public key is registered as an SSH signing key. It makes no
GitHub writes and does not retrieve a 1Password token or private key. Omit
`--signing-profile` to use the same name as `--profile`; different names are
supported. Use `pair --replace` for a deliberate changed pairing. Existing
native login and signing configurations remain available; pair each account
once before new runs.

`list` reads native installation metadata and public pair records locally, including
unpaired profiles. It reports configured profiles without claiming login validity.
`use` checks the selected native account, public signing-key registration and repository
push access before saving selection in private host state. Omit `--profile` for a
saved selection or sole configured profile; multiple profiles require a choice. An
unpaired native profile can check repository access but must be paired before selection
can be saved. Failed checks leave any saved selection unchanged.

`status --profile NAME` inspects the global pair. Without `--profile`, repository
status and fresh runs use saved selection, a unique owner pair or the sole configured
pair. A conflicting run `--github-profile` requires a deliberate `use --profile NAME`.
Repository identity resolves from explicit `--repo`, saved selection, initialized
identity, then legacy origin discovery. `init` reads SSH origin metadata locally
without using host SSH authentication. If no identity can be inferred, supply
`--repo OWNER/REPO` once to `use`; later commands reuse its saved selection. Only
GitHub.com is supported. `status --verify` checks the selected native account, public-key
registration and, in repository mode, push permission. It does not retrieve a signing
secret or prove GitHub's Verified attribution.

Pair records and repository selections live outside checkouts in private SDLC
state, with mode-`0700` directories and mode-`0600` files. Records contain public
IDs, login, key and fingerprint, with no vault references, bootstrap paths or
tokens. Repository selection binds the canonical checkout root and GitHub
repository independently of later origin edits/removal. Changed pair metadata
requires deliberate `use --profile NAME` again.
Each linked worktree has its own selection, although run capture currently
requires an ordinary checkout. All accounts share one native runtime image;
each retains its separate auth cache.

Unattended signing currently requires macOS or Linux host file ownership
checks. Windows signing stops until equivalent ownership protection is
implemented; installation and other CLI capabilities have their own support.

For the disposable trial and current validation limits, use the
[Docker GitHub test guide](github-docker-test.md).

## Provider login

After building the shared image, run these commands from an interactive terminal:

```sh
sdlc auth login
sdlc auth login --provider claude
sdlc auth status --all
```

Codex uses its device login flow. Follow the displayed browser instructions;
device authentication must be enabled for your account or workspace. Login has a
fifteen-minute limit. API-key authentication and other providers remain future
work. Claude uses its normal browser login through the unmodified CLI. Open the
displayed URL and paste the code into the terminal if the browser cannot reach
the container callback. These commands use your own account; subscription access
is not shared or used through a custom API client. See [provider usage](provider-usage.md).

Successful login completes the provider's sign-in flow and confirms that a fresh
container can load the saved account credentials. Claude may still show its
native first-launch setup when you first run `sdlc interactive --provider claude`,
including another sign-in prompt. Complete those prompts using the same account,
then exit with `/exit`. The terminal setup persists in the same private volume;
launch again to check that it opens without repeating sign-in. The saved-login
check does not verify whether terminal setup is complete. See the
[upstream report](https://github.com/anthropics/claude-code/issues/65725).

Codex is the default provider when `--provider` is omitted. `sdlc auth login`
logs in to Codex and `sdlc auth status` checks Codex. Select Claude with
`--provider claude`. Check both providers with `sdlc auth status --all`;
`--all` is available only for status and cannot be combined with `--provider`.
An explicitly empty or unsupported provider is rejected before Docker starts.
Status starts a fresh container with networking disabled and a read-only
credential volume. It reports whether the CLI can load stored account credentials,
without printing account details or raw provider output. This does not validate a
token remotely, check model access or make a paid model request. Status exits
nonzero if any requested provider lacks a usable stored login.

A successful status check reports:

```text
codex: saved account login found (offline check)
claude: saved account login found (offline check)
```

This confirms that the official client can load the saved account credentials. It
does not repeat browser sign-in or contact the provider to check current access. Completing
browser login and loading its saved credentials are separate checks; the offline
status message describes the latter and does not report a login failure.

Each installation has one labelled local Docker volume per provider. Codex retains
only `auth.json`; its home, settings and logs are disposable. Claude Code manages
its own native configuration/authentication cache directly in its volume, using
the [documented container pattern](https://code.claude.com/docs/en/devcontainer#persist-authentication-and-settings-across-rebuilds).
SDLC checks file safety metadata but does not read, copy or interpret Claude
credential contents. The cache can also retain provider settings, account metadata
and native logs; treat all of it as private. The installation identity lives in
`auth-installation.json` beside the runtime record. Keep the same state directory
and Docker engine to reuse the volumes after reinstalling or rebuilding.

Sign in once per provider for this installation. Container removal, image rebuilds
and CLI reinstalls preserve login state. The official clients handle refresh;
repeat login when a client requires it. Authenticating on every job would add
friction without changing which uses the account permits.

Login containers use the recorded immutable image, a non-root user, an empty
home, a read-only root filesystem and no Linux capabilities. They receive no
project checkout, host credentials, signing key, GitHub token or Docker socket.
Only login has network access. A separate offline initializer sets volume
ownership with limited privileges and never reads credential contents. The
runtime lease prevents image replacement during authentication; the selected
provider's cache lock serializes its readers and writers.

Claude authentication commands use documented [safe and restricted modes](https://code.claude.com/docs/en/cli-reference).
They retain native authentication, disable user customizations such as hooks and
MCP servers, and exclude cached user settings, including custom credential helpers
and environment overrides. Managed provider policy still applies. Updates and
nonessential traffic are disabled for these commands; the pinned image is updated
through a local rebuild.

Treat these volumes as passwords. They are file-backed Docker storage, without
encryption supplied by SDLC; anyone with Docker administration access can read
them. This persistence is separate from the 1Password signing-key resolver. SDLC refuses to mount volumes with unexpected ownership labels,
drivers or driver options, and refuses linked or publicly readable credential
files.

Login requires terminal input and output. Keep that terminal private and avoid
terminal recording: login URLs and one-time codes appear there. Docker logging is
disabled for all authentication containers, and SDLC creates no authentication
log files. Claude's own private cache logs may persist. Host proxy environment and
Docker-config proxy injection are excluded;
environments that require a proxy need a future explicit configuration option.

If login fails or is interrupted, check the selected provider with `sdlc auth
status --provider codex` or `--provider claude` and retry the relevant login.
Codex login can replace malformed contents in a safe credential file.
Claude Code manages its own recovery and cache writes, including during a failed
login; SDLC makes no promise that its previous cache is unchanged.
Unsafe storage permissions or links require inspection before retrying; SDLC does
not delete credential volumes automatically. Containers are removed after each
operation, including cancellation. If cleanup fails or the host crashes, inspect
leftover `sdlc-auth-*` containers in Docker before rebuilding the image. Login does
not yet provide logout or remote token revocation.

The command contracts follow the pinned provider versions: [Codex authentication](https://learn.chatgpt.com/docs/auth),
[Codex login implementation](https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/cli/src/login.rs),
and [Claude authentication](https://code.claude.com/docs/en/authentication).
An incompatible provider upgrade fails the status check rather than displaying
unrecognised provider output.

See [the workflow](workflow.md) for the next capabilities.

## Interactive provider sessions

After building the shared image and logging in to the selected provider:

```sh
sdlc interactive
sdlc interactive --provider claude
```

Codex is selected when `--provider` is omitted. Explicit selection with
`--provider codex` is also supported.

Both providers default to full access inside the container:

| Provider | Default native settings | Command-line control |
| --- | --- | --- |
| Codex | `--sandbox danger-full-access --ask-for-approval never` | `--approval never` or `--approval on-request` |
| Claude | `--permission-mode bypassPermissions` | `--permission-mode default`, `manual`, `acceptEdits`, `plan`, `auto`, `dontAsk` or `bypassPermissions` |

The shared rule to stop implementation until a human answers any question still
applies in every mode. Full access changes tool approvals, not that rule.

Claude's full-access session settings include the documented
`skipDangerousModePermissionPrompt: true`, so its responsibility warning does not
repeat in every disposable container. Other permission modes receive empty
settings. This setting does not override managed policy. See the
[native warning behaviour](https://code.claude.com/docs/en/permission-modes#skip-all-checks-with-bypasspermissions-mode).

For example:

```sh
sdlc interactive --approval on-request
sdlc interactive --provider claude --permission-mode manual
sdlc interactive --provider claude --permission-mode plan
```

Options apply to this session. Unsupported values, explicitly empty modes and
another provider's option are rejected before Docker starts. The native client
enforces managed policy and mode availability, which can refuse or reduce the
requested access. The pinned Claude client visibly switches to `auto` when
managed policy disables bypass permissions. SDLC does not restart or switch
modes itself. Claude's native `auto` mode can fall back to Manual when unavailable.
`dontAsk` denies tools that need approval, including
human-question tools; it is distinct from full access. See the official
[Codex flags](https://learn.chatgpt.com/docs/developer-commands) and
[Claude permission modes](https://code.claude.com/docs/en/permission-modes).

Codex always uses Docker as its filesystem and network boundary in this launcher.
`on-request` enables native approval requests without creating an inner sandbox
or requiring approval for every shell command. The inner restricted Linux sandbox
is unavailable under the container's current restrictions.

The command checks the stored login offline, starts the recorded shared image and
attaches your terminal to the unmodified provider CLI. Enter prompts in its native
interface and exit through that CLI. A missing login directs you to `sdlc auth
login --provider ...`. Provider errors and questions appear directly in the
terminal. SDLC does not submit a prompt, restart a failed session or bypass usage
limits.

This first slice opens an empty `/workspace`. It receives the shared instruction
snapshot and the skills and agents already built into the image. It does not
capture the current repository or load work secrets. Workspace files, temporary
homes and session history are discarded on exit. There is no saved progress log,
checkpoint or resumable session yet; do not use it for work you need to keep.

Only the selected provider's private native cache persists. The official client
handles refresh. Codex's scratch `auth.json` points to its persistent cache so
native refresh writes survive interruption; native logout is propagated on exit.
This follows the pinned client's [file-cache implementation](https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/login/src/auth/storage.rs)
and must be rechecked on upgrade. Claude manages its cache directly; it may retain account metadata and
native configuration. Its transcript history and automatic memory are disabled.

Sessions run as a non-root user with a read-only root filesystem, no Linux
capabilities, bounded resources and no host repository or Docker socket. They
have network access and can access their own provider cache, so use trusted
prompts. Full access can read and modify all writable paths in the container,
including the selected provider's cache. Codex uses Docker as the isolation
boundary, following the
[official container guidance](https://learn.chatgpt.com/docs/agent-approvals-security).
This avoids adding privileges for a second Linux sandbox. Claude uses fresh
read-only settings and an empty strict MCP configuration, and disables account
MCP connectors. Its global instructions,
skills and agents remain available; cached rules, commands, styles, workflows and
plugins are hidden by disposable directories. Documented native work-data
directories, including plans and file history, are also disposable. Recheck this
layout when upgrading the pinned client. Native settings changes cannot persist
through the read-only settings file. Managed provider policy still applies.

The selected provider's cache lock prevents overlapping login or session operations
on that cache. A shared runtime lease prevents image replacement during a session;
the other provider can run concurrently.
Normal exit and cancellation remove the container and instruction snapshot while
keeping the provider cache. If cleanup fails or the host crashes, inspect leftover
`sdlc-interactive-*` containers before trying again. Reinstall the CLI after this
change; the existing image can be reused.

## Retained storage and runtime variants

Use `sdlc storage` from a project repository to report retained `.sdlc` sizes, categories and run ages. `sdlc storage --older-than 30 --json` filters the run rows by their latest recorded activity; total directory sizes still cover the complete scan. This command reads metadata and file sizes without initializing accounts or removing files. See [run history](run-history.md) for scan boundaries and partial reports.

A single-ticket run can select `--runtime NAME`. Resume recovers that selection from its journal. Named runtimes have their own image tag and state while retaining the installation's account caches and build lock. To test committed local skill changes without replacing a paused run's default image, build with `sdlc runtime build --name reviewed-skills --source . --skills-source /path/to/skills`, then select `--runtime reviewed-skills` on the new ticket run. See [runtime variants](runtime-variants.md) for provenance, rebuild options and limitations.
