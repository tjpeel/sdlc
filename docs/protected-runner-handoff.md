# Protected unattended runner handoff

Latest continuation: [worker execution handoff](worker-execution-handoff.md).
The operator is now considering a simpler trusted-initiator model with no host
mounts apart from credential injection and Docker-in-Docker checks. The newer
handoff records its privilege limits, credential findings, Sandbox preflight and
T3 remote assessment. This document preserves the earlier protected-service
proposal; do not assume its gate 0 remains the selected next action.

Updated: 2 October 2026. Research and test evidence: 1 October 2026.

Continuation on 2 October: the operator selected a functional Docker-in-Docker
compatibility spike and added interchangeable Codex/Claude implementation and
review. See [the Docker ticket job guide](docker-ticket-jobs.md) for the new
runner and validation. This does not pass the protected-service gates below;
their installation and credential boundary remain separate work.

SDLC should encode the operator's development lifecycle so agents can complete
authorised work while the operator is away. The immediate goal is a prompt from
another local repository requesting implementation of a supplied ticket set in
Docker, with the outer harness and reusable credentials protected.

The front runner for testing is a protected native host service, ordinary
Docker workers, a constrained SSH signer and renewable GitHub App tokens scoped
to one enrolled repository. This is a recommendation for a trial. It is not an
accepted architecture, installed service or production security claim.

**The next action is gate 0:** prepare a reviewable fake-key service package,
installation/removal artefacts and bounded job API. Native host protection and
container-to-service connectivity remain unproved. The last live preflight was
blocked by execution permissions. Keep the subsequent trial on disposable
credentials until its protection gates pass.

## Read first

| Document | Purpose |
| --- | --- |
| [Repository instructions](../AGENTS.md) | Public-source and credential handling rules. |
| This handoff | Current scope, requirements, recommendation, research index, unresolved questions and resumption plan. |
| [Host-service trial runbook](../spikes/security/host-service-test-runbook.md) | Ordered gates, positive/negative controls, stop conditions and cleanup. |
| [Direct worker findings](../spikes/security/direct-worker-notes.md) | Latest signing/push contract and the proposed authority split. |
| [Security investigations](../spikes/security/README.md) | Evidence catalogue, source links and earlier alternatives. |
| [Current runtime README](../README.md) | How the original credential-bearing container experiment works. |

The direct worker requirement below supersedes the earlier helper-created
commit approach in [the options snapshot](options.md). VM/Sandboxes research
remains historical; VM provisioning is outside the current trial. No accepted
ADR, implementation tickets, branch, commit or PR was created by this discovery.
No `CONTEXT.md` or context map existed when this handoff was prepared.

## User requirements

These requirements came from the operator; the service design is separate.

- Begin repository work from up-to-date remote main in an isolated workspace.
  Local uncommitted changes are not the starting state. Capture explicitly
  supplied local tickets/specifications separately because a fresh clone may
  omit them.
- Follow ticket dependencies. Each dependent ticket starts from its
  predecessor's completed, worker-created signed head.
- Implement, test, obtain a separate review, fix findings and rerun checks.
  The reviewer reports findings; the implementer makes fixes.
- The worker itself runs `git commit -S` and `git push`. Its signed commit IDs
  must stay unchanged when pushed and used as a dependent ticket's parent.
- Create one validated draft PR per ticket. The first targets main; each later
  PR targets its predecessor's branch. Preserve failed work and request
  attention. General job merge/deployment is outside the unattended endpoint.
- Permit the agent's Full access mode inside the Docker job boundary. This does
  not grant privileged Docker execution, host mounts or the host Docker API.
- Protect reusable signing and token-issuing keys, delegated job authority and
  the outer harness from other host agents. A public repository still needs
  write authentication.
- Run unattended after bootstrap without interactive vault approval for each
  signature or repeatedly creating repository credentials for each prompt.
- A real, already-completed local ticket example drives the initial trial. Use
  it to investigate compatibility; do not repeat completed work or modify its
  existing PRs as part of the spike.
- Sandcastle is an inspiration whose process was investigated. The operator
  explicitly does not want to adopt it.

Standalone tasks should eventually save validated artefacts without requiring
a repository. T3 Code, selecting another execution host and standalone work are
deferred while the one local repository case establishes installation/security.
Do not assume a T3 environment automatically creates the required isolated
workspace or attaches to an independently launched CLI conversation.

## Recommended trial and trust boundaries

The native service would own reusable keys, repository enrolment, policy,
installed code/dependencies and job records under a separate OS identity. The
outer harness submits a bounded request. A Docker worker receives a signing
capability and repository-scoped transport access. It creates and pushes its own
signed objects. The service executes fixed operations rather than project code.

There are two boundaries to prove: containment of the execution worker and
protection of credential/service authority from host agents. Docker containment
alone does not protect host-owned keys or authenticated clients. A process with
management access can inspect or enter a worker and take its delegated token.

The proposed installation is one protected machine component plus small
per-repository enrolment. Enrolment would record immutable repository identity,
SDLC version, runtime/check requirements and allowed input locations. Credentials
would stay outside repositories; running jobs would retain their pinned version.
Distribution, version updates and the exact onboarding interface are not settled
or implemented. The operator's earlier onboarding question was deliberately
left open rather than treating a loaded choice as agreement.

The worker token intentionally delegates writes within the enrolled repository.
Provider token scope alone does not restrict branches or require reviewed code.
Separate provider rules or enforced transport policy must supply those limits.
An SSH Git signature also does not encode a repository identity: an approved
public signed commit can be copied elsewhere. Job signing policy limits new
signature requests; provider token scope limits authenticated repository access.
A GitHub token does not constrain which signatures a signer may produce.

## Research and investigation index

| Investigation | Finding | Evidence and limits |
| --- | --- | --- |
| Same-UID host access | An ignored `0600` file in a `0700` directory remained readable/writable to another process using its owner identity. New signer/issuer fixtures demonstrate the same problem. | [Host control](../spikes/security/test_host_identity.py), [signer tests](../spikes/security/test_worker_signer.py), [push tests](../spikes/security/test_push_spike.py). Passing controls demonstrate failed isolation. |
| Local ticket capture | Bounded inline-linked Markdown is captured as bytes with hashes and metadata. Escapes, symlinks, hardlinks, FIFOs, protected directories and excessive input are rejected. | [Collector](../spikes/security/input_snapshot.py), [tests](../spikes/security/test_input_snapshot.py). Root/ancestor enrolment is assumed; capture is non-atomic, parser partial, secret-content detection absent. |
| Earlier helper publication | A fresh bare helper imported objects, preserved trees, rebuilt signed commits and checked stacked parents. Adversarial review fixed ambient Git configuration, transport quoting and base-check issues. | [Prototype](../spikes/security/publisher_spike.py), [limits](../spikes/security/publisher-notes.md). Local remotes/mock PRs only; rebuilding signed commits does not meet the later worker contract. |
| Worker-created SSH signing | Git calls a shim; a constrained signer returns a signature for an exact authorised payload; Git in the worker creates the signed object. Incremental approvals and cached retries work. | [Signer/shim](../spikes/security/worker_signer.py), [protocol](../spikes/security/worker-signing-notes.md). Anonymous inherited descriptor is a local fixture, not deployed Docker transport or production review admission. |
| Worker push and renewal | Real worker Git pushes unchanged objects. A synthetic provider enforces repository-scoped fake tokens; renewal cannot choose a different repository, permissions or lifetime. Receiver policy separately checks refs/signatures. | [Provider](../spikes/security/push_spike.py), [protocol](../spikes/security/worker-push-notes.md), [fixtures](../spikes/security/worker_push_fixtures.py). Stdio adapter, HMAC fake tokens and job-aware gate are not GitHub or OS isolation. |
| Combined direct worker flow | Three broker-signed worker commits across two tickets reach two remote branches unchanged, with the predecessor's signed head as parent. Wrong-repository push fails. | [Acceptance test](../spikes/security/test_worker_end_to_end.py). Local Git only; no live PR, GitHub verification or container boundary. |
| Application compatibility | An application workload was inspected privately without starting services. Its configuration stays outside public source. | [Runbook application gate](../spikes/security/host-service-test-runbook.md#application-gate). SDK, network adaptation and complete application checks remain untested in the proposed worker. |
| OrbStack, job VMs and Docker Sandboxes | Earlier investigation compared isolation, controller access, nested Docker and credential forwarding. | [Security research](../spikes/security/README.md). No VM/Sandboxes adoption; no live proof of those alternatives. |
| Sandcastle and client integration | Provider/lifecycle/event/branch patterns informed discovery. Desktop/T3 SSH and app-server routes were considered. | [Options snapshot](options.md), [original validation](validation.md). Reference research only; no Sandcastle adoption or proven UI attachment. |

The capture prototype accepts Markdown under `.tickets` and `.specifications`,
follows inline links without titles and reads explicit status/dependency
metadata rather than scheduling work. Repository-document references must
resolve against the job clone rather than the personal checkout. Its supported
parser is deliberately smaller than general Markdown.

On 1 October the security suite passed **73** checks: 24 earlier publisher,
12 host/input, 18 worker signing, 18 push and one combined flow. Signing and the
combined flow had independent review; the coordinator repeated the push suite.
The original runtime suite passed **84** earlier that day. These are dated
results; they do not prove a protected service or live container connection.

No real credential was a spike fixture. Ticket bodies and real-case metadata
were kept outside public source. Temporary private reports are optional context,
not dependencies of this handoff. Ask the operator for the current target and
ticket roots only if they are missing from the new prompt or available authorised
context, before performing target-repository work.

## Existing code versus future work

The launcher [scripts/sdlc.py](../scripts/sdlc.py) can be called from another
repository using its path. It selects an explicit profile/repository and starts
the original image. This is not an installable protected host service or an
automatic repository onboarding process.

The original runtime in [runtime/](../runtime/) uses a Node/Bookworm image,
named workspace/authentication volumes, one supplied ticket per run and an
optional SSH overlay. It supplies a raw PAT and SSH signing private key to the
worker. Prompted publication separation and account/signature checks do not
enforce independence from that worker. Its publisher expects a user token and
checks `gh api user`; it is not GitHub App installation-token compatible.
The worker image has no .NET SDK or Docker/Compose toolchain. No ticket scheduler,
production admission service or independent validated publication gate exists.

The repository already has dependency-update/merge workflows. They were not
changed by this discovery and are separate from the requested job endpoint.
The earlier generated-key Docker/SSH smoke test is also separate from the
currently unproved protected-service architecture.

All handoff/spike files are saved in the working tree. As of this handoff, the
security directory was untracked and README edits were uncommitted. Preserve
them; do not assume they exist in Git HEAD or overwrite them as fresh work.
No commit or publication was requested for this handoff.

## Unresolved questions

| Question | Why it blocks progress | Next evidence or decision |
| --- | --- | --- |
| Who admits a job and protects its grant? | UID cannot distinguish agents sharing an account. A separate file owner does not authenticate callers. | Specify bounded synthetic admission and prove another host agent cannot steal/use the grant. Future automatic prompt admission remains open. |
| Who admits each exact signing payload? | Fixture `authorize`/`approve` methods are trusted test setup, not a validation/review system. | Bind checks/review to immutable candidate bytes while allowing the worker's required incremental commits. |
| How is the native service installed and updated? | User-writable code, interpreters, libraries or policy could expose protected keys. | Prepare concrete install/removal/update effects and test protected ownership plus restart/rollback. |
| How does the separate service identity control Docker? | The current backend belongs to the interactive user; access and socket recreation cannot be assumed. | Prove controller access without granting other agents or workers unrestricted management. |
| What is the container-to-service transport? | The inherited descriptor works only in the local process fixture. | Implement authenticated, bounded transport and test actual container requests, failures and cancellation. |
| How does Compose work without a worker Docker socket? | Test endpoints and bind paths depend on where the daemon and test runner execute. | Prove bounded controller actions and worker-local fixture networking. |
| Will the organisation permit the chosen App/signing setup? | Installation approval, actor attribution and signature verification are external requirements. | Obtain authorised disposable GitHub evidence before work-repository enrolment. |
| What happens on base drift, interruption and ambiguous publication? | Current experiments lack durable concurrent state/recovery and a remote base cannot be atomically frozen by a local check. | Decide policies and test leases, budgets, restart recovery, token revocation and duplicate PR handling. |
| How will stacked PR CI run? | A repository's CI must admit predecessor-targeted PRs as well as PRs targeting the default branch. | Verify CI admission without silently adding workflow permissions or changing the real example. |

GitHub App research established selected-repository, explicit-permission token
minting and one-hour expiry. The protected issuer would hold the App RSA key;
the separate SSH signing key remains account-level. Public visibility does not
waive write authentication. Contents write and Pull requests write are distinct
needs; workflow edits may require additional permission. The API actor is the
App, separate from Git author/committer and signing-account identity.

Ending a job stops renewal but does not invalidate issued GitHub tokens. Revoke
outstanding tokens explicitly; failed revocation or controller failure leaves
them usable until expiry. Minting a replacement does not imply old-token
revocation. The fake provider's short expiry and immediate job-aware denial are
additional test controls. It checks tokens at connection start; expiry or
revocation during an already-authenticated push is not rechecked before ref
updates. Issuer state also lacks concurrent transactions. Treat real tokens as
opaque values.
[Token contract](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app),
[installation authentication](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation).

A one-repository PAT and SSH deploy key were investigated as alternatives. A
PAT lacks the App-style child-token renewal mechanism; a deploy key alone
does not provide REST PR creation. Interactive 1Password approvals do not meet
unattended execution. Neither a protected file nor a non-exportable signer
prevents misuse through a callable authenticated client; ambient user `gh` and
signing authority need their own enforced boundary.

## Active plan of action

Continue the [staged runbook](../spikes/security/host-service-test-runbook.md).
Later stages require the earlier gates to pass; preparing files is not proof.

1. **Gate 0, not started:** define the fake trial's trusted admission, fixed
   service operations, owners and transport. Prepare the minimal native service,
   protected executable/dependency chain, install/removal artefacts and tests.
   Keep installation effects concrete and reviewable before a privileged action.
2. **Gates 1-2, not proved:** install with the authorised mechanism and prove
   legitimate unattended operation plus host-agent denials. Then connect an
   actual ordinary Docker worker with no host credentials or management socket.
3. **Gate 3:** reproduce direct signed commits and pushes inside that container,
   including dependent branches, rejected wrong-scope requests and teardown.
4. **Gate 4:** prove synthetic .NET/Compose compatibility, then verify an
   unchanged tracked-file snapshot of the example application. Keep supplied
   ticket packs and ignored local state out of Docker build contexts.
5. **Gate 5:** test an explicitly authorised disposable GitHub installation,
   Verified signatures, scoped writes, renewal/revocation and stacked draft PRs.
6. **Gate 6:** run the prompt lifecycle with input status/dependency handling,
   separate review, candidate-bound checks, failure preservation and recovery.

The last attempted live preflight on 1 October was blocked: Docker Unix API
access was denied, a named Unix socket bind returned EPERM, changing a fake
marker to another owner returned EPERM, and a nested sandbox policy could not
be applied. Anonymous socketpair IPC did work. No service identity, installed
daemon, host ACL or real credential was created. No elevation was attempted;
these were permission failures, not automatic approval review rejections.

The operator's session instructions limited elevation to `gh auth status`,
`gh pr`, `gh api` and `git push`. A future session must check its current
permissions/instructions rather than assuming it may elevate native install or
Docker commands. The operator also requested ordinary branch/commit names,
without a `codex/` branch prefix or Conventional Commit prefixes. Privileged
setup remained subject to those elevation restrictions during the unattended
spike attempt.

The compatibility trial needs synthetic SDK/package restore, Compose, build and
test checks, with controller-owned teardown after failure or cancellation.
Published daemon-host ports do not establish worker-local endpoints. Keep real
application commands, ports and service configuration in a private test plan.
Use generated fixture credentials, never deployed service credentials or shared
queues.

## Starting a new conversation

Suggested prompt:

> Read AGENTS.md, docs/protected-runner-handoff.md and
> spikes/security/host-service-test-runbook.md. Continue gate 0 of the protected
> unattended runner trial: prepare the fake-key native service, bounded job
> admission/API and reviewable install/removal artefacts. Preserve the worker's
> own signed commit and push contract. Use disposable credentials, retain
> working-tree changes, and report any permission-blocked live gate accurately.

Local protocol reproduction, with Python, Git and OpenSSH:

```sh
python3 -m unittest discover -s spikes/security -p 'test_*.py' -v
python3 -m unittest discover -s tests -v
```

The next session should record fresh evidence when implementation changes or
environment changes warrant it. Keep keys, real account settings, ticket bodies,
authentication, reports and logs outside public source/build contexts. Before
any publication inspect the actual staged files; ignore rules alone do not
protect already tracked content.
