# Focused diagnostics during ticket work

The implementer needs quick evidence while diagnosing a failure. Running the
configured suite before every exploratory command delays that evidence, and
retaining every exploratory request as a required check makes later candidates
repeat work that has already served its purpose.

## Implemented workflow

On macOS and Linux, the running implementation CLI can call a fixed helper with
a batch of machine requests. The helper transfers only those requests through a
private mailbox; the controller runs them in its separate credential-free
worker. Results return to the same native invocation. The helper executes no
repository scripts or tests in the authenticated worker and has no publication
or signing capability. Reviewers receive no helper or mailbox mounts.

The agent commits a checkpoint before requesting tools. The controller pins the
accepted tree throughout copying, execution and artifact collection. A dirty
checkpoint or stale candidate returns an error without evidence so the agent
can correct its request and continue. Frozen-input, infrastructure, transport
integrity and cleanup failures stop the invocation. Concurrent helper calls are
serialized; cancellation joins the service before temporary storage is removed.

Source correction requests use the existing source-only worker and artifact
bounds. The agent assesses any returned diff, applies the intended correction
and commits it. The controller never applies it to the implementation workspace.
Live requests retain only bounded metadata and private logs; their results never
become required publication gates.

An implementation handoff can return `diagnostics_requested` with a separate
`diagnostic_requests` batch. The controller runs these requests in its existing
credential-free check worker and returns their bounded process output to the
original implementation session. Independent requests are processed together;
commands within a request remain sequential and stop when a prerequisite fails.
A diagnostic command's nonzero exit is useful evidence, not a publication gate.

The latest diagnostic batch is retained separately from required verification.
It is cached only for the same candidate, inputs, commands and runtime. Later
candidates do not rerun retired diagnostic batches. Request and output bounds,
clean committed source, frozen inputs and successful worker cleanup still apply.
Operational failures stop the controller without presenting incomplete results
as usable evidence. Diagnostics consume native invocations under the existing
run limit, but do not spend configured-check, CI or independent-review repairs.

Use `checks_requested` when a coherent candidate is ready for the configured
suite and any required regression verification. Return `implemented` only after
current passing evidence, local review and PR metadata. Diagnostic success cannot
replace those gates. Existing saved verification requests remain required;
this change does not silently reclassify earlier requests or alter saved budgets.
Source corrections also keep their existing formatter/verification handoff path
when the live helper is unavailable.

At CI, a failed check accompanied by pending checks keeps the controller waiting.
Once the reported checks finish, the implementer receives one failure packet for
that published revision. Existing cancellation, identity and revision checks
remain in place. Sonar feedback includes the annotations available through the
current collector; it is not a complete Sonar issue or hotspot inventory.

An unavailable GitHub identity lookup is reported separately from a confirmed
identity mismatch. Both block CI inspection. The controller does not change
accounts, infer identity from stored-login presence, or retry sign-in.

## Agent decisions

The agent should assess the whole feedback packet, group related repairs into a
coherent candidate, and choose focused diagnostics while developing. Neither
this workflow nor the implementation skill requires one issue per commit or
one issue per repair. The implementer self-reviews the complete changes and
current evidence. The controller owns the required opposite-provider review
after CI; a separate delegated local review is no longer mandatory. Full checks,
signing and independent review apply when the candidate is ready. The controller
owns tool execution and publication; the agent owns diagnosis, edits and
deciding which evidence it needs.

## Context and token efficiency

Context headroom and token consumption are design constraints throughout ticket
work. Feedback should contain the current candidate, actionable failures and the
evidence needed for the next decision. Refer to retained evidence by its identity
instead of repeating successful output or obsolete diagnostic bodies. Group
related failures so the implementer can repair them in one coherent pass.

Use [native usage measurements](../usage-metrics.md) to assess input, cached input,
output, observed context and compactions alongside elapsed time, repair rounds
and the final outcome. Missing observations remain unknown. Subscription
allowances and cumulative token totals do not establish current context
occupancy. Larger raw log intake must not enlarge the excerpts sent to agents.
Durable histories and cumulative accounting remain intact while the official
clients manage their context through supported mechanisms.

## Limits and further work

The live helper uses the existing official CLI's shell tool rather than a custom
provider client. Windows keeps the structured diagnostic handoff until the
mailbox has equivalent safe file handling there. Source changes still require
committed checkpoints; this avoids introducing an ambiguous dirty-source test
snapshot. The helper does not certify full checks or publish a candidate.

Configured command chains remain fail-fast because later commands may depend
on a successful restore or build. To collect independent diagnostic failures
together, use separate requests in the same batch. Do not continue a dependent
pipeline merely to obtain more errors.

Offline regressions establish scheduling and acceptance boundaries. A connected
trial is needed to measure elapsed-time or token savings; this change makes no
measured performance claim.
