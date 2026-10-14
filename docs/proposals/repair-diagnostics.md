# Focused diagnostics during ticket work

The implementer needs quick evidence while diagnosing a failure. Running the
configured suite before every exploratory command delays that evidence, and
retaining every exploratory request as a required check makes later candidates
repeat work that has already served its purpose.

## Implemented workflow

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
Source correction artifacts keep their existing formatter/verification path.

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
one issue per repair. Full checks and independent review apply when that
candidate is ready. The controller owns tool execution and publication;
the agent owns diagnosis, edits and deciding which evidence it needs.

## Limits and further work

Diagnostics still return through an SDLC handoff and another native invocation.
They do not provide an interactive tool inside a running model turn. A future
controller-backed diagnostic tool could avoid that handoff while retaining the
same worker isolation, candidate attribution and cleanup rules. That needs its
own lifecycle design and offline regressions before changing either provider's
execution path.

Configured command chains remain fail-fast because later commands may depend
on a successful restore or build. To collect independent diagnostic failures
together, use separate requests in the same batch. Do not continue a dependent
pipeline merely to obtain more errors.

Offline regressions establish scheduling and acceptance boundaries. A connected
trial is needed to measure elapsed-time or token savings; this change makes no
measured performance claim.
