"""Fixed transport for credential-free controller diagnostics. Invoke with python3 -I."""
import fcntl
import json
import os
import re
import stat
import socket
import subprocess
import sys
import time
import uuid

REQUESTS = "/sdlc-diagnostic-requests"
RESPONSES = "/sdlc-diagnostic-responses"
WORKSPACE = "/workspace"
REQUEST_LIMIT = 256 * 1024
RESPONSE_LIMIT = 512 * 1024


class LiveTransportUnavailable(Exception):
    pass

def object_pairs(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate JSON field")
        value[key] = item
    return value

def safe_open(path, flags, mode=0o644):
    fd = os.open(path, flags | os.O_NOFOLLOW | os.O_CLOEXEC, mode)
    if flags & os.O_CREAT:
        os.fchmod(fd, mode)
    info = os.fstat(fd)
    if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
        os.close(fd)
        raise ValueError("invalid mailbox file")
    return fd

def main():
    format_only = len(sys.argv) == 2 and sys.argv[1] == "--format"
    if len(sys.argv) > 1 and not format_only:
        raise ValueError("invalid helper option")
    raw = b"[]" if format_only else sys.stdin.buffer.read(REQUEST_LIMIT + 1)
    if len(raw) > REQUEST_LIMIT:
        raise ValueError("request exceeds bound")
    batch = json.loads(raw, object_pairs_hook=object_pairs)
    if not isinstance(batch, list) or not (0 if format_only else 1) <= len(batch) <= 16:
        raise ValueError("submit an array of 1 to 16 diagnostic requests")
    ids = set()
    for request in batch:
        if not isinstance(request, dict) or set(request) - {"id", "purpose", "commands", "baseline", "source_patch"}:
            raise ValueError("invalid diagnostic request fields")
        identifier = request.get("id")
        if not isinstance(identifier, str) or not re.fullmatch(r"[a-z][a-z0-9_-]{0,63}", identifier) or identifier in ids:
            raise ValueError("invalid or duplicate diagnostic id")
        ids.add(identifier)
        if len(json.dumps(request, separators=(",", ":")).encode()) > 8192:
            raise ValueError("diagnostic request exceeds 8 KiB")
    env = {"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": "/tmp", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_TERMINAL_PROMPT": "0", "GIT_OPTIONAL_LOCKS": "0"}
    git = ["/usr/bin/git", "--no-replace-objects", "-c", "safe.directory=" + WORKSPACE, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-C", WORKSPACE]
    fd = safe_open(RESPONSES + "/service.json", os.O_RDONLY)
    with os.fdopen(fd, "rb") as stream:
        descriptor = json.loads(stream.read(4097), object_pairs_hook=object_pairs)
    capability = descriptor.get("capability")
    port = descriptor.get("port")
    if descriptor.get("version") != 1 or not isinstance(capability, str) or not re.fullmatch(r"[a-f0-9]{64}", capability) or not isinstance(port, int) or isinstance(port, bool) or not 1 <= port <= 65535:
        raise ValueError("invalid service descriptor")
    try:
        service = socket.create_connection((descriptor.get("host", "host.docker.internal"), port), timeout=10)
        service.sendall(capability.encode())
        if service.recv(1) != b"\x01":
            service.close()
            raise LiveTransportUnavailable()
    except OSError:
        raise LiveTransportUnavailable()
    service.setblocking(False)
    def ensure_service():
        try:
            if service.recv(1, socket.MSG_PEEK) == b"":
                raise ValueError("diagnostic service stopped")
        except BlockingIOError:
            return
    ensure_service()
    lock = safe_open(REQUESTS + "/lock", os.O_RDWR | os.O_CREAT)
    try:
        while True:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                ensure_service()
                time.sleep(0.05)
        # A killed helper can leave an active request. Drain it before replacing
        # the mailbox, so the controller keeps ownership through worker cleanup.
        try:
            fd = safe_open(REQUESTS + "/request.json", os.O_RDONLY)
        except FileNotFoundError:
            previous = None
        else:
            with os.fdopen(fd, "rb") as stream:
                previous = json.loads(stream.read(REQUEST_LIMIT + 1), object_pairs_hook=object_pairs).get("id")
        if previous:
            while True:
                ensure_service()
                try:
                    fd = safe_open(RESPONSES + "/response.json", os.O_RDONLY)
                except FileNotFoundError:
                    time.sleep(0.05)
                    continue
                with os.fdopen(fd, "rb") as stream:
                    old = json.loads(stream.read(RESPONSE_LIMIT + 1), object_pairs_hook=object_pairs)
                if old.get("id") == previous:
                    break
                time.sleep(0.05)
        ensure_service()
        def revision(expression):
            value = subprocess.check_output(git + ["rev-parse", "--verify", expression], env=env, stderr=subprocess.DEVNULL, timeout=30).decode().strip()
            if not re.fullmatch(r"[a-f0-9]{40}([a-f0-9]{24})?", value):
                raise ValueError("invalid candidate identity")
            return value
        identifier = str(uuid.uuid4())
        envelope = {"version": 1, "id": identifier, "head": revision("HEAD"), "tree": revision("HEAD^{tree}"), "requests": batch}
        if format_only:
            envelope["format_only"] = True
        encoded = json.dumps(envelope, separators=(",", ":")).encode()
        if len(encoded) > REQUEST_LIMIT:
            raise ValueError("request exceeds bound")
        temporary = REQUESTS + "/request-" + identifier
        fd = safe_open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL)
        try:
            with os.fdopen(fd, "wb") as stream:
                stream.write(encoded)
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(temporary, REQUESTS + "/request.json")
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)
        while True:
            ensure_service()
            try:
                fd = safe_open(RESPONSES + "/response.json", os.O_RDONLY)
            except FileNotFoundError:
                time.sleep(0.05)
                continue
            with os.fdopen(fd, "rb") as stream:
                raw_response = stream.read(RESPONSE_LIMIT + 1)
            if len(raw_response) > RESPONSE_LIMIT:
                raise ValueError("response exceeds bound")
            response = json.loads(raw_response, object_pairs_hook=object_pairs)
            if response.get("id") != identifier:
                time.sleep(0.05)
                continue
            if set(response) - {"version", "id", "results", "error", "format_base_head"} or response.get("version") != 1:
                raise ValueError("invalid controller response")
            if "format_base_head" in response and (not format_only or response["format_base_head"] != envelope["head"]):
                raise ValueError("formatter base revision differs")
            sys.stdout.write(json.dumps(response, separators=(",", ":")) + "\n")
            return
    finally:
        os.close(lock)
        service.close()

try:
    main()
except LiveTransportUnavailable:
    sys.stdout.write('{"error":"Live diagnostics unavailable for this turn. Submit diagnostics_requested with diagnostic_requests for controller diagnostics; retain the original session and required check gates."}\n')
    sys.exit(0)
except Exception:
    # Never echo raw Git, file, or transport exceptions from the authenticated worker.
    sys.stdout.write('{"error":"Diagnostic request could not complete. Check the request format and committed checkpoint, then retry; stop if the controller reports an operational hold."}\n')
    sys.exit(1)
