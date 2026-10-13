# Feature backlog

Features requested for later implementation.

## F001: Continue when the independent reviewer is unavailable

Added: 2026-10-13. Status: deferred; implementation has not started.

An otherwise completed implementation should be able to continue when its
independent review cannot run, including when the selected reviewer has missing
or expired authentication. The user must be told that independent review was
not completed and be able to initiate it later when the provider is available.

Expected behaviour:

- Apply the same rule to either review provider: Claude reviewing a Codex
  implementation, or Codex reviewing a Claude implementation. Use the selected
  role rather than a provider-specific exception.
- Skip the unavailable review step and allow the run to reach the stage normally
  reached after review completion. Persist an explicit skipped/not-performed
  review status and reason, and show the user that independent review remains
  outstanding.
- Keep implementation completion, repository checks, CI and signing requirements.
  Failure of the implementation provider remains a blocking failure and cannot
  use this skip path.
- Offer a supported action to initiate the outstanding independent review on the
  same saved run once its selected provider is available. Retain the selected
  account and model, original implementation session, history and budgets.
- Bind the later review to the then-current exact published revision. Recheck
  the relevant evidence when the revision has changed; retain the earlier skip
  record and record the later review outcome separately.
- Preserve findings from any completed review and pass them to the implementor
  for assessment through the existing handoff. An unavailable later reviewer
  must not erase those findings.
- Keep human merge decisions and prevent automatic merge through this feature.

Implementation should define how supported reviewer-unavailability outcomes are
classified and presented, and how users initiate the later review. Current runs
retain their existing independent-review gates until this feature is implemented.

## F002: Resume a run to address human PR comments

Added: 2026-10-13. Status: implemented in `0.1.0-beta.43`.

Allow the user to resume a saved run whose PR is awaiting human review or input
after comments have been added. The original implementation agent should assess
the comments, make justified changes, and explain comments it cannot address.
The updated PR must pass current checks and undergo independent review again
before returning for another human review.

Expected behaviour:

- Resume the original saved run and implementation session for its existing PR.
  Retrieve the current review comments, threads and general PR comments as
  feedback for the agent to assess.
- Work through each comment in turn. Commit each correction separately, or
  combine comments that concern the same logical change into one commit. Record
  which comments each commit addresses.
- If the agent cannot address a comment, reply to that comment or thread with
  the reason and any clarification or decision needed. Keep its unresolved status
  visible in the handoff to the human.
- Retain comment identities, updates, assessments, commits and replies so another
  resume handles new or changed feedback without duplicating work or responses.
  Interpret outdated inline comments against the current source and their
  original review context.
- Publish corrections through the existing signing and PR workflow. Run required
  repository checks and wait for all required pipeline checks to pass for the
  final published head.
- Once that exact head is green, obtain a fresh review from the selected
  independent provider, with the human comments and the implementor's responses
  available as context. Handle new independent findings through the existing
  implementation handoff before returning the PR for human review.
- Earlier independent approval must not satisfy this new review cycle. Later
  edits invalidate readiness for the changed revision. A review skipped under
  F001 must remain explicitly outstanding rather than being reported as passed.
- Preserve the selected accounts and models, history, budgets, provider holds and
  human merge decision. Receiving comments must not reset limits or bypass an
  implementation failure, and this feature must not automatically merge the PR.

Implemented action: `sdlc resume --run RUN_ID --pr-feedback` starts from a ready
saved PR. Ordinary resume continues an interrupted cycle; explicit feedback
resume also recovers its recorded fetch or changed-comment reply hold. Existing
human questions still use `sdlc answer`. The dashboard and private checkpoint
report show pending feedback, unresolved comments and outstanding replies.
Original sessions, selected roles and cumulative budgets are retained; F001
remains deferred. See [the CLI guide](cli.md#address-human-pr-feedback).

## F003: Start dependent tickets from a tested combined base

Added: 2026-10-13. Status: implemented in beta.50.

Allow a ticket with multiple dependencies to start before its parent PRs merge
into the integration branch. For example, tickets 1 and 2 can be implemented in
parallel, then ticket 3 can start from a tested combination of their reviewed
changes. Independent tickets should not need an artificial linear dependency
chain to make later work possible.

Expected behaviour:

- Keep independent parent tickets on their own branches and preserve feature
  priorities, dependencies, path-overlap rules and the selected ticket parallel
  limit. Parents must meet the applicable readiness requirements before their
  changes are included in a dependent ticket's base.
- Build a separate derived branch from the recorded integration revision and
  exact eligible parent commit IDs. Preserve the original PRs and their human
  merge decisions; creating this base must not approve or merge those PRs.
- Run the required repository checks against the combined candidate. Report
  merge conflicts or failing combined checks before starting the dependent
  ticket; individual parent checks do not establish that their combination works.
- Start the dependent ticket on its own branch from the fixed, tested derived
  commit. Target its PR at the derived branch so the review diff contains only
  that ticket's changes.
- Persist parent commit IDs, integration revision, derived-base revision and
  the dependent ticket's own commit boundary in the feature checkpoint. Resume
  from that recorded context rather than silently following moving parent heads.
- Invalidate the derived base and descendant readiness when a parent revision
  changes. Rebuild and validate the combination, then reconcile affected work
  through the original saved implementation session and normal review workflow.
- Once the parent PRs have merged into the integration branch, restack only the
  dependent ticket's own changes onto the current integration revision and
  retarget its PR. Handle squash merges without duplicating parent changes or
  carrying the derived branch's merge commits into the ticket's own changes.
- Repeat the required signing, repository checks, CI and independent review for
  the reconciled published head. Preserve saved accounts, models, inputs, runtime,
  history and budgets throughout the transition.

Implemented through the existing feature command and private dependency plan.
The controller retains immutable `sdlc-derived/` generations, exact parent and
verification context, source and signed tree evidence, and the child's own
commit boundary. Conflicting or failing combinations leave the child unstarted;
changed parents require reconciliation and current checks and review. Operational
checkpoint recovery retains prior evidence and original jobs. See
[the feature run guide](cli.md#run-a-feature) for continuation and merge sequencing.

## F004: Make live run progress easier to read

Added: 2026-10-13. Status: deferred; feasibility review complete, implementation
has not started.

Improve human readability when following a run in a terminal or the SDLC shell.
Show useful signs of liveness during quiet periods, highlight important events,
make structured output readable, and offer timestamps. Keep ordinary run output
compact. Choose the presentation approach after comparing its runtime and
maintenance costs; a separate full-screen lens is an option, not a requirement.

### Current behaviour and available reuse

- The private progress stream already records time, run, source, provider, role,
  stage and attempt. Its shared formatter displays labels and text but omits
  time, stage and attempt. The same formatter serves live output, CLI progress
  and both shell modes. See [progress events and formatting](../internal/runprogress/progress.go).
- Progress reads use bounded batches and incremental cursors. Unchanged status
  produces no human output, even though the controller heartbeat continues.
  The existing registry records heartbeat, last activity, waiting reason and
  waiting start time. Heartbeat freshness is an indication of controller
  reporting, not proof that a provider is making progress or a fresh process
  ownership check. See [progress polling](../cmd/sdlc/progress.go) and
  [run status](../internal/runstatus/registry.go).
- Native JSON is summarized before it reaches the progress stream. Many fields
  are discarded; surviving message and command-output text can contain JSON.
  Original native events remain in separate private logs. Styling the current
  progress text cannot recover discarded fields. See
  [native summaries](../internal/runprogress/stream.go).
- The rich shell already has a bounded scrolling progress view, follow/pause
  controls, a footer and copy selection. Its Robby Russell-style prompt uses
  green/red, cyan, blue and yellow terminal colours. Dashboard sections already
  use semantic colours, but there is no shared progress palette and colour
  capability checks are inconsistent. See [shell rendering](../internal/shell/model.go)
  and [dashboard presentation](../internal/dashboard/presentation.go).

### Options and recommendation

| Approach | Benefit | Cost or limit |
| --- | --- | --- |
| Extend the shared progress renderer | Consistent timestamps, labels, emphasis and optional JSON formatting in the terminal and both shells | Cannot restore information discarded during capture; needs explicit human-display options so normal output stays compact |
| Add a status row or footer to the existing follower | Shows current stage, wait duration, heartbeat age and time since output without filling scrollback | Plain terminals need a controlled update cadence; paused/copy views must remain stable |
| Add a separate full-screen lens | More room for expandable JSON, filters and navigation | More UI state and maintenance; should reuse the same events and renderer rather than introduce another log pipeline |
| Pipe existing structured batches to an external formatter | Useful for experimenting with timestamp and JSON layouts before implementation | Requires another tool, needs empty-batch filtering and does not provide consistent shell styling or recover lost native fields |

Start with the shared renderer and a small follower status area. Keep one bounded
progress stream and transform only new events as they arrive. Do not persist a
second decorated log, rescan raw logs on every refresh, or add provider calls to
animate the display. Expand retained semantic metadata only where the readability
review establishes a concrete need. Consider a fuller lens later if the existing
viewport cannot meet that need.

### Expected behaviour

- During a quiet active run, show the current stage, time since the last output
  and heartbeat age. Show known CI, provider, runtime or human waits explicitly,
  with elapsed wait time where recorded. Distinguish fresh heartbeat, stale or
  unknown controller status, and stopped work. An animation or fresh heartbeat
  must not imply successful checks, provider health or model activity.
- Reuse the existing polling results. Animate or update elapsed time locally;
  do not perform process, Docker or provider queries on each animation tick.
  Prefer an updating row in a live terminal and the shell footer. For ordinary
  scrollback or explicit redirected follow, use sparse status lines on changes
  or a bounded cadence. Synthetic display ticks must not enter persisted events
  or change progress cursors.
- Define semantic styles consistent with the Robby Russell prompt: cyan/blue
  headings and identities, green confirmed success, yellow waits or warnings,
  red failures or required action, and subdued routine metadata. Derive outcome
  styling from trusted controller or command-result metadata rather than phrases
  in arbitrary output. Preserve readable labels when colour is unavailable.
- Compare standard ANSI terminal-palette colours with optional richer palettes.
  Standard colours respect the user's terminal theme and should be the first
  option. Centralize capability handling for TTY, `TERM=dumb` and `NO_COLOR`,
  including an empty-but-present `NO_COLOR`. Keep redirected plain output and
  machine `--json` free of decoration; choose exact human-format flags during
  implementation rather than advertising them as available now.
- Offer bounded pretty printing and syntax highlighting for complete JSON that
  is actually available. Compare keys, strings, numbers and booleans using the
  semantic palette. Keep a compact form or collapsed detail for large objects;
  preserve values, key order and numeric precision. Malformed, incomplete or
  truncated JSON remains readable plain text. Do not expand every native event
  or promise reconstruction of discarded payloads. A later raw-detail action
  would need explicit selection, existing private-log boundaries and its own
  bounded read policy.
- Offer recorded event timestamps, with a compact local-time prefix and a clear
  date/timezone option for longer sessions. Preserve capture timestamps during
  replay. Legacy fallback currently stamps events when read; label those times
  as observed or unknown rather than implying historical occurrence times.
  Indent multiline continuations so source labels and JSON remain easy to scan
  and copy.
- Sanitize untrusted text and terminal controls before adding trusted ANSI
  styling. Preserve output bounds, wrapping, incremental cursor semantics,
  multi-run attribution, scroll position, pause/copy controls and plain-shell
  prompt serialization. Following or closing a view must not change controller
  execution, agent feedback, accounts, history, budgets or delivery gates.

Before implementation, compare compact, decorated and expanded layouts using
disposable streams containing quiet waits, high-volume output, multiple runs,
multiline JSON, invalid JSON and ANSI controls. Measure added CPU, allocations,
memory, output volume and display latency against the current follower. Separate
polling cost from rendering cost; enforce existing bounds and avoid full-history
work on refresh. Include narrow terminals, monochrome output, Unicode wrapping,
copy selection, interrupted cursors and legacy timestamps. This review establishes
reuse points and tradeoffs; no performance improvement or final colour scheme
has been measured yet.
