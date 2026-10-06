# Ticket workflow

The installed Go CLI coordinates work from a local Git repository through one
shared SDLC Docker image for provider workers, checks and publication, with a
separate official 1Password CLI image for signing-key resolution. macOS and Linux
are the primary hosts; Windows needs a Linux-container engine and does not yet
support the complete signing/publication route. The [user guide](user-guide.md)
covers setup and daily use; the [CLI reference](cli.md) lists exact commands.

## Implemented: one selected ticket

Install the CLI, build the shared runtime, authenticate the selected provider and
configure the [GitHub and signing profile](cli.md#github-login-and-signing), then
run `sdlc init` in the project and `sdlc github use --profile NAME` to save its
account/key selection outside the checkout. Review `.sdlc/project.json` checks.
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
Run preflight includes local Markdown requirements linked within the selected
work reference, including specifications, decisions and related tickets. Use
repeatable `--input` for additional exact requirements outside that link set.
Linked tickets supply context; they are not executed by a single-ticket run. The pinned
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
[provider rules](provider-usage.md). Supervised private trials have passed this
delivery/review/repair path with both implementation providers. Later pairing
and dashboard features need separate validation. Detached supervision remains
future work; complete connected feature trials remain unvalidated.

Before provider execution, SDLC freezes the selected GitHub profile, numeric
account and repository IDs, canonical repository name, effective project Git
name/email, approved Ed25519 public key and runtime image. `github pair`
explicitly binds a native account to a signing profile after checking public-key
registration; names can differ. Fresh runs use saved repository selection or a
unique owner pair or sole configured pair. `github use` can omit `--profile`
for a saved selection or sole configured profile; `--github-profile` can select a
registered pair for an unbound checkout but cannot override a conflicting saved
selection. Saved repository identity wins over origin changes; changed pair metadata requires deliberate reselection. Pair and
repository records contain public identity metadata in private external state,
without vault references or tokens. Existing frozen journals retain their legacy
same-name signing route.
Native GitHub credentials persist in separate private plaintext Docker volumes.
Host credentials do not override the selection. Each Codex/Claude cache still
holds one account per installation.

The delivery sequence is:

1. The implementation follows `tjpeel-engineering-implement` and its testing,
   verification and whole-ticket local review gates. It makes unsigned candidate
   commits and requests controller checks through a structured handoff.
2. The controller runs configured check argument arrays against a disposable copy
   of the committed tree. Passing evidence resumes the exact original native
   implementation session. Changed trees need fresh checks.
3. After local review and `tjpeel-pr-draft` metadata, the trusted Docker publisher
   imports the candidate bundle into controlled Git metadata. It requires passing
   evidence for the exact candidate head/tree, signs every delivered commit with
   the frozen identity, verifies the approved key and checked tree, and rechecks
   account/repository IDs, base and remote head. An exact push
   lease rejects competing changes. Its read-only GitHub profile cache and the
   signing key resolved by the separate official 1Password Service Account CLI
   remain outside provider/check workers. The key enters over stdin into tmpfs
   and its file is removed before push. No host authentication fallback is used.
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
The disposable daemon uses the classic `overlay2` image store. Its backing
filesystem must support OverlayFS; startup fails rather than falling back to VFS,
which cannot build layers containing Unix sockets left by some build tools.

The authenticated provider still has shell access. Instructions prohibit running
repository scripts, tests and dependency installation there, but cannot enforce
that separation. Repository code could read the provider cache or disclose other
accessible data. Network destination restrictions and credential encryption are
not implemented. See the [security risks](../README.md#security-boundary-and-risks).

The signing bootstrap is an explicit mode-`0600` plaintext bearer-token file
outside repositories; the private key stays in the dedicated vault until resolved
into temporary container storage. SDLC provides no OS credential store or volume
encryption. The separate resolver has a 35-second internal timeout, managed
labels and automatic container removal. Native GitHub login needs no App
installation; Apps remain an optional alternative when administration permits.
Offline tests cover native cache/logout and disposable signed publication with
fake GitHub replies. Supervised private trials have also passed connected signed
delivery and review/repair. Use the [Docker GitHub test guide](github-docker-test.md)
to validate a new account/key setup, including organisation OAuth/SSO policy.

## Progress and resume

Controllers can overlap in one repository using separate captured workspaces and
branches. Codex and Claude use separate cache leases; the same provider's native
operations queue through cleanup. Queue waiting is cancellable and appears in
the dashboard. GitHub profiles have independent cache leases, with authentication/publication
serialized within each profile on the same runtime image. Runtime builds require
exclusive ownership of the shared runtime.
Each integration check invocation uses its own Docker daemon and namespace,
allowing the same internal service ports across invocations. Host capacity,
external services, account limits and merge conflicts still need coordination.

The host controller remains a foreground process. Closing its terminal can stop
execution; detached controller supervision and restart reconciliation are not
implemented. Closing only the dashboard does not stop a running controller.

`sdlc dashboard` watches registered local runs across repositories without
contacting providers or controlling execution. It separates controller heartbeat
from output activity, displays attention reasons and retains unavailable records.
Overviews show ten runs per page, with completed work below attention items and
active work. Use `--page N`, or n/p followed by Enter in a live terminal, to page
history. Select details with `--run RUN_ID`, optionally adding `--logs` for a bounded
private tail. `dashboard forget --run RUN_ID` removes only the registration after
checking controller ownership. Saved work has no automatic expiry. Private
`dashboard export` reports and full backup guidance are in [run history](run-history.md).
Optional local macOS notifications use `--notify desktop --sound` on the run or
watching dashboard. Dashboard alerts also cover stale controllers; delivery
failure cannot stop ticket execution. Usage fields are optional native measurements; aggregate counters never
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
image, shared instructions and frozen GitHub/signing identity. Implementation repairs resume the exact recorded
native session; independent review always starts fresh. Retain the journal and
native session files. See [resume commands](cli.md#logs-questions-and-resume).

For an unpublished implementation question caused by a missing file, inspect
`sdlc inputs --run RUN_ID`, then attach it with `--add RELATIVE_PATH --dry-run`
and repeat without `--dry-run`. Attachment records new hashes without replacing
existing inputs or starting a provider. Then answer the recorded question. An
interrupted attachment must be completed before answer or resume.

## Feature scheduling and stacked PRs

`sdlc run --reference REFERENCE --all --parallel 2` schedules pending tickets from
one durable feature checkpoint. Add `--watch` to keep the foreground controller
observing human merges; otherwise it completes currently unblocked work and
exits. Repeat the command to continue. `--parallel` defaults to `1` and accepts
`1` through `8`; same-provider cache locks can still queue. Optional
`--alternate-providers` alternates implementers in planned ticket order, retaining
an opposite-provider reviewer for every ticket.

A new feature starts from a clean checkout of the published integration revision.
Its optional private `plan.json` declares exact filenames, lower-first priority,
`depends_on` names and relative `touches` prefixes that serialize known overlap.
Without metadata, numeric filename order is used and tickets are independent.
See [feature plans and commands](cli.md#run-a-feature) for the schema.

A ticket with one parent starts when that parent has reached signed, tested and
reviewed `ready`, using its recorded branch/head as source and PR base:

```text
main
  ticket-one    PR base: main
    ticket-two  PR base: ticket-one
```

A ticket with multiple parents waits until all parents have merged into the
integration branch. No controller merges or approves automatically.

When a parent squash-merges or the integration base moves, the controller
restacks only the affected ticket's own commits. It verifies the old PR head and
owned branch and publishes with an exact lease, then repeats signing, isolated
checks, current CI and fresh opposite-provider review. Conflicts go back to the
original native implementation session. A product decision stops for a human.
Unrecognised external changes stop for reconciliation rather than being overwritten.

Compatible existing runs are adopted or resumed without duplicate work. One
feature checkpoint freezes the plan, models, accounts, runtime, common inputs,
each ticket's linked requirements and checks. Explicit file recovery applies only
to the selected paused ticket and retains the original frozen hashes.
Human-attention states stop feature execution; resolve the recorded
individual run with its resume/answer command, then repeat the feature command.
The dashboard retains individual-run reporting. Detached supervision remains
future work. Offline tests cover feature scheduling and reconciliation; a
complete connected feature trial remains to be validated.

The [ticket-stream prompt](prompts/implement-ticket-stream.md) remains a design
template for a harness with prepared inputs. The SDLC repository itself develops
on `main`; generated ticket branches belong to the project requesting work.

The [research archive](../research/README.md) preserves earlier investigations
and prototypes. Their commands do not define current CLI behaviour.
