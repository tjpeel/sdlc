---
name: sdlc-drive
description: Start and monitor authorised local SDLC ticket or feature runs through implementation, checks, signed draft PR publication, CI and independent review. Use for SDLC execution, status and checkpoint recovery; not for directly implementing code or reviewing a PR.
---

# Drive SDLC work

This is a proposed host-harness skill for the current CLI. It is not installed or
bundled into SDLC's worker image. Use it in the outer harness that controls the
host terminal. Never invoke it inside an SDLC implementation or review worker.

Let the Go controller run the engineering workflow. Do not spawn agents to poll
status, duplicate its review, or reproduce its publication steps.

## Establish the requested work

1. Identify the project checkout, work reference and exact numbered ticket or
   requested whole feature. Read applicable repository instructions. For a continuation, identify the existing
   run before launching anything. Never turn a request to inspect status into a
   new implementation run.
2. Establish the authorised scope from the conversation: connected provider use,
   configured vault access, signing and GitHub branch/draft PR publication. Reuse
   existing authorisation. If a necessary action is outside that scope, prepare
   the offline selection first and ask only for the missing authority.
3. Run `sdlc --version` and consult `sdlc --help` and command help when needed.
   From the project, use `sdlc work --reference REFERENCE` to find ticket order.
   Discovery checks paths and filenames; it does not establish dependency
   readiness or the correctness of requirements. Read the selected ticket and
   only the extra specification/decision files needed for it.
4. Inspect `.sdlc/project.json` checks and `input_files`. A root `.env` needed by
   Compose must be selected as a check input; it is a copied file, not an exported
   environment. Do not put secrets in ticket inputs. Do not invent tests or
   reject work using a stricter ticket format than the CLI requires.
5. Check `sdlc runtime status --offline`, `sdlc auth status --all`,
   `sdlc github status` and the paired `sdlc signing status --profile NAME`.
   These are local checks, not proof of remote validity. Use existing successful
   setup evidence; do not retrieve the signing key on each status poll. Missing
   setup goes back to the user through the official onboarding flow. Never read
   native credential files, request pasted tokens, or run `gh auth token`.

## Select and launch one ticket

Codex implements by default and Claude reviews. `--provider claude` reverses the
roles. The implementation provider must have login; a missing opposite reviewer
can pause the run after draft publication and CI. Prefer both logins before
starting work intended to reach human review without intervention.

Use configured model/effort defaults unless the user requests another supported
selection. The controller does not need a reasoning model. The pinned engineering
skills choose agents for mapping, writing and validation. Do not substitute a
more expensive lead simply to supervise progress. A requested model and a native
reported model are different evidence; do not claim unreported identity or effort
was confirmed.

Preview the exact selection, adding only needed inputs and flags:

```sh
sdlc run --reference REFERENCE --ticket 01-ticket-title.md \
  --input .sdlc/work/REFERENCE/specification.md --docker-tests --dry-run
```

Run preflight includes Markdown requirements linked within the selected work
reference. Use `--input` for additional exact requirements; do not select whole
folders. Omit `--input` or `--docker-tests` when they are unnecessary. Inspect the source,
repository, branch/base and role selection. Resolve a wrong repository account
using `sdlc github use --profile NAME`; do not silently change the pairing or
signing key. A dry run performs no connected access validation.

Launch the selected command without `--dry-run` in a managed host terminal or
process session whose lifetime you can maintain and monitor. Preserve the full
run ID and process-session identifier. Keep the host awake and the controller
alive; SDLC does not provide detached supervision. A dashboard is a viewer, not
a replacement for the controller. Do not report a background shell launch as
durable unattended execution.

Keep a small private checkpoint: checkout, reference, ticket, full run ID,
controller session, selected roles and last meaningful state. Do not retain
credential values. The run freezes its inputs, image and account/key identity;
host edits or runtime updates do not change that run.

## Launch and continue a feature

For authorised whole-feature work, use the controller's scheduler:

```sh
sdlc run --reference REFERENCE --all --parallel 2 --dry-run
sdlc run --reference REFERENCE --all --parallel 2 --watch
```

Add approved common `--input` files and check flags to both commands. A new
feature requires a clean checkout at the published integration revision. Review
the optional private `plan.json` for exact filename dependencies, lower-first
priorities and overlapping `touches` prefixes; consult the
[CLI feature guide](../../cli.md#run-a-feature) for its schema. Do not infer
serial dependencies from filename order alone.

`--parallel` defaults to `1` and accepts `1` through `8`. With
`--alternate-providers`, planned ticket order alternates implementers starting
from `--provider`; each uses the opposite reviewer. Do not combine alternation
with explicit model overrides. Native cache locks can still queue.

A single-parent child may start from the parent's signed, tested and reviewed
`ready` draft PR and targets that parent's branch/head. Multiple-parent children
wait until all parents merge into the integration branch. The controller
restacks only owned ticket commits after parent squash merges or base movement,
using recorded PR heads and exact leases, then repeats signing, checks, CI and
fresh review. Conflicts return to the original implementer session; human
product decisions remain human decisions. Do not perform a separate rebase or
force push from the harness.

Keep the foreground feature controller alive when using `--watch`. Without it,
currently unblocked work completes and the command exits. Repeat the command to
continue its durable checkpoint: compatible existing runs are adopted or
resumed without duplication, and plan, models, accounts, runtime, common inputs
and checks remain frozen. A human-attention stop ends feature execution. Resolve
the individual recorded run using its resume/answer command, then rerun the
feature command. Track child run IDs through the individual-run dashboard; do
not claim the dashboard has a separate feature view.

Feature dry-run stays offline and reads selected Markdown only to validate
linked requirements. It does not read credentials or make model calls.
Offline tests do not establish that a connected feature trial
has passed.

## Monitor with little context

Poll the controller process and a compact local snapshot:

```sh
sdlc dashboard --json --run RUN_ID
```

Use the full recorded ID. An overview contains only ten rows per page; when
discovering runs, follow `pages` with `--page N` rather than assuming page one is
complete. Poll at a bounded interval such as 10 seconds, backing off when nothing
changes. Summarise transitions, current role, CI and attention states. Do not load
full journals, source copies or transcript tails on every poll. Use
`sdlc dashboard --run RUN_ID --once` for a specific problem; add `--logs` only when
needed and keep the output private. Logs, summaries and questions can contain
private code or secrets.

| Observation | Action |
| --- | --- |
| Live implementation, checks, publication, CI, review or repair | Continue monitoring; the controller owns the next step. |
| Waiting for the provider cache | Let the lease queue clear; queue time counts toward the invocation timeout. |
| `waiting_for_human` | Present the recorded current questions. For a missing file in an unpublished implementation, inspect `sdlc inputs --run ID`, preview `--add RELATIVE_PATH --dry-run`, then attach the authorised file. Obtain the user's answer and resume the same run. Never fabricate a product decision. |
| `awaiting_reviewer` | Explain which opposite provider needs official login, then resume after it is available. Do not review with the implementer as a fallback. |
| `blocked` or `failed` | Read the specific stop reason. Fix only an authorised operational cause; resume once it has changed. Do not loop unchanged failures. |
| Stale, stopped unexpectedly, or unavailable | Confirm the original controller is no longer alive before recovery. Do not start a second owner or treat missing metadata as success. |
| Provider policy refusal, access denial, account restriction or usage exhaustion | Stop and report the cause. Do not switch identities, restart repeatedly, replay tokens or bypass the provider's restrictions. |
| `ready` | Check the completion conditions below before reporting success. |

Resume from the initiating project with the original reference, ticket and ID:

```sh
sdlc run --reference REFERENCE --ticket 01-ticket-title.md --resume RUN_ID
```

For a recorded human question, append `--answer-file /PRIVATE/PATH/answer.md`.
Create that answer as an owned mode-0600 regular file without links in an owned
mode-0700 directory outside Git. Use a private editor or file-writing tool; never
place the answer text in shell arguments or a public file. After safe consumption,
remove the extra answer copy only when authorised; the run retains its own
private history.
Resume retains frozen choices and the original native implementation session.
Do not start fresh merely because a repair is needed, and do not try to change
image, identity or roles mid-run. Only the supported offline `inputs --add`
operation may append missing requirements to an eligible paused run; it must
preserve original hashes and leave an audit record. External PR/base changes require
reconciliation, not a force push.

## Recognise completion and retain evidence

Success means the controller has finished successfully, the available run
journal reports `ready`, a draft PR URL is present, CI passed for the recorded
published head, and there are no unresolved questions, findings, limitations or
stop reasons. The
controller enforces checks against the candidate tree, signed publication and
independent review. Signed commit hashes can differ from unsigned check hashes;
do not require those hashes to be equal or waive a tree mismatch.

Report the run ID, implementation/review providers, PR link, checks/CI outcome and
repair rounds if available. State that it is ready for human review. Do not merge,
approve or remove draft status. Do not claim exact cost or context usage when
the native clients did not report it.

Keep artifacts unless the user authorises cleanup. For a requested portable
summary, export to an existing owned mode-0700 directory outside Git:

```sh
sdlc dashboard export --run RUN_ID --to /PRIVATE/PATH/run-reports
```

`dashboard remove` is the recommended spelling; `dashboard forget` remains an alias.
Both hide registry entries and retain saved files.

The report is private and is not a resumable backup. `dashboard remove --run ID`
hides only the registry entry; saved work remains and can still block an update.
Do not run it merely to make a failure disappear. `dashboard remove --all` (also `forget --all`) previews
clearing stopped registrations across the installation; `--scope project`
narrows it, `--yes` clears them and `--dry-run` explicitly previews.

`storage purge --run ID|--all [--yes|--dry-run]` deletes saved files.
It previews permanent deletion of stopped saved runs from the current Git
repository, including unregistered runs. `--yes` authorises deletion. Affected
feature series checkpoints are also removed; report their abandonment from the
preview. Tickets and specs remain. Live runs or series refuse removal, and
`--all` excludes archived references and credentials.

`work archive --reference TASK-123 [--dry-run]` moves the entire reference tree
to a unique directory under `.sdlc/work/.archive/` and removes its run registrations.
This reversible local move needs no `--yes`, retains complete evidence and frees
the source name for fresh scoping. Live runs or series refuse archiving.
Archived work is excluded from active discovery, resume and runtime guards;
a later update may replace its image. Do not promise later resumability or
describe it as a portable backup.

For a feature, report each recorded ticket state and PR link. A completed
non-watch invocation can leave children waiting for dependencies or human
merges; report those remaining gates. Do not describe the whole feature as
complete merely because one ticket is `ready`. Keep checkpoints and native
sessions unless the user authorises cleanup.
