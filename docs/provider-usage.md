# Provider usage rules

SDLC must respect provider terms, supported authentication routes and usage
limits. Review the current official rules when changing provider integration;
this document records the boundary reviewed on 13 October 2026. No implementation
can guarantee that a provider will never restrict an account.

## Codex

SDLC invokes the unmodified Codex CLI for device login, offline login status and
interactive terminal sessions and non-interactive ticket sessions through
`codex exec`. The authentication guide documents file-backed login caching and
moving that cache into Docker. SDLC stores the cache privately and gives it only to the
same user's local Codex container. See [Codex authentication](https://learn.chatgpt.com/docs/auth).

Interactive sessions use the ordinary [Codex CLI](https://learn.chatgpt.com/docs/codex/cli).
The client receives prompts directly from the account owner. Docker provides the
outer isolation boundary; native approvals default to `never` and can be set to
`on-request` for the session, following the
[container security guidance](https://learn.chatgpt.com/docs/agent-approvals-security).
No custom API client, credential broker or subscription sharing is involved.

For quota snapshots, the same worker invokes `codex app-server` and its documented
[`account/rateLimits/read`](https://learn.chatgpt.com/docs/app-server) method over
local stdio before and after native model work. The official client owns the
authenticated service request. SDLC sends no prompt, starts no thread or turn,
does not decode credentials, and never consumes a quota-reset credit. The probe
is bounded and optional; unavailable quota information does not restart work or
change the selected account.

For a resumed Codex thread with a recorded unmatched native tool call, the same
worker can request the documented `thread/resume` and `thread/compact/start`
methods through the unmodified `codex app-server`. Recovery requires the same
idle thread and selected model, account, workspace and headroom route. Only the
official client writes the compaction record; SDLC neither fabricates tool
results nor edits the rollout. This is model work, included inside the native
attempt's timing and before/after quota snapshots. Compaction is counted, and
token measurement remains explicitly partial because its separate token usage
is not inferred. A provider rejection stops recovery without a fallback account,
new session, budget reset or automatic retry.
See [the app-server protocol](https://developers.openai.com/codex/app-server/).

Login does not authorise every later use of the account. Ticket runs are limited
to the account owner's local single-user native CLI job, using trusted inputs.
Review the chosen account, repository trust and execution mode against
[non-interactive Codex guidance](https://learn.chatgpt.com/docs/non-interactive-mode).
API keys are the documented default for automation. Any account-based exception
must fit the documented conditions; its credentials must stay out of public or
untrusted execution environments.

Respect account access controls, usage policies and rate limits. SDLC must not
change identities or restart work to bypass a denial or usage cap. See
[OpenAI terms](https://openai.com/policies/eu-terms-of-use/) and
[usage policies](https://openai.com/policies/usage-policies/).

## Claude

Claude login invokes the unmodified official client. Anthropic explicitly permits an
end user to sign into the unmodified Claude Code binary with their own subscription,
including in a hosted container. It restricts third-party credential collection
and using subscription credentials to intermediate service access for other users.
See [Claude Code legal and compliance](https://code.claude.com/docs/en/legal-and-compliance).

Anthropic's [container guide](https://code.claude.com/docs/en/devcontainer#persist-authentication-and-settings-across-rebuilds)
documents a named volume for Claude Code's configuration and authentication cache.
SDLC uses this pattern: the official CLI reads, writes and refreshes its own
cache. Interactive sessions invoke the ordinary
[Claude terminal CLI](https://code.claude.com/docs/en/cli-reference) with a selected
native permission mode, defaulting to `bypassPermissions` inside the isolated
container. See [Claude permission modes](https://code.claude.com/docs/en/permission-modes).
Ticket runs submit the account owner's selected work through native `claude -p`;
SDLC does not override managed restrictions.
SDLC does not extract token fields or use them for custom provider requests.
Quota probes initialize a separate native `claude -p` stream-json control session
and send only `get_usage`, without a user message or model prompt. The
[published Agent SDK declaration](https://app.unpkg.com/@anthropic-ai/claude-agent-sdk@0.3.220/files/sdk.d.ts)
marks this usage method experimental. The adapter was checked against Claude
Code 2.1.296 on 12 October 2026; it degrades to unknown on an unavailable window
or incompatible response. The official binary owns authentication and any service
request. SDLC decodes neither credentials nor authenticated HTTP traffic, and
discards session cost, behaviour and account metadata from the response.
When the native client atomically refreshes its cache, SDLC preserves that opaque
file in the same user's private login volume, without decoding its contents.
Persistence works across Docker mounts; a failed transfer retains the private
session copy for recovery. Recovery refuses to overwrite a cache changed by a
later login or session; it preserves both generations for inspection. Signing in
again for every container is unnecessary;
re-authenticate when the provider requires it.

Account login and cache persistence do not approve every unattended use. Before
running a ticket, review the account type and execution mode against
the [Consumer Terms](https://www.anthropic.com/legal/consumer-terms),
[Commercial Terms](https://www.anthropic.com/legal/commercial-terms) and
[`claude -p` guidance](https://code.claude.com/docs/en/headless). API or supported
cloud authentication is the route for integrations outside permitted native-client
use. Ordinary end-user CLI login does not itself require an API key.

The locally built image already contains the published Claude CLI. Its presence
does not approve the archived prototype's subscription automation design.

## Development and execution

Use offline fixtures to test storage and status handling. Fake tokens must never
leave network-disabled test containers. Real login requires the account owner's
action through the official provider flow; SDLC does not automate browser sign-in
or implement token refresh itself.

Implementation workers must stop on exhausted usage, unsupported credentials, access
denials, account holds and policy refusals. Any retry of a transient failure must
remain within the provider's documented limits. Credentials belong to one
authorised user; SDLC must not pool subscriptions or make their usage available
to other users.

## Local ticket execution

`sdlc run` invokes the unmodified official clients in their documented headless
modes: [Codex non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode)
and [Claude print mode](https://code.claude.com/docs/en/headless). It uses stored
native account login for the same user's local job. SDLC rejects CI execution;
this account route is not a service for other users, shared subscription broker
or general unattended deployment route. Use API or supported cloud authentication
for integrations outside the documented native-client account conditions.
Explicitly authorised supervised private trials have exercised this local route
with both implementation providers. Those results do not authorise other
accounts, execution modes or future connected tests.

The selected implementer must have login. Missing opposite-reviewer login allows
draft PR publication and CI before pausing at `awaiting_reviewer`. Review never
uses the implementation account as a substitute. Repairs resume the original
native implementation session; each independent review starts fresh. A reported
model mismatch stops execution; a missing model report cannot establish the
requested identity. Effort is a request that providers may cap.

Independent local ticket sessions may invoke the same provider concurrently.
SDLC does not reserve Codex or Claude for a ticket's lifetime. Each unmodified
native client receives a private writable snapshot of its owner's authentication;
it still performs its own login checks, service requests and refresh. Brief
transactions protect shared-cache snapshots and opaque refresh persistence.
Unchanged snapshots never overwrite a later login or refresh. A conflicting
refresh retains the private candidate and stops the affected invocation rather
than guessing which credential generation remains valid. Native authentication
errors and account usage limits stop work without automatic re-authentication,
identity switching or restarting to evade the limit.

On supported local hosts, an implementation session can call a fixed diagnostic
helper through the official client's existing shell tool. The helper reads only
Git checkpoint metadata and a bounded machine request, then waits for the
controller's isolated check worker. It invokes no repository scripts, dependency
installation or tests in the authenticated worker. Its request mailbox is
separate from its read-only response mount; reviewers receive neither. The
controller pins the accepted source tree, retains only bounded private results,
and cancels and joins outstanding diagnostics before removing temporary mounts.
Source correction artifacts use the existing source-only worker and size bounds.
This adds no provider API client, authentication route or credential broker.

Offline tests establish concurrent execution and storage protection, not provider
tolerance of simultaneous token rotation. The reviewed official documentation
describes native file-backed caching and container persistence; it does not
guarantee every concurrent refresh succeeds. Connected account trials require
separate authorisation.

An implementation worker may explicitly request a bounded source correction
artifact through `verification_requests.source_patch`. Its formatter commands
run in a fresh source-only check copy and HOME with no provider authentication,
publication credentials, private inputs or Docker daemon. A fixed network-disabled
collector exports only selected committed text as a private diff with revision
and content hashes. Completed check commands also return bounded actual process
output to the original implementation session, with exit status and explicit
truncation. This output is untrusted data and can contain private source or values
printed from selected check inputs; size limits do not make it secret-free.
The authenticated worker
treats that code as data, reviews and applies the intended correction, commits
it and requests full verification. This artifact channel does not authorise raw
log access, credential export or execution of repository tools in the provider
worker; failed commands, failed cleanup and stale candidates cannot export.

Native provider caches remain accessible inside authenticated workers. Separate
check workers have no provider caches or host publication credentials, but a
prompt cannot prevent the provider's shell from running repository code or
reading its own cache. Use trusted repositories and selected inputs; network
destinations are not restricted. Private JSONL, diagnostics and native session
storage may retain sensitive material. Stop on policy refusals, access denials,
usage exhaustion and unsupported account routes; resume only after resolving the
cause without identity switching or limit evasion.

When implementation needs private companion source, it returns a bounded
repository request. A trusted controller container uses the run's already
selected GitHub profile through the official `gh repo clone` command, with the
profile volume mounted read-only. Requests are limited to the selected project's
GitHub owner. The controller checks out the default branch with Git history,
keeps the reader alive through a tar export, and validates retained source before
recovering interrupted staging. The provider receives a read-only
source mount and its revision in the private workspace. It receives no GitHub
cache, and the controller does not execute the companion repository's code.

## CI failure evidence

The artifact extension was checked against GitHub's official
[Actions artifact API](https://docs.github.com/en/rest/actions/artifacts) and
[`gh api` documentation](https://cli.github.com/manual/gh_api) on 14 October 2026.
The existing read-only official client retrieves artifact metadata and ZIP bytes
through generated repository endpoints. It does not add a provider client or
authentication route. Artifact metadata supplies the workflow/head association,
expiry, size and SHA256 digest; the documented download endpoint redirects to a
temporary archive URL.

Only artifacts corroborated by successful upload records from the current job
are inspected. The collector binds them to the repository, published head and
current attempt, compares metadata before and after download, and verifies the
archive digest. It reads bounded ZIP members in memory without extracting or
executing them. Supported Trivy reports project neutral finding fields; secret
matches and source snippets are excluded before ordinary masking and excerpt
selection. Private archive bytes are discarded after collection.

The trusted CI reader uses the run's selected read-only GitHub profile through
the frozen runtime's unmodified official `gh api` client. GitHub documents
[workflow job metadata and log downloads](https://docs.github.com/en/rest/actions/workflow-jobs),
including the repository read permissions and temporary log-download redirect.
For job logs only, the reader uses the documented
[`gh api --allow-escape-sequences`](https://cli.github.com/manual/gh_api) option
to receive coloured output into its bounded buffer. It removes terminal
controls and masks private values before retaining excerpts. Metadata requests
keep the client's default output protection.
SDLC verifies the repository, published head, check identity, Actions run attempt
and job association before collecting a failed job. It fetches only verified
GitHub API resources; diagnostic URLs do not authorise external requests.

The reader mounts no provider cache, signing key, worker checkout or Docker
socket. The host collector masks recognised credentials and values from
hash-verified retained check-only inputs before retaining bounded excerpts.
It never sends the masking dictionary or credential-cache contents to Docker
or the implementation provider. Collection, identity, decoding, size and cleanup
failures retain an operational hold before granting a repair.

Sanitised CI text remains untrusted private project data. Masking cannot prove
that arbitrary logs contain no unknown secret formats. Only the original
implementation session receives failure context; independent review and
portable status exports omit it. Ordinary recovery of an eligible saved
missing-context stop reuses the existing CI grant and original session rather
than resetting accounting or changing the account route.

## Saved external inference proxy

New ticket, feature and PR-adoption runs require the saved [Headroom proxy](headroom.md) address. Existing runs retain their recorded route.
The official client still owns login, refresh, account selection and model requests.
Only inference forwarding changes; login and status remain direct. SDLC does not
read token fields, construct authenticated provider requests itself or provide a
shared subscription gateway. The local proxy sees the native request headers and
bodies in memory, so selecting it extends the trusted execution boundary.

Codex uses the built-in `openai_base_url` setting and matching `OPENAI_BASE_URL`
for `/v1` inference. It retains the built-in provider's native account authentication.
The [authentication guide](https://learn.chatgpt.com/docs/auth) documents account
authentication with a forwarding proxy; the
[configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference)
documents the inference URL setting. `chatgpt_base_url` belongs to the login route
and is not changed. The pinned client's
[provider implementation](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/model-provider-info/src/lib.rs#L421)
applies an explicit inference URL before choosing its account/API default.

Claude uses `ANTHROPIC_BASE_URL` alone. The
[subscriptions and gateways guide](https://code.claude.com/docs/en/llm-gateway#subscriptions-and-gateways)
documents retention of saved subscription authentication when only the base URL
changes. SDLC supplies no gateway credential, API key or credential helper.
Headroom forwards the native OAuth capability headers and does not substitute
authentication. Gateway instructions do not grant permission for other account
types, shared access or execution outside the native client's documented conditions.

External proxy routing was checked against the official authentication,
configuration and subscriptions-and-gateways references above on 16 October 2026.
It uses the same native-client forwarding settings and account route. The proxy
operator controls logging, retention, compression, routing, retries and telemetry;
SDLC cannot attest to an external proxy's policy. The address is an explicit trust
choice and must not be a shared subscription gateway or replace account handling.
SDLC never manages the external process or attributes its shared aggregate totals
to one run. Legacy managed proxies retain their invocation-scoped lifecycle and
conservative policy. Connected quality and subscription trials require separate
authorisation; offline validation alone does not establish real account access
or savings on engineering work.

## Independent reviewer usage exhaustion

An independent review can end at the human-review checkpoint when the selected
native reviewer conclusively exhausts usage. This records a skipped review with
its exact published revision and selected provider/model/attempt, rather than an
approval. No replacement provider, model or account is selected. All current
implementation, isolated verification, signed publication and exact-head CI
checks still apply; pending findings, questions and operational failures keep
their holds.

Claude supplies the native `rate_limit_event` rejection. The pinned Codex
0.162.1 [exec event protocol](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/exec/src/exec_events.rs)
exposes fatal error messages without their typed error codes. Its
[UsageLimitReachedError display](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/protocol/src/error.rs)
supplies canonical usage-limit prefixes; SDLC decodes only those top-level fatal
events into a bounded category and retains no error payload. Other formats and
request throttling remain holds. Unknown quota probes, warnings and percentages
near a limit are not exhaustion evidence.

The native wrapper confirms completion only after its opaque authentication
snapshot has been safely persisted. Parser, output, authentication persistence,
container cleanup and model-identity failures cannot become skipped reviews.
Compatible older usage holds require the exact controller-owned stop reason,
same failed review attempt, matching retained published snapshot and corroborated
wrapper completion; ambiguous evidence remains a hold. This lifecycle policy
does not change authentication or native execution routes.

Claude can label a native `rate_limit` assistant error `<synthetic>`. That error
envelope does not identify an inference model. SDLC retains the selected model
from native initialization and continues to check real model reports. Recovery
of an earlier stop requires the retained event stream to corroborate that exact
envelope, the selected initialization, rejected usage window and safe native
completion. It preserves the original metrics and does not dispatch the provider.

## Native speed selection

New-run speed controls were checked against the official
[Codex configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference)
and [Claude fast-mode guide](https://code.claude.com/docs/en/fast-mode) on
14 October 2026. Codex fast requests use the native `service_tier` configuration;
normal uses its isolated client's default tier. Claude uses the documented
`--settings` fast-mode value. Old saved runs without a speed selection retain
their previous native command.

Claude fast mode supports Opus 5.5, Opus 5 and Opus 4.8. SDLC refuses an
unsupported selected model before dispatch rather than allowing a model switch.
On subscription plans, fast mode draws paid usage credits outside the included
allowance, even when allowance remains. Selecting the flag does not enable or
purchase credits, establish account access, or prove the effective service speed.
No live fast-mode provider request was made while validating these controls.


## Native session and delegation telemetry

The session reporting extension was checked on 15 October 2026 against the
[Codex non-interactive documentation](https://learn.chatgpt.com/docs/non-interactive-mode),
the pinned official
[Codex 0.162.1 exec event schema](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/exec/src/exec_events.rs),
[Claude headless documentation](https://code.claude.com/docs/en/headless),
[Claude cost tracking](https://code.claude.com/docs/en/agent-sdk/cost-tracking),
and the official
[Claude Agent SDK message types](https://github.com/anthropics/claude-agent-sdk-python/blob/main/src/claude_agent_sdk/types.py).
Claude's [Agent tool result reference](https://code.claude.com/docs/en/hooks#agent)
distinguishes resolved models and final-request token counts from whole-agent
usage. These are optional observations, not an inference of served settings.

The host collector reads the existing official clients' JSONL output. It adds no
provider request, authentication path, transcript reader, SDK execution route or
hook. Existing hook restrictions remain in force. Requested controller settings
are recorded separately from native observations; child reasoning, speed and
missing usage remain unknown. Claude initialization may additionally report
`fast_mode_state`, as declared in the official SDK system initialization type;
this client state is not a delivered-speed measurement. Tests use disposable
offline events only.

## PR-based Claude review effort

The default review selection was checked on 15 October 2026 against the official
[Claude Code effort documentation](https://code.claude.com/docs/en/model-config#adjust-effort-level),
[headless execution guide](https://code.claude.com/docs/en/headless) and
[legal and compliance guidance](https://code.claude.com/docs/en/legal-and-compliance).
It uses the existing unmodified official client's native `--effort` option and
account route. No classifier model call, replacement model, account change or
extra authentication route is added. Offline source inspection supplies diff
statistics and fixed risk signals; the controller selects low, medium or high
before starting the exact-boundary review. Explicit settings and old saved runs
remain fixed. This policy does not raise effort or restart jobs in response to
usage limits, authentication failures or access denials.

## PR entry points

The PR entry-point extension was checked on 15 October 2026 against the official
[Codex non-interactive guidance](https://learn.chatgpt.com/docs/non-interactive-mode),
[Claude programmatic execution guide](https://code.claude.com/docs/en/headless),
and [Claude account rules](https://code.claude.com/docs/en/legal-and-compliance).
Review-only work uses the existing unmodified native client, selected account,
model, reasoning settings and isolated review snapshot. It adds no authentication,
credential extraction, SDK request, account switch or subscription-sharing route.
Awaiting feedback makes no provider call; checks-only work stays in the existing
credential-free check worker. Human-review declarations are local metadata, not
provider review results. Provider, policy, access and usage holds cannot be cleared
by choosing a new entry point.

## Independent inspection continuation

Continuation was checked on 15 October 2026 against the official
[Codex non-interactive session resume guide](https://learn.chatgpt.com/docs/non-interactive-mode#resume-a-non-interactive-session)
and [Claude headless session guidance](https://code.claude.com/docs/en/headless).
It reuses the existing native resume argument and private session directory with
the same selected provider, model, effort and speed. It adds no authentication
route, account switch, replacement session or custom provider client. Native
failures and usage restrictions stop work; unfinished review evidence cannot be
converted into a skipped review. Tests use offline native-event fixtures only.

## Numeric instruction and native-context observations

The observation-only extension was reviewed on 19 October 2026 against the
[official non-interactive documentation](https://learn.chatgpt.com/docs/non-interactive-mode),
[App Server documentation](https://learn.chatgpt.com/docs/app-server),
[pinned Codex 0.163.0 protocol source](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/protocol/src/protocol.rs),
and Claude's [headless](https://code.claude.com/docs/en/headless) and
[context usage](https://code.claude.com/docs/en/statusline) documentation.
The account type, execution route, official commands, authentication persistence
and native recovery remain unchanged. No connected provider test was performed.

The worker projects only numeric context and compaction fields from newly appended
root Codex rollout records; this supplementary local schema is version-pinned and
best-effort, not a documented stable exec API. It never edits a rollout, forwards
transcript text, reads authentication fields or initiates model work. Unsupported
or missing evidence remains unknown. Model requests and cumulative accounting
continue through the unmodified official client. The scanner does not implement
a native headroom percentage, force compaction or reset sessions or usage.

Catalogue fingerprints and file sizes describe available pinned image material,
not model loading. Root tool-output measurements discard text and identifiers.
These optional observations are private run metrics without external export.
