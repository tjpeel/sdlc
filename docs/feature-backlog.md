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

Added: 2026-10-13. Status: deferred; implementation has not started.

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

Implementation should define the supported resume action and how comment
processing and outstanding replies are shown to the user.
