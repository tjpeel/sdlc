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
pins. No account login occurs during installation. `--sdlc-only` keeps the runtime;
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
sdlc update --sdlc-only
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
`Next: sdlc update --sdlc-only`. The command still exits with a failure status;
complete the saved work before retrying a full update. Terminal failures use a
red heading and an accented next action. Redirected output and `NO_COLOR` keep
plain text.

`--sdlc-only` remains available while work is paused. Runtime build/update commands
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

The command creates `.sdlc/work/` and, when needed, adds `/.sdlc/` to Git's
local `info/exclude` file. It resolves that file through Git, including in linked
worktrees, and preserves existing exclude rules. The whole local SDLC directory,
including `project.json`, is ignored by default. Repeated initialization adds the
broader rule to existing work-only setups without changing project settings.
Private process inputs and run
output under this directory must be ignored and untracked. Initialization fails
if they or case variants of their paths are tracked or staged, or if repository
ignore rules prevent this protection. Remove private files from tracking deliberately before retrying;
initialization does not change the index or erase Git history.

Portable settings live in `.sdlc/project.json`:

```json
{
  "version": 1,
  "branch_format": "{reference}-{ticket}",
  "check_selection": "runtime",
  "checks": [],
  "input_files": []
}
```

New projects use `check_selection: "runtime"`. Initialization displays a current
preview without saving inferred commands. The controller resolves the suite from
committed candidate metadata before each checking phase; untracked files, check
outputs and private check inputs do not determine it. Changed commands invalidate
previous verification. Missing or ambiguous discovery stops the run.

For a custom complete sequence, set `check_selection: "explicit"` and supply
argument arrays in `checks`, for example `[["go", "test", "./..."], ["go", "vet", "./..."]]`.
Existing settings and saved runs without `check_selection` retain their explicit
frozen commands. Runtime selection ignores the saved `checks` array.
.NET inspection prefers the complete Required checks section in `CONTRIBUTING.md`
or `README.md`, then a simple CI sequence. Otherwise it derives restore, build and
test commands from one solution or project, `global.json` and test-project metadata.
VSTest and Microsoft Testing Platform use different arguments. Missing restores
are added for the fresh check workspace; Compose sequences include an exit trap
for teardown and enable `docker_tests`. The summary identifies the source files.
Unsupported shell/setup context, conditional or custom test output paths and
ambiguous targets require explicit configuration. Inspection executes no project commands.

When `.config/dotnet-tools.json` pins supported stable CSharpier 1.x, runtime discovery
places local tool restore and a root formatting check before the other inferred
checks, including Compose startup and builds:

```sh
dotnet tool restore
dotnet csharpier check .
```

Documented root formatter options and environment settings are preserved. The
check reports formatting errors without rewriting source. Unsupported tool pins
or malformed and ambiguous manifests require explicit checks; a forced refresh
keeps usable saved settings when it cannot infer the formatter safely. See the
[CSharpier CLI documentation](https://csharpier.com/docs/CLI).

Before the full suite, the controller also discovers supported formatters from
the committed candidate. For pinned CSharpier 1.x, it restores the local tool and
formats new or changed C# source in batches of at most 16 files, with the working
directory at the repository root. Project formatter configuration remains in
effect. This preparation also applies to saved runs with explicit or legacy
checks; it does not replace their frozen suite.

Formatter commands run in an independent source copy without provider
credentials, private check inputs or Docker-test access. An empty correction
continues to full checks. A nonempty correction returns a bounded private diff
and hashes to the original implementation session, which reviews, applies and
commits it before requesting checks again. SDLC does not apply corrections to
the agent's workspace. Preparation evidence is tied to the candidate and commands
and reused while they remain unchanged. Existing input, cleanup, artifact-size,
stale-candidate and repair limits still apply. Other formatter implementations
can use the same preparation phase; arbitrary lint scripts are not inferred as
formatting commands.

A failed preparation still permits new agent-requested diagnostics or smaller
source corrections in the isolated check worker. Their results do not clear the
failed preparation or establish a passing full suite.

`input_files` starts empty. During a run, these exact relative files are copied
only into the separate credential-free check worker. Use this for reviewed test
configuration; it does not supply requirements to the provider. Files named
explicitly by `--input` are provider-visible, even if also in `input_files`.
Files already tracked in source remain visible as normal source. Keep credentials, account details, vault references
and host paths out of these settings. To share reviewed portable configuration,
explicitly add only this file with `git add -f .sdlc/project.json`. Already tracked
settings remain tracked; ignore rules do not remove them from the index.

For Compose checks that require an ignored root `.env`, explicitly configure
`"input_files": [".env"]` and use `sdlc run --docker-tests`. SDLC freezes the file
at capture and copies it to the check workspace; it does not export its contents
as process environment variables. Compose's `--env-file` selects interpolation
input, while service `env_file` supplies container environment. See the
[tested .NET Compose example](../examples/dotnet-smoke/README.md). Keep the `.env`
itself ignored and untracked and use settings for disposable test services.

Repeated initialization validates and preserves existing settings, including
custom checks. `sdlc init --force` repeats inspection, selects runtime discovery
and refreshes `docker_tests`. It preserves `input_files`, `branch_format`, NuGet cache
preferences and saved runs. Before replacing changed settings it saves their exact
previous bytes under the ignored `.sdlc/work/.archive/init-*/project.json` path,
shown in the summary. If inspection cannot produce usable checks, existing settings
are retained. Force can repair stale or invalid check arguments; the remaining
preferences must still be valid. Forced initialization does not change frozen
execution checkpoints. It rejects unsupported settings versions, invalid paths
and unsafe filesystem links.
No project checks run during initialization. It needs only local Git access;
it does not fetch source, contact providers, bind an account profile or require
Docker. `init` accepts `--force` and remembers a unique GitHub origin identity locally,
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

To retire a reference while keeping its complete local evidence:

```sh
sdlc work archive --reference TASK-123 --dry-run
sdlc work archive --reference TASK-123
```

`archive` moves the complete `.sdlc/work/TASK-123` tree into a unique directory
under `.sdlc/work/.archive/` and removes its run registrations. It refuses live
runs or series. This reversible local move needs no `--yes`; the source reference
is then free for fresh scoping. Archived trees are excluded from active reference
discovery, resume and runtime guards. A later update may replace the recorded
runtime image, so the archive is not a portable backup and does not guarantee
later resumability.

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
bounds ticket controllers within that feature. A free slot starts the next
eligible ticket without waiting for other active tickets to finish. Provider
calls have no separate SDLC concurrency limit. Host stream lines carry ticket labels; each private native event
file retains its original bytes. The dashboard continues to show individual ticket runs.

### Amend an unstarted ticket's dependencies

Use the supported amendment command when a saved feature needs a different
dependency set for a ticket that has not started:

```sh
sdlc dependencies --reference YOUR_WORK_REFERENCE --ticket 03-add-workflow.md \
  --depends-on 01-add-api.md --depends-on 02-add-tests.md --dry-run
sdlc dependencies --reference YOUR_WORK_REFERENCE --ticket 03-add-workflow.md \
  --depends-on 01-add-api.md --depends-on 02-add-tests.md
```

Each `--depends-on` names an exact ticket filename. The supplied flags replace
that ticket's complete dependency set; use `--depends-on ''` to clear it.
`--json` prints the amendment result. The command runs offline and does not
start a ticket or connect a provider.

The feature controller must be stopped, and the child must have no prepared
base or saved run. Unknown tickets, self-dependencies, cycles, changed plan
inputs and unsafe paths are rejected. The amendment preserves ticket order,
priorities, path declarations, parent results and all frozen execution settings.
It updates the private plan and checkpoint together and records the change.
If interrupted, repeat the same amendment command to complete its recorded
transaction before continuing the feature.

Continue with the ordinary `sdlc run --reference YOUR_WORK_REFERENCE --all`
command. Every parent must reach its normal ready or merged checkpoint before
the child can start. Multiple parents still require a checked and signed
combined base; CI and independent review remain required for the child's PR.
Avoid editing the saved plan or journal manually.

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
that parent's recorded branch/head. A ticket with multiple dependencies can
start when each parent is ready or merged. SDLC combines current integration
with the exact ready parent revisions, checks the source combination in an
isolated worker and publishes a signed derived base with the same verified
tree. The checkpoint retains both source and signed revisions. Conflicts or failed combined checks
stop before the child starts. Its PR targets that derived branch so its review
diff contains only its own changes.
SDLC never merges or approves PRs automatically.

A conflicting or failing combination retains its failed generation and leaves
the child unstarted. Correct the affected parent work through its saved run,
complete its normal checks and review, then continue the feature to build a new
generation. For an operational failure, resolve its reported cause and continue
the same feature; retained preparation and publication checkpoints support retry.

Review a dependent PR while it targets the derived branch, but wait until it is
retargeted to integration before merging it. Merging into the temporary base
would not merge that work into integration.

Derived bases are immutable generations bound to the integration revision,
parent revisions and frozen verification context. Repeating a feature command
reuses an unchanged recorded generation. Source PR updates invalidate dependent
readiness; dependent work waits while its parents complete their checks and
review. A first parent merge updates the remaining parent from integration,
then SDLC rebuilds the combination and restacks the child once that parent is
ready again. After all parents merge, it replays only the child's own commits
onto integration and retargets the original PR.

Derived branches use `sdlc-derived/` followed by the generation identity. They
remain recorded for recovery and are not reused for a different context.

A stale dependent ticket keeps its original hold while its parents reconcile.
For a pending human question, `sdlc answer` records the response without starting
work on the stale base. Continue the owning feature to reconcile that base before
the saved run consumes the answer. Restacking invalidates old checks,
CI and review evidence; a previously published head cannot establish readiness
for the revised source.

Restacking changes commit IDs because Git includes parent IDs in each commit's
hash. SDLC keeps linear ticket history, batches observed parent updates and
avoids repeated restacks for an unchanged context. It preserves the original
jobs, sessions and accounting, and requires fresh checks, signing, CI and
independent review for the updated head. Recorded derived branches are retained
for resume and audit; this workflow does not delete them automatically.

If the implementation has unfinished tracked edits, reconciliation retains them
in an unsigned ticket commit before replay. The original implementer assesses
that work before the normal verification and publication pipeline continues.
Untracked files remain in the workspace; reconciliation refuses to overwrite
colliding files.

### Continue and reconcile a feature

Each work reference has one durable private feature checkpoint. Repeating the
feature command continues recorded work and adopts compatible existing ticket
runs instead of creating duplicates; ambiguous or incompatible runs stop for
attention. The feature freezes its plan, models, accounts, runtime, common
inputs and checks. Restore changed inputs or use a new reference; rerunning is
not a way to replace those choices.

Older clients could save an empty-check configuration and run IDs before ticket
preparation failed. If no run directories exist and no result records prepared
or published work, repeating the feature command uses the corrected project
configuration while preserving selected providers, models and account pairing.
`--dry-run` previews this recovery without changing the checkpoint. Execution
freezes the corrected preparation under the feature lock. Once run data exists,
the original settings remain frozen.

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
| `--branch` | Explicit override; defaults to the project's `branch_format`, or `{reference}-{ticket}` when omitted. |
| `--repo` | GitHub.com `OWNER/REPO`; defaults to saved selection, then initialized identity, then legacy origin discovery. Supply it when no identity can be inferred. |
| `--github-profile` | Select a registered account/key pair for an unbound repository; omission uses saved repository selection or a unique owner pair or sole configured pair. A conflicting saved selection fails. |
| `--model`, `--effort` | Override the implementation lead's model and effort. |
| `--review-model`, `--review-effort` | Override the opposite provider's review lead. |
| `--docker-tests` | Override the project `docker_tests` default; enable the separate privileged integration-test daemon. Use `--docker-tests=false` to disable it for a new run. |
| `--timeout` | `2h` per controller invocation; accepts `1m` through `24h`. Runs with a persisted limit override have no implicit deadline; explicit timeouts and parent deadlines still apply. |
| `--resume` | Continue a recorded run ID with its original settings. |
| `--override-run-limits` | Admit a single saved-run resume at a corroborated local numeric hold and persist permission to continue past controller numeric ceilings. |
| `--answer-file` | Bounded nonempty UTF-8 human answer, only with `--resume`. |
| `--dry-run` | Print an offline plan. |

New branch names use the exact work reference when Git accepts it, with a branch-safe rendering for references containing spaces or invalid characters. `{ticket}` is the lowercase description from the numbered filename, with its sequence number and `.md` removed: reference `TASK-123` and ticket `01-add-cache.md` produce `TASK-123-add-cache`. No run ID is appended. `branch_format` must start with `{reference}` and contain each supported placeholder exactly once; `{reference}/{ticket}` is an alternative. Planned names are validated and shown before starting work. Feature plans reject tickets that resolve to the same name. Use `--branch` for an explicit individual-run name; resumed runs always keep their recorded branch.

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
Testing Platform configuration. `init` prepares supported .NET sequences locally;
`init --force` refreshes them after repository guidance or manifests change.

Configured `input_files` receive a separate copy in the check workspace. Their
untracked files stay out of the provider workspace unless selected with
`--input`; files already tracked in source remain visible as normal source. Keep credentials
out of these files; SDLC's path checks do not identify every secret in file
contents. Check workers receive no provider cache, signing keys, GitHub token,
SSH agent or host Docker socket. They have network access for dependencies.
Set `"docker_tests": true` in `.sdlc/project.json` to enable Docker integration
checks by default for new ticket and feature runs. An absent setting defaults to
false. `--docker-tests` and `--docker-tests=false` override that preference for a
new run. Resuming a saved run or feature retains its recorded setting.

Enabling Docker tests starts a separate privileged Docker-in-Docker daemon,
job network and disposable volumes. This mode is for trusted integration checks;
privileged containers do not isolate hostile code from the engine host. Cleanup
errors stop delivery. Recognised missing-daemon diagnostics stop checks as an
operational failure without spending a provider repair turn. The private check
log remains available. A run recorded without Docker tests needs a new run with
them enabled; its frozen setting cannot change on resume. Compiler or formatter
failures still request source repair when a later Docker cleanup also fails.

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
The fixed companion-source reader also mounts the selected GitHub profile
read-only and runs a fixed set of controller commands, including official
`gh repo clone`, for a bounded same-owner request. It has no signing or vault
access and does not run project code. Provider and check workers receive no GitHub profile, host configuration,
desktop SSH agent or Docker socket. No GitHub App installation is required for
this native CLI route. See the [Docker GitHub test guide](github-docker-test.md).
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

### Recover a push-protection rejection

When GitHub rejects an actual publication push for detected secrets, SDLC can
return verified original commit, repository-relative file and line locations to
the same implementation session. GitHub names the publisher's signed commits;
the trusted publisher maps them back to the original source history and checks
each location at that exact revision. Current file line numbers may differ.
Raw diagnostics, detected values, scanner titles and exemption URLs remain in
private storage.

The implementer assesses the reported content and corrects every affected
unpublished commit. A later deletion or a change to commit IDs alone does not
establish historical cleanup. The controller retains the rejected source
evidence, protects the inherited source and any verified published prefix, and
requires a meaningful correction before another push. A detection outside that
unpublished suffix or an uncertain credential decision needs human resolution.
SDLC does not request an exemption or bypass push protection.

The corrected candidate repeats full checks, local review, signing and protected
publication, followed by current CI and independent review. Separate persisted
publication-repair accounting preserves existing sessions, identities, history
and all other budgets. Transport, authentication, lease, signing, malformed
metadata, overflow and cleanup failures retain operational holds.

Continue the same saved run with `sdlc resume --run RUN_ID` after installing a
version supporting this recovery. A legacy generic `command_failed` diagnostic
does not prove which command failed; a fresh trusted push result must establish
the rejection before it can authorise a repair handoff. GitHub's
[command-line push-protection guide](https://docs.github.com/en/code-security/how-tos/secure-your-secrets/work-with-leak-prevention/push-protection-on-the-command-line)
explains why removal must cover every affected commit.

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

On macOS and Linux, the running implementer can call
`/usr/bin/python3 -I /sdlc-diagnostics.py` with a JSON `requests` batch on stdin.
The fixed helper obtains the current committed head/tree and uses a private
controller mailbox; checks run in the separate credential-free worker. The
response contains bounded results for every independent request, including
semantic failures. Source correction requests return validated private artifacts
for the implementer to assess, apply and commit. They never modify the host
workspace or become permanent verification gates.

Source correction commands must change only the selected source paths. When a
formatter changes other committed files, the live response rejects that request
without an artifact and retains the other batch results. The agent can narrow
the formatter command from the working-directory root and retry. Source
integrity, frozen-input and cleanup failures still stop the invocation.

Commit a checkpoint before calling the helper. Dirty checkpoints and stale
candidates return recoverable errors without provisional evidence; the agent can
correct its checkpoint and continue during that same native invocation. Frozen
input, infrastructure, transport-integrity and cleanup failures stop the
invocation. Reviewers have no helper or mailbox mounts. Windows retains the
structured handoff below. The implementer self-reviews its complete changes;
the controller performs the required opposite-provider review after CI.

An implementer can return `diagnostics_requested` with a bounded
`diagnostic_requests` batch to obtain focused machine evidence before the
configured suite. The batch runs in the existing isolated check worker and
returns every independent request's results to the original session, including
nonzero exits. Commands within each request still stop when a prerequisite fails.
These requests replace the latest diagnostic batch and never enter the retained
required verification set. Unchanged results are cached; changed source, inputs,
commands or runtime invalidate them. Diagnostic results cannot establish
publication readiness. Use `checks_requested` for the full suite and retained
required `verification_requests` when a coherent candidate is ready. Older saved
requests retain their existing required status. See the
[repair workflow](proposals/repair-diagnostics.md).

Within the repair bound, a configured check failure still allows pending structured
verification requests to run in the isolated check worker. Failed commands prevent
reuse of their source copy and HOME. Passing an additional request does not replace
the failed configured checks or permit publication. Completed command results
return to the original implementation session as untrusted data: combined stdout
and stderr, exit status, revision, retained head/tail bytes and explicit omissions.
Each execution captures at most 64 KiB, with at most 16 KiB per command; retained
and returned command output shares a 16 KiB aggregate budget across the current
suite, additional requests and diagnostic batch. Oversized output is drained and marked truncated.
Binary or split UTF-8 bytes use an explicit base64 encoding. Narrow diagnostic
requests can obtain more focused output without error-specific adapters.

Output can include source, assertions and values printed from private check inputs;
returning it to the selected implementation provider is part of running checks.
These limits bound context and storage, not disclosure. Controller setup messages,
arbitrary host logs and native transcripts are not exported through this channel.
Operational or cleanup failures remain holds and cannot return provisional output.
Independent-review packets and checkpoint exports exclude command output. The
original implementation session assesses newly completed results before publication.

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
`--timeout`, `--dry-run` and the explicit recovery flags described below. Implementation turns and repairs resume the exact
recorded native session, never the latest unrelated session. Each independent
review starts fresh. Cancellation, timeout, invalid handoffs, missing inputs,
usage/access limits and policy refusals retain a stopped checkpoint. Fix the
reported cause before resuming; never restart or change identities to evade
provider restrictions. Journals and native storage must remain intact for resume.

Without a persisted override, explicitly resuming a run stopped by the repository-check repair limit begins
another finite repair window. The controller rechecks the candidate and returns
fresh failure metadata to the same implementation session when repair is needed.
Cumulative repair and turn counts remain recorded. The native-turn ceiling defaults to 24; a single explicit extension is described below.
This renewal applies only to that check-limit stop, not provider, CI or independent
review holds.

### Supply missing CI failure context

Failed CI checks automatically return relevant context for the exact published
revision to the original implementation session. The controller collects check
output and annotations, plus failed GitHub Actions job and step details and
bounded log excerpts. Native Sonar rule, severity and committed-location findings
remain included. Pending checks keep waiting; collection failures retain an
operational hold before any repair grant.

An older saved implementation may have stopped for missing CI evidence without
recording a question. Ordinary resume refreshes the failed revision and sends
collected context to its original session, reusing the existing CI grant:

```sh
sdlc resume --run RUN_ID
```

This recovery accepts only the corroborated missing-context stop. It retains
original session and candidate bindings, input hashes, cumulative accounting,
check and review gates, and the interrupted-dispatch fence. It does not reset
budgets or resume provider, access, policy or unrelated requirements holds.
If no relevant context is available, the run stays held.

The selected GitHub profile stays in the trusted read-only controller. It masks
recognised credential material and values from hash-verified retained check-only
inputs before keeping excerpts, and records masking and truncation explicitly.
Collected text is untrusted private evidence for the original implementer;
independent review and portable status exports omit it. Masking does not prove
that arbitrary CI output contains no unknown secrets. Raw response overflow,
malformed data, stale identity, unsafe inputs and failed cleanup stop collection.

Selected human context remains available as a fallback:

A saved implementation can stop for missing CI failure evidence without
recording a question. Supply selected factual context through the controller:

```sh
sdlc resume --run RUN_ID --ci-feedback-file /PATH/TO/CI_CONTEXT.txt --dry-run
sdlc resume --run RUN_ID --ci-feedback-file /PATH/TO/CI_CONTEXT.txt
```

The file must contain nonempty UTF-8 text of at most 64 KiB. Keep credentials
out of it. The preview reports hashes and identities without printing the text.
The same flag is available on a
single-ticket `sdlc run --resume RUN_ID`.

This action supports only a stopped native implementation that needs CI
evidence after an existing repair grant for the failed published head. It
requires the original session, a clean candidate and current failed CI.
Unrelated requirements, provider, policy, access, usage and review holds remain
held. Other recovery flags cannot be combined with this action.

SDLC records a private audit, binds the context to the candidate at preparation
and the failed publication, then rechecks the candidate and inputs before
dispatch. The original implementer receives the text as untrusted evidence and
decides how to address it. The existing CI grant, rounds and cumulative counts
stay intact; native calls still receive normal accounting. Full repository
checks, signing, publication CI and independent review remain required.

Only one supplement is accepted for each failed published head. An interrupted
operation before dispatch can continue with its saved context after its cause
is resolved. An ambiguous interruption after dispatch stays held for inspection
to avoid sending the context twice.

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

### Missing private check inputs

A text answer also does not capture a private environment or test-input file
that appeared after launch. For a stopped, unpublished implementation run, use
the explicit check-only action:

```sh
sdlc inputs --run RUN_ID --add-check-input .env --dry-run
sdlc inputs --run RUN_ID --add-check-input .env
sdlc resume --run RUN_ID
```

`--add-check-input` is repeatable and cannot be combined with provider-visible
`--add`. It snapshots selected files from the recorded project root into the
private check-input directory, records their paths and hashes, and invalidates
configured, formatter and additional verification. It leaves the checkpoint
state, original session, counters and allowances intact. The file bytes never
enter the provider workspace, review snapshot or published source. Existing
captured files cannot be replaced; unsafe paths, linked files, conflicting
destinations and resource-budget overflow are refused.

For saved feature runs, beta.47 uses the frozen run settings for check-only
capture even if the project's current check configuration has changed. Original
requirements and check files must still match their saved hashes. The project
configuration also remains hash-checked when it is a recorded input. This does
not change the feature coordinator's configuration guard or select new checks.

An interrupted capture retains immutable staged bytes and its check-only scope;
complete that operation before resuming. Check-only recovery records require
beta.46 or newer. `--json` returns metadata without file bodies. Provider, input,
cleanup and other unresolved holds still require their normal resolution.

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
sdlc dashboard remove --run RECORDED_RUN_ID
sdlc dashboard remove --all --scope project
sdlc storage purge --all --dry-run
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

Runs have no automatic expiry. `dashboard remove --run ID` hides one stopped
run's registry entry immediately while retaining saved files and lock files.
Saved work can still block an update; resuming registers the run again.
`dashboard remove --all [--yes|--dry-run] [--scope project|installation]` previews clearing
stopped registrations by default. Use `--yes` to clear them. The default scope
is the installation.

`storage purge --run ID|--all [--yes|--dry-run]` permanently deletes stopped
saved runs in the current Git repository, including runs absent from the
dashboard. Both selections preview by default and require `--yes` for deletion.
It also removes affected feature series checkpoint directories; the preview
reports the abandonment. Ticket and specification files remain. Live runs or
series refuse removal. `--all` excludes archived references and credentials.
`dashboard remove` is the recommended spelling; `dashboard forget` remains an
alias. Both accept `--run ID` or `--all`, `--scope project|installation`, and
`--yes|--dry-run`. Single-run removal remains immediate unless `--dry-run` is
selected; bulk removal previews unless `--yes` is selected.

Use `sdlc dashboard export --run ID --to PRIVATE_DIRECTORY` before hiding or
deleting a run to keep a private checkpoint report outside Git checkouts. The
destination must already exist with mode `0700`; reports have mode `0600` and
cannot overwrite an existing export. A report cannot resume a run. See
[run history and retention](run-history.md) for safeguards and backup guidance.

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

Multiple controllers can operate in the same repository or different folders
using separate captured workspaces, branches and native sessions. Each ticket
engages its selected implementer and then the opposite reviewer independently.
Two Codex calls or two Claude calls can run together; SDLC does not reserve a
provider or impose a provider concurrency limit. Feature `--parallel` controls
ticket scheduling within one work reference, not other commands or folders.

Each native client receives a private writable copy of the same user's stored
authentication. Brief cache transactions protect snapshots and refreshed-cache
persistence without locking model execution. An unchanged session cache does not
overwrite a later login or refresh. A conflicting refresh stops the affected
invocation and retains its private cache for inspection; native authentication
errors and provider usage limits still stop work. Authentication is checked
inside the registered controller before implementation and at the review stage
for the reviewer. `sdlc run --timeout` includes setup and runtime waits.

GitHub profiles such as `personal` and `work` use separate native caches and
leases while sharing the runtime image. Authentication and publication serialize
within one profile; separate profiles can coexist. Codex and Claude still each
have one account cache per installation. Profile selection does not authorise
switching identities to evade limits or access denials.

Provider and GitHub operations hold a shared runtime lease. Runtime builds need exclusive
ownership and retain their existing immediate failure when the runtime is in use.
A provider waiting behind a build checks runtime identity after acquiring its
lease; an existing run still rejects an image that differs from its recorded image.
Installation identity creation has a separate short lock. Each provider operation
has its own controller ownership record. Matching live operations can share the
provider volume; unmanaged or orphaned containers still block reuse. Inspect and
stop/remove those containers deliberately before retrying. A container label alone
does not establish that its controller remains alive.
External Docker operations remain outside SDLC's coordination.

When upgrading from the serialized provider implementation, finish older
controllers and their container cleanup before starting new calls to that
provider. Their containers have no operation ownership record and cannot safely
share the cache with the new implementation. A retained refresh from an older
Claude session also requires private inspection before resume; the upgrade
preserves that file rather than guessing its relationship to the current cache.

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
after runtime selection, the error reports the partial outcome. `--sdlc-only`
skips runtime preparation; `--dependencies` selects newer public dependencies.

Subsequent updates can run from any directory:

```sh
sdlc update --dry-run
sdlc update
sdlc update --pull
sdlc update --agent-tools
sdlc update --agent-tools --update-dockerfile
sdlc update --dependencies
sdlc update --sdlc-only
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
newer exact public versions supported by `runtime update`. `--agent-tools` refreshes
only Codex, Claude, the skills catalogue and the agents catalogue, retaining other
dependency pins. An incomplete older pin record is filled from installed inventory;
an existing exact Docker daemon tag may be resolved to its digest without upgrading its version.
The selected exact versions are saved locally; source Dockerfile pins remain the shared build baseline.
`runtime update --agent-tools` performs the same runtime-only refresh.
`runtime status` only inspects versions. Default `update` rebuilds the source
baseline; `--agent-tools` refreshes the four agent tools and `--dependencies`
refreshes the full dependency set. Add `--update-dockerfile` to `--agent-tools`
when those four selections should also become source pins.
`--sdlc-only` updates the host SDLC program and shell while retaining all runtime
components. `--cli-only` remains a compatibility alias. These modes cannot be combined.
These options also work with
`/update` in the shell. Reopen existing shells after installation.

Resumable runs and incomplete feature series in known project roots block runtime
replacement under the build lock. Ready standalone tickets and fully merged
features permit it. The error identifies the saved work and offers `--sdlc-only`.
Checks cover registered, catalogued, current and source roots, including forgotten
runs in those roots. An external root removed from every locator cannot be found;
keep projects registered while they contain unfinished work.
Archived references are excluded. Use `storage purge` to delete stopped saved
runs or `work archive` to retain a complete reference outside active work; hiding
a dashboard entry with `forget` does not release the guard.

To replace the runtime while leaving stopped work where it is, use:

```sh
sdlc runtime build --source /PATH/TO/SDLC_SOURCE --force
```

`--force` reports the affected runs and features, retains their files and the
previous runtime image, and replaces the selected runtime. Saved checkpoints
keep their recorded runtime identity and cannot resume against the replacement.
This option does not bypass active controllers, dependent containers, invalid
checkpoint data or engine validation. It applies only to `runtime build`;
ordinary builds and runtime updates retain the saved-work guard.

### Where version pins live

| Selection | Stored in | How it changes |
| --- | --- | --- |
| SDLC application version | `internal/buildinfo/version.go` and changelog | A delivered application iteration increments the beta; install builds that source version. |
| Repository runtime baseline | `runtime/Dockerfile`, plus managed image defaults | CI updates Codex, Claude Code, GitHub CLI and catalogue pins weekly or manually; other source pins are maintained explicitly or by their configured dependency automation. |
| Local selected runtime | Private `runtime.json`, immutable image ID and inventory | `runtime update` or `update --dependencies` saves exact private selections. Default `update` or `runtime build --source-pins` returns to the source baseline. |
| Saved run and feature settings | Private checkpoints | Frozen for their work; installation does not migrate them. |

Local updates keep selections private by default. `--agent-tools --update-dockerfile`
also writes the selected Codex, Claude, skills and agents pins to the source
`runtime/Dockerfile`. It changes exactly `CODEX_VERSION`, `CLAUDE_VERSION`,
`SKILLS_REVISION` and `AGENTS_REVISION`, retaining unrelated pins and text.
`--update-dockerfile` requires `--agent-tools`. The outer `sdlc update` uses its
selected source checkout; direct `runtime update` also requires an explicit
`--source /PATH/TO/SDLC_SOURCE` for writeback.

The source write follows successful runtime selection, including when the runtime
is already current. If the runtime updates but the source write fails, the command
returns an explicit partial result. Dry-run writes no source files; outer
`update --dry-run` remains offline. The command does not commit or push. CI uses
these source changes after a reviewed commit reaches the build checkout.

`runtime build` retains private pins for an ordinary rebuild; `--source-pins`
explicitly applies the checkout's defaults. Reinstalling only SDLC does not
change provider versions. Debian packages are not individually pinned during
ordinary rebuilds, so retaining tool pins does not guarantee an identical image.

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
both operations; `--sdlc-only` performs only executable replacement.

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
sdlc runtime update --agent-tools
sdlc runtime update --agent-tools --update-dockerfile --source /PATH/TO/SDLC_SOURCE
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
context. Selected pins alter that context's Dockerfile. Successful selection
records the pins and exact build recipe in private `runtime.json`. The tracked
source remains unchanged unless `--agent-tools --update-dockerfile` is selected;
that option writes only the four agent tool pins described above. No update
command commits or pushes the source changes. Subsequent builds
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

### Address human PR feedback

After adding comments to a ready PR, resume its saved run:

```sh
sdlc resume --run RUN_ID --pr-feedback
sdlc resume --run RUN_ID --pr-feedback --dry-run
```

The same action is available on the explicit single-ticket command:

```sh
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-add-feature.md \
  --resume RUN_ID --pr-feedback
```

The controller reads inline review threads, review summaries and general PR
comments. It sends new or changed feedback to the original implementation
session in order. The implementor commits each correction separately, records
the comments it addresses, and supplies a reason when a comment cannot be
addressed. The controller posts that reason to the original thread or PR,
retaining reply acknowledgements so an interrupted resume can recover without
posting it twice. Human threads remain open for the reviewer to resolve.

Assessments returned with `checks_requested` remain provisional, including
requests for the configured suite alone. The same implementation session can
refine them after receiving verification or companion source. A final assessment
must still match the current unreplied comment revision. Only then can SDLC post
the reply; it does not resolve the thread.

An assessment accompanying a human question also remains provisional until the
session completes it after the answer.

Comment revisions, original inline context, assessments, candidate and signed
commit IDs, and replies stay in the private run journal. The dashboard shows
pending feedback, unanswered replies and unresolved comments. A later explicit
feedback resume handles new or edited comments; unchanged assessed feedback
does not start another provider turn.

Corrections use the existing repository checks, signing, publication and CI
gates. The final published head needs a fresh independent review, including
when assessment needs no source change. The original accounts, models, session,
history and all remaining budgets are retained. The action cannot be combined
with a budget extension, recovery initializer or answer file. If the controller
asks a human question, use `sdlc answer`; for an interrupted active cycle, use
ordinary `sdlc resume` after resolving its stop reason. Missing reviewer access
continues to stop at `awaiting_reviewer`.

If a comment changes before its reply is posted, the controller withholds the
stale reply. Use `--pr-feedback` again for that recorded feedback hold to fetch
the changed comment and continue the same review cycle.

`--dry-run` only previews the saved selection; it does not read GitHub feedback,
grant capacity, post replies or start a provider. This workflow does not merge
the PR. It starts from a ready saved PR and cannot bypass an earlier
implementation or independent-review failure.

Use beta.43 or newer for a run that has started feedback processing; earlier
CLIs cannot read its added feedback history. Saved runs without that history
remain compatible with this CLI.

Use beta.49 or newer once a journal contains provisional assessments or their
retained history. An ordinary resume can recover the matching legacy
`no pending human feedback revision` hold
by validating its retained response and requests before starting another native
turn. It preserves the original session, assessments, counters and limits.

### Diagnose a stopped run

```sh
sdlc diagnose --run RUN_ID
sdlc diagnose --run RUN_ID --json
```

This command reads saved state without resuming work or contacting a service.
It reports fixed state and hold categories, counters, evidence identities and a
suggested next action. Raw output, prompts, comment text, arbitrary commands,
account identities and credentials are omitted. A publication failure can also
identify a validated private diagnostic file by path, size and hash. Its raw
contents remain private and are never sent to the implementation session.

Stopped run commands show the next action and this diagnostic command. Inspect
the reason before resuming; the command does not clear holds or change budgets.

### Recover a legacy completed-review hold

`--initialize-review-repair-budget N` is available only for a single saved-run
`--resume`, with `N` from 1 to 3. It admits only a completed independent review
with stored findings, no unresolved questions or limitations, no existing review
allowance, and the exact legacy review repair-bound hold. Current source, passing
verification, selected inputs, published tree and CI must still match. Recovery
passes the stored findings directly to the original implementation session; it
neither reruns the completed reviewer nor resets counters, sessions or other
budgets. The allowance is persisted before implementation and cannot be reset.
A failed provider turn does not refund it; ordinary supported resume continues
that same pending assessment, subject to provider holds and the native-turn cap.

### Continue a run at its native-turn ceiling

Use `sdlc resume --run RUN_ID --extend-session-turn-budget N`, or add the same
flag to a single `sdlc run --reference REFERENCE --ticket TICKET --resume RUN_ID`.
`N` must be 1–24. This permits one extension for a saved run blocked at exactly
24 attempts by the implementation or independent-review native-turn limit.
The resulting ceiling, at most 48 attempts, is shared by both roles.

The original implementation session, cumulative attempts and phase budgets remain
recorded. The grant is audited and persisted before native work; failed native
calls consume attempts, and the extension cannot be repeated or reset. Resume
without the flag continues within the granted ceiling. This does not extend the
configured-check, CI or review repair allowances or bypass provider failures,
human questions, changed inputs or unfinished cleanup. It cannot be combined with
an answer file or another recovery initializer. `--dry-run` previews admission
without granting turns.


### Continue a saved run past local numeric limits

Use beta.45 or newer to grant a durable override for one saved run:

```sh
sdlc resume --run RUN_ID --override-run-limits
sdlc resume --run RUN_ID --override-run-limits --dry-run
```

The same flag is available with a single `sdlc run --reference REFERENCE
--ticket TICKET --resume RUN_ID`. Admission requires the exact supported local
numeric hold, its matching saved stage and corroborating accounting, or a locally
verified saved ready checkpoint on beta.48 or newer. It does not
admit provider access or usage failures, policy refusals, human questions, missing
inputs, unfinished cleanup or other operational holds. Legacy missing CI or
review budgets still require their existing explicit recovery initializer.
The flag cannot be combined with an answer file, PR feedback, a recovery
initializer or the finite session-turn extension. Dry-run previews admission
without saving a grant or executing work.

Once granted, ordinary resumes retain the override without repeating the flag.
At a ready checkpoint, the command saves permission without running a provider
or starting PR-feedback processing. Use `sdlc resume --run RUN_ID --pr-feedback`
when new comments are ready for assessment; the saved override remains in force
through that cycle and later cycles. This does not poll for future comments.
History and counters are retained rather than reset. Journals containing a
ready-checkpoint override require beta.48 or newer.
Formatter preparation honours the same saved repair-limit override; a pending
correction still requires agent review and a committed repair before full checks.
It lifts the controller ceilings on native calls, configured-check repair rounds,
CI repairs, independent-review handoffs, repeated verification feedback, retained
verification request IDs and PR metadata correction attempts. The original
implementation session, selected accounts and models, existing turn extension,
history, evidence and cumulative counters remain intact. The audit records the
original checkpoint and accounting; an unrecorded prior configured ceiling is
shown as unknown rather than inferred.

An overridden run has no implicit two-hour controller deadline. An explicit
`--timeout` still applies to that invocation, and a parent deadline or
cancellation still stops the controller with its checkpoint retained. This also
applies to later ordinary resumes of the overridden run.

When the saved native outcome was rejected only because the retained verification
ID limit was reached, resume admits and processes that saved batch without another
model call. Stable IDs preserve requests already admitted by an older controller;
companion source requests and PR feedback assessments receive their ordinary
handling. Failed batch admission leaves retained requests and evidence unchanged.

Provider authentication, access and usage holds remain enforced. Checks must
still verify the current clean committed source; signing, CI on the exact
published head, fresh independent review and human merge remain required.
Same-head repair and unchanged-failure guards still stop work, and PR metadata
must still satisfy the publication contract. Per-response verification batches
remain limited to 16 requests, request commands and bytes remain bounded, and the
private journal remains limited to 1 MiB. The override does not authorize account
switching, session restarts or bypassing provider restrictions.

Journals with an override require beta.45 or newer. Saved runs without an override
remain compatible, and their existing default ceilings remain in force.
