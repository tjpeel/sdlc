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
   implementation session. The implementation can submit structured
   `verification_requests` for additional machine work without asking the user
   to run it. Generic requests supply argument arrays; regression requests also
   name original production files and an exact captured source revision. The
   controller runs the commands against the candidate, then against a separate
   copy retaining candidate tests with only those original files restored. The
   candidate must pass; the final baseline command must return the specified
   nonzero exit and literal assertion markers. Setup, compiler, formatter and
   runtime failures cannot satisfy that comparison.
   Requests remain outstanding across turns and resume. Publication requires
   passing project checks and all additional requests for the exact candidate.
   Unchanged requests reuse completed evidence; temporary runtime failures can
   retry after recovery. Changed trees or requests need fresh checks. A third
   request for identical evidence without progress blocks with a diagnostic.
   To obtain a formatter correction without running repository tools in the
   authenticated worker, request a source artifact:

   ```json
   {"id":"format-source","purpose":"review the formatter correction","commands":[["dotnet","tool","restore"],["dotnet","csharpier","format","src/Example.cs"]],"baseline":null,"source_patch":{"paths":["src/Example.cs"]}}
   ```

   Commands run on a fresh source-only copy and HOME without private inputs,
   credentials, shared build state or a Docker daemon. A fixed network-disabled
   collector returns a private unified diff, committed head/tree and content
   hashes for up to 16 selected committed UTF-8 text files. Each file is limited
   to 256 KiB, each diff to 32 KiB and artifact JSON in the journal to 128 KiB.
   Frozen inputs, private/configuration paths, unselected committed changes,
   mode changes, links, deletions and binary files are refused. Source artifacts
   cannot be combined with regression baselines; ordinary requests omit
   `source_patch` or use `null`.
   A nonempty artifact is unapplied data, not passing evidence. The original
   worker first matches the current committed head/tree and selected source
   hashes to the artifact and its `before_sha256` values. It discards stale
   artifacts, reviews and applies the intended correction, commits it and requests
   the full configured suite again. The controller never changes implementation
   source. Empty corrections can pass the persistent request for the current
   committed candidate. Failed commands, failed cleanup and stale candidates
   cannot export artifacts. Raw logs remain private.
   A diff-budget overflow returns a bounded refinement diagnostic without an
   artifact. Reduce both `source_patch.paths` and formatter arguments to the
   same named files, or split the selection into smaller requests. The controller
   does not retry an unchanged request automatically; other collection errors
   retain their operational stop behavior.
3. After local review and `tjpeel-pr-draft` metadata, new runs validate the exact
   reference in the title and an explicit body field, plus all seven description
   headings in order with non-empty content. A default Markdown PR template in
   the repository root, `docs/` or `.github/` supplies its own heading structure.
   The contract is frozen at capture; older saved runs keep their original
   policy. Invalid metadata returns bounded correction feedback to the same
   implementation session without repeating checks for unchanged source.
   The trusted Docker publisher imports the candidate bundle into controlled
   Git metadata. It requires passing
   evidence for the exact candidate head/tree, signs every delivered commit with
   the frozen identity, verifies the approved key and checked tree, and rechecks
   account/repository IDs, base and remote head. An exact push
   lease rejects competing changes. Its read-only GitHub profile cache and the
   signing key resolved by the separate official 1Password Service Account CLI
   remain outside provider/check workers. The key enters over stdin into tmpfs
   and its file is removed before push. No host authentication fallback is used.
   Git commands disable automatic maintenance while the controller owns the
   publication cache. This keeps imported objects stable through strict integrity
   checks and signing; retained candidate and signature checks still apply.

4. Every reported CI check must pass for the recorded current PR base/head.
   Missing checks wait for up to thirty minutes for CI to start, then block with a
   retained checkpoint; configure CI and resume. Pending checks wait; failed, cancelled or skipped
   checks return a bounded repair. External PR changes stop for reconciliation.
5. A fresh opposite-provider session uses `tjpeel-pr-review` on the exact published
   snapshot and selected requirements, without the implementation transcript or
   publication credentials. Actionable findings return to the original
   implementation session. Repairs repeat checks, publication, CI and a fresh
   independent review.
   Frozen companion source and its complete history accompany the snapshot.
   A source-only reviewer request stages additional same-owner source through
   the existing controller reader and starts another fresh review after exact
   PR and CI validation. It retains the original implementation session and
   cumulative accounting without spending repair grants. Access questions
   remain owned by review.

6. Passing current CI and a complete review without findings produce `ready` for
   human review. The PR stays draft. SDLC never merges or approves automatically.

Failed CI checks are inspected by the trusted GitHub publisher through the
frozen runtime's official `gh api` client and selected read-only GitHub profile.
The current collector mounts no worker source, Docker socket or signing material.
It checks the repository, published head, check identity and Actions run attempt
before returning relevant check output, annotations, failed steps and job-log
excerpts to the original implementer. It fetches only verified GitHub API
resources, never arbitrary URLs from diagnostic text.

The collector masks recognised credential material and values from hash-verified
retained check-only inputs before excerpting. Generic text remains untrusted,
private project evidence; masking cannot guarantee that all unknown secret
formats are absent. Masking and truncation are explicit. Response bounds,
malformed data, input drift, changed check/run identities, API errors and cleanup
failures hold the run before any repair grant.

Native Sonar findings remain a separate structured channel: bounded severity,
verified rule or diagnostic kind, committed source path and positive location.
Cognitive-complexity findings can include verified current and allowed values.
A GitHub Actions workflow name containing Sonar does not establish native Sonar
analysis. The agent receives relevant failure evidence and chooses its own repair.

Ordinary resume can refresh a saved native stop caused by missing CI context
without granting another repair or changing the original session. Its private
audit freezes both the blocked summary and limitations and binds the collected
context to the candidate and failed publication. An ambiguous dispatch stays
held. Full local checks, signing, current CI and fresh independent review remain
required after any correction.

CI repairs have a separate persisted budget of at most twelve grants. Each grant
records its failed published head, so unchanged-head failures cannot obtain
another repair turn. Total round history and native dispatch accounting
remain intact, as do configured-check and review bounds. New runs initialize CI
accounting before publication. Published legacy journals with unknown CI
accounting do not receive an automatic fresh budget. For an existing legacy
`blocked`/`ci` repair-bound hold with the original session and publication, use
`sdlc run --reference REFERENCE --ticket NUMBERED_FILE --resume RUN_ID --initialize-ci-repair-budget`
once. Other holds and already initialized or exhausted CI budgets reject this
option; it does not reset historical counters or renew other budgets.

The normal configured-check and review repair bound is twelve rounds. Both leads use the pinned
`read_low`, `read_medium` and `read_high` roles where their skills direct;
implementation also uses `write_medium`. The controller's authority to publish
one ticket does not authorise rewriting unrelated work.

## Repository checks and Docker integration tests

The initial target includes .NET 10 `.slnx` solutions with API or consumer code,
unit tests and integration tests. Use the repository's reviewed check commands
and orchestration. Initialization prepares supported .NET check sequences from
required-check guidance, simple CI or manifests; `init --force` refreshes inferred
checks while preserving other project preferences and saved runs. Review exact
argument arrays in `.sdlc/project.json` that fit
`global.json`, the SDK and the selected VSTest or Microsoft Testing Platform
runner. Unsupported setup and ambiguous test output paths need explicit configuration. See Microsoft's
[dotnet test guidance](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-test).


For an xUnit v3 project using the native .NET 10 Microsoft Testing Platform
runner, a focused check can use the following argument array after the build:

```json
["dotnet", "test", "--project", "tests/Example.Tests/Example.Tests.csproj",
 "--no-build", "--", "--filter-method",
 "Example.Tests.CountTests.ReturnsCount", "--minimum-expected-tests", "1"]
```

`--filter-method` matches the fully qualified method name. Combining a class
filter with only a short method name can select no tests. Check the result's
executed test count; do not accept an empty selection as verification. With
.NET 8/9 MTP integration, use the positional project form and the `--` separator.
For VSTest, use its own `--filter` syntax, for example
`FullyQualifiedName=Example.Tests.CountTests.ReturnsCount`. Consult the selected
runner's `--help`, [Microsoft's runner guidance](https://learn.microsoft.com/en-us/dotnet/core/testing/unit-testing-with-dotnet-test)
and [xUnit's MTP documentation](https://xunit.net/docs/getting-started/v3/microsoft-testing-platform).
Use `--no-build` only when this candidate phase already produced the requested
configuration and framework's build outputs.

SDLC's fixed SDK check workers set
`DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE=true` and
`DOTNET_SKIP_WORKLOAD_INTEGRITY_CHECK=true`. These control separate SDK activities;
the latter skips the first-run workload check that can emit the workload
verification warning. No workload update runs in the read-only worker. Projects
requiring installed workloads need an appropriate runtime; they can explicitly
override the integrity flag in their check command. Nested SDK containers use
their own image/environment settings, so set these flags there when appropriate.
See [Microsoft's environment-variable reference](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-environment-variables).

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
branches, or operate independently from different folders. Codex and Claude calls
have no provider execution reservation: multiple calls to the same provider can
overlap. Each client writes its own native cache, with brief transactions for
snapshot and refresh persistence. Conflicting refreshes retain recovery material
and stop only the affected invocation. GitHub profiles have independent cache leases, with authentication/publication
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
private tail.

`dashboard remove` is the recommended spelling; `dashboard forget` remains an alias.
Both hide registry entries and retain saved files.

`dashboard remove --run RUN_ID` hides only the registration after
checking controller ownership. Saved files remain and can still block updates.
`dashboard remove --all` previews clearing stopped registrations across the
installation; use `--scope project` to narrow it and `--yes` to clear them.
`--dry-run` explicitly previews.

`storage purge --run RUN_ID` or `storage purge --all` previews permanent
deletion of stopped saved runs in the current repository, including unregistered
runs. `--yes` deletes them. Affected feature series checkpoints are also removed,
and the preview reports their abandonment. Tickets and specs remain. Live runs
or series refuse removal; archived references and credentials are excluded.

`work archive --reference TASK-123 [--dry-run]` moves the whole reference tree to
a unique directory under `.sdlc/work/.archive/`, removes its run registrations
and frees the source name for fresh scoping. It retains all evidence and refuses
live runs or series. This reversible local move needs no `--yes`. Archives are
excluded from active discovery, resume and runtime guards; their image may later
be replaced, so this is not a portable backup or a promise of resumability.

Saved work has no automatic expiry. Private `dashboard export` reports and full
backup guidance are in [run history](run-history.md).
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
`1` through `8`. Free slots take the next eligible ticket while other tickets
remain active; provider calls can overlap across tickets and features. Optional
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

A ticket with multiple parents can start from a tested combination of their
ready changes and current integration. The controller creates a signed derived
branch from exact recorded parent revisions and checks the source aggregate
before starting the child. Publication verifies that its signed counterpart
has the same tree, retaining both revisions in the checkpoint. The child's PR targets that branch, keeping its diff
limited to its own work. No controller merges or approves the parent PRs.

When a parent merges, dependent readiness is invalidated while the remaining
parents update from integration and complete their normal checks and review.
The child waits without preventing those parent repairs. Once the updated
parents are ready, their changes are combined and checked again before the
child restacks. After every parent merges, the child replays only its own work
onto integration and its existing PR is retargeted there. An unchanged recorded
context is reused; changed context requires fresh verification.

The aggregate base is one synthetic commit, not part of the child's own
commit range. Restacks rewrite that range because its parent changes; original
source PRs and recorded history remain retained. Derived refs are immutable
generations and remain available for resume and audit.

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
Stale descendants retain their original holds without preventing parent repair.
A human answer can be recorded while the base is stale; the owning feature
reconciles the base before continuing work or consuming the answer. Current
human-attention states stop feature execution; resolve the recorded
individual run with its resume/answer command, then repeat the feature command.
The dashboard retains individual-run reporting. Detached supervision remains
future work. Offline tests cover feature scheduling and reconciliation; a
complete connected feature trial remains to be validated.

The [ticket-stream prompt](prompts/implement-ticket-stream.md) remains a design
template for a harness with prepared inputs. The SDLC repository itself develops
on `main`; generated ticket branches belong to the project requesting work.

The [research archive](../research/README.md) preserves earlier investigations
and prototypes. Their commands do not define current CLI behaviour.
