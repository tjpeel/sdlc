#!/usr/bin/env python3
"""Run a disposable Colima trial without sharing Mac files or host credentials."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import socket
import stat
import subprocess
import sys
import tarfile
import tempfile
import time

FIXTURE = Path(__file__).resolve().parent
PROFILE = "trial"
GUEST = "/tmp/sdlc-colima-fixture"
FILES = ("trial.sh", "inner-trial.sh", "compose.yaml", "http/Dockerfile", "http/index.html",
         "app/Trial.csproj", "app/Program.cs", "app/NuGet.Config")


def parse_args():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--execute", action="store_true", help="Create and delete a disposable VM.")
    parser.add_argument("--full-worker", action="store_true", help="Build the actual worker and run the simulated-provider ticket trial.")
    parser.add_argument("--host-control", action="store_true", help="Pause a generated worker for separate-account probes; requires a prepared source bundle.")
    parser.add_argument("--public-manifest", type=Path, help="New external readable endpoint manifest for the host-control probe.")
    parser.add_argument("--colima", default="colima", help="Installed or portable Colima executable.")
    parser.add_argument("--lima-bin", type=Path, help="Directory containing portable limactl.")
    parser.add_argument("--disk-image", type=Path, help="Local Colima-supported Docker image; avoids the host download cache.")
    parser.add_argument("--state-dir", type=Path, help="Fresh private directory outside this repository.")
    return parser.parse_args()


def find_binary(value):
    candidate = shutil.which(value)
    if not candidate:
        raise RuntimeError(f"Required executable is unavailable: {value}")
    return str(Path(candidate).resolve())


def environment(root, colima, lima_bin):
    # HOME is preserved. Credentials and ambient daemon selection are omitted.
    env = {key: os.environ[key] for key in ("HOME", "USER", "LOGNAME") if key in os.environ}
    paths = [str(Path(colima).parent)]
    if lima_bin:
        paths.append(str(lima_bin.resolve()))
    docker = shutil.which("docker")
    if not docker:
        raise RuntimeError("The Docker client is required; Docker Desktop is not.")
    paths.extend([str(Path(docker).resolve().parent), "/usr/bin", "/bin", "/usr/sbin", "/sbin"])
    env.update(PATH=":".join(dict.fromkeys(paths)), TERM="dumb", LANG="en_US.UTF-8", SSH="/usr/bin/ssh")
    for variable, name in (("COLIMA_HOME", "colima"), ("LIMA_HOME", "lima"),
                           ("COLIMA_CACHE_HOME", "cache"), ("DOCKER_CONFIG", "docker"),
                           ("TMPDIR", "tmp")):
        folder = root / name
        folder.mkdir(mode=0o700)
        env[variable] = str(folder)
    return env


def run(command, env, root, label, *, timeout=300, payload=None, monitor=None):
    with (root / f"{label}.log").open("wb") as log:
        process = subprocess.Popen(command, env=env, stdin=subprocess.PIPE if payload is not None else subprocess.DEVNULL,
                                   stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        try:
            if monitor:
                monitor(process)
            process.communicate(input=payload, timeout=timeout)
        except BaseException:
            # Colima invokes Lima children. Stop the owned command group before
            # running VM deletion; a timeout must not leave a boot command alive.
            try:
                os.killpg(process.pid, signal.SIGTERM)
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                pass
            except ProcessLookupError:
                pass
            finally:
                # A direct child can exit while a descendant ignores TERM.
                # Kill the owned group even when waiting on its leader succeeds.
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait(timeout=10)
            raise
    if process.returncode:
        raise RuntimeError(f"{label} failed (exit {process.returncode}); see its private log.")


def bundle_sources(repo):
    manifest = repo / '.sdlc-trial-sources.json'
    if not manifest.exists():
        return None, None
    if manifest.is_symlink() or manifest.stat().st_size > 1024 * 1024:
        raise RuntimeError('Prepared source manifest must be a bounded regular file.')
    value = json.loads(manifest.read_text())
    if value.get('version') != 1 or not re.fullmatch(r'host-control-[0-9a-f]{12}', value.get('trial_id', '')):
        raise RuntimeError('Invalid prepared source manifest.')
    sources = []
    seen = set()
    for item in value['files']:
        relative = Path(item['path'])
        if relative.is_absolute() or '..' in relative.parts or relative.as_posix() in seen:
            raise RuntimeError('Unsafe or duplicate prepared source path.')
        seen.add(relative.as_posix())
        allowed = relative.parts[0] in ('runtime', 'scripts', 'tests') or relative.as_posix() in (
            'research/container-spike/sdlc.py',
            '.github/dependency-files-filter.jq', '.github/dependency-pr-filter.jq',
            '.github/workflows/update-runtime-pins.yml') or relative.parts[:3] in (
            ('research', 'spikes', 'colima'), ('research', 'spikes', 'host-control'))
        if not allowed or any(part in ('.git', '.secrets', '__pycache__') or part == 'profiles.local.json' for part in relative.parts):
            raise RuntimeError('Prepared source is outside the public allowlist.')
        path = repo / relative
        if path.is_symlink() or any(parent.is_symlink() for parent in path.parents if parent != repo) or not path.is_file():
            raise RuntimeError('Prepared sources must be regular files without symlink ancestors.')
        data = path.read_bytes()
        if len(data) != item['size'] or hashlib.sha256(data).hexdigest() != item['sha256']:
            raise RuntimeError('Prepared source hash changed: ' + str(relative))
        sources.append(relative)
    return sources, value


def fixture_archive(full_worker=False, host_control=False):
    repo = FIXTURE.parents[2]
    prepared, metadata = bundle_sources(repo)
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w") as archive:
        for name in FILES:
            if prepared is not None and Path('research/spikes/colima') / name not in prepared:
                raise RuntimeError('Prepared bundle is missing a required fixture file.')
            path = FIXTURE / name
            if path.is_symlink() or not path.is_file():
                raise RuntimeError('Fixture transfer requires an explicit regular file.')
            archive.add(path, arcname=name, recursive=False)
        if full_worker:
            repo = FIXTURE.parents[2]
            allowlist = ["runtime", "scripts", "tests", "research/container-spike/sdlc.py", ".github/dependency-files-filter.jq",
                         ".github/dependency-pr-filter.jq", ".github/workflows/update-runtime-pins.yml"]
            if prepared is None:
                if host_control:
                    raise RuntimeError('Host-control mode requires a prepared manager-owned source bundle.')
                tracked = subprocess.check_output(["git", "ls-files", "-z", "--", *allowlist], cwd=repo)
                sources = [Path(os.fsdecode(name)) for name in tracked.split(b"\0") if name]
                sources += [Path("research/spikes/colima/full_trial.sh"), Path("research/spikes/colima/full_fixture.py")]
            else:
                sources = [path for path in prepared if path.parts[0] in ('runtime', 'scripts', 'tests', '.github')
                           or path.as_posix() in ('research/container-spike/sdlc.py', 'research/spikes/colima/full_trial.sh', 'research/spikes/colima/full_fixture.py')]
                if host_control:
                    ticket = Path('research/spikes/host-control/protected-input.txt')
                    if ticket not in prepared:
                        raise RuntimeError('Prepared bundle is missing the protected ticket.')
                    sources.append(ticket)
            for relative in sources:
                path = repo / relative
                if path.is_symlink() or not path.is_file():
                    raise RuntimeError("Transfer requires a regular public source file: " + str(relative))
                archive.add(path, arcname="full-source/" + relative.as_posix(), recursive=False)
    return buffer.getvalue()


def host_control_monitor(process, ssh, env, root, repo, colima, trial_id, public_path):
    def guest(*arguments, timeout=15):
        return subprocess.run(ssh + list(arguments), env=env, stdin=subprocess.DEVNULL,
                              capture_output=True, text=True, timeout=timeout)

    deadline = time.monotonic() + 900
    while True:
        if process.poll() is not None:
            raise RuntimeError('Worker fixture exited before the host-control readiness gate.')
        result = guest('cat', '/tmp/' + trial_id + '/ready.json')
        if result.returncode == 0:
            admission = json.loads(result.stdout)
            break
        if time.monotonic() > deadline:
            raise RuntimeError('Worker did not reach host-control readiness in fifteen minutes.')
        time.sleep(1)
    if admission['trial_id'] != trial_id or not re.fullmatch('[0-9a-f]{64}', admission['worker_id']):
        raise RuntimeError('Invalid generated worker admission.')
    expected_ticket = hashlib.sha256((repo / 'research/spikes/host-control/protected-input.txt').read_bytes()).hexdigest()
    if admission['ticket_sha256'] != expected_ticket:
        raise RuntimeError('The worker received a different admitted ticket.')
    worker = admission['worker_id']
    docker = ['docker', '--host', 'unix:///var/run/docker.sock']
    inspected = guest(*docker, 'inspect', worker)
    if inspected.returncode:
        raise RuntimeError('Manager positive control could not inspect the owned worker.')
    detail = json.loads(inspected.stdout)[0]
    if detail['Config']['Labels'].get('sdlc.host-control.trial') != trial_id:
        raise RuntimeError('Worker does not belong to this control trial.')
    marker = '/run/sdlc/manager-positive-control'
    positive = guest(*docker, 'exec', worker, 'sh', '-ec', 'touch "$1"; test -f "$1"; rm "$1"', 'sh', marker)
    if positive.returncode:
        raise RuntimeError('Manager positive control could not enter the owned worker.')
    docker_socket = root / 'colima/trial/docker.sock'
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
        client.settimeout(5)
        client.connect(str(docker_socket))
        client.sendall(b'GET /_ping HTTP/1.0\r\nHost: localhost\r\n\r\n')
        reply = client.recv(4096)
        if b'200 OK' not in reply:
            raise RuntimeError('Manager Docker-socket positive control failed.')
    value = {'schema_version': 1, 'trial_id': trial_id, 'manager_uid': os.geteuid(),
             'manager_pid': os.getpid(), 'manager_state_root': str(root), 'manager_copy_root': str(repo),
             'targets': {'private_canary': str(root / 'private-canary.txt'),
                         'protected_input': str(repo / 'research/spikes/host-control/protected-input.txt'),
                         'docker_socket': str(docker_socket)},
             'worker': {'id': worker, 'label_key': 'sdlc.host-control.trial', 'label_value': trial_id},
             'cli': {'colima_binary': colima,
                     'lima_binary': str(Path(shutil.which('limactl', path=env['PATH'])).resolve()),
                     'profile': PROFILE, 'instance': 'colima-trial'},
             'admission': admission,
             'positive_controls': {'guest_ssh': True, 'docker_ping': True, 'worker_inspect': True, 'worker_exec': True}}
    config = root / 'lima/colima-trial/ssh.config'
    if config.exists():
        settings = {}
        for line in config.read_text().splitlines():
            pair = line.strip().split(None, 1)
            if len(pair) == 2 and pair[0].lower() in ('hostname', 'port', 'user'):
                settings[pair[0].lower()] = pair[1]
        if settings.get('hostname') in ('127.0.0.1', 'localhost') and settings.get('port', '').isdigit():
            value['ssh'] = {'host': '127.0.0.1', 'port': int(settings['port']), 'username': settings['user'],
                            'identity_file': str(root / 'lima/_config/user')}
    public_path = public_path.resolve()
    if public_path.parent != root.parent:
        raise RuntimeError('Publish endpoint metadata beside the short external state directory.')
    descriptor = os.open(public_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o644)
    with os.fdopen(descriptor, 'w') as target:
        os.fchmod(target.fileno(), 0o644)
        json.dump(value, target, indent=2)
        target.write('\n')
    (root / 'host-control-admission.json').write_text(json.dumps(value, indent=2) + '\n')
    print('Manager positive controls passed. Endpoint manifest: ' + str(public_path), flush=True)
    print('Worker paused for ten minutes. From the manager account, create ' + str(root / 'release') + ' to continue.', flush=True)
    deadline = time.monotonic() + 590
    while not (root / 'release').exists():
        if process.poll() is not None or time.monotonic() > deadline:
            raise RuntimeError('Host-control worker stopped or the release deadline expired.')
        time.sleep(0.5)
    release = root / 'release'
    if release.is_symlink() or not release.is_file() or release.stat().st_uid != os.geteuid():
        raise RuntimeError('Release must be a manager-owned regular file.')
    if bundle_sources(repo)[1] is None:
        raise RuntimeError('Prepared manager sources are no longer valid.')
    result = guest('touch', '/tmp/' + trial_id + '/release')
    if result.returncode:
        raise RuntimeError('Manager could not release its owned worker.')


def write_summary(root, summary):
    (root / "result.json").write_text(json.dumps(summary, indent=2) + "\n")


def main():
    args = parse_args()
    if not args.execute:
        print("Prepared trial only. Add --execute and a local --disk-image to create a disposable VM.")
        return 0
    os.umask(0o077)
    if args.host_control and (not args.full_worker or not args.public_manifest or not args.state_dir):
        raise RuntimeError('Host-control mode needs --full-worker, --state-dir and --public-manifest.')
    if platform.system() != "Darwin" or platform.machine() != "arm64":
        raise RuntimeError("This initial trial targets Apple silicon macOS.")
    if not args.disk_image or not args.disk_image.is_file():
        raise RuntimeError("Supply a local supported Colima Docker disk image with --disk-image.")
    colima = find_binary(args.colima)
    if args.lima_bin and not (args.lima_bin / "limactl").is_file():
        raise RuntimeError("--lima-bin must contain limactl.")
    root = args.state_dir.resolve() if args.state_dir else Path(tempfile.mkdtemp(prefix="sdlc-colima-", dir="/tmp")).resolve()
    repo = FIXTURE.parents[2]
    trial_id = None
    if args.host_control:
        _, prepared = bundle_sources(repo)
        if not prepared:
            raise RuntimeError('Prepare a source bundle before running the host-control test.')
        trial_id = prepared['trial_id']
        if repo.name != trial_id + '-copy' or root.name != trial_id:
            raise RuntimeError('Use the prepared copy and matching short trial state directory.')
        if root.parent != Path('/tmp').resolve():
            raise RuntimeError('Host-control state must use the short external /tmp trial directory.')
        permissions = repo.stat()
        if permissions.st_uid != os.geteuid() or stat.S_IMODE(permissions.st_mode) != 0o700:
            raise RuntimeError('The prepared copy must be manager-owned and mode 0700.')
    if root == repo or repo in root.parents:
        raise RuntimeError("Keep VM state and logs outside the tracked repository.")
    if root.exists() and any(root.iterdir()):
        raise RuntimeError("Use a new or empty private state directory.")
    root.mkdir(mode=0o700, exist_ok=True)
    permissions = root.stat()
    if permissions.st_uid != os.getuid() or stat.S_IMODE(permissions.st_mode) != 0o700:
        raise RuntimeError("The state directory must be owned by this user and have mode 0700.")
    # Do not reuse an existing profile, Docker configuration or authentication.
    if any((root / name).exists() for name in ("colima", "lima", "docker")):
        raise RuntimeError("Use a fresh state directory, not existing Colima or Docker settings.")
    env = environment(root, colima, args.lima_bin)
    if args.host_control:
        (root / 'private-canary.txt').write_text('generated-host-control-canary\n')
    if len(str(root / "lima" / "colima-trial" / "ssh.sock")) > 90:
        raise RuntimeError("Use a shorter state path for macOS Unix sockets.")
    summary = {"platform": "macOS arm64", "mode": "full-worker" if args.full_worker else "fixture",
               "vm_started": False, "guest_checks_passed": False,
               "fixture_passed": False, "cleanup_passed": False}
    start = time.monotonic()
    command = [colima, "start", PROFILE, "--vm-type", "vz", "--arch", "aarch64",
               "--runtime", "docker", "--cpus", "4" if args.full_worker else "2",
               "--memory", "8" if args.full_worker else "4", "--disk", "40" if args.full_worker else "20",
               "--root-disk", "10", "--mount", "none", "--ssh-agent=false",
               "--ssh-config=false", "--activate=false", "--port-forwarder", "none",
               "--binfmt=false", "--vz-rosetta=false", "--network-address=false",
               "--network-host-addresses=false", "--mount-inotify=false", "--template=false",
               "--disk-image", str(args.disk_image.resolve())]
    failure = None
    attempted = False
    handled_signals = (signal.SIGINT, signal.SIGTERM, signal.SIGHUP)
    previous_handlers = {item: signal.getsignal(item) for item in handled_signals}

    def interrupted(signum, frame):
        for item in handled_signals:
            signal.signal(item, signal.SIG_IGN)
        raise KeyboardInterrupt(f"Interrupted by signal {signum}.")

    for item in handled_signals:
        signal.signal(item, interrupted)
    try:
        payload = fixture_archive(args.full_worker, args.host_control)
        run([colima, "version"], env, root, "colima-version", timeout=30)
        run(["limactl", "--version"], env, root, "lima-version", timeout=30)
        print("Starting the dedicated Colima VM with no Mac mounts or agent forwarding.", flush=True)
        attempted = True
        run(command, env, root, "startup", timeout=600)
        summary["vm_started"] = True
        # Probe only a generated canary, never real host files or credentials.
        canary = root / "mac-only-canary"
        canary.write_text("disposable-host-canary\n")
        checks = """set -eu
test \"$(uname -s)\" = Linux
test -z \"${SSH_AUTH_SOCK:-}\"
test ! -e \"$1\"
if findmnt -rn -t virtiofs,9p,fuse.sshfs | grep -q .; then
  echo 'Unexpected host-sharing filesystem' >&2; exit 1
fi
docker --host unix:///var/run/docker.sock version
test -S /var/run/docker.sock
printf 'Guest mount, canary, agent and Docker checks passed.\\n'
"""
        ssh = [colima, "ssh", "--profile", PROFILE, "--"]
        run(ssh + ["sh", "-c", checks, "sh", str(canary)], env, root, "guest-checks")
        summary["guest_checks_passed"] = True
        run(ssh + ["mkdir", "-p", GUEST], env, root, "guest-directory")
        run(ssh + ["tar", "-xf", "-", "-C", GUEST], env, root, "fixture-transfer",
            payload=payload)
        if args.full_worker:
            print("Building the actual coding worker and running the simulated-provider ticket trial.", flush=True)
            script = GUEST + "/full-source/research/spikes/colima/full_trial.sh"
            invocation = ssh + ["bash", script, "--execute"]
            monitor = None
            if args.host_control:
                invocation += ['--host-control', trial_id]
                monitor = lambda process: host_control_monitor(process, ssh, env, root, repo, colima, trial_id, args.public_manifest)
            run(invocation, env, root, "full-worker", timeout=6000, monitor=monitor)
        else:
            print("Running the guest DinD, Compose, .NET and fake-secret fixture.", flush=True)
            run(ssh + ["bash", GUEST + "/trial.sh", "--execute"], env, root, "fixture", timeout=1800)
        summary["fixture_passed"] = True
    except (OSError, RuntimeError, subprocess.SubprocessError, KeyboardInterrupt) as error:
        failure = str(error) or type(error).__name__
        summary["error"] = failure
    finally:
        # Keep repeated interrupts from skipping bounded resource deletion.
        for item in handled_signals:
            signal.signal(item, signal.SIG_IGN)
        if attempted:
            print("Deleting only the owned trial VM and its container data.", flush=True)
            try:
                run([colima, "delete", PROFILE, "--data", "--force"], env, root, "cleanup", timeout=120)
                data_disk = root / "lima" / "_disks" / "colima-trial"
                if data_disk.exists():
                    # A boot that never reached runtime provisioning can leave
                    # its Lima data disk despite colima delete --data.
                    run(["limactl", "disk", "delete", "--force", "colima-trial"],
                        env, root, "cleanup-disk", timeout=120)
                # A failed boot may not create an instance at all. Any surviving
                # instance disk or PID file keeps cleanup marked incomplete.
                instance = root / "lima" / "colima-trial"
                if instance.exists():
                    raise RuntimeError("The owned Lima instance directory remains after deletion.")
                if data_disk.exists():
                    raise RuntimeError("The owned Lima data disk remains after deletion.")
                summary["cleanup_passed"] = True
            except (OSError, RuntimeError, subprocess.SubprocessError) as error:
                summary["cleanup_error"] = str(error)
        summary["elapsed_seconds"] = round(time.monotonic() - start, 1)
        write_summary(root, summary)
        for item, handler in previous_handlers.items():
            signal.signal(item, handler)
    print("Trial evidence directory: " + str(root))
    if failure or not summary["cleanup_passed"]:
        print(failure or "Cleanup was not confirmed.", file=sys.stderr)
        return 1
    print("Colima trial passed; VM and guest data removed.")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, RuntimeError) as error:
        print(f"Colima trial: {error}", file=sys.stderr)
        sys.exit(1)
