---
name: sdlc-drive
description: Start and monitor authorised local SDLC ticket runs through implementation, checks, signed draft PR publication, CI and independent review. Use for SDLC execution, status and checkpoint recovery; not for directly implementing code or reviewing a PR.
---

# Drive SDLC work

This is a proposed host-harness skill for the current CLI. It is not installed or
bundled into SDLC's worker image. Use it in the outer harness that controls the
host terminal. Never invoke it inside an SDLC implementation or review worker.

Let the Go controller run the engineering workflow. Do not spawn agents to poll
status, duplicate its review, or reproduce its publication steps.

## Establish the requested work

1. Identify the project checkout, work reference and exact numbered ticket. Read
   applicable repository instructions. For a continuation, identify the existing
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

Omit `--input` or `--docker-tests` when they are unnecessary. Inspect the source,
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
| `waiting_for_human` | Present the recorded current questions. Obtain the user's answer, save it in a private file, then resume the same run with `--answer-file`. Never fabricate a product decision. |
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
image, identity, inputs or roles mid-run. External PR/base changes require
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

The report is private and is not a resumable backup. `dashboard forget` removes
only the dashboard index entry; it retains the saved work. Do not run it merely
to make a failure disappear.

The CLI currently executes one ticket per invocation. There is no series or
alternating-provider flag. For explicitly requested independent concurrent work,
track each run separately and use unique branches. Jobs have separate workspaces
and Docker check daemons; a shared native provider cache serialises that
provider's operations. Do not promise simultaneous access to the same cache.
Dependent tickets need an explicitly selected predecessor source/base; never
advance them merely because discovery listed them next.
