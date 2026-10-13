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

Added: 2026-10-13. Status: deferred; implementation has not started.

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

Implementation should define derived-branch naming and retention, combined-check
evidence, and how conflicts or invalidated bases are presented and recovered.
Current multi-parent tickets continue to wait for all parents to merge until this
feature is implemented.
