# Ticket workflow

The installed Go CLI coordinates work from a local Git repository through one
shared SDLC Docker image. macOS and Linux are the primary hosts; Windows needs a
Linux-container engine. [The CLI guide](cli.md) covers setup and exact commands.

## Implemented: one selected ticket

Install the CLI, build the shared runtime, authenticate the selected provider,
then run `sdlc init` in the project. Review the checks in `.sdlc/project.json`.
Shared instructions are private installation settings; each run captures their
current body. The image contains pinned public skills and agent definitions.
Runtime status reports available updates; automatic update prompts remain future
work.

The work layout is:

```text
.sdlc/work/<reference>/
  decisions.md
  specification.md
  tickets/
    01-ticket-title.md
    02-ticket-title.md
```

`sdlc work --reference REFERENCE` discovers numbered filenames in numeric order.
`sdlc run --reference REFERENCE --ticket NUMBERED_FILE` selects exactly one ticket.
Add exact specification or decision paths with repeatable `--input`; SDLC does
not automatically supply them or execute the remaining tickets. The pinned
implementation skill retains its eligibility, dependency, missing-input and
human-decision gates. A run consumes prepared requirements rather than creating
decisions, specifications or tickets.

The controller captures current source and selected requirements into private
storage, records source/base revisions and creates a unique destination branch.
Local source changes are preserved in a separate baseline commit. It leaves the
initiating checkout intact. The selected local base must match GitHub at
publication, and the checkout must include that selected base revision. Capture
currently rejects linked worktrees, source symlinks,
submodules and unsafe Git metadata.

Codex is the default implementer (`gpt-6.1-sol`, medium effort), with Claude
(`claude-opus-5-5`, high effort) as independent reviewer. Selecting Claude uses
`claude-sonnet-5-5` at medium effort and Codex `gpt-6.1-sol` at high effort for
review. Lead flags and optional private `models.json` settings select full model
identifiers. Native clients load the pinned subagent model/effort policy. Model
identity is checked when the client reports it; no report leaves it unverified,
and providers may cap effort. See [model settings](cli.md#models-and-pinned-agents).

The implementation account must have stored login. Missing reviewer login does
not block draft PR delivery and CI: the run then pauses at `awaiting_reviewer`
until that opposite provider is authenticated. It never falls back to the
implementer for independent review. These account-authenticated runs support the
owner's local single-user CLI job and reject CI execution; see the
[provider rules](provider-usage.md). No live provider run has yet validated the
new execution path.

The delivery sequence is:

1. The implementation follows `tjpeel-engineering-implement` and its testing,
   verification and whole-ticket local review gates. It makes unsigned candidate
   commits and requests controller checks through a structured handoff.
2. The controller runs configured check argument arrays against a disposable copy
   of the committed tree. Passing evidence resumes the exact original native
   implementation session. Changed trees need fresh checks.
3. After local review and `tjpeel-pr-draft` metadata, the host controller recreates
   candidate commits using host Git identity/signing settings, verifies enabled
   signatures and confirms the published tree matches the checked tree. Host Git
   and `gh` push the branch and create or update its draft PR. No secret-store
   retrieval is implemented; configure host signing and GitHub authentication.
4. Every reported CI check must pass for the recorded current PR base/head.
   Missing checks wait for up to two minutes for CI to start, then block with a
   retained checkpoint; configure CI and resume. Pending checks wait; failed, cancelled or skipped
   checks return a bounded repair. External PR changes stop for reconciliation.
5. A fresh opposite-provider session uses `tjpeel-pr-review` on the exact published
   snapshot and selected requirements, without the implementation transcript or
   publication credentials. Actionable findings return to the original
   implementation session. Repairs repeat checks, publication, CI and a fresh
   independent review.
6. Passing current CI and a complete review without findings produce `ready` for
   human review. The PR stays draft. SDLC never merges or approves automatically.

The normal repair bound is three rounds. Both leads use the pinned
`read_low`, `read_medium` and `read_high` roles where their skills direct;
implementation also uses `write_medium`. The controller's authority to publish
one ticket does not authorise rewriting unrelated work.

## Repository checks and Docker integration tests

The initial target includes .NET 10 `.slnx` solutions with API or consumer code,
unit tests and integration tests. Use the repository's reviewed check commands
and orchestration. Initialization discovers `.slnx` files but does not generate
.NET checks. Configure exact argument arrays in `.sdlc/project.json` that fit
`global.json`, the SDK and the selected VSTest or Microsoft Testing Platform
runner. Do not infer test separation from project names. See Microsoft's
[dotnet test guidance](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-test).

Checks and dependency installation run in separate disposable workers without
provider caches, host signing keys, SSH agents or GitHub publishing credentials.
Configured `input_files` supply exact test configuration to those workers;
untracked files stay out of the provider workspace unless explicitly selected.
Tracked files remain visible as normal source. Explicit `--input` requirements
are visible to both providers. Keep credentials
out of both; path checks cannot detect every secret in file contents.

`--docker-tests` explicitly enables a separate privileged Docker-in-Docker daemon
with a job network and disposable workspace, socket and daemon-data volumes.
The host Docker socket is never mounted. This mode requires trusted integration
tests; a privileged daemon is not containment for hostile code. Cleanup runs on
success, failure and cancellation; cleanup errors stop delivery.

The authenticated provider still has shell access. Instructions prohibit running
repository scripts, tests and dependency installation there, but cannot enforce
that separation. Repository code could read the provider cache or disclose other
accessible data. Network destination restrictions and credential encryption are
not implemented. See the [security risks](../README.md#security-boundary-and-risks).

## Progress and resume

`sdlc dashboard` watches registered local runs across repositories without
contacting providers or controlling execution. It separates controller heartbeat
from output activity, displays attention reasons and retains unavailable records.
Select details with `--run RUN_ID`, optionally adding `--logs` for a bounded private
tail. Usage fields are optional native measurements; aggregate counters never
stand in for current context occupancy. See the [dashboard guide](cli.md#watch-local-runs).

Private state is stored under:

```text
.sdlc/work/<reference>/runs/<ticket-stem>/<run-id>/
```

The journal records stages, input hashes, source/check evidence, publication
revisions and the original implementation session ID. Native structured JSONL
streams to the terminal and private event files; diagnostics and check logs stay
with the run. Source snapshots and native session storage survive container
removal. Keep this ignored directory and terminal output private.

A structured human question stops at `waiting_for_human`. Resume the recorded run
with `--answer-file` only after answering its questions. Operational failures,
cancellation, timeout, missing inputs, incomplete handoffs, access/usage limits
and policy refusals also retain a stopped checkpoint. Repair the cause before
resume; never switch identities or repeatedly restart to evade provider limits.

Resume preserves the original provider/model settings, checks, inputs, runtime
image and shared instructions. Implementation repairs resume the exact recorded
native session; independent review always starts fresh. Retain the journal and
native session files. See [resume commands](cli.md#logs-questions-and-resume).

## Future: stacked-ticket streams

The supported command runs one ticket. The agreed stream design remains future
work: one branch and draft PR per ticket, with each ticket starting from its
verified predecessor and using that predecessor branch as PR base:

```text
main
  ticket-one    PR base: main
    ticket-two  PR base: ticket-one
      ticket-three  PR base: ticket-two
```

A future controller must retain the original implementation owner, verify explicit
dependencies, wait for every current PR's CI, and review both each incremental
slice and the complete stack against its original `main` revision. An earlier
repair invalidates affected descendants' CI and review evidence; restacking must
preserve ticket boundaries and reconcile changed remote state. Rebase and
force-push need explicit authority. A finding requiring a product decision stops
for a human answer. No automatic merge is planned.

The [ticket-stream prompt](prompts/implement-ticket-stream.md) is a design template
for a harness with prepared repositories and inputs, not the prompt submitted by
`sdlc run`. Secret-store integration, stream orchestration, broader recovery and
cleanup management, and an outer-harness `sdlc` skill remain deferred. The SDLC
repository itself continues development on `main`; ticket branches belong to the
project requesting work.

The [research archive](../research/README.md) preserves earlier investigations
and prototypes. Their commands do not define current CLI behaviour.
