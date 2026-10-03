# Protected host service and Docker trial

Status: recommended candidate for testing, not an accepted architecture or a
working installer. Prepared on 1 October 2026; handoff updated on 2 October 2026.

Read the [continuation handoff](../../docs/protected-runner-handoff.md) for the
agreed scope and research index before starting this trial.

The front runner is one protected native host service, ordinary Docker workers,
a constrained SSH commit signer, and renewable GitHub App tokens scoped to one
enrolled repository. The machine component is installed once. Repository
onboarding records a version, repository identity and check configuration;
credentials stay with the service. The worker runs `git commit -S` and
`git push` itself. Its signed object IDs remain unchanged.

This candidate avoids interactive vault approval for each operation and repeated
PAT creation. App tokens expire after one hour; every mint request must name the
registered repository ID and minimum permissions. Stopping renewal does not
revoke an existing token. [GitHub token contract](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app).

Use disposable keys and synthetic repositories until the host and Docker gates
pass. No VM provisioning is part of this trial. A failed gate stops later stages;
the result records what failed and what evidence is missing.

## Starting evidence

The public security suite passed 73 checks. Its direct worker additions passed
37: 18 signing, 18 push and one combined two-ticket flow. Three worker-created
signed commits reached two local remote branches unchanged. Separate review
confirmed the signing and combined flows. These are local protocol proofs.

Controls also proved that a same-UID process can read disposable service keys.
No protected native service identity or container-to-service transport has been
installed. The local signing descriptor is not a Docker transport, and the fake
provider is not a GitHub API implementation.

The attempted live preflight on this host failed:

| Probe | Observed result | Meaning |
| --- | --- | --- |
| Docker Engine version through its Unix API | Permission denied | This shell cannot use the tested Docker endpoint. |
| Bind a Unix service socket in a disposable directory | Operation not permitted | This shell cannot bind that named Unix endpoint; the anonymous socketpair fixture still works. |
| Change a disposable marker to another file owner | Operation not permitted | This process cannot bootstrap protected ownership. |
| Apply a nested native sandbox policy | Policy application failed | No secret-read denial was proved. |

Current repository instructions limit elevation to the specified GitHub/Git
operations. No service, account, socket ACL, host credential or Docker setting
was changed. These are execution-permission blockers, not automatic approval
review rejections. Any privileged installation must first have concrete,
reviewable installation and removal artefacts.

## Trial sequence

| Gate | Work | Required evidence before advancing |
| --- | --- | --- |
| 0. Package the fake-key service | Prepare the native service, installer, removal procedure, fixed job API and a protected executable/dependency chain. Review identities, files, privileges and permitted actions before installation. | Exact install effects and rollback are known; no real credential or general command/signing endpoint is included. These artefacts are not implemented yet. |
| 1. Prove host protection | Install under a dedicated service identity. Run a legitimate unattended job and attacks from a second host-agent process. | The job succeeds; the second process cannot read keys, replace code/policy, administer the service, steal grants or obtain unauthorised signatures/tokens. |
| 2. Connect an ordinary Docker worker | Replace the descriptor fixture with an authenticated container-to-native channel. Admit a fixed job rather than caller-supplied Docker options. | An actual container completes a permitted request; wrong-job, revoked and expired grants fail. Permitted retries retain the authorised result. Host agents cannot inspect/enter it or launch containers exposing protected data. |
| 3. Sign and push in Docker | Use a synthetic repository and fake provider. The worker creates signed increments and a dependent branch, then pushes its own objects. | Remote IDs, trees, signatures and parent chain match; wrong-repository access and unapproved signing fail. Cancellation closes authority and the controller tears down the worker. |
| 4. Prove application compatibility | Add .NET 10 and controlled Compose fixtures, then verify an unchanged tracked-file application snapshot. | Worker-local fixture endpoints work, documented checks pass, and controller-owned teardown runs after success, failure and cancellation. |
| 5. Test GitHub in a disposable destination | Enrol a selected-repository App installation and dedicated account signing public key. Test renewal, signed pushes and stacked draft PRs. | GitHub signature verification, App attribution, repository limits and provider ref rules are observed; outstanding-token revocation and failed-revocation intervals are recorded. |
| 6. Exercise the prompt lifecycle | Capture the supplied local ticket set separately from fresh remote main. Implement, test, obtain separate review, fix/recheck and create one draft PR per ticket targeting its predecessor. | Only eligible tickets run; dependent work starts from the predecessor's signed head; failures preserve results and request attention. No automatic merge or deployment occurs. |

The next session starts at gate 0. Installation and live test permissions are prerequisites
for gates 1 and 2; the current session could not meet them unattended. Do not
treat preparing artefacts, a successful same-UID fixture, or missing Docker
access as a passed protection gate.

## Host and Docker checks

Service credentials and state belong to a separate OS identity. Installed code,
interpreter, dependencies, enrolment and update policy must be protected from
agent writes. The service does not execute project scripts. Administrative
authority stays with the operator. Apple provides system-service registration
facilities; this runbook does not claim an installer has been built.
[Apple service management](https://developer.apple.com/documentation/servicemanagement/smappservice).

A separate owner protects files but does not authorise callers. UID checks do
not distinguish agents sharing a user account. Before installation, define how
the trusted launcher admits this job and protects its grant from another agent.
For the first trial, an operator may admit one immutable synthetic job before
unattended execution. Automatic admission from future prompts remains a design
question; do not introduce repeated per-signature approvals to hide that gap.

Prove denials for service control, credential reads and writes, arbitrary
signing, arbitrary repository/permission selection, another job's grant,
Docker inspection/exec, privileged launches and mounts of protected directories.
Use disposable equivalents of ambient authenticated clients too. Protecting
new SDLC keys does not automatically protect existing interactive-user clients.

The worker receives neither the host Docker socket/API nor the host home,
issuer directory, signing private key, SSH agent or unrestricted controller API.
The agent may run with Full access inside its container while Docker restricts
mounts, namespaces, capabilities and resources. Docker management is a trusted
operation and must reject caller-selected escape options.
[Docker daemon security](https://docs.docker.com/engine/security/).

The current Docker backend belongs to the interactive host user. A new native
service identity must not be assumed to have access to it. Test the protected
controller's access and socket lifecycle explicitly; do not solve it by making
the API world-accessible or giving the worker the socket. Inspect effective
container settings from a trusted operator/controller.

Grant expiry stops future signing/renewal. Already emitted signatures remain
valid. Raw GitHub tokens can remain usable until expiry if revocation fails.
Repository token scope does not impose branch or review policy. Tests must
distinguish provider controls from the synthetic service's stronger job-aware
gate. Treat tokens as opaque values; do not assume a fixed length or format.
The current signer intentionally reuses its job capability across increments
and caches the signature for an approved same-job retry. Do not require all
replays to fail; cross-job, revoked and expired requests must fail.

## Application gate

Use a synthetic workload to test SDK and package restore, Compose-backed
services, unit tests, integration tests and teardown. Keep a real application's
commands and configuration in its own private test plan.

Host-published ports alone do not provide worker `localhost` endpoints. Prove
the controller's job network/relay arrangement synthetically before running the
application.
The worker must not gain unrestricted daemon access to make Compose convenient.

Build from a reviewed tracked-file snapshot of the selected commit. Git ignore
rules do not control Docker build contexts; a local ignored ticket can still be
sent to a build. Keep ticket packs, authentication, ignored local files, logs
and job results outside the build context. Use generated fixture credentials.
The real completed ticket example drives compatibility, not permission to
repeat its implementation or modify its existing PRs.

## Evidence and cleanup

For every gate, record the source/image versions, effective owner/permissions,
job ID, tested caller identity, expected and actual denials, signed object IDs,
remote refs, teardown and outstanding authority. Mark results passed, failed,
blocked or not run. Keep reports outside tracked source; retain no key/token
bytes in reports or command arguments.

Stop issuance and new signing before cleanup. Terminate only this trial's jobs,
containers, networks and volumes; preserve bounded diagnostic results privately.
Account for remaining public signatures and tokens. Disable the test App
installation if token revocation cannot be confirmed. Service rollback restores
protected code/policy and does not cancel existing tokens or public signatures.
The installer must list exactly which trial resources it removes; avoid broad
Docker cleanup or deleting shared credential/configuration directories.

The existing local proof can be reproduced without credentials or Docker:

```sh
python3 -m unittest discover -s spikes/security -p 'test_*.py' -v
```

This command does not install a service or pass the live gates.
[Direct worker evidence](direct-worker-notes.md),
[signing protocol](worker-signing-notes.md),
[push protocol](worker-push-notes.md).
