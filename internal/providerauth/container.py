"""Runs in the shared image with an empty home; never returns account details."""

import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile


FILES = {"codex": "auth.json"}
LIMIT = 1024 * 1024
HOME_BASE = "/home/node"


def credential(path, owner, allow_invalid=False):
    """Reject links, permissive files and unexpectedly large credential data."""
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except FileNotFoundError:
        return None
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1
                or info.st_uid != owner or stat.S_IMODE(info.st_mode) != 0o600):
            raise ValueError("unsafe credential file")
        if info.st_size > LIMIT:
            if allow_invalid:
                return None
            raise ValueError("invalid credential file")
        data = stream.read(LIMIT + 1)
    try:
        valid = len(data) <= LIMIT and isinstance(json.loads(data), dict)
    except (ValueError, UnicodeError):
        valid = False
    if not valid:
        if allow_invalid:
            return None
        raise ValueError("invalid credential file")
    return data


def initialise(directory, filename):
    info = directory.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid not in (0, 1000):
        raise ValueError("unsafe storage directory")
    # Root has only CHOWN/FOWNER; it cannot read an existing user's 0600 file.
    # The unprivileged login helper validates the file before it is used.
    os.chown(directory, 1000, 1000)
    os.chmod(directory, 0o700)


def persist(directory, filename, data):
    credential(directory / filename, os.getuid(), allow_invalid=True)
    fd, temporary = tempfile.mkstemp(prefix=".credential-", dir=directory)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, directory / filename)
        fd = os.open(directory, os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def classify(result):
    # Codex can print a masked API key. Never forward its status output.
    message = (result.stdout + result.stderr).strip()
    if result.returncode == 0 and message == "Logged in using ChatGPT":
        return "stored"
    if result.returncode == 1 and message == "Not logged in":
        return "missing"
    return "invalid"


def run(provider, action, directory=Path("/provider-auth")):
    filename = FILES[provider]
    os.umask(0o077)
    if action == "init":
        initialise(directory, filename)
        return 0
    info = directory.lstat()
    if (not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid()
            or stat.S_IMODE(info.st_mode) != 0o700):
        raise ValueError("unsafe storage directory")
    data = credential(directory / filename, os.getuid(), allow_invalid=action == "login")
    if action == "status" and data is None:
        print(json.dumps({"state": "missing"}))
        return 0
    with tempfile.TemporaryDirectory(prefix="sdlc-auth-", dir=HOME_BASE) as home:
        config = Path(home) / ".codex"
        config.mkdir(mode=0o700)
        if data is not None:
            (config / filename).write_bytes(data)
        env = {"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": home,
               "LANG": "C.UTF-8", "TERM": "xterm-256color", "CODEX_HOME": str(config)}
        command = ["codex", "-c", 'cli_auth_credentials_store="file"']
        status_args = ["login", "status"]
        if action == "login":
            login_args = ["login", "--device-auth"]
            result = subprocess.run(command + login_args, env=env, cwd=home)
            if result.returncode:
                return 1
        result = subprocess.run(command + status_args, env=env, cwd=home,
                                capture_output=True, text=True, timeout=30)
        state = classify(result)
        if action == "status":
            print(json.dumps({"state": state}))
            return 0
        if state != "stored":
            return 1
        data = credential(config / filename, os.getuid())
        if data is None:
            return 1
        persist(directory, filename, data)
        return 0


def main():
    provider, action = sys.argv[1:]
    if provider not in FILES or action not in ("init", "login", "status"):
        return 1
    try:
        return run(provider, action)
    except Exception:
        # Exception strings, provider errors and JSON can contain secrets.
        if action == "status":
            print(json.dumps({"state": "invalid"}))
            return 0
        print("SDLC could not complete authentication safely.", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
