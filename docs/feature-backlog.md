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
