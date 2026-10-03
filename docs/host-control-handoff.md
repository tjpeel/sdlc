# Host control boundary handoff

Updated: 2 October 2026.

The Colima worker runtime passed. Protection from further commands by the
initiating harness has not been established. The next useful test is one
manually admitted job whose VM management belongs to a different OS account.
The [guided test](host-control-test.md) now supplies a public-source bundler,
manager readiness gate and bounded shell probe. The separate-account test has
not run; the prepared probes cover part of the denial matrix below.

The [runtime report](colima-trial.md) records the live results. The broader
[worker execution handoff](worker-execution-handoff.md) preserves credential,
Docker Sandboxes, T3, remote-machine and Windows research. This document updates
the next step after the operator challenged retained host management access.

## What we learned

| Question | Evidence |
| --- | --- |
| Can the actual worker run in Colima? | Yes. The passing full run built the image, passed 132 offline tests, and checked the installed Codex/Claude CLIs and catalogues. |
| Can it run Docker-backed work? | Yes. Privileged DinD, nested Compose/build/relative binds/localhost and the worker's .NET SDK passed. |
| Can the runner implement and review a ticket? | Both provider orders passed using simulated responses. Actual authenticated model behavior remains untested. |
| Can it sign and publish? | Real signed commits and local Git pushes passed. Draft-PR API operations were simulated; no GitHub write occurred. |
| Were credentials embedded in the image? | Generated keys and fake tokens entered runtime tmpfs after the build. No vault access or host authentication was used. |
| Did it avoid host shares? | No Mac filesystem sharing or forwarded SSH agent; no guest-directory or guest outer Docker-socket binds in the worker/daemon. Internal named volumes and tmpfs remained. |
| Did cleanup work? | The passing run removed its containers, volumes, worker image, VM and Docker data disk. |
| Was further host control prevented? | No. The host used Colima SSH and Docker management during the running trial. |

The successful full run took 233.5 seconds. Its first attempt exposed copied-file
ownership and read-only fixture cleanup errors; these were corrected before the
passing repeat. See the report for versions and repeat steps.

Command elevation allowed approved processes outside the initiating harness's
command sandbox. It did not grant a temporary Colima capability that expired
after startup, and it was not an OS-root session. The same host account owned
the generated Lima management key, VM state and forwarded Docker socket.
Mode-0700 state directories protect those resources from other accounts; they
do not protect them from their owner.

A later live same-UID control explicitly obtained access through all ten
prepared probes: protected-file/key opens, Docker ping/worker inspection,
manager signal permission, Colima, Lima and direct SSH. The worker then completed
both simulated provider orders and owned VM/data cleanup in 240.2 seconds.
This confirms retained management by the owner; it does not test separate-account
denials. See the [guided test](host-control-test.md) for the repeat steps.

Ending an elevated command does not revoke those management resources. A later
harness command can encounter another sandbox/approval check, but that is a
harness restriction. Colima still supplies management routes while the VM runs.
Lima documents direct SSH access using its generated configuration/key; Docker
documents socket access as a management capability.
[Lima SSH](https://lima-vm.io/docs/usage/ssh/),
[Docker socket protection](https://docs.docker.com/engine/security/protect-access/).

Supplying worker credentials once is a separate matter. Removing runtime tmpfs
does not revoke a token or a key copied elsewhere. The full-access worker can
read credentials supplied to it. A secret store does not remove SSH, Docker,
hypervisor or T3 management authority.

## The protection to test

Use this concrete question:

> Can an unprivileged initiating host account issue further commands, replace
> admitted inputs, or change the running worker when a separate account owns
> the worker's VM and all its management authority?

Two meanings of "host" need different answers:

| Actor | Scope |
| --- | --- |
| Initiating harness under an account without administrator authority | Candidate for enforced protection using a separate management identity. Not yet tested. |
| Trusted human/manager account | Retains authority to start, inspect, stop and clean up the worker. |
| Host root, administrator authorisation or hypervisor administrator | Outside this proposed boundary. An ordinary local Colima VM cannot prevent its administrator controlling it. |

The proposal assumes the initiating harness cannot obtain administrator
authorisation, use cached sudo rights, impersonate the manager, or operate the
trusted human's terminal/UI. If those capabilities remain available, the account
separation does not address them. Record those exclusions rather than claiming
protection against every process on the host.

The initial admitted ticket deliberately influences the worker. This test targets
additional influence after admission. A ticket supplied by an untrusted party is
still instructions to a full-access worker; freezing it does not make its contents
safe or trustworthy.

## Next test: one job, two host accounts

Start with manual admission. No vault installation, submission API, signer
service, T3 connection or real model/GitHub credentials are needed for this test.

1. **Prepare separate identities.** A trusted human provisions two disposable
   non-administrator host accounts: `initiator` and `runner` are example names.
   The initiating probe runs as the former; Colima/Lima and the worker manager
   run as the latter. Neither probe execution nor later management attempts get
   elevation into the manager account. Leave the existing human account and its
   credentials untouched.
2. **Protect management state and executable code.** Give the manager private
   state, SSH keys, Docker configuration, sockets and VM disks. Their ancestors
   must not be writable by the initiator. Use a manager-owned copy of the launcher,
   runtime helpers, dependencies and configuration; invoking code directly from
   the initiator-writable checkout would reopen control. Use an explicit child
   environment and close inherited stdin, terminal/control sockets and unrelated
   file descriptors. Export only non-sensitive status and endpoint identifiers.
3. **Admit one generated job.** The human approves and copies a synthetic ticket,
   linked context and descriptor into manager-owned storage. Record their hashes,
   the runtime/image identity, provider settings and exact base commit. Generate
   disposable credentials under the manager identity and inject them after the
   image build. Reuse the passed no-Mac-share worker/DinD shape and its real Git,
   signature and nested-Docker probes, with model/PR responses simulated.
4. **Keep the VM alive for the access test.** Add a manager-controlled readiness
   gate before starting ticket execution. From the manager account, reach the
   actual guest and outer Docker daemon and create/remove a disposable control
   marker. Publish only the test endpoint paths/ports and owned process IDs so
   the initiator probes the same live resources. This positive control prevents
   a stopped VM or wrong endpoint producing a false pass.
5. **Probe from an ordinary host shell.** Run the denial checks below under the
   initiator account, outside the coding tool's command sandbox. Record effective
   UIDs, target identifiers, return codes and OS errors, never key/token bytes.
   Missing CLI tools, prompt-based refusals and sandbox rejection are inconclusive.
6. **Release and repeat.** The manager releases the fixed job. Disconnect the
   submitting session, repeat relevant interference probes during execution, and
   require the worker to finish without further initiator input. Verify admission
   hashes/base, acceptance checks, reviewed SHA, signatures, local push and owned
   resource cleanup. Repeat for the reverse provider order if the boundary passes.

This is a candidate OS-account boundary, not a claim that the current desktop
harness already runs under it. A shell probe establishes permissions for that
identity. A subsequent real harness test must run under the same restricted
identity and verify that its tools, UI integrations and approval routes do not
restore management access.

## Required denial checks

Target only generated resources owned by this trial. Do not inspect personal
SSH keys, agent inventories, provider authentication or vaults.

| Attempt from the initiator | Required result |
| --- | --- |
| Read/copy the trial's management key or fake runtime credentials | OS access denied; no bytes disclosed. |
| Reach the actual forwarded Docker socket, inspect the worker or `docker exec` a canary command | Permission/authentication denied; worker canary absent. |
| Use Colima/Lima shell, direct SSH or an existing control socket | Permission/authentication denied against the live guest. |
| Replace manager-owned launcher, helpers, dependencies or configuration | Denied; recorded runtime identity unchanged. |
| Rewrite admitted ticket/context/descriptor or select another ticket | Protected-copy writes denied; original admission hashes retained. |
| Change the initiator-owned original ticket/context/source after admission | Allowed on the original; no change to worker-admitted inputs/base. |
| Rewrite the manager-owned synthetic remote/ref or worker workspace/disk | Denied; recorded base and resulting work unchanged. |
| Signal the owned manager/VM processes | A non-destructive `kill(pid, 0)` permission probe returns `EPERM`; the manager confirms those processes remain alive. |
| Use a manager terminal, broad privileged helper, inherited descriptor or alternative management API | No such route available to the initiator. Any discovered route fails the test. |

Debugging/attach probes can be added for the platform when they can be tested
against an owned disposable helper safely. Unsupported attach tooling is not
evidence of process isolation. The signal-permission probe alone does not prove
debugger or process-memory protection. Do not use destructive probes against
unrelated host processes, files, VMs or Docker resources.

## Base commit and admission gap

The current [job runner](../runtime/bin/sdlc-job) accepts a `base_branch` and
resolves it during clone/fetch in `fresh_checkout`. It does not bind admission to
an explicit base SHA. Freezing the ticket alone leaves an independently mutable
repository ref as another way to influence work after admission.

The prepared host-control fixture captures the manager VM's synthetic bare
remote ref before admission, checks it again at release, and verifies the worker
records that exact SHA. The remote remains inside the manager-owned VM.
Changing the initiator's original files/refs must have no effect on the admitted
copies. This bounds the generated test; it does not solve mutable real remotes.

A reusable submission path will need explicit base-SHA binding or an equivalent
protected repository snapshot, along with fixed ticket/context hashes and runtime
policy. Reject changes after admission instead of resolving new inputs later.

## Acceptance and stopping rules

Call this a pass only when the manager positive controls succeed, every required
management/interference attempt is denied by the OS/authentication boundary, and
permitted edits to initiator-owned originals have no effect on admitted values.
The job must also complete its real local signing/push/nested-Docker flow. Keep
private evidence outside tracked source. Include identity, ownership, endpoint
and cleanup results; redact secret values from logs.

If ordinary shell access under the initiator can enter the worker, stop and fix
that route before testing real models or publication credentials. Do not call a
tool approval prompt or an agreement to avoid further commands a security pass.

Two users inside a VM still managed by the initiating host account do not test
this boundary: the outer Colima/Docker management path bypasses them. Likewise,
root-owned launcher files running the manager under the initiating UID are
insufficient. A protected service would have to own execution and all management
state under a separate identity. Apple's launchd documentation describes service
lifecycle, but does not establish that this design has been deployed or tested.
[Apple launchd guidance](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html).

If local account separation proves impractical, repeat the same fixed-job test on
an independently managed remote Linux machine. Give the initiating harness no
remote administrator, SSH, Docker, T3 prompt or terminal capability. Remote
location alone does not remove those permissions. Tailscale can provide private
reachability; it does not define the application control grant.

Only after this first boundary test passes should a minimal programmatic
admission interface be considered: submit a bounded fixed job and read status,
with no general shell, exec, prompt-follow-up or mutable-input operation. The
older protected-service research is reference material, not a prerequisite for
the manual test.
