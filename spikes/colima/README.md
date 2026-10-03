# Colima worker trials

The [host launcher](run_trial.py) creates a disposable Apple silicon Linux VM
with Mac sharing disabled. See the [trial report](../../docs/colima-trial.md)
for results, pinned tools, repeat steps and researched Windows alternatives.

The [guided host control test](../../docs/host-control-test.md) adds a reviewed
public-source bundle, a pause before ticket execution, manager positive controls
and probes from a separate host account. Use its `--host-control` instructions;
the ordinary full-worker command below does not establish that boundary.

## Full worker

Add `--full-worker` to the host launch command to build the repository's actual
worker image and run the ticket lifecycle with generated credentials and
simulated provider/GitHub responses. The VM uses four CPUs, 8 GiB of RAM and a
40 GiB Docker data disk. Its build needs public package and image downloads.

```sh
python3 spikes/colima/run_trial.py --execute --full-worker \
  --disk-image "$trial_root/docker-vm.raw.gz" \
  --state-dir "$trial_root/full-run"
```

The guest [full fixture](full_trial.sh) installs the real provider CLIs and
catalogues through `runtime/Dockerfile`, checks that both CLIs are unauthenticated,
runs the offline test suite, and exercises actual nested Docker and the worker's
.NET SDK. It tests Codex implementation/Claude review, then the reverse order.
The jobs run sequentially, each with a fresh workspace and DinD daemon.

Generated ticket inputs and test helpers are copied into containers. The signing
key and fake token are generated after image creation and streamed into runtime
tmpfs. No guest directory or guest Docker socket is bound into the worker or its
DinD daemon; internal named workspace/socket/data volumes connect them. Only the
DinD daemon is privileged. Ticket inputs are root-owned and non-writable by mode;
they are not a read-only filesystem mount.

The actual runner clones a disposable local Git repository, creates a branch,
signs a commit, verifies the reviewed SHA, and pushes to its local bare remote.
The provider responses and draft-PR API are stubs. This does not authenticate or
call real models, publish a GitHub PR, or prove simultaneous model sessions.
The image build downloads public GitHub assets without account credentials.

Without starting a VM or containers:

```sh
bash spikes/colima/full_trial.sh --check
```

## Smaller DinD fixture

Run this fixture inside a dedicated disposable Colima Linux VM with Mac file
sharing and SSH-agent forwarding disabled. The VM is the containment boundary:
the privileged DinD container can compromise its guest. This fixture does not
prove resistance to a VM escape.

Copy this directory into the VM using an explicit transfer rather than a shared
Mac directory. The guest needs Bash, tar, coreutils `timeout`, `od`, and a running
Docker engine at `/var/run/docker.sock`. It also needs network access to pull the
public Docker, BusyBox and .NET SDK images. The Docker-in-Docker image includes
the inner Compose plugin; this smaller fixture uses no provider CLI or real credentials.

```sh
bash /path/in/guest/colima/trial.sh --execute
```

For a fixture-only check that starts no containers:

```sh
bash spikes/colima/trial.sh --check
```

The trial starts an owned privileged `docker:29.4.0-dind` container. Its daemon
uses an inner Unix socket; neither the outer Docker socket nor a guest path is
mounted into it. The launcher bypasses the image's stock entrypoint and checks
that Docker API ports 2375 and 2376 have no listener. Source is transferred with
`docker cp`. A generated fake token
is piped into a tmpfs file after the image is pulled. It never enters a build
context, image environment or command argument.

Inner Compose builds an HTTP fixture, checks its published port from the DinD
container's localhost, checks service discovery from another inner container,
and proves a named volume is writable or read-only according to its mount. A
separate SDK container builds and runs copied .NET 10 source with its network
disabled. A deliberately failing inner command also checks cleanup.

There are Docker volumes entirely inside the VM: DinD's anonymous data volume
and the nested Compose test volume. These are not Mac filesystem shares. The
script removes its own containers and volumes on completion or failure; it does
not prune unrelated Docker resources. Destroy the dedicated VM after the trial
to remove its image cache and guest state. Credentials injected into any live
worker must separately be revoked when the run ends.

Passing the fixture proves basic nested Docker compatibility. It does not prove
the full ticket workflow, CLI authentication, signing, pushing or PR creation.
