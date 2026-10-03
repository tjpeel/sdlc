# Guided host control test

Prepared: 2 October 2026.

This test asks whether a separate, unprivileged host account can enter or change
a Colima worker after a trusted manager has admitted its ticket. It uses a
synthetic repository, generated signing keys and fake tokens. It needs no vault,
provider login, GitHub account or real work ticket.

The [handoff](host-control-handoff.md) defines the boundary and the full acceptance
criteria. The steps here exercise the prepared subset. A denied shell probe does
not establish protection from an administrator or from every desktop integration.

## What can be done before the live test

The public-source bundler, bounded access probe and manager readiness gate are
implemented. Generated-fixture tests cover source admission, changed hashes,
symlinks, exact transfer membership, endpoint validation and denial classification.
The existing full worker trial already passed both provider orders with simulated
responses, nested Docker, SSH signatures and local Git pushes.

The separate-account trial requires a trusted human to provision and use the
accounts. No account changes, personal authentication or secret-store access are
part of preparation.

A live same-UID control completed in 240.2 seconds on 2 October. All ten access
probes obtained access, including Colima, Lima, direct SSH and the Docker API.
Manager positive controls passed; the generated worker then completed 132 offline
tests and both simulated provider orders, and its VM/data cleanup passed.
The report correctly kept `protection_established` false. The first control
attempt put the probe in the manager copy and was refused before reporting;
cleanup passed before the corrected repeat. The separate-account test has not run.

## 1. Prepare two standard accounts

Use two disposable **Standard** macOS accounts: one for the manager and one for
the initiating shell. A trusted human creates them in System Settings → Users &
Groups → Add User. Keep the passwords private.
[Apple's account setup instructions](https://support.apple.com/en-gb/guide/mac-help/mchl3e281fc9/mac).

The manager account needs Python 3, a Docker **client**, and permission to run
Apple's virtualization framework. Its VM supplies the Docker engine. The
initiator needs Python 3 and its own accessible copies of the Colima/Lima clients.
Docker Desktop is unnecessary; the Docker client supplied by OrbStack can be
used. The launcher explicitly targets the trial daemon.

Run each side in that account's ordinary Terminal session. Keep the manager
session logged in while switching to the initiator. Do not run the negative
probe from the coding tool's command sandbox, as root, or with permission to
impersonate the manager. The two accounts must have different UIDs and neither
should belong to the macOS `admin` group:

```sh
id -u
id -Gn
```

Provision these accounts once. Account creation is not a per-ticket step. If a
separate standard account cannot start the VM, record that limitation before
adding a privileged helper; the helper would introduce another management route.

## 2. Prepare and review the public bundle

From the source checkout, before switching accounts:

```sh
bundle_parent=$(mktemp -d /tmp/sdlc-boundary.XXXXXX)
bundle="$bundle_parent/bundle"
python3 -B spikes/host-control/prepare.py --output-dir "$bundle"
shasum -a 256 "$bundle/source.tar"
tar -tf "$bundle/source.tar"
```

Record the displayed SHA-256 independently of the transfer. The archive contains
only the public runtime/test allowlist and explicit trial helpers. It excludes
Git metadata, local profiles, authentication, logs and caches. Its ticket is a
generated text-repair example; `original-ticket.md` is an external original that
can be changed later.

A trusted human gives each account the same reviewed `source.tar` and the
recorded hash. Transfer these public files only. Do not transfer existing Colima
state, SSH keys, authentication directories or old evidence logs. The manager
does not run code directly from the initiator's writable checkout.

In each account, set `bundle` to its own external copy of the bundle and check it
against the independently recorded value:

```sh
bundle=/ABSOLUTE/PATH/TO/THIS_ACCOUNTS/BUNDLE
expected_bundle_sha=PASTE_REVIEWED_SHA256
printf '%s  %s\n' "$expected_bundle_sha" "$bundle/source.tar" | shasum -a 256 -c -

trial_id=$(python3 - "$bundle/source.tar" <<'PY'
import re, sys, tarfile
with tarfile.open(sys.argv[1]) as archive:
    name = archive.getmembers()[0].name.rstrip('/')
assert re.fullmatch(r'host-control-[0-9a-f]{12}-copy', name)
print(name.removesuffix('-copy'))
PY
)
```

Stop if the checksum fails. Do not substitute a checksum copied from an
unreviewed replacement archive.

## 3. Install public tools into each account's private copy

The **manager** extracts the bundle directly beneath `/private/tmp`, which keeps
Lima's Unix socket paths short:

```sh
umask 077
copy="/private/tmp/$trial_id-copy"
if mkdir -m 700 "$copy"; then
  tar -xf "$bundle/source.tar" -C /private/tmp && chmod 700 "$copy"
else
  printf '%s\n' 'Stop: the manager copy already exists. Prepare a fresh trial.' >&2
  unset copy
fi
```

Stop if extraction fails or reports an existing copy. Do not reuse an old copy
or change its permissions to continue this trial. Atomic creation and macOS's
sticky `/private/tmp` directory prevent the other account replacing this root;
its mode-0700 contents remain manager-owned.

The **initiator** extracts its own copy beneath a separate private directory:

```sh
umask 077
client_parent=$(mktemp -d /tmp/sdlc-probe-client.XXXXXX)
tar -xf "$bundle/source.tar" -C "$client_parent"
copy="$client_parent/$trial_id-copy"
chmod 700 "$copy"
```

In **each account**, download the pinned public clients below. Previously
downloaded assets can be reused after checking these hashes. Extract the whole
Lima archive: `limactl` alone is insufficient.

```sh
tools=$(mktemp -d /tmp/sdlc-public-tools.XXXXXX)
curl --fail --location --output "$tools/colima" \
  https://github.com/abiosoft/colima/releases/download/v0.10.3/colima-Darwin-arm64
curl --fail --location --output "$tools/lima.tar.gz" \
  https://github.com/lima-vm/lima/releases/download/v2.2.0/lima-2.2.0-Darwin-arm64.tar.gz

printf '%s  %s\n' \
  980ad8bf61a4ca370243f4cb41401a61276dcd2c2502bee7b9b86f9250169f34 "$tools/colima" \
  bbdef91774885a0d05f7b048c4eb89ae2bcf3a0c252ae7ca7934e63df76d93c3 "$tools/lima.tar.gz" \
  | shasum -a 256 -c -
```

Continue only after both hashes pass:

```sh
tar -xzf "$tools/lima.tar.gz" -C "$copy"
install -m 755 "$tools/colima" "$copy/bin/colima"
```

These are the tested Apple silicon assets from the
[Colima release](https://github.com/abiosoft/colima/releases/tag/v0.10.3) and
[Lima release](https://github.com/lima-vm/lima/releases/tag/v2.2.0).

The **manager only** also needs the supported compressed VM image:

```sh
curl --fail --location --output "$tools/docker-vm.raw.gz" \
  https://github.com/abiosoft/colima-core/releases/download/v0.10.4/ubuntu-24.04-minimal-cloudimg-arm64-docker.raw.gz
printf '%s  %s\n' \
  32242674b046b5057e60c4aba334b51e3665f05412cda89ed081cc2de153ae5c41f6b105b5c442cbe48d78e2cc21e9ba1950e406b6fb4fc2fd1dd2259240abbd \
  "$tools/docker-vm.raw.gz" | shasum -a 512 -c -
```

This hash is from Colima's
[supported image manifest](https://github.com/abiosoft/colima/blob/v0.10.3/embedded/images/images.txt).
Stop on any mismatch. No global install or personal configuration import is needed.
The manager's Python, Docker client, supporting libraries and tool directories
must also be owned by the manager or a trusted administrator and protected from
initiator writes. A private launcher invoking an initiator-writable dependency
would reopen influence before the worker starts.

## 4. Manager: start one admitted worker

In the manager's ordinary terminal, after the checks above:

```sh
state="/private/tmp/$trial_id"
endpoints="/private/tmp/$trial_id-endpoints.json"
python3 -B "$copy/spikes/colima/run_trial.py" \
  --execute --full-worker --host-control \
  --colima "$copy/bin/colima" --lima-bin "$copy/bin" \
  --disk-image "$tools/docker-vm.raw.gz" \
  --state-dir "$state" --public-manifest "$endpoints"
```

Leave this terminal running. The fresh VM uses four CPUs, 8 GiB of RAM and a
40 GiB Docker data disk. A cold build downloads public dependencies and images.
There are no Mac shares or forwarded host agent. Docker-in-Docker runs inside
the VM; only its daemon is privileged. Generated credentials enter runtime tmpfs
after the worker image is built.

Before readiness, the manager enters the live guest, pings its Docker socket,
inspects the exact labelled worker, and creates/removes a canary through
`docker exec`. The worker then pauses before processing the ticket. The public
manifest gives the initiator generated endpoint identifiers, not credentials.
It also records the ticket hash and the synthetic remote's exact base commit.

Start the next step when the manager prints **Manager positive controls passed**.
There are about ten minutes to probe and release; a timeout fails the trial and
starts owned-resource cleanup. A missing readiness manifest is not a denial pass.

## 5. Initiator: probe the live manager resources

Switch to the separate initiator account. Use that account's `copy` and the same
`trial_id`; the clients must be accessible to the initiator:

```sh
endpoints="/private/tmp/$trial_id-endpoints.json"
probe_result="$client_parent/probe-result.json"
python3 -B "$copy/spikes/host-control/probe.py" \
  --manifest "$endpoints" --result "$probe_result" --ordinary-shell \
  --colima "$copy/bin/colima" --lima "$copy/bin/limactl" --raw-ssh
python3 -m json.tool "$probe_result"
```

The `--raw-ssh` option requires the manifest's generated SSH endpoint. If the
manager did not publish it, record the omission and run without that option;
raw SSH coverage remains incomplete.

The probe reads and discards at most one byte from the generated canary/key,
tries write-open without truncating the protected ticket, pings and inspects the
exact owned Docker worker, tests manager signal permission with `kill(pid, 0)`,
and runs fixed `true` commands through Colima, Lima and direct SSH. It never
prints credential bytes or client diagnostics. It uses explicit trial state and
a fresh Colima client configuration to avoid falling back to personal settings.

| Result | Meaning |
| --- | --- |
| `DENIED` | All implemented probes were denied and manager positive controls were present. The complete boundary test remains pending. |
| `FAILURE` | At least one management or protected-file operation obtained access. |
| `INCONCLUSIVE` | A target/tool was missing, failed ambiguously, timed out, or lacked positive controls. |
| `CONTROL` | Explicit same-UID baseline; cannot establish protection. |
| Probe refused | Identity, sandbox, manifest or output preconditions failed. |

For the two-account test, do not add `--same-user-control` to get around a matching
UID. A CLI reporting “not running” is inconclusive even when another probe sees
permission denial. Stop on obtained access; fix that route before supplying real
credentials. Resolve inconclusive checks before claiming the implemented probes
passed. The report deliberately keeps `protection_established` false.

Also change the **initiator's own original**, after admission:

```sh
printf '%s\n' 'Changed after admission; must not become the worker ticket.' \
  > "$bundle/original-ticket.md"
```

This edit should succeed. It must not change the manager's copied ticket hash or
the worker's recorded base commit. The current fixture generates linked context
and its descriptor privately; editing an independently supplied original context
or descriptor is a later admission test.

## 6. Manager: release and check completion

The trusted human reviews the initiator's bounded JSON report. Keep it outside
tracked source. For a deliberate same-UID control, obtained access is expected;
for the two-account test it fails the boundary.

In a second manager terminal, set `trial_id` to the admitted identifier and release
the paused worker only after recording the probe outcome:

```sh
state="/private/tmp/$trial_id"
touch "$state/release"
```

Disconnect the initiator's shell. Keep the manager terminal running; the worker
requires no more input from the initiator. The first job runs Codex implementation
and Claude review using simulated responses. A second fresh job runs the reverse
order. The probe gate currently covers the first job only.

At completion, from the manager account:

```sh
python3 -m json.tool "$state/result.json"
```

Require `vm_started`, `guest_checks_passed`, `fixture_passed` and `cleanup_passed`
to be true, with no error. The fixture checks the admitted ticket/base, real SSH
signatures, the reviewed commit, local Git push, nested Docker and both provider
orders. The launcher deletes only its trial VM and Docker data. No authenticated
model call or real GitHub PR is made.

If the launcher fails, inspect its private evidence as the manager. Interruption
normally triggers bounded cleanup; forced termination or host shutdown can bypass
it. Do not use broad Docker pruning or delete unrelated OrbStack/Colima resources.
Retain evidence privately until the result is reviewed, then remove only the
generated paths belonging to this trial.

## 7. Decide what the result establishes

A denied run establishes the implemented shell checks for the initiating UID.
The full [denial matrix](host-control-handoff.md#required-denial-checks) still
requires launcher/helper/configuration and directory replacement checks, VM
process/disk checks, existing control sockets, inherited descriptors and alternate
privileged or UI routes. It also requires interference checks during execution
and a gated repeat with the reverse provider order. These are not implied by a
`DENIED` report or by `kill(pid, 0)` alone.

The next application test must run the actual initiating harness under the same
restricted identity and check its integrations and approval routes. Running the
desktop harness under the original administrator account would not reproduce
this boundary. A trusted manager, host administrator or hypervisor administrator
retains control of a local VM.

Real model authentication and a repository-scoped publication trial follow only
after the intended boundary has been demonstrated. They are unnecessary for this
first access test.
