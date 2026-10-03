# Execution options and a decision loop

Updated: 3 October 2026.

The proposed production `sdlc` command would admit a ticket, start an unattended
worker, show its progress and supply a human decision when it pauses. An AI harness is optional.
The execution machine and the interface used to control it are separate choices.

The smallest next implementation would extend the existing ticket launcher with
private repository registration and durable paused jobs. Use Colima for local
trials. A dedicated Ubuntu machine is a straightforward remote target if its
whole installation can be treated as expendable. Add a VM on that machine when
the worker must also be contained from the Ubuntu management host.

This document distinguishes existing features, completed trials and proposals.
The accompanying [decision-loop spike](../spikes/decision-loop/README.md) is an
offline simulation with a separately installable command. It does not run Docker,
call providers, execute checks, sign commits, push or create a PR.

## Execution placement

| Option | How work would run | Cost, control and evidence |
| --- | --- | --- |
| Existing Docker runner | Codex or Claude implements in a worker container; another session reviews. A privileged DinD sidecar runs integration tests. | Implemented. The normal launcher still binds captured inputs and helpers. It does not satisfy the stricter copied-input/no-host-share design. |
| Dedicated Colima VM | A separate Linux VM hosts the worker and its DinD sidecar. Source and ticket bytes are copied in; no Mac directory or agent socket is shared. | Full runtime trial passed with generated credentials and simulated provider/API responses. The Mac account owning Colima retains management. |
| Direct Lima VM | Provision Linux and Docker with a custom Lima configuration. | More provisioning control than the Docker-oriented Colima wrapper. Plain mode removes convenience integrations; Docker still needs installing. No runtime trial here. |
| UTM VM on macOS | Create a Linux VM through a GUI, disable sharing, then install Docker and the runner. | Useful for inspecting a persistent guest manually. More setup than the tested Colima profile. No trial here. |
| Multipass VM | Provision Ubuntu through a CLI, then install Docker and the runner. | An alternative on macOS or Windows. Check the released version and supported backend; host management remains available. No trial here. |
| T3 directly in Colima | The Mac T3 app connects to a guest T3 server; provider processes and Docker run in the guest. | Interactive remote environment. Providers would run alongside guest Docker rather than inside the existing worker container. Guest service support needs checking. No live connection tested. |
| T3 inside the worker container | Install a T3 server in the Docker worker and connect the Mac app to it; retain DinD. | Preserves the container shape but adds service, network, session-storage and lifecycle work. T3 is not installed in the current image. No trial here. |
| Herdr on a remote Linux worker | Use a terminal client over SSH/Tailscale to supervise persistent Codex/Claude panes and worker-local Docker. | Released multi-machine/CLI support assessed. Broad ongoing input remains available. No live Herdr trial here; see the [assessment](herdr-remote-assessment.md). |
| Ubuntu VM on Windows | Keep Windows and run the execution environment in a Linux guest on the other machine. | Hyper-V or VirtualBox can supply the VM. Disable host shares and unnecessary integration. Windows retains VM administration. No Windows trial here. |
| Dedicated Ubuntu installation | Run the CLI/T3/providers on a separate physical machine. Use native Docker, or retain the worker container and DinD. | Fewest runtime layers for remote work. With privileged DinD, treat the Ubuntu installation as part of the worker's trust domain. No machine installation or trial here. |
| KVM guest on Ubuntu | The dedicated Ubuntu host manages a disposable Linux guest containing Docker, worker and DinD. | Keeps Docker privilege inside a separate guest kernel. Adds provisioning and cleanup work; hardware virtualization is required. No KVM trial here. |
| Other remote Linux VM | Place the same worker/manager arrangement on a separately administered remote machine. | Apply the same grants, credential scope and retention rules. A remote location alone does not restrict the initiating client. No remote trial here. |
| Docker Sandboxes | Use its microVM and private Docker engine for the provider. | Assessed from documentation. The Sandbox CLI was unavailable, so no live trial ran. Workspace, skill and agent integrations need an explicit review. |
| Another OrbStack machine | Use an isolated machine within OrbStack's Linux environment. | Machines and Docker share a kernel. This does not supply the separate guest-kernel boundary of Colima or KVM. Isolated-machine options were researched, not tested. |

Primary references: [Colima profiles](https://colima.run/docs/profiles/),
[Lima plain mode](https://lima-vm.io/docs/config/plain/),
[UTM](https://mac.getutm.app/),
[Multipass stable installation](https://canonical.com/multipass/docs/stable/how-to-guides/install-multipass/),
[Hyper-V requirements](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/host-hardware-requirements),
[VirtualBox](https://docs.oracle.com/en/virtualization/virtualbox/7.2/user/Introduction.html),
[Ubuntu libvirt](https://ubuntu.com/server/docs/how-to/virtualisation/libvirt/),
[Docker Sandbox isolation](https://docs.docker.com/ai/sandboxes/security/isolation/),
[OrbStack architecture](https://docs.orbstack.dev/architecture).

Colima is not a Windows solution. The released Multipass 1.16.4 installation
guidance uses Hyper-V or VirtualBox on Windows; newer documentation describes
additional backends. Check the installed release before relying on those. Hyper-V
also depends on the Windows edition and hardware capabilities.
[Multipass release](https://github.com/canonical/multipass/releases/tag/v1.16.4)

The [earlier Windows comparison](colima-trial.md#windows-equivalents) also covers
standalone QEMU, Windows Lima and WSL2. QEMU needs more VM/network setup; Windows
Lima remains a separate compatibility test. WSL2 is convenient for Docker but is
unsuitable for enforcing no Windows filesystem access against a root worker:
turning off automatic drive mounts does not prevent manual mounts.
[WSL configuration](https://learn.microsoft.com/en-us/windows/wsl/wsl-config)

Colima also supports Linux. Its current trial launcher here targets Apple silicon
macOS. Multipass, QEMU, Lima and Colima are open source; the host virtualization
framework may be a proprietary OS component. VirtualBox's base package is GPLv3
and its optional Extension Pack has separate licensing. These are separate
considerations from whether a VM protects a host from its worker.
[Colima](https://github.com/abiosoft/colima#readme),
[VirtualBox licensing](https://www.oracle.com/virtualization/technologies/vm/downloads/virtualbox-downloads.html)

If the container requirement is relaxed on a dedicated Ubuntu machine, provider
processes can use that machine's native Docker daemon for tests. That removes
DinD but grants those processes substantial authority over the Ubuntu machine.
If the container requirement remains, the existing privileged DinD design still
applies. Docker documents the broad capabilities and device access granted by
privileged containers. A VM contains that authority in its guest; its trusted
administrator can still manage the guest.
[Docker privileges](https://docs.docker.com/reference/cli/docker/container/run/#escalate-container-privileges---privileged),
[Docker daemon access](https://docs.docker.com/engine/security/protect-access/)

No installation or disk wipe is part of these proposals. Replacing Windows with
Ubuntu would be a separate operator decision, with backup and hardware checks.

## What T3 adds

T3 is an optional interactive interface to a remote execution environment. Its
documented v0.0.45 Linux release provides standalone ARM64/x64 server archives.
Install the selected provider CLIs and authenticate them privately on the
execution machine; the T3 server does not supply that authentication.
[T3 installation](https://github.com/pingdotgg/t3code/blob/v0.0.45/docs/user/install.md)

The Mac app can add a remote environment through Settings → Connections → Add
environment → SSH. That route starts or reuses the remote server and tunnels the
connection. Private-network pairing is another route:
`t3 serve --tailscale-serve` starts an exposed server, and `t3 pair --tailscale`
generates a pairing link for an existing server. T3 Connect supplies a further
account-based connection option.
Turning off the Mac app's Local environment stops its local server, agents and
terminals. Linux background services use systemd user services and lingering;
check those facilities in the actual guest image before choosing this route.
[Remote access](https://github.com/pingdotgg/t3code/blob/v0.0.45/docs/user/remote-access.md),
[Background service](https://github.com/pingdotgg/t3code/blob/v0.0.45/docs/user/background-service.md)

An SSH connection grants shell authority. Pairing can avoid granting a separate
general SSH login, but an authorised T3 client can still influence provider
sessions and use the environment's terminal capabilities. Tailscale controls
private reachability and network policy; T3 still needs its own authorisation.
[T3 authentication](https://github.com/pingdotgg/t3code/blob/v0.0.45/docs/internals/environment-auth.md),
[Tailscale policy](https://tailscale.com/docs/features/access-control/acls)

T3 owns the sessions it launches. It does not automatically attach to a batch
Codex/Claude process launched independently by the SDLC runner. A saved SDLC log
does not become a T3 thread without integration. Server persistence also does
not prove that a particular provider job survives every client disconnect; test
that with the selected release.

Use T3 when ongoing interactive control is wanted. Use a fixed-ticket SDLC job
when admission, checks, review and publication should follow a bounded process.
Both can be offered on the same machine, with separate workspaces and grants.

[The Herdr assessment](herdr-remote-assessment.md) covers a terminal-based
alternative over SSH/Tailscale, including status/feedback commands, native Docker,
credential provisioning and a proposed remote trial.

## Credentials without a required vault product

A real publication trial needs dedicated credentials, not access to an existing
vault or personal authentication inventory. The human can keep them in private files on
the manager or retrieve individual values from an optional encrypted store.
Provider authentication is separate from the GitHub publication token and SSH
signing key. Real values never enter source, build contexts or examples.

| Storage option | What it changes | Remaining work |
| --- | --- | --- |
| Private files | Minimal setup; a human owns dedicated credential files and launch-time delivery. | File permissions, expiry, revocation and private backups remain operator responsibilities. |
| gopass | Open-source encrypted local storage, usually GPG with Git-backed storage. | Decryption authority and caching matter. Its threat model assumes no local attacker. |
| SOPS with age | Encrypted configuration without operating a server. | Protect the decryption identity and the plaintext supplied to the worker. |
| OpenBao | Service policies, scoped identities and expiring/use-limited access. | Adds TLS, persistence, unsealing, backup and service operation. Not required for this trial. |
| 1Password CLI | Optional commercial retrieval, including service accounts scoped to selected vaults. | Ordinary desktop integration has broader account scope; a service-account token is another secret. No access requested or granted here. |
| Infisical / AgentVault | Secret management or an experimental HTTP-brokering path. | Licensing, supported operations and compatibility need checking; this does not provide Git SSH signing. |

See the [credential research and source links](worker-execution-handoff.md#secret-store-research-and-outcome)
for the assessed limits. No store is installed or accessed by this spike.

Launch-time delivery keeps credentials out of the built image. The copied-input
Colima fixture uses generated values delivered after build into tmpfs. The
ordinary runner currently uses environment-backed Compose secrets, which are
copied into the container filesystem. These are different paths.

Neither injection route makes a key or token single-use. A Full access worker can
read and copy supplied credentials. Container deletion does not revoke them.
Dedicated scope, expiry and explicit revocation govern later use. Avoid sharing a
personal SSH agent or importing a host provider-auth directory. The
[credential lifetime findings](worker-execution-handoff.md#credentials-storage-injection-and-lifetime)
explain this distinction.

## Remote VM environment files

When T3 and the provider processes run directly in a Linux VM, their tests can
use the VM's own Docker Engine and Compose. DinD is unnecessary for that shape.
Install the repository's SDKs/tools, provider CLIs and required skills/agents in
the VM, authenticate privately, then clone there. The VM is the execution
boundary; access to its ordinary rootful Docker daemon grants substantial
authority over that VM. No Mac Docker socket or directory share is needed.
[Docker group privileges](https://docs.docker.com/engine/install/linux-postinstall/)

A clone supplies tracked files. A private, untracked local `.env` must be
provisioned separately. The checked T3 v0.0.45 remote-access documentation does
not describe automatic synchronization of local `.env` files. An SSH/Tailscale
connection makes remote access possible; it does not itself copy configuration.

T3 documents per-provider-instance environment variables and a **Sensitive**
option for redacted display. Its implementation stores marked values separately
as server secrets. This can suit a small set of values for the selected
remote provider instance. It is not a documented whole-`.env` importer, and it
does not make those values available automatically to every terminal or service.
Sensitive storage does not prevent the provider process from reading a value
supplied to it.
[T3 provider setup](https://github.com/pingdotgg/t3code/blob/v0.0.45/docs/user/install.md#providers),
[Claude provider settings](https://github.com/pingdotgg/t3code/blob/v0.0.45/docs/user/providers-claude.md),
[Server secret storage](https://github.com/pingdotgg/t3code/blob/v0.0.45/apps/server/src/serverSettings.ts#L802)

The simplest manual provisioning route is an explicit file transfer over SSH.
The following commands are instructions for the human operator's own terminal;
they were not executed against a worker or real secrets in this assessment.
`worker` is a placeholder for a configured SSH host. Verify its host identity
through your normal setup and choose a dedicated file containing only the values
needed by that repository's tests.

```sh
ssh -o ForwardAgent=no worker \
  'umask 077; mkdir -p "$HOME/.local/share/sdlc/env" && chmod 700 "$HOME/.local/share/sdlc/env"'
scp -o ForwardAgent=no /PRIVATE/PATH/test.env \
  worker:.local/share/sdlc/env/example.env
ssh -o ForwardAgent=no worker \
  'chmod 600 "$HOME/.local/share/sdlc/env/example.env"'
```

OpenSSH `scp` transfers the file over an authenticated SSH connection. Only
paths occur in these command arguments; values do not enter a prompt, command
argument or shell history. The remote directory restricts access while the
file's permissions are set. Keep this human provisioning connection separate
from any initiating AI harness if its continued control is meant to be limited.
[OpenSSH file transfer](https://man.openbsd.org/scp)

Run the repository's Compose command in the cloned VM checkout with an explicit
path, for example:

```sh
docker compose --env-file "$HOME/.local/share/sdlc/env/example.env" up -d
```

`--env-file` supplies values for Compose interpolation. A service receives
environment values through its `environment` or `env_file` configuration; the
file alone does not inject every value into every container. It also does not
export values into the parent shell or an independently launched host test
process. Use the application's supported configuration loader for those tests.
If tooling requires a literal repository-root `.env`, place a private copy there
only after verifying it is untracked and ignored, and exclude it from builds,
archives and publication. Avoid treating a dotenv file as executable shell code.
[Compose interpolation](https://docs.docker.com/compose/how-tos/environment-variables/variable-interpolation/),
[Container environment](https://docs.docker.com/compose/how-tos/environment-variables/envvars-precedence/)

This protects transport and limits ordinary filesystem access. The authorised
worker and VM administrator can still read supplied values, and a Full access
provider can copy them. The file remains on the guest disk until removed; this
is persistent provisioning rather than a one-use credential. Use dedicated test
values and choose retention/expiry deliberately. File deletion does not revoke
a copied credential.

For the existing Docker job runner, the
[private `test_env_file` option](docker-ticket-jobs.md#bootstrap) is already
implemented. It loads raw `NAME=value` entries into the worker through a Compose
overlay; that is separate from a direct T3 VM. The offline decision-loop CLI
neither transfers nor loads environment files. No live T3 secret-transfer test
has been performed.

## A command-line onboarding flow

The existing `scripts/sdlc.py` already launches work from a terminal. The
[container onboarding](container-onboarding.md) and
[ticket-job guide](docker-ticket-jobs.md) remain the working entry points.
Onboarding need not run a model.

A production bundled CLI could offer this sequence:

1. `doctor`: inspect the selected runtime, provider versions, skills/agents and
   storage permissions without running a ticket or reading credentials.
2. `repo onboard`: register a private repository ID, remote, exact-base policy,
   ticket convention, runtime and provider roles. Inspect proposed check and
   cleanup commands; do not execute repository scripts during discovery.
3. `repo inspect`: display the resulting policy and any proposed generic
   repository templates. Store real URLs, account settings and credential
   references in external private configuration. Review repository changes
   before writing them; ticket content stays outside tracked source.
4. `run`: admit one selected ticket and linked work context, resolve and record
   the exact base SHA, create one workspace, then clone and start a new branch.
5. `status`, `events`, `questions`: show private job state and structured requests.
   A future `logs --follow` can display provider diagnostics as data.
6. `answer`, then `continue`: admit one response for one checkpoint and resume
   through checks and independent review before publication.

Those production commands are a proposed interface. The offline spike below
implements only registration, simulated admission, questions, answers,
continuation, status and events. It registers descriptive check labels; it does
not discover or execute commands. It captures local Git HEAD, not a remote base
or implementation worktree. Installing it demonstrates packaging, not runtime
onboarding.

## When the worker needs a decision

The worker should finish its current provider turn with a structured
`needs_input` result. Persist the question and checkpoint, mark the job paused,
and exit that process cleanly. An ambiguous sentence in console output is not a
reliable trigger. A crash or timeout remains a failure unless an explicit,
validated decision request was recorded.

The production checkpoint needs these facts:

- Job, attempt and question IDs, bounded question text and listed choices.
- Admitted ticket/context hashes, policy, runtime identity and exact base SHA.
- Current implementation HEAD and a recoverable snapshot of tracked and
  untracked work. A Git commit alone may omit unfinished work.
- Provider session reference, completed stages and private diagnostic locations.
- Publication state, including whether a push or PR outcome needs reconciliation.

Keep the workspace, checkpoint and provider session data outside tracked source.
Stop the test daemon and integration resources while waiting. If the worker is
destroyed, its tmpfs credentials disappear from that mount; an authorised manager
must reinject credentials when continuing. Copied credentials are still subject
to expiry/revocation. The current Colima fixture destroys its whole guest on exit,
so it needs a retention policy before it can host a genuinely paused job.

An answer is a new admitted input. Bind it to the job, question and checkpoint
generation. Start with selection from a fixed choice list. Bounded free text can
follow when real questions require it, but it is still an instruction to a Full
access worker. Do not expose an `exec` or shell-command field in the answer API.

| Situation | Required runner behaviour |
| --- | --- |
| Question recorded | Save recoverable work, mark `needs_input`, stop publication and release the active provider process. |
| Valid answer recorded | Save it once; continuation is a separate explicit action. |
| Stale, duplicate or concurrent answer | Reject it or return the already recorded outcome; never launch a second continuation. |
| Admitted snapshot or paused work changed | Reject continuation and require a new checkpoint/admission. Editing the original ticket after capture does not alter admitted work. |
| Answer changes implementation | Run checks and independent review again against the resulting exact HEAD. Prior review does not authorise the new change. |
| Push/PR outcome uncertain | Stop and reconcile the remote state before retrying. |
| Manager restarts | Recover a durable transition or report an explicit recoverable failure. A JSONL log alone is not a transaction system. |

The current implementation schema has `complete` and `blocked`; the job runner
turns a blocked implementation into a failed job. Failed jobs retain workspace
and diagnostics, but there is no supported answer/resume path. The new lifecycle
therefore needs runner changes, not just a console wrapper.

## Feedback routes to spike

| Route | Useful property | Limit and next proof |
| --- | --- | --- |
| Structured result, stop, restart with admitted answer | Portable across providers; manager owns checkpoint and continuation. | Recommended first integration. Requires a recoverable workspace and a new attempt prompt. The offline CLI tests this state sequence only. |
| Native provider session resume | Preserves more conversation context. Codex documents exact-session resume; Claude documents resume and deferred tool calls. | Add versioned adapters and retain private provider state. Session restoration does not restore a deleted workspace. Not integrated or tested here. |
| T3 question/permission UI | Human can guide an interactive provider session remotely. | Suitable for supervised work. It retains broader ongoing prompt/terminal control and does not automatically control independent batch jobs. |
| Decision file or narrow manager API | Human can answer remotely without general worker shell access. | Admit a bounded response carrying job/question/checkpoint IDs. Transport and manager authentication need implementation. Never source/evaluate a response file. |
| Leave an interactive terminal open | Lowest setup cost for a manually supervised session. | Requires a durable connection/process and keeps credentials and general command access available. It does not supply the intended fixed-ticket lifecycle. |

Codex can emit JSONL events and a schema-constrained final result; its
noninteractive CLI also documents `exec resume`. Claude headless mode supports
structured output and session resume. Its documented deferred-tool mechanism
requires a permission host; the Agent SDK offers a user-input callback. Those
mechanisms need explicit adapters and tests for the runner's pinned versions.
[Codex noninteractive mode](https://learn.chatgpt.com/docs/non-interactive-mode),
[Claude headless mode](https://code.claude.com/docs/en/headless),
[Claude deferred tools](https://code.claude.com/docs/en/hooks#defer-a-tool-call-for-later),
[Claude user input](https://code.claude.com/docs/en/agent-sdk/user-input)

Use a private JSONL event journal for stage transitions and keep provider stdout,
stderr and summaries as separate private diagnostics. The existing Codex adapter
already records events; the Claude adapter currently captures a final JSON
envelope. A progress view can tail these files, but the manager must never execute
their contents or automatically interpret log text as a human response.

## Try the installed offline command

Use Python 3 on Linux, macOS or WSL. This spike uses POSIX locks. Select a new
external installation prefix and private external state directory. The installer
copies only the CLI and its ticket-capture helper; it does not copy this checkout,
credentials or configuration. Existing prefixes are refused. Nothing is installed
globally.

Both parent directories must already exist. Use canonical absolute paths without
symlink components; resolve aliases such as `/tmp` on macOS before supplying
them. The [generated demo](../spikes/decision-loop/README.md#generated-local-demo)
creates suitable paths and a synthetic repository/ticket for the commands below.

```sh
python3 -B spikes/decision-loop/install.py --prefix /PRIVATE/NEW/sdlc-spike
/PRIVATE/NEW/sdlc-spike/bin/sdlc --help
```

With a generated local Git repository containing one committed HEAD and an
untracked synthetic ticket, use the installed command:

```sh
sdlc=/PRIVATE/NEW/sdlc-spike/bin/sdlc
state=/PRIVATE/NEW/decision-state

"$sdlc" --state-dir "$state" repo add --id demo \
  --path /PRIVATE/GENERATED/REPOSITORY --remote example/demo \
  --runtime colima --implementer codex --reviewer claude --check unit
"$sdlc" --state-dir "$state" repo list
"$sdlc" --state-dir "$state" job start --repo demo \
  --ticket .sdlc/work/tickets/1/ticket-1.md
```

Copy the generated `job_id` into `JOB`. Inspect its status and question, then use
the returned `request_id` and `checkpoint` values in the answer:

```sh
"$sdlc" --state-dir "$state" job status JOB
"$sdlc" --state-dir "$state" job question JOB
"$sdlc" --state-dir "$state" job answer JOB \
  --request REQUEST --checkpoint CHECKPOINT --choice keep-api
"$sdlc" --state-dir "$state" job continue JOB
"$sdlc" --state-dir "$state" job events JOB
```

The deliberate synthetic question asks whether to keep or change a public API.
It is not generated by a model or inferred from ticket content. The terminal
result says `SIMULATED_COMPLETE`; no implementation or publication occurred.
Try continuation before answering, a stale checkpoint and a repeated answer:
each must fail. The spike is a functional prototype, not a manager service or a
crash-recovery system. See its README for the exact tested scope and limitations.

Validation passed: 27 lifecycle and installer tests, the generated local demo,
and an independent lifecycle using the externally installed package with the
provider roles reversed. Both reached `SIMULATED_COMPLETE` with publication
disabled. Run the offline suite with:

```sh
python3 -B -m unittest discover -s spikes/decision-loop -p 'test_*.py' -v
```

## Authority and the next integration test

A CLI is an interface, not evidence of human authority. Another unrestricted
process under the same OS account can invoke it, alter its private state or use
the account's Colima/Docker/SSH management grants. Mode `0700`, question IDs and
hashes catch ordinary mistakes and stale inputs; they do not separate that
process from the human.

The earlier same-account control trial exercised ten management/file probes and
obtained authority through all ten. The Colima compatibility trial remains useful
for worker-to-Mac containment, but it does not protect the worker from its owner.
See [the host-control evidence](host-control-handoff.md) and
[the separate-account test steps](host-control-test.md).

The next integrated test should be manually admitted on a separately owned
manager or dedicated remote machine. Give an initiating client status/submission
access only if needed; keep answer, continuation, management and publication
grants with the human-controlled manager. Do not place those credentials or an
administrative SSH key in the initiating AI harness's environment. An authorised
T3 client is a separate, broader supervised mode.

Use generated credentials and simulated provider output first. Pause after a
synthetic decision, stop integration resources, restart the manager, submit one
bound answer, restore the exact workspace and reinject runtime credentials. Then
run checks, independent review and a local signed publication fixture. Test denied
initiator operations, stale/duplicate responses, concurrent continuations,
changed work and uncertain publication. Only a successful separate-identity trial
can support that access claim. An administrator of the execution host remains
trusted.

Completed evidence is limited to the Colima runtime, signed local Git pushes,
nested Docker, 132 offline runtime tests, simulated provider/PR responses in both
provider orders, the same-account control trial and offline decision-loop tests.
No live T3 connection, separate-account denial, Windows/Ubuntu/KVM runtime,
authenticated model call or real GitHub PR has been tested in these trials.
