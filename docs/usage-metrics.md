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
Requested models and the worker image describe configuration; native version and
reported-model confirmation remain unknown when the client does not emit them.
The `repair` role also includes ordinary resumed implementation after checks.
Use the journal's repair rounds to count failed checks, CI or review findings.

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

The dashboard rounds displayed durations to whole seconds; JSON retains the
recorded milliseconds. Its run elapsed duration spans the first controller start
to the latest stop, including gaps between invocations. Provider elapsed sums
the recorded provider-attempt intervals, so these values can differ.
The `LAST OUTPUT` column shows time since output for live controllers and a fixed
recorded timestamp for stopped or stale controllers. Overview timestamps use the
time zone shown in the dashboard heading; details include the UTC offset.

New attempt records and `sdlc usage --json` also report native event reception
timing: time to the first JSONL event, the longest interval between events, and
silence after the last event. Group summaries retain the largest measured
interval and recorded/missing attempt counts. No event contents or arguments
enter these numeric records. These intervals can include setup, tool execution,
agent waits and buffered output; they do not establish provider queue or model
compute time. Agent-tool start/completion messages in the private progress log
help distinguish reported delegation waits. Only parsed native events count;
malformed or oversized lines do not contribute timing. Older records have no
such timing.

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

`Context: not reported` means the native client supplied no current prompt/context
size. It does not mean zero usage. The cumulative native token totals shown next
to it cannot establish current context occupancy or a context percentage.

Native dollar estimates describe API-equivalent cost. They do not measure a
subscription bill, remaining allowance or time until a cap. Claude quota events
can provide a last observed utilisation, status and reset time. They are timestamped
observations, not live account polling. The Codex execution stream does not emit
account quota windows. New workers capture them separately through the official
client's local App Server protocol.

## Subscription percentages

New Codex and Claude provider invocations capture percentage remaining immediately before
native model work and after it exits, including ordinary failures. The worker
uses its existing selected login. Codex sends only initialization and
`account/rateLimits/read` requests to the official `codex app-server` client.
Claude uses the native stream-json `get_usage` control request declared in the
[official Agent SDK](https://app.unpkg.com/@anthropic-ai/claude-agent-sdk@0.3.220/files/sdk.d.ts).
That interface is experimental and was checked against Claude Code 2.1.296;
unsupported clients or missing windows remain unknown. Its `utilization` is a
percentage from 0 to 100, unlike the 0-to-1 native `rate_limit_event` ratio.
Only reported windows are retained; model cost and account metadata are discarded.
There is no model prompt or direct authenticated HTTP request. A four-second
deadline limits each probe; unavailable or unsupported clients leave an explicit
unknown result and do not prevent native work.

Private attempt records retain both snapshots. The baseline is checkpointed
as soon as received, before processing later model output. `sdlc usage`, its JSON
output, dashboard details and checkpoint reports show the first baseline and
last end snapshot for each provider across the selected run. Individual role
groups retain their own boundaries. Reading these reports never polls accounts.
Provider work timing excludes the probes when native boundary markers are
available; older event streams retain their existing timing semantics.

An allowance falling from 80% remaining to 75% remaining is **5 percentage points
used**, not 6.25% of the original remaining balance. Changes are calculated only
for the same metered bucket, window duration and reset time. A missing boundary,
reset, expired window, clock reversal or increased allowance leaves usage unknown.
Values have the provider's reporting precision; an unchanged percentage can
coexist with real token use. The counters describe the account, so concurrent
sessions can contribute to a change. Do not change login while a run is active.

Older runs cannot recover a missed starting snapshot. Claude's native quota
events remain separate last observations and do not replace either boundary.

## Comparing Headroom

Use equivalent disposable tickets and checks with the same models, effort,
runtime and inputs. Start with direct runs, then passthrough to measure forwarding
overhead, then conservative optimization. Compare several runs rather than
resuming one native session with different proxy settings. The selected variant
is frozen for the run and its repairs/review.

Compare native input/cache/output counts, attempt duration, check outcomes,
repair rounds, independent-review findings and completion. Headroom's request,
input/output and saved-token counters have their own `headroom_proxy_aggregate`
source and cover traffic passing through that invocation's proxy. Input can use
provider-reported counts or a local fallback; saved tokens are local compression
estimates, and output uses upstream response counts. The proxy can include native
child traffic beyond the root thread's emitted usage. Its headline savings ratio
therefore does not establish the percentage reduction in native client tokens.
Keep proxy counters separate from native counters and subscription capacity.
A lower estimate with worse checks or more repairs is not a useful saving.

## Storage and privacy

Records live under the run's private `metrics/` directory, with directory mode
`0700` and file mode `0600`. They contain no prompts, tool arguments, transcript
text, authentication headers or account identifiers. The native session ID is
retained privately for cumulative reconciliation and omitted from usage summaries,
dashboard JSON and run reports. Records and existing native logs stay outside
tracked source. No metric exporter or external analytics service is enabled.
