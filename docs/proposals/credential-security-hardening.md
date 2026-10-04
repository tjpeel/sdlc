# Credential security investigation and hardening handoff

Status: source investigation and proposed hardening, recorded on 4 October 2026.
The changes below are not implemented by this document. Sandcastle was an early
inspiration for SDLC; adopting it is outside this work.

SDLC's active Go CLI has stronger credential separation and container safeguards
than the reviewed Sandcastle implementation. Its remaining risks can be reduced
without introducing interactive approval for every operation. Unattended work
can run within a fixed policy and stop at a checkpoint when an operation exceeds
that policy. A usable credential must still belong to a trusted client process,
and the administrator controlling that process remains trusted.

## Scope and evidence

- Sandcastle: public source at commit
  [`62307ab65ad9f8414f7a4893d96328a0353b0c3f`][sc-revision], dated 2 October 2026;
  package version `0.12.0`. Findings apply to that snapshot.
- SDLC: the initial review included the working changes above commit
  `5f80aada9ef4e0d7c281229962a05acee16661d2`. Its core credential boundaries were
  rechecked at `3c8b610621ea22b57653697d483f6151dac423f2` before this handoff.
- The SDLC baseline is the installed Go CLI. The earlier Compose/Python runner
  and research prototypes have different mounts and are not the active baseline.
- The review covered credential intake, storage, transport, refresh, runtime
  isolation, Git metadata, logs, build inputs and cleanup. It did not establish
  that either application withstands a live attack or that its dependencies are
  free of vulnerabilities.
- No account was connected, real credential file opened, provider sign-in
  automated or connected provider test run. Offline checks used disposable data.

Official documentation was consulted on 4 October 2026. Recheck it against the
selected account type, execution mode and pinned client versions before changing
authentication or running connected validation. Follow
[provider usage](../provider-usage.md) and
[publication safety](../publication-safety.md).

## Current SDLC credential boundary

| Area | Active behaviour | Evidence |
| --- | --- | --- |
| Intake | Official Codex device login and official Claude login run inside dedicated containers. Host Codex/Claude caches are not automatically imported. API-key execution is currently unimplemented. | [Native authentication helper](../../internal/providerauth/container.py), [CLI contract](../cli.md#provider-login) |
| Storage | Separate local Docker volumes named `sdlc-auth-<INSTALLATION_ID>-codex` and `sdlc-auth-<INSTALLATION_ID>-claude`. Native files appear at `/provider-auth/auth.json` and `/provider-auth/.credentials.json`. Claude can also retain native account/setup metadata. | [Volume management](../../internal/providerauth/auth.go), [native filenames](../../internal/providerauth/container.py) |
| File protection | Credential directories use UID/GID `1000:1000` and mode `0700`; credential files require private ownership, mode `0600`, regular-file and link checks. Volume labels, local driver and driver options are validated. | [Authentication implementation](../../internal/providerauth/auth.go), [file checks](../../internal/providerauth/container.py) |
| Transport | The selected cache is mounted into its worker. Secret values are not passed through Docker arguments or environment metadata. Child environments exclude inherited host credentials and routing overrides. | [Container arguments](../../internal/providerauth/auth.go), [child environment](../../internal/providerauth/container.py) |
| Runtime | Recorded image, non-root client, read-only root filesystem, dropped capabilities, `no-new-privileges`, resource limits, private tmpfs and disabled Docker logs. Provider execution requires a local Linux-container engine. | [Worker launch](../../internal/providerauth/auth.go), [engine validation](../../internal/runtimeimage/runtime.go) |
| Status and concurrency | Offline status mounts the selected cache read-only with no network. Per-provider leases and surviving-container checks prevent concurrent reuse; status cannot establish remote validity. | [Authentication manager](../../internal/providerauth/auth.go) |
| Publication | Provider and check workers have no GitHub publishing cache or signing key. The trusted publisher receives a separate GitHub cache and resolves signing through a separate component. | [Publisher](../../internal/workrun/docker_publish.go), [credential boundaries](../github-credentials.md) |

These are filesystem and process protections, not encryption. Docker
administrators and sufficiently privileged host users can inspect provider
volumes. The 1Password signing resolver does not protect native provider caches.
Rebuilds preserve those caches. See the
[documented residual risks](../../README.md#security-boundary-and-risks).

## Sandcastle comparison

Sandcastle primarily transports user-supplied environment values to official
clients. No automatic host authentication-cache import, Keychain extraction,
token parsing or custom token-refresh implementation was found in the reviewed
source. Claude's scaffold requests a `CLAUDE_CODE_OAUTH_TOKEN` from the official
`claude setup-token` command, or an Anthropic API key.

Credentials normally reside in plaintext `.sandcastle/.env`. The generated
Git ignore excludes that file and logs, but Sandcastle does not enforce private
file permissions or encryption. Its resolver forwards every declared nonempty
environment value together, including any other provider keys or GitHub tokens.
This differs from SDLC's selected-provider cache and separate publisher.
[Credential scaffold][sc-init], [environment resolver][sc-env],
[environment merging][sc-merge-env].

| Runtime | Credential handoff and limits |
| --- | --- |
| Docker / Podman | Values occur in `run -e NAME=value` arguments and container configuration. Container processes can read them. Generic user mounts can expose native auth files; they are writable unless configured read-only. |
| Vercel | Sends resolved environment values to `Sandbox.create`. Credentials enter the remote sandbox and cloud-provider trust boundary. Cleanup calls `stop()`; this source does not establish deletion or secure erasure. |
| Daytona | The creation callback ignores Sandcastle's resolved environment. Normal resolver/agent values do not reach the runtime; separate SDK creation configuration needs its own review. |
| `noSandbox()` | Runs on the host, inheriting the entire process environment and native host credential-store access. Its close operation does not remove host credentials. |

Runtime evidence: [Docker][sc-docker], [Podman][sc-podman], [Vercel][sc-vercel],
[Daytona][sc-daytona], [host execution][sc-host].

### Findings at the pinned Sandcastle revision

| Finding | Severity and condition | Evidence |
| --- | --- | --- |
| Writable host Git metadata | High for a malicious agent with mount write access. Docker/Podman share the original or parent `.git` across `head`, `branch` and `merge-to-head`. Changed hooks/configuration can execute on later host Git operations; `merge-to-head` performs a host merge automatically. Isolated cloud providers do not use these mounts. | [Git mounts][sc-git-mounts], [host merge][sc-host-merge] |
| Host permission bypass | High in default non-interactive `run({sandbox: noSandbox()})`. The orchestrator requests bypass; Claude and Codex disable native safeguards by default. Explicit Claude permission mode or Codex auto-review changes that behaviour. Standalone interactive execution handles the distinction correctly. | [Orchestrator][sc-orchestrator], [Codex flags][sc-codex], [Claude flags][sc-claude] |
| Credentials in startup errors | Medium: failed Podman startup includes secret-valued arguments in Node's error message, propagated into ordinary logs. Docker has a similar fallback when stderr is empty. File-mounted credentials are not exposed by this particular error path. | [Podman errors][sc-podman], [Docker errors][sc-docker-errors], [error logging][sc-log-errors] |
| Credentials in build context | Medium when customised: the default context is `.sandcastle/`, where `.env` and logs live, without generated build-ignore exclusions. The default Dockerfile does not copy them into its image; a custom broad `COPY` can. Git ignore is not a build exclusion. | [Image build][sc-build], [scaffold][sc-init] |
| Container safeguards and reproducibility | Medium/design limitation: local launches omit SDLC's capability removal, read-only root, privilege-escalation restriction, memory and PID limits. Generated provider images use floating base tags and unpinned client installation. This is not evidence of a known dependency vulnerability. | [Launch arguments][sc-docker], [image templates][sc-images] |
| Retained diagnostics | Medium: logs and default session capture retain model/tool output without secret filtering. Output-file modes are not explicitly restricted. Actual local readability depends on parent permissions and umask. | [Logs][sc-display], [session capture][sc-capture] |

The Codex scaffold also emits `OPENAI_KEY`, with no mapping found to the
documented Codex authentication variables. Treat that as an unresolved setup
correctness issue rather than a credential-exfiltration finding. Daytona's
dropped environment is likewise a credential-delivery defect, not proof of a leak.

## SDLC findings to carry forward

### Authenticated tools can read their provider cache

The current headless launch selects Codex `danger-full-access` with approvals
`never`, and Claude `bypassPermissions`. The official CLI and its tools share the
worker's usable native cache. Instructions to leave tests and dependency
installation to credential-free check workers are not an enforced boundary.
The provider shell can still execute repository code or read its own cache.
[Native flags](../../internal/providerauth/headless.py),
[worker mounts](../../internal/providerauth/headless.go),
[controller instructions](../../internal/workrun/runner.go).

Preserve the existing publisher/check separation. Add supported native tool
confinement and explicit cache-read denials where enforceable. Protect both
shell commands and native file tools, prohibit unsandboxed fallback, and keep
settings under controller control. A compromised official client or Docker
administrator remains outside that protection.

### Network destinations are unrestricted

Authenticated workers use bridge networking, and ordinary checks also have
outbound access. There is no enforced destination allowlist or separation from
reachable host/LAN services.
[Headless networking](../../internal/providerauth/headless.go),
[check networking](../../internal/workrun/checks.go).

Enforce default-deny egress outside workers, with distinct policies for login,
provider execution, dependency fetching, checks and publication. Allow only
required destinations; account for DNS, direct-IP traffic, IPv6, redirects,
private addresses and metadata endpoints. A proxy environment variable alone
is not enforcement. Allowed destinations and model requests remain disclosure
channels, so destination restrictions do not establish complete secret protection.

### Authentication refresh and retained session state

Headless workers bind private host session storage at `/session` and mount a
separate provider volume at `/provider-auth`. An auth symlink inside the native
session directory points into that volume. Native atomic refresh can replace
the symlink with a regular credential file. Abrupt termination can bypass final
cleanup and leave that file in retained host session storage.
[Mounts](../../internal/providerauth/headless.go),
[`auth_link` and `finish_auth`](../../internal/providerauth/headless.py).

The handoff recheck identified an additional failure path for Claude:
`finish_auth` tries `os.replace` from the session bind into the separate auth
volume. Cross-mount rename normally fails with `EXDEV`; its `finally` block then
unlinks the refreshed source. If the native client has replaced the symlink,
cleanup can fail while losing the refreshed file and leaving the old cache.
An injected-`EXDEV` offline check confirmed that error handling. Actual native
refresh across Docker mounts was not exercised. Earlier discussion described
moving refreshed Claude auth as intended behaviour; it was not verified.

Preferred direction: native authentication writes remain inside the persistent
provider volume, while only approved resumable state reaches host run storage.
Claude's interactive path already keeps `CLAUDE_CONFIG_DIR` in the volume with
controlled settings/instruction overlays. Codex needs equivalent protection for
configuration, agents, hooks, plugins and routing. Preserve native account/setup
state and determine the pinned clients' actual session/database requirements.
[Interactive implementation](../../internal/providerauth/interactive.go),
[native configuration](../../internal/providerauth/container.py).

A tmpfs-only native configuration root would prevent crash remnants on host
storage but could lose refreshed authentication during a crash. It is a weaker
default for recovery unless that tradeoff is deliberate. Exporting transcripts
alone still cannot guarantee they contain no secrets printed by an agent.

### Privileged integration checks

The optional per-job Docker daemon is privileged, and check workers receive its
socket and network namespace. It avoids mounting the host Docker socket and
provider caches, but is not a boundary against hostile tests. A shared Docker
Desktop VM does not isolate these checks from other workloads in that same VM.
[Check daemon](../../internal/workrun/checks.go),
[Docker privilege documentation][docker-privilege].

For hostile or less-trusted checks, use a disposable job VM with no provider
cache, publisher credential or shared host directory. Keep ordinary checks
unprivileged. This differs from merely moving every SDLC component onto one
shared remote host; coordinate with the
[remote VM proposal](remote-vm-execution.md).

### Retention, quotas and credential lifecycle

Raw provider events pass into retained output without secret filtering. Private
storage limits local exposure, but diagnostics and transcripts remain sensitive.
Provider volumes have no application-enforced storage quota, dedicated provider
logout/purge is missing, and lost installation metadata can orphan caches.
[Output handling](../../internal/workrun/provider.go),
[documented storage limits](../cli.md#provider-login).

Add bounded logs, explicit raw-diagnostic retention, validated exports, session
retention and storage-pressure handling. Add logout through supported official
client commands and validated local purge under the existing provider lease.
Refuse purge while a container holds the cache. Distinguish local deletion,
remote revocation and secure erasure. Encryption of Docker storage and backups
reduces offline theft; it does not protect unlocked storage from its administrator.

## Proposed implementation order

1. **Correct authentication persistence.** Keep native refresh inside the auth
   store, retain only approved session state, and add safe crash reconciliation.
   Prioritise the conditional Claude cross-mount failure.
2. **Enforce network boundaries.** Separate runtime policies and restricted
   dependency fetching from checks. Keep enforcement outside the worker.
3. **Constrain native tools.** Validate supported sandbox and file-tool rules for
   the pinned clients without weakening the outer Docker restrictions. Stop
   when confinement is unavailable; do not silently select full access.
4. **Isolate privileged checks.** Introduce a disposable VM execution boundary
   where hostile-code containment is required.
5. **Complete operational controls.** Add retention, quotas/storage-pressure
   handling, logout, purge, orphan inspection and encrypted-storage guidance.

Current official guidance supports unattended constrained execution:
Codex's approval policy `never` works with restricted sandbox modes, and its
permission profiles support read denials. Claude supports automatically allowed
sandboxed commands, credential restrictions and disabled unsandboxed retries.
Claude's Bash sandbox does not cover native file tools or every helper process;
those need separate controls. Native sandbox compatibility inside the existing
Docker restrictions remains a validation task.
[Codex security][oa-security], [Codex permissions][oa-permissions],
[Claude sandboxing][claude-sandbox].

Do not create a custom subscription-token broker, replay tokens through a custom
client, switch identities to evade limits, or weaken managed provider policy.
Any design that moves inference authentication into a separate application must
use a documented provider-supported API/cloud route. Own-user native login and
an integration serving other users have different account/service boundaries.
[OpenAI authentication][oa-auth], [non-interactive guidance][oa-headless],
[Claude credential-use rules][claude-legal].

## Acceptance and validation for future work

| Change | Required evidence |
| --- | --- |
| Authentication persistence | Fake native clients atomically replace credentials across the real container mount layout. Refresh succeeds; cancellation, `SIGKILL`, controller restart and resume do not export auth into session state or lose a successfully persisted refresh. Model EXDEV and interrupted writes explicitly. |
| Session export | Allowlist the pinned clients' required resumable files/databases. Reject credential files, links, traversal, unsupported customization and unsafe metadata. Demonstrate resume after normal exit and interruption. |
| Egress | On an isolated test network with no provider access, mock allowed destinations succeed and disallowed destinations, direct-IP/DNS bypass and host/LAN routes fail. Fake credentials never reach provider services. Rules remain enforced when a worker ignores proxy variables. |
| Tool confinement | Disposable fake caches cannot be read by shell children, native file tools, alternate paths or symlinks. Test extension/helper and unsandboxed-fallback paths. Ordinary edits, Git candidate commits and credential-free controller checks remain usable without prompts. |
| Privileged checks | A disposable VM has no mounted or inherited provider/publication credentials. Per-job cleanup and resource/network boundaries survive failure. Ordinary checks do not acquire privileged execution. |
| Retention and storage | Exercise oversized output, full storage, interrupted writes and expired retention. Report truncation and preserve required checkpoints. Never claim redaction guarantees secret-free logs. |
| Logout/purge | The provider lease and surviving-container guard prevent races. Reject mismatched labels, unowned paths and arbitrary deletion targets. Report local purge separately from provider-side revocation. |

Offline tests are the first gate. Any connected account trial requires separate
explicit authorization under [provider usage](../provider-usage.md), with the
chosen account type and execution mode reviewed against current official rules.
Stop on policy refusal, access denial, exhausted usage or unsupported auth; an
unattended job may need a human checkpoint to resolve those conditions.

## Validation recorded in this investigation

- Earlier offline checks passed: 14 provider-authentication Python tests,
  20 interactive tests, seven headless tests, and `go test` for
  `internal/providerauth`, with downloads disabled and temporary Go cache storage.
  These establish fixture/launch contracts, not live account acceptance or
  complete runtime isolation.
- A disposable Node failed-command check confirmed that `execFile` includes
  argument values in its error message, supporting the Podman logging finding.
- An offline injected-`EXDEV` check of the SDLC Claude cleanup confirmed removal
  of the refreshed source and retention of the old destination after the error.
  It did not reproduce native refresh or a real cross-mount Docker rename.
- Sandcastle's complete test suite, live cloud retention, connected provider
  behaviour and proposed hardening mechanisms were not tested.

[sc-revision]: https://github.com/mattpocock/sandcastle/commit/62307ab65ad9f8414f7a4893d96328a0353b0c3f
[sc-init]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/InitService.ts#L409-L441
[sc-env]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/EnvResolver.ts#L49-L72
[sc-merge-env]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/mergeProviderEnv.ts#L8-L30
[sc-docker]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/DockerLifecycle.ts#L126-L169
[sc-docker-errors]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/DockerLifecycle.ts#L7-L19
[sc-podman]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/sandboxes/podman.ts#L195-L248
[sc-vercel]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/sandboxes/vercel.ts#L159-L169
[sc-daytona]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/sandboxes/daytona.ts#L80-L95
[sc-host]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/sandboxes/no-sandbox.ts#L49-L88
[sc-git-mounts]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/SandboxFactory.ts#L264-L287
[sc-host-merge]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/SandboxLifecycle.ts#L408-L445
[sc-orchestrator]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/Orchestrator.ts#L140-L146
[sc-codex]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/AgentProvider.ts#L782-L797
[sc-claude]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/AgentProvider.ts#L1190-L1203
[sc-log-errors]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/run.ts#L772-L785
[sc-build]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/DockerLifecycle.ts#L59-L64
[sc-images]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/InitService.ts#L207-L309
[sc-display]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/Display.ts#L144-L228
[sc-capture]: https://github.com/mattpocock/sandcastle/blob/62307ab65ad9f8414f7a4893d96328a0353b0c3f/src/AgentProvider.ts#L465-L478
[oa-security]: https://learn.chatgpt.com/docs/agent-approvals-security
[oa-permissions]: https://learn.chatgpt.com/docs/permissions
[oa-auth]: https://learn.chatgpt.com/docs/auth
[oa-headless]: https://learn.chatgpt.com/docs/non-interactive-mode
[claude-sandbox]: https://code.claude.com/docs/en/sandboxing
[claude-legal]: https://code.claude.com/docs/en/legal-and-compliance
[docker-privilege]: https://docs.docker.com/engine/containers/run/#runtime-privilege-and-linux-capabilities
