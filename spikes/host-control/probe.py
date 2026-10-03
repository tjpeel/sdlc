#!/usr/bin/env python3
"""Probe only the disposable targets in a manager's public trial manifest.

Run this from the ordinary initiator shell after the manager publishes a live
manifest. No probe writes a target file, sends a nonzero manager signal, executes
a Docker command, or uses the initiator's authentication or client configuration.
"""
import argparse
import errno
import http.client
import json
import os
from pathlib import Path
import re
import signal
import socket
import stat
import subprocess
import sys
import tempfile
import time


REPOSITORY = Path(__file__).resolve().parents[2]
MAX_MANIFEST_BYTES = 16384
MAX_RESPONSE_BYTES = 65536
TIMEOUT_SECONDS = 5
DENIED = "ACCESS_DENIED"
OBTAINED = "ACCESS_OBTAINED"
INCONCLUSIVE = "INCONCLUSIVE"
TRIAL_PATTERN = re.compile(r"host-control-[0-9a-f]{12}\Z")
PATH_PATTERN = re.compile(r"/[A-Za-z0-9_. /+-]+\Z")
DENIED_ERRNOS = {errno.EACCES, errno.EPERM}


class ManifestError(ValueError):
    pass


def require(value, message):
    if not value:
        raise ManifestError(message)


def exact_keys(value, required, optional=()):
    require(isinstance(value, dict), "Manifest sections must be objects.")
    require(set(required) <= value.keys() <= set(required) | set(optional),
            "Manifest contains missing or unknown fields.")


def integer(value, minimum, maximum):
    require(type(value) is int and minimum <= value <= maximum,
            "Manifest contains an invalid integer.")
    return value


def absolute_path(value):
    require(isinstance(value, str) and len(value) <= 1024
            and PATH_PATTERN.fullmatch(value), "Manifest contains an unsafe path.")
    path = Path(value)
    require(path.is_absolute() and str(path) == value
            and ".." not in path.parts and "." not in path.parts,
            "Manifest paths must be absolute and canonical.")
    return path


def under(path, root):
    return path != root and root in path.parents


def root_path(value, basename, *, allow_own_client_copy=False):
    path = absolute_path(value)
    require(path.name == basename, "Trial root does not match the trial identifier.")
    require((path != REPOSITORY or allow_own_client_copy) and REPOSITORY not in path.parents,
            "Trial roots must be outside the repository.")
    # Resolving existing ancestors also allows macOS's /tmp -> /private/tmp.
    try:
        resolved = path.resolve()
    except OSError as error:
        if error.errno not in DENIED_ERRNOS:
            raise ManifestError("Cannot resolve the trial root.") from None
        resolved = path
    require(resolved.name == basename and not under(resolved, REPOSITORY)
            and (resolved != REPOSITORY or allow_own_client_copy),
            "Trial root resolves outside its permitted namespace.")
    return path


def target_path(value, root):
    path = absolute_path(value)
    require(under(path, root), "Target is outside its disposable trial root.")
    # Inaccessible manager directories are expected. A visible symlink escape
    # is rejected before any open or connection is attempted.
    try:
        resolved = path.resolve()
        resolved_root = root.resolve()
    except OSError as error:
        if error.errno not in DENIED_ERRNOS:
            raise ManifestError("Cannot resolve a target path.") from None
    else:
        require(under(resolved, resolved_root), "Target follows a symlink outside its trial root.")
    return path


def executable_path(value, root, name):
    path = absolute_path(value)
    copied = path == root / "bin" / name
    installed = str(path) in {
        f"/opt/homebrew/bin/{name}", f"/usr/local/bin/{name}", f"/usr/bin/{name}"
    }
    formula = "lima" if name == "limactl" else "colima"
    cellar = re.fullmatch(
        rf"/(?:opt/homebrew|usr/local)/Cellar/{formula}/[A-Za-z0-9_.+-]+/bin/{name}",
        str(path))
    require(copied or installed or cellar, "CLI executable is outside known trial or install paths.")
    if copied:
        target_path(str(path), root)
    try:
        info = path.stat()
    except OSError as error:
        # A denied path or missing executable remains a probe observation.
        require(error.errno in DENIED_ERRNOS | {errno.ENOENT, errno.ENOTDIR},
                "Cannot inspect the CLI executable.")
    else:
        require(stat.S_ISREG(info.st_mode), "CLI executable must be a regular file.")
        if copied:
            require(not path.is_symlink(), "Copied CLI executable must not be a symlink.")
    return path


def unique_object(pairs):
    value = {}
    for key, item in pairs:
        require(key not in value, "Manifest contains duplicate fields.")
        value[key] = item
    return value


def client_override(value, trial_id, name):
    """Accept an installed CLI or the initiator's own generated public copy."""
    path = absolute_path(str(value))
    copied = path.parent.parent
    if path == copied / "bin" / name and copied.name == trial_id + "-copy":
        root_path(str(copied), trial_id + "-copy", allow_own_client_copy=True)
        executable_path(str(path), copied, name)
        try:
            info = path.stat()
        except OSError:
            raise ManifestError("The initiator's copied CLI must be accessible.") from None
        require(info.st_uid == os.getuid() and info.st_mode & 0o022 == 0,
                "The copied CLI must be initiator-owned and not writable by other accounts.")
    else:
        executable_path(str(path), Path("/__no_trial_copy__"), name)
        try:
            info = path.stat()
        except OSError:
            raise ManifestError("The installed CLI override must be accessible.") from None
    require(stat.S_ISREG(info.st_mode) and info.st_mode & 0o111,
            "The CLI override must be a regular executable file.")
    return str(path)


def load_manifest(filename):
    try:
        fd = os.open(filename, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(fd, "rb") as stream:
            require(stat.S_ISREG(os.fstat(stream.fileno()).st_mode),
                    "Manifest must be a regular file.")
            content = stream.read(MAX_MANIFEST_BYTES + 1)
        require(len(content) <= MAX_MANIFEST_BYTES, "Manifest exceeds the size limit.")
        value = json.loads(content, object_pairs_hook=unique_object)
    except (OSError, UnicodeError, json.JSONDecodeError, RecursionError):
        raise ManifestError("Cannot read a valid public trial manifest.") from None
    exact_keys(value, ("schema_version", "trial_id", "manager_uid", "manager_pid",
                       "manager_state_root", "manager_copy_root", "targets", "worker", "cli"),
               ("ssh", "admission", "positive_controls"))
    require(type(value["schema_version"]) is int and value["schema_version"] == 1,
            "Unsupported manifest schema.")
    trial = value["trial_id"]
    require(isinstance(trial, str) and TRIAL_PATTERN.fullmatch(trial), "Invalid trial identifier.")
    integer(value["manager_uid"], 1, 2**31 - 1)
    integer(value["manager_pid"], 2, 2**31 - 1)
    state = root_path(value["manager_state_root"], trial)
    copied = root_path(value["manager_copy_root"], trial + "-copy")
    require(not under(state, copied) and not under(copied, state), "Trial roots must be separate.")
    exact_keys(value["targets"], ("private_canary", "protected_input", "docker_socket"))
    targets = value["targets"]
    target_path(targets["private_canary"], state)
    target_path(targets["protected_input"], copied)
    target_path(targets["docker_socket"], state)
    require(targets["private_canary"] != targets["docker_socket"], "Probe targets must be distinct.")
    exact_keys(value["worker"], ("id", "label_key", "label_value"))
    worker = value["worker"]
    require(isinstance(worker["id"], str) and re.fullmatch(r"[0-9a-f]{64}", worker["id"]),
            "Worker ID must be the full generated container ID.")
    require(worker["label_key"] == "sdlc.host-control.trial" and worker["label_value"] == trial,
            "Worker label must identify this trial.")
    if "admission" in value:
        admission = value["admission"]
        exact_keys(admission, ("trial_id", "base_commit", "ticket_sha256", "job_id", "worker_id"))
        require(admission["trial_id"] == trial and admission["worker_id"] == worker["id"],
                "Admission must identify this trial's worker.")
        for field, width in (("base_commit", 40), ("ticket_sha256", 64), ("job_id", 32)):
            require(isinstance(admission[field], str)
                    and re.fullmatch(r"[0-9a-f]{" + str(width) + r"}", admission[field]),
                    "Admission contains an invalid identifier.")
    if "positive_controls" in value:
        exact_keys(value["positive_controls"], ("guest_ssh", "docker_ping", "worker_inspect", "worker_exec"))
        require(all(type(item) is bool for item in value["positive_controls"].values()),
                "Positive controls must contain boolean observations.")
    exact_keys(value["cli"], ("colima_binary", "profile"), ("lima_binary", "instance"))
    cli = value["cli"]
    require(cli["profile"] == "trial", "Colima profile must be the isolated trial profile.")
    executable_path(cli["colima_binary"], copied, "colima")
    if "lima_binary" in cli:
        require(cli.get("instance") == "colima-trial", "Lima instance must be colima-trial.")
        executable_path(cli["lima_binary"], copied, "limactl")
    else:
        require("instance" not in cli, "Lima instance requires its CLI executable.")
    if "ssh" in value:
        exact_keys(value["ssh"], ("host", "port", "username", "identity_file"))
        ssh = value["ssh"]
        require(ssh["host"] == "127.0.0.1", "Raw SSH must target the generated loopback endpoint.")
        integer(ssh["port"], 1024, 65535)
        require(isinstance(ssh["username"], str)
                and re.fullmatch(r"[a-z_][a-z0-9_-]{0,31}", ssh["username"]), "Invalid SSH guest user.")
        require(absolute_path(ssh["identity_file"]) == state / "lima" / "_config" / "user",
                "Raw SSH must use only Lima's generated trial identity.")
        target_path(ssh["identity_file"], state)
    return value


def observation(name, outcome, reason, **details):
    return {"probe": name, "outcome": outcome, "reason": reason, **details}


def os_error(name, error):
    if error.errno in DENIED_ERRNOS:
        return observation(name, DENIED, "operating_system_denied_access", errno=error.errno)
    reasons = {
        errno.ENOENT: "target_missing", errno.ENOTDIR: "target_parent_missing",
        errno.ECONNREFUSED: "endpoint_refused_connection", errno.ESRCH: "manager_process_missing",
        errno.ETIMEDOUT: "operation_timed_out", errno.ELOOP: "symlink_target_rejected",
    }
    return observation(name, INCONCLUSIVE, reasons.get(error.errno, "operating_system_error"),
                       errno=error.errno)


def reject_symlinks(path, root):
    # Inspect known path components only; never list a directory or read a key.
    relative = path.relative_to(root)
    current = root
    for part in (None, *relative.parts):
        if part is not None:
            current /= part
        if stat.S_ISLNK(current.lstat().st_mode):
            raise OSError(errno.ELOOP, "Symlink target rejected")


def file_probe(name, filename, root, write=False):
    path = Path(filename)
    try:
        reject_symlinks(path, Path(root))
        fd = os.open(path, (os.O_WRONLY if write else os.O_RDONLY) | os.O_NOFOLLOW | os.O_NONBLOCK)
        try:
            if not stat.S_ISREG(os.fstat(fd).st_mode):
                return observation(name, INCONCLUSIVE, "target_is_not_regular_file")
            if not write:
                os.read(fd, 1)  # Discard the byte; contents never enter the report.
        finally:
            os.close(fd)
    except OSError as error:
        return os_error(name, error)
    return observation(name, OBTAINED, "file_opened_for_write_without_writing" if write
                       else "file_read_without_disclosing_contents")


class DeadlineSocket(socket.socket):
    """Keep the whole HTTP exchange bounded, including a slowly sent body."""
    def __init__(self):
        super().__init__(socket.AF_UNIX, socket.SOCK_STREAM)
        self.deadline = time.monotonic() + TIMEOUT_SECONDS

    def update_timeout(self):
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError(errno.ETIMEDOUT, "Endpoint deadline elapsed")
        self.settimeout(remaining)

    def connect(self, address):
        self.update_timeout()
        return super().connect(address)

    def sendall(self, data, flags=0):
        self.update_timeout()
        return super().sendall(data, flags)

    def recv_into(self, buffer, nbytes=0, flags=0):
        self.update_timeout()
        return super().recv_into(buffer, nbytes, flags)


class UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, filename):
        super().__init__("localhost", timeout=TIMEOUT_SECONDS)
        self.filename = filename

    def connect(self):
        self.sock = DeadlineSocket()
        self.sock.connect(self.filename)


def docker_get(filename, route):
    connection = UnixHTTPConnection(filename)
    try:
        connection.request("GET", route, headers={"Connection": "close"})
        response = connection.getresponse()
        content = response.read(MAX_RESPONSE_BYTES + 1)
        if len(content) > MAX_RESPONSE_BYTES:
            raise ValueError("Oversized response")
        return response.status, content
    finally:
        connection.close()


def docker_probes(manifest):
    filename = manifest["targets"]["docker_socket"]
    name = "docker_ping"
    try:
        reject_symlinks(Path(filename), Path(manifest["manager_state_root"]))
        status, content = docker_get(filename, "/_ping")
    except OSError as error:
        return [os_error(name, error)]
    except (http.client.HTTPException, ValueError):
        return [observation(name, INCONCLUSIVE, "invalid_or_oversized_endpoint_response")]
    if status in (401, 403):
        return [observation(name, DENIED, "docker_api_denied_request", http_status=status)]
    if status != 200 or content.strip() != b"OK":
        return [observation(name, INCONCLUSIVE, "docker_ping_not_confirmed", http_status=status)]
    results = [observation(name, OBTAINED, "docker_api_accepted_read_only_ping")]
    # Once Docker access succeeds, inspect only the full worker ID and require
    # its trial label. No exec, stop, kill, delete or file-transfer API is used.
    name = "docker_owned_worker_inspect"
    worker = manifest["worker"]
    try:
        status, content = docker_get(filename, "/containers/" + worker["id"] + "/json")
        if status in (401, 403):
            results.append(observation(name, DENIED, "docker_api_denied_request", http_status=status))
        elif status != 200:
            results.append(observation(name, INCONCLUSIVE, "owned_worker_not_confirmed", http_status=status))
        else:
            value = json.loads(content)
            labels = value.get("Config", {}).get("Labels", {})
            owned = value.get("Id") == worker["id"] and isinstance(labels, dict) \
                and labels.get(worker["label_key"]) == worker["label_value"]
            results.append(observation(name, OBTAINED if owned else INCONCLUSIVE,
                                       "owned_worker_inspected" if owned else "owned_worker_label_not_confirmed"))
    except OSError as error:
        results.append(os_error(name, error))
    except (http.client.HTTPException, ValueError, AttributeError, UnicodeError):
        results.append(observation(name, INCONCLUSIVE, "invalid_or_oversized_endpoint_response"))
    return results


def signal_probe(pid):
    try:
        os.kill(pid, 0)
    except OSError as error:
        return os_error("manager_signal_zero", error)
    return observation("manager_signal_zero", OBTAINED, "signal_zero_accepted_no_signal_sent")


def sanitized_environment(manifest, scratch, binary):
    state = Path(manifest["manager_state_root"])
    client_directories = [str(Path(binary).parent)]
    client_directories.extend(str(Path(value).parent) for key, value in manifest["cli"].items()
                              if key in ("colima_binary", "lima_binary"))
    env = {"PATH": ":".join(dict.fromkeys(
        (*client_directories, "/usr/bin", "/bin", "/usr/sbin", "/sbin"))),
        "LANG": "C", "LC_ALL": "C", "TERM": "dumb", "SSH": "/usr/bin/ssh",
        "COLIMA_HOME": str(scratch / "colima-client"), "LIMA_HOME": str(state / "lima"),
        "COLIMA_CACHE_HOME": str(scratch / "cache"), "DOCKER_CONFIG": str(scratch / "docker"),
        "TMPDIR": str(scratch / "tmp")}
    if "HOME" in os.environ:
        env["HOME"] = os.environ["HOME"]
    for name in ("colima-client", "cache", "docker", "tmp"):
        (scratch / name).mkdir(mode=0o700, exist_ok=True)
    return env


def command_probe(name, command, env, scratch):
    try:
        # The anonymous file bounds retained diagnostic bytes and leaves no log.
        with tempfile.TemporaryFile(dir=scratch) as diagnostic:
            process = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                       stderr=diagnostic, env=env, cwd=scratch, start_new_session=True)
            try:
                process.wait(timeout=TIMEOUT_SECONDS)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=TIMEOUT_SECONDS)
                return observation(name, INCONCLUSIVE, "command_timed_out")
            diagnostic.seek(0)
            output = diagnostic.read(16384).lower()
            code = process.returncode
    except OSError as error:
        # Failure to launch a client establishes nothing about VM authority.
        return observation(name, INCONCLUSIVE, "client_could_not_start", errno=error.errno)
    if code == 0:
        return observation(name, OBTAINED, "fixed_guest_true_command_succeeded", exit_code=code)
    # A CLI may hide the syscall errno. Only an explicit access denial is evidence;
    # absent VM state, unavailable tools and other errors remain inconclusive.
    unavailable = (b"connection refused", b"no such file", b"not found", b"timed out",
                   b"no route", b"could not resolve", b"connection reset", b"broken pipe",
                   b"host key verification failed", b"does not exist", b"dyld",
                   b"error while loading shared libraries", b"cannot execute", b"can't exec", b"sandbox")
    if any(item in output for item in unavailable):
        return observation(name, INCONCLUSIVE, "cli_target_or_connection_unavailable", exit_code=code)
    if b"permission denied" in output or b"operation not permitted" in output:
        return observation(name, DENIED, "cli_reported_access_denial", exit_code=code)
    return observation(name, INCONCLUSIVE, "cli_failed_without_confirmed_access_denial", exit_code=code)


def cli_probes(manifest, scratch, raw_ssh=False):
    cli = manifest["cli"]
    binary = cli["colima_binary"]
    env = sanitized_environment(manifest, scratch, binary)
    # v0.10.3 falls back to HOME/.colima if stat(COLIMA_HOME) fails. Give it
    # accessible disposable metadata while keeping LIMA_HOME aimed at the real
    # protected manager instance. Profile trial still selects colima-trial.
    # https://github.com/abiosoft/colima/blob/v0.10.3/config/files.go
    # https://github.com/abiosoft/colima/blob/v0.10.3/config/profile.go
    colima_state = Path(env["COLIMA_HOME"])
    try:
        reject_symlinks(colima_state, scratch)
        usable = stat.S_ISDIR(colima_state.stat().st_mode)
    except OSError as error:
        results = [observation("colima_guest_true", INCONCLUSIVE,
                               "state_unavailable_client_fallback_risk", errno=error.errno)]
    else:
        if usable:
            results = [command_probe("colima_guest_true", [binary, "ssh", "--profile", "trial", "--", "true"], env, scratch)]
        else:
            results = [observation("colima_guest_true", INCONCLUSIVE, "state_unavailable_client_fallback_risk")]
    if "lima_binary" in cli:
        binary = cli["lima_binary"]
        env = sanitized_environment(manifest, scratch, binary)
        results.append(command_probe("lima_guest_true", [binary, "shell", "colima-trial", "true"], env, scratch))
    if raw_ssh:
        ssh = manifest["ssh"]
        command = ["/usr/bin/ssh", "-F", "/dev/null", "-n", "-T", "-o", "BatchMode=yes",
                   "-o", "ConnectTimeout=3", "-o", "ConnectionAttempts=1",
                   "-o", "IdentityAgent=none", "-o", "IdentitiesOnly=yes",
                   "-o", "UserKnownHostsFile=/dev/null", "-o", "GlobalKnownHostsFile=/dev/null",
                   "-o", "StrictHostKeyChecking=no", "-o", "PasswordAuthentication=no",
                   "-o", "KbdInteractiveAuthentication=no", "-o", "ForwardAgent=no",
                   "-o", "PreferredAuthentications=publickey",
                   "-o", "ClearAllForwardings=yes", "-o", "ControlMaster=no",
                   "-o", "ControlPath=none", "-i", ssh["identity_file"], "-p", str(ssh["port"]),
                   ssh["username"] + "@127.0.0.1", "true"]
        env = sanitized_environment(manifest, scratch, "/usr/bin/ssh")
        results.append(command_probe("raw_ssh_guest_true", command, env, scratch))
    return results


def probe(manifest, scratch, raw_ssh=False):
    targets = manifest["targets"]
    state = manifest["manager_state_root"]
    results = [file_probe("private_canary_read", targets["private_canary"], state),
               file_probe("private_canary_open_write", targets["private_canary"], state, write=True),
               file_probe("protected_input_open_write", targets["protected_input"],
                          manifest["manager_copy_root"], write=True),
               file_probe("management_key_read", Path(state) / "lima" / "_config" / "user", state)]
    results.extend(docker_probes(manifest))
    results.append(signal_probe(manifest["manager_pid"]))
    results.extend(cli_probes(manifest, scratch, raw_ssh))
    return results


def guard_identity(manifest, same_user_control, ordinary_shell):
    require(ordinary_shell, "Confirm the ordinary initiator shell with --ordinary-shell.")
    sandbox = os.environ.get("CODEX_SANDBOX", "").lower()
    require(sandbox in ("", "none", "disabled"),
            "A sandboxed runner cannot establish this host-account boundary.")
    require(not os.environ.get("APP_SANDBOX_CONTAINER_ID"),
            "An app sandbox cannot establish this host-account boundary.")
    uid = os.getuid()
    if same_user_control:
        require(uid == manifest["manager_uid"], "A same-user control requires the manager's exact UID.")
        return uid
    require(uid != manifest["manager_uid"], "Initiator and manager UIDs match; use --same-user-control.")
    require(uid != 0 and 80 not in os.getgroups(),
            "The negative probe requires a non-root account outside macOS admin group 80.")
    return uid


def result_path(filename, manifest):
    path = Path(filename)
    require(path.is_absolute() and path.parent.is_dir(), "Result needs an existing absolute output directory.")
    resolved = path.resolve()
    roots = [REPOSITORY]
    for name in ("manager_state_root", "manager_copy_root"):
        root = Path(manifest[name])
        roots.append(root)
        try:
            roots.append(root.resolve())
        except OSError as error:
            require(error.errno in DENIED_ERRNOS, "Cannot resolve a manager root for output validation.")
    for root in roots:
        require(resolved != root and root not in resolved.parents,
                "Write the result outside the repository and both manager roots.")
    require(not path.exists() and not path.is_symlink(), "Result file already exists.")
    return path


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, required=True, help="Public manager-generated JSON manifest.")
    parser.add_argument("--result", type=Path, required=True, help="New JSON file outside the repository and manager roots.")
    parser.add_argument("--ordinary-shell", action="store_true", help="Confirm this is the ordinary initiator shell.")
    parser.add_argument("--same-user-control", action="store_true", help="Run a control with the manager UID; never establishes protection.")
    parser.add_argument("--raw-ssh", action="store_true", help="Also try the manifest's generated SSH endpoint and identity.")
    parser.add_argument("--colima", type=Path, help="Use an accessible installed or initiator-owned trial-copy CLI.")
    parser.add_argument("--lima", type=Path, help="Use an accessible installed or initiator-owned trial-copy limactl.")
    args = parser.parse_args(argv)
    try:
        manifest = load_manifest(args.manifest)
        uid = guard_identity(manifest, args.same_user_control, args.ordinary_shell)
        require(not args.raw_ssh or "ssh" in manifest, "Raw SSH requires the optional generated endpoint in the manifest.")
        if args.colima:
            manifest["cli"]["colima_binary"] = client_override(args.colima, manifest["trial_id"], "colima")
        if args.lima:
            manifest["cli"]["lima_binary"] = client_override(args.lima, manifest["trial_id"], "limactl")
            manifest["cli"]["instance"] = "colima-trial"
        output = result_path(args.result, manifest)
        started = time.monotonic()
        with tempfile.TemporaryDirectory(prefix="host-control-probe-", dir=output.parent) as directory:
            results = probe(manifest, Path(directory), args.raw_ssh)
        obtained = any(item["outcome"] == OBTAINED for item in results)
        uncertain = any(item["outcome"] == INCONCLUSIVE for item in results)
        positive_controls_ready = bool(manifest.get("positive_controls")) \
            and all(manifest["positive_controls"].values())
        summary = "CONTROL" if args.same_user_control else "FAILURE" if obtained else \
            "INCONCLUSIVE" if uncertain or not positive_controls_ready else "DENIED"
        report = {"schema_version": 1, "trial_id": manifest["trial_id"], "manager_uid": manifest["manager_uid"],
                  "initiator_uid": uid, "same_user_control": args.same_user_control,
                  "ordinary_shell_attested": args.ordinary_shell, "summary": summary,
                  "client_overrides": {"colima": bool(args.colima), "lima": bool(args.lima)},
                  "protection_established": False, "probe_denials_established": summary == "DENIED",
                  "complete_trial_pending": True, "manager_positive_controls_ready": positive_controls_ready,
                  "admission": manifest.get("admission"), "access_obtained": obtained,
                  "elapsed_seconds": round(time.monotonic() - started, 3), "probes": results}
        fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as stream:
            json.dump(report, stream, indent=2)
            stream.write("\n")
        print(summary + ": " + str(output))
        return 0 if summary in ("DENIED", "CONTROL") else 1 if summary == "FAILURE" else 2
    except ManifestError as error:
        print("Probe refused: " + str(error), file=sys.stderr)
        return 3
    except OSError:
        # Do not echo parser input, file content, subprocess diagnostics or keys.
        print("Probe refused: manifest, identity, shell or output precondition failed.", file=sys.stderr)
        return 3


if __name__ == "__main__":
    sys.exit(main())
