# Usage metrics

New ticket and feature runs save a private numeric record for every native
provider invocation. Records survive dashboard refreshes, repairs and controller
resumes. Read them without starting Docker or contacting a provider:

```sh
sdlc usage --since 7d
sdlc usage --scope project --since 30d --json
sdlc usage --run FULL_RUN_ID --json
sdlc dashboard --once --run FULL_RUN_ID
```

The external CLI defaults to installation scope. Project scope filters registered
runs to the current Git checkout. `--since` selects runs updated within the period
and includes their full recorded attempts; it does not slice individual requests
at the cutoff. `--run` requires the exact registered ID and ignores age. Existing
runs without records show missing coverage. Preparation failures have no provider
usage; absent counters are unknown.

## What is recorded

Each attempt records its role, requested provider/model/effort, worker image,
optimizer mode, timestamps, outcome and optional native usage. Implementation,
repair and independent review remain distinct. A running record is saved before
native execution and replaced atomically after completion, failure or cancellation.
A process crash can leave it pending; reporting does not invent its final usage.

Summaries group attempts by provider, requested model, role and optimizer mode.
They expose token categories, measured/unknown attempt coverage, elapsed time,
native model breakdown, peak observed input context, compaction/retry/turn event
counts and last recorded quota signals when available. The private attempt record
also retains optional native durations and API-equivalent dollar estimates.

Observed controller time covers active controller invocations, including setup,
provider waits and publication. Check-worker time and CI polling wait are reported
separately. Provider-attempt elapsed time includes setup and queueing; it is not
active inference time. Time between controller invocations is excluded from these
controller intervals. Older journals have no recorded timing intervals.

## Counter meanings

- Codex input includes cached input. Cache reads are a subset; reasoning output
  is a subset of output. Do not add either subset again. The pinned `codex exec`
  JSONL emitter reports cumulative thread totals; resumed attempts subtract the
  earlier complete checkpoint from the same native session.
- Claude input is fresh input, excluding cache reads and cache creation.
  `result.modelUsage` includes native subagents and is the authoritative whole
  session total. Top-level `result.usage` covers the main loop and is not added
  again. The pinned Claude release restores these totals on resume, as documented
  in [cost tracking](https://code.claude.com/docs/en/agent-sdk/cost-tracking).
- A missing baseline, decreasing cumulative counters, incomplete final event or
  absent required category leaves the interval unknown. Missing optional
  categories also remain unknown across contributing attempts. Known totals
  describe measured attempts; always compare their coverage.
- Aggregate session tokens are not current context occupancy. Only explicit
  native context observations establish a peak, window or percentage. Events the
  native client does not emit remain unobserved.

Native dollar estimates describe API-equivalent cost. They do not measure a
subscription bill, remaining allowance or time until a cap. Claude quota events
can provide a last observed utilisation, status and reset time. They are timestamped
observations, not live account polling. The current Codex execution stream does
not emit account quota windows; those remain unknown.

## Comparing Headroom

Use equivalent disposable tickets and checks with the same models, effort,
runtime and inputs. Start with direct runs, then passthrough to measure forwarding
overhead, then conservative optimization. Compare several runs rather than
resuming one native session with different proxy settings. The selected variant
is frozen for the run and its repairs/review.

Compare native input/cache/output counts, attempt duration, check outcomes,
repair rounds, independent-review findings and completion. Headroom's request,
input/output and saved-token counters have their own `headroom_proxy_aggregate`
source. They are tokenizer estimates and cover traffic passing through that
invocation's proxy. Keep them separate from native counters and subscription
capacity. A lower estimate with worse checks or more repairs is not a useful saving.

## Storage and privacy

Records live under the run's private `metrics/` directory, with directory mode
`0700` and file mode `0600`. They contain no prompts, tool arguments, transcript
text, authentication headers or account identifiers. The native session ID is
retained privately for cumulative reconciliation and omitted from usage summaries,
dashboard JSON and run reports. Records and existing native logs stay outside
tracked source. No metric exporter or external analytics service is enabled.
