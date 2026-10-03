# Colima worker trial

The full worker runtime trial passed on 2 October 2026 on Apple silicon macOS.
It built the actual coding image inside a dedicated Linux VM and exercised the
ticket runner in both provider orders. It made real SSH-signed commits and pushed
them to disposable local Git remotes. Model responses and GitHub PR operations
were simulated, as selected by the operator.

The VM had no Mac filesystem shares or forwarded SSH agent. Docker-in-Docker
ran entirely inside it. OrbStack was not used for the trial's containers or made
a different default Docker context.

The trial used portable tools in a private temporary directory. No global
installation, real credentials, vault access, provider model calls or private
repository inputs were needed. The VM, its Docker data disk and its containers
were removed after completion. Private diagnostic logs remain outside the
tracked repository.

## Full worker results

| Check | Live result |
| --- | --- |
| Actual `runtime/Dockerfile` build | Passed; its public `runtime/` directory was the entire build context. |
| Real Codex and Claude CLIs | Installed and version/auth-status checks passed; both were unauthenticated. |
| Skills and agents for both providers | Installed and supporting paths available; real model discovery/use was not tested. |
| Offline runtime suite | All 132 tests passed inside the worker. |
| Worker and daemon mounts | No guest-directory bind mounts or guest outer Docker socket. Internal workspace/socket/data volumes and runtime tmpfs. |
| Worker privilege | Non-root agent process; only its separate DinD daemon was privileged. |
| Per-job nested Docker API | Unix socket only; no TCP listener on ports 2375/2376. |
| Nested build, Compose relative bind and worker localhost | Passed in both jobs. These inner binds refer to VM-owned workspace files. |
| Worker .NET SDK | SDK 10.0.401 built and ran a generated console app in both jobs. |
| Ticket intake | Copied generated `.sdlc/work/tickets/123/ticket-1.md` and linked context; snapshot hashes and saved inputs verified. |
| Credential injection | Generated SSH key and fake token streamed into tmpfs after image creation; mode-0400 worker-owned files. |
| Git HTTPS credential helper | Real helper returned the supplied fake token without a network call. |
| Codex implementation / Claude review | Actual runner stages passed with simulated provider responses. |
| Claude implementation / Codex review | Actual runner stages passed with simulated provider responses. |
| Clone, branch, SSH signing and push | Real Git operations passed against a disposable local bare repository. |
| Review/publication gate | Pushed SHA matched the reviewed SHA; commit signature verified; one simulated draft PR per job. |
| Cleanup | Owned containers, volumes, worker image and generated key removed; VM and Docker data disk deletion confirmed. |

The successful full run took 233.5 seconds, including the image build and its
dependency downloads, but excluding prior tool/VM-image downloads. Its VM used
four CPUs, 8 GiB of memory and a 40 GiB Docker data disk. It ran the jobs
sequentially, with fresh workspaces and daemons. It does not demonstrate
simultaneous model sessions or concurrent-job isolation.

The image contained Codex 0.159.3, Claude Code 2.1.287, GitHub CLI 2.102.0,
.NET SDK 10.0.401, Docker CLI/DinD 29.8.2 and Compose 5.5.1. The guest Docker
engine was 29.5.2. No authenticated model session, GitHub API request or GitHub
write occurred; image creation downloaded public GitHub release/catalogue assets.

The first full run exposed a fixture permissions error: copied ticket files
needed root ownership before removing their write bits. Generated read-only
snapshot directories also needed writable permissions for fixture cleanup.
Both were corrected without adding worker capabilities. That failed run's VM
and data disk were deleted before the passing repeat.

## Earlier DinD fixture

The smaller fixture also passed live on the same date. It tested the VM and
nested Docker shape before building the coding worker.

| Check | Live result |
| --- | --- |
| Apple Virtualization Linux VM boot | Passed. |
| Host sharing filesystems absent | Passed: no virtiofs, 9p or SSHFS mount. |
| Generated Mac canary inaccessible to guest | Passed. |
| SSH-agent forwarding absent | Passed. |
| Guest Docker daemon | Passed; Docker Engine 29.5.2 on Linux arm64. |
| Privileged Docker-in-Docker | Passed with `docker:29.4.0-dind`. |
| Nested Docker API | Unix socket only; ports 2375 and 2376 had no listener. |
| Nested Compose build and localhost HTTP | Passed with Compose 5.1.1. |
| Inner container service discovery | Passed. |
| Writable and read-only inner volumes | Passed. |
| Copied-source .NET 10 build and execution | Passed; SDK container networking disabled. |
| Fake-token runtime injection | Passed: generated after launch, piped into a mode-0600 tmpfs file. |
| Fake token excluded from fixture image configuration/history | Passed. |
| Cleanup after intentionally failing command | Passed. |
| Final VM and guest data removal | Passed; no owned VM directories, data disks or PID files remained. |

Versions: Colima 0.10.3, Lima 2.2.0, and the Colima core 0.10.4 arm64 Docker
image. Download SHA256 values were verified against GitHub release metadata;
Colima also validated its supported image digest. The successful run took about
105 seconds, excluding tool/image downloads.

An initial attempt within the command sandbox failed to bind Lima's local Unix
socket. The operator approved a scoped elevated trial, which then passed. This
was a host command permission issue, not evidence of Colima incompatibility.
The failed boot left a sparse data disk despite normal deletion; the launcher
now explicitly removes and checks that owned disk as well.

## Repeat the trial

The later [host control trial](host-control-test.md) adds a manager-owned source
bundle, positive controls and a readiness gate. Its same-UID control completed
in 240.2 seconds: all ten prepared access probes obtained access while the VM was
live, then the full worker and owned cleanup passed. Separate-account denials
remain untested. Use that runbook for the boundary experiment; the commands below
repeat the runtime compatibility trial.

The [host launcher](../spikes/colima/run_trial.py) targets Apple silicon macOS.
It needs Colima, Lima and the Docker client, plus a local Docker VM image
supported by the selected Colima version. Docker Desktop is not required.
Colima's configuration and Docker client state are created in separate private
directories. `HOME` is preserved, while ambient credentials, daemon selection
and agent sockets are omitted from the child environment.

For the tested image:

```sh
trial_root=$(mktemp -d /tmp/sdlc-colima.XXXXXX)
curl --fail --location \
  --output "$trial_root/docker-vm.raw.gz" \
  https://github.com/abiosoft/colima-core/releases/download/v0.10.4/ubuntu-24.04-minimal-cloudimg-arm64-docker.raw.gz

image_digest=32242674b046b5057e60c4aba334b51e3665f05412cda89ed081cc2de153ae5c41f6b105b5c442cbe48d78e2cc21e9ba1950e406b6fb4fc2fd1dd2259240abbd
(cd "$trial_root" && printf '%s  %s\n' "$image_digest" docker-vm.raw.gz | shasum -a 512 -c -)

python3 spikes/colima/run_trial.py --execute \
  --disk-image "$trial_root/docker-vm.raw.gz" \
  --state-dir "$trial_root/run"
```

Run from the repository root in a terminal allowed to start a VM and bind local
sockets. For portable binaries, add `--colima "$trial_root/colima"` and
`--lima-bin "$trial_root/tools/bin"` after downloading and verifying the
[Colima release](https://github.com/abiosoft/colima/releases/tag/v0.10.3) and
[Lima release](https://github.com/lima-vm/lima/releases/tag/v2.2.0) archives.
Keep the compressed supported VM image; do not bypass its validation with
`--force-disk-image`.

The launcher creates a fresh profile in isolated state directories, disables Mac
mounts, agent forwarding, personal SSH configuration changes and default Docker
context activation, then checks the guest. It copies the fixture using an SSH
stdin tar stream. The [guest fixture](../spikes/colima/README.md) uses `docker cp`
for the next transfer into DinD. No host directory or outer Docker socket is
bound into that container.

There are tmpfs and Docker data volumes inside the guest. They are not Mac
filesystem shares. If the final requirement prohibits these internal mounts as
well, this trial is not evidence that requirement has been met.

The launcher always attempts bounded deletion of its owned VM and data disk,
including interruption/failure paths. Inspect its external `result.json` and
logs if cleanup fails. A forced process kill or host shutdown can bypass normal
cleanup; do not prune unrelated Colima or OrbStack resources.

Fixture-only checks, without starting a VM or Docker containers:

```sh
python3 spikes/colima/run_trial.py
bash spikes/colima/trial.sh --check
bash spikes/colima/full_trial.sh --check
```

To build the actual worker and run its ticket lifecycle with generated
credentials and simulated provider/API responses, use a fresh state directory
and add `--full-worker`:

```sh
python3 spikes/colima/run_trial.py --execute --full-worker \
  --disk-image "$trial_root/docker-vm.raw.gz" \
  --state-dir "$trial_root/full-run"
```

This allocates four CPUs, 8 GiB of RAM and a 40 GiB guest Docker data disk. It
downloads public runtime dependencies into the guest. It transfers only tracked
`runtime`, `scripts` and `tests` sources, three required workflow/filter files,
and the two explicit trial helpers. It excludes the Mac checkout's `.git`,
untracked work, local profiles and credentials. Both trial modes remove their
VM and guest data; use a new state directory for each repeat.

## What this establishes

A separate Colima VM can run Docker-backed checks without Mac file sharing.
The privilege risk belongs to the disposable guest: the privileged DinD daemon
can compromise it. The VM supplies the additional boundary to macOS. These
checks demonstrate ordinary filesystem separation and workload compatibility,
not resistance to a kernel or hypervisor exploit. Host management access remains
available through Colima/Lima.

Elevation for the trial did not remove that authority after startup. The same
host account owned the manager state/key/socket; subsequent Colima SSH and Docker
commands remained possible. The [host control handoff](host-control-handoff.md)
records the next proposed test with a separate management identity. That test
has not run.

The full fixture validates the actual runner with generated work and simulated
provider/API responses. It checks that the real CLIs and catalogues are available;
it does not prove how authenticated models use them, or that a real GitHub account
accepts the signing key and publication permissions. The console build is SDK
evidence, not a complete application integration suite.

The trial adapts ticket/helper transfer to copied inputs. The normal Docker job
launcher still has its existing input/helper binds; this does not silently change
that production path. A real implementation run needs a new authorised ticket,
human-run provider authentication and dedicated publication credentials supplied
privately. Keep those inputs and resulting logs outside tracked source.

Sources: [Colima profiles](https://colima.run/docs/profiles/),
[Colima configuration](https://colima.run/docs/configuration/),
[pinned image manifest](https://github.com/abiosoft/colima/blob/v0.10.3/embedded/images/images.txt),
[Docker privilege model](https://docs.docker.com/reference/cli/docker/container/run/#escalate-container-privileges---privileged).

## Windows equivalents

Colima supports macOS and Linux. It does not advertise a native Windows backend.
The host launcher in this repository currently targets Apple silicon macOS;
Windows options below were researched, not executed.
[Colima README](https://github.com/abiosoft/colima#readme)

| Option | Fit for a disposable Docker worker |
| --- | --- |
| Multipass with Hyper-V | Closest convenient cross-platform CLI: create a named Ubuntu VM, install Docker inside it and run the same guest fixture. Released Windows support uses Hyper-V by default, with VirtualBox as an alternative. |
| Dedicated Hyper-V Linux VM | Independent Linux kernel and virtual disk, with explicit control over host integration. The full Hyper-V role requires Windows Pro or Enterprise. |
| VirtualBox base package | Open-source hypervisor alternative for an independent Linux VM. Windows Arm support is experimental. |
| QEMU | Open-source full-system VM; more image, networking and lifecycle setup. Windows WHPX acceleration uses Microsoft's hypervisor; TCG can emulate without it. |
| Lima 2.2 | Windows default changed to QEMU. Installation documentation still calls Windows untested, so this needs a separate compatibility trial. |
| WSL2 distribution | Convenient Docker environment, but unsuitable for enforcing no Windows filesystem access against a root worker. Disabling automatic drive mounts still allows manual mounts. |

[Multipass stable installation](https://canonical.com/multipass/docs/stable/how-to-guides/install-multipass/),
[Hyper-V requirements](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/get-started/install-hyper-v?pivots=windows),
[VirtualBox platforms](https://docs.oracle.com/en/virtualization/virtualbox/7.2/user/Introduction.html),
[QEMU accelerators](https://www.qemu.org/docs/master/system/introduction.html),
[Lima 2.2 release](https://github.com/lima-vm/lima/releases/tag/v2.2.0),
[WSL configuration](https://learn.microsoft.com/en-us/windows/wsl/wsl-config).

On 2 October 2026, Multipass's published latest release was 1.16.4. Its live
documentation already described 1.17's HCS backend and Windows Home support.
HCS uses native Windows Host Compute System APIs; it is not WSL2. Check the
installed release and `multipass get local.driver` before following the newer
instructions. The released Hyper-V/VirtualBox recipe is the current baseline.
[Published release](https://github.com/canonical/multipass/releases/tag/v1.16.4),
[1.17 release notes](https://canonical.com/multipass/docs/latest/reference/release-notes/1.17.0/).

For a Multipass trial, set `local.privileged-mounts=false` and create an explicitly
named VM that is not the configured primary instance. The primary instance
automatically shares the host home directory; host mounts default on for macOS
and Linux. Bootstrap Docker with cloud-init, transfer public fixture files
explicitly, and keep repository clones and credentials on the guest's disk or
runtime tmpfs. Avoid shared directories, redirected drives and clipboard
integration with a manually managed VM too.
[Primary instance](https://canonical.com/multipass/docs/latest/how-to-guides/manage-instances/use-the-primary-instance/),
[Mount control](https://canonical.com/multipass/docs/stable/reference/settings/local-privileged-mounts/),
[Cloud-init](https://canonical.com/multipass/docs/stable/how-to-guides/manage-instances/launch-customized-instances-with-multipass-and-cloud-init/).

Multipass and QEMU are open source. Hyper-V and Apple's virtualization
frameworks are proprietary operating-system components. VirtualBox's base
package is GPLv3; its optional Extension Pack has separate licensing.
[Multipass source](https://github.com/canonical/multipass),
[QEMU license](https://www.qemu.org/docs/master/about/license.html),
[VirtualBox licensing](https://www.oracle.com/virtualization/technologies/vm/downloads/virtualbox-downloads.html).

VM managers retain authority to run commands inside their guests. Multipass
daemon access also permits host-file mounts. Keep that management authority
outside the coding harness if preventing host-side command injection is required.
The guest boundary protects host files from ordinary worker operations; it does
not make the host controller unable to direct the worker.
[Multipass security](https://canonical.com/multipass/docs/latest/explanation/about-security/).
