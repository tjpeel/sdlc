"""Run native provider authentication and TUIs with isolated command homes."""

import json
import fcntl
import hashlib
import re
from contextlib import contextmanager
import os
from pathlib import Path
import signal
import stat
import subprocess
import sys
import tempfile


FILES = {"codex": "auth.json", "claude": ".credentials.json", "synthetic": "auth.json"}
INTERACTIVE_MODES = {
    "synthetic": ("allow", "ask"),
    "codex": ("never", "on-request"),
    "claude": ("bypassPermissions", "default", "manual", "acceptEdits",
               "plan", "auto", "dontAsk"),
}
LIMIT = 1024 * 1024
HOME_BASE = "/home/node"
WORKSPACE = Path("/workspace")
SESSION_INSTRUCTIONS = Path("/session-instructions.md")
SESSION_SETTINGS = Path("/session-settings.json")
CODEX_SKILL_INSTALLER = "/opt/sdlc/catalogues/skills/scripts/install-skills"
CLAUDE_CATALOGUES = Path("/opt/sdlc/claude")


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


def persist(directory, filename, data, verify=None):
    claude_credential_metadata(directory / filename, os.getuid())
    fd, temporary = tempfile.mkstemp(prefix=".credential-", dir=directory)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        if verify is not None:
            verify()
        os.replace(temporary, directory / filename)
        fd = os.open(directory, os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)



BASELINE = ".native-auth-base.json"


def opaque_cache(path, replacement_ok=False, allow_invalid=False):
    """Read a bounded private native cache without interpreting credentials."""
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except FileNotFoundError:
        return None
    with os.fdopen(descriptor, "rb") as stream:
        before = os.fstat(stream.fileno())
        if (not stat.S_ISREG(before.st_mode) or before.st_uid != os.getuid()
                or before.st_nlink != 1 or stat.S_IMODE(before.st_mode) != 0o600):
            raise ValueError("unsafe native cache")
        if not 0 < before.st_size <= LIMIT:
            if allow_invalid:
                return None
            raise ValueError("invalid native cache size")
        data = stream.read(LIMIT + 1)
        after = os.fstat(stream.fileno())
        current = path.lstat()
        generation = lambda i: (i.st_dev, i.st_ino, i.st_size, i.st_mtime_ns, i.st_ctime_ns)
        if len(data) != before.st_size or generation(before) != generation(after) or (not replacement_ok and generation(before) != generation(current)):
            raise ValueError("changed native cache")
        return data


@contextmanager
def cache_transaction(directory):
    path = directory / ".native-auth.lock"
    descriptor = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    try:
        info = os.fstat(descriptor)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid()
                or info.st_nlink != 1 or stat.S_IMODE(info.st_mode) != 0o600
                or not os.path.samestat(info, path.lstat())):
            raise ValueError("unsafe native cache lock")
        fcntl.flock(descriptor, fcntl.LOCK_EX)
        if not os.path.samestat(info, path.lstat()):
            raise ValueError("changed native cache lock")
        yield
    finally:
        os.close(descriptor)


def cache_digest(data):
    return None if data is None else hashlib.sha256(data).hexdigest()


def cache_epoch(directory):
    return opaque_cache(directory / ".native-auth-epoch")



@contextmanager
def private_config(home, provider, directory):
    config = Path(home) / (".codex" if provider == "codex" else ".claude")
    config.mkdir(mode=0o700)
    try:
        yield config
    finally:
        if (config / BASELINE).exists():
            recovery = Path(tempfile.mkdtemp(prefix=".native-recovery-", dir=directory))
            baseline = opaque_cache(config / BASELINE)
            baseline_name = BASELINE
            try:
                candidate = opaque_cache(config / FILES[provider])
            except (OSError, ValueError):
                # Unsafe native output is never copied into recovery storage.
                candidate = None
                baseline_name = ".unsafe-native-auth-base.json"
            persist(recovery, baseline_name, baseline)
            if candidate is not None:
                persist(recovery, FILES[provider], candidate)

def snapshot_auth(config, provider, directory, login=False):
    path = config / FILES[provider]
    if path.exists() or path.is_symlink() or (config / BASELINE).exists():
        finish_snapshot(config, provider, directory)
    with cache_transaction(directory):
        if login:
            persist(directory, ".native-auth-epoch", json.dumps({"epoch": os.urandom(16).hex()}).encode())
        data = opaque_cache(directory / FILES[provider], allow_invalid=login)
        epoch = cache_epoch(directory)
        # Publish the baseline only after a complete private cache copy.
        if data is not None:
            persist(config, FILES[provider], data)
        persist(config, BASELINE, json.dumps({"digest": cache_digest(data), "epoch": cache_digest(epoch), "login": login}).encode())
    return path


def finish_snapshot(config, provider, directory):
    path = config / FILES[provider]
    data = opaque_cache(path)
    generation = lambda info: (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns)
    source_generation = generation(path.stat()) if data is not None else None
    def verify_source():
        if opaque_cache(path) != data or (data is not None and generation(path.stat()) != source_generation):
            raise ValueError("native cache changed before persistence")
    baseline = credential(config / BASELINE, os.getuid())
    if baseline is None:
        raise ValueError("missing native cache baseline")
    record = json.loads(baseline)
    if (not isinstance(record, dict) or set(record) != {"digest", "epoch", "login"}
            or type(record["login"]) is not bool
            or any(value is not None and (not isinstance(value, str) or re.fullmatch(r"[0-9a-f]{64}", value) is None)
                   for value in (record["digest"], record["epoch"]))):
        raise ValueError("invalid native cache baseline")
    # Unchanged invocations never overwrite another invocation's native refresh.
    if cache_digest(data) != record.get("digest"):
        with cache_transaction(directory):
            target = directory / FILES[provider]
            current = opaque_cache(target, allow_invalid=record["login"])
            if (cache_digest(cache_epoch(directory)) != record.get("epoch")
                    or (not record.get("login", False) and cache_digest(current) != record.get("digest") and current != data)):
                raise ValueError("saved native cache changed; private refresh needs inspection")
            if current != data:
                if data is None:
                    verify_source()
                    target.unlink(missing_ok=True)
                    persist(directory, ".native-auth-epoch", json.dumps({"epoch": os.urandom(16).hex()}).encode())
                else:
                    persist(directory, FILES[provider], data, verify=verify_source)
    verify_source()
    # Removing the baseline first makes interrupted cleanup fail closed. A
    # leftover private file without a baseline cannot be mistaken for logout.
    (config / BASELINE).unlink()
    descriptor = os.open(config, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
    path.unlink(missing_ok=True)

def classify(result):
    # Codex can print a masked API key. Never forward its status output.
    message = (result.stdout + result.stderr).strip()
    if result.returncode == 0 and message == "Logged in using ChatGPT":
        return "stored"
    if result.returncode == 1 and message == "Not logged in":
        return "missing"
    return "invalid"


def claude_credential_metadata(path, owner):
    """Validate file metadata without opening Claude's native credential cache."""
    try:
        info = path.lstat()
    except FileNotFoundError:
        return False
    if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1
            or info.st_uid != owner or stat.S_IMODE(info.st_mode) != 0o600):
        raise ValueError("unsafe credential file")
    return True


def classify_claude(result):
    # The official status command owns credential loading. Drop account details,
    # configuration paths and stderr rather than passing them back to the host.
    try:
        if len(result.stdout) > LIMIT:
            return "invalid"
        record = json.loads(result.stdout)
    except (ValueError, TypeError):
        return "invalid"
    if not isinstance(record, dict):
        return "invalid"
    if (result.returncode == 0 and record.get("loggedIn") is True
            and record.get("authMethod") == "claude.ai"
            and record.get("apiProvider") == "firstParty"):
        return "stored"
    if (result.returncode == 1 and record.get("loggedIn") is False
            and record.get("authMethod") == "none"):
        return "missing"
    return "invalid"


def interactive_environment(home):
    # Pass no host credentials, proxy configuration or provider overrides. The
    # image's tools and the selected native account cache are the only inputs.
    return {"PATH": "/opt/dotnet:/usr/local/bin:/usr/bin:/bin", "HOME": home,
            "LANG": "C.UTF-8", "TERM": "xterm-256color",
            "DOTNET_ROOT": "/opt/dotnet", "DOTNET_CLI_TELEMETRY_OPTOUT": "1",
            "DOTNET_SKIP_FIRST_TIME_EXPERIENCE": "1"}


def interactive_process(command, env):
    """Keep the native TUI attached while allowing orderly container shutdown."""
    child = None
    terminating = False

    def terminate(signum, frame):
        nonlocal terminating
        terminating = True
        if child is not None:
            try:
                child.send_signal(signum)
            except ProcessLookupError:
                pass

    # A Python handler resets to the default when the child execs. Ctrl-C thus
    # reaches the native TUI without interrupting our wait or cache bookkeeping.
    handlers = {number: signal.getsignal(number)
                for number in (signal.SIGTERM, signal.SIGINT)}
    signal.signal(signal.SIGTERM, terminate)
    signal.signal(signal.SIGINT, lambda signum, frame: None)
    try:
        child = subprocess.Popen(command, env=env, cwd=WORKSPACE)
        if terminating:
            child.send_signal(signal.SIGTERM)
        code = child.wait()
        return code if code >= 0 else 128 - code
    finally:
        for number, handler in handlers.items():
            signal.signal(number, handler)


def require_session_instructions():
    info = SESSION_INSTRUCTIONS.lstat()
    if not stat.S_ISREG(info.st_mode) or not os.access(SESSION_INSTRUCTIONS, os.R_OK):
        raise ValueError("missing session instructions")


def finish_codex_interactive(config, directory):
    finish_snapshot(config, "codex", directory)


def codex_interactive(directory, mode):
    target = directory / FILES["codex"]
    if credential(target, os.getuid()) is None:
        raise ValueError("missing credential file")
    require_session_instructions()
    with tempfile.TemporaryDirectory(prefix="sdlc-session-", dir=HOME_BASE) as home, private_config(home, "codex", directory) as config:
        (config / "AGENTS.md").symlink_to(SESSION_INSTRUCTIONS)
        snapshot_auth(config, "codex", directory)
        skills = Path(home) / ".agents" / "skills"
        env = interactive_environment(home)
        env["CODEX_HOME"] = str(config)
        result = subprocess.run(
            ["bash", CODEX_SKILL_INSTALLER, "--provider", "codex",
             "--prefix", "tjpeel", "--", str(skills)],
            env=env, cwd=home, capture_output=True)
        if result.returncode:
            raise ValueError("could not prepare image skills")
        command = ["codex", "-c", 'cli_auth_credentials_store="file"',
                   "--no-daemon", "--sandbox", "danger-full-access",
                   "--ask-for-approval", mode]
        try:
            return interactive_process(command, env)
        finally:
            finish_codex_interactive(config, directory)



def claude_catalogues(directory):
    for name in ("skills", "agents"):
        target = CLAUDE_CATALOGUES / name
        path = directory / name
        if not target.is_dir():
            raise ValueError("missing image catalogue")
        if path.is_symlink() and path.readlink() == target:
            continue
        if path.exists() or path.is_symlink():
            raise ValueError("conflicting catalogue path")
        path.symlink_to(target, target_is_directory=True)


def claude_interactive(directory, mode):
    path = directory / FILES["claude"]
    if not claude_credential_metadata(path, os.getuid()):
        raise ValueError("missing credential file")
    require_session_instructions()
    # Both paths are readonly mounts of the same file. Cached instructions must
    # never replace the body selected through the SDLC CLI.
    if not (directory / "CLAUDE.md").samefile(SESSION_INSTRUCTIONS):
        raise ValueError("missing shared Claude instructions")
    settings_info = SESSION_SETTINGS.lstat()
    if (not stat.S_ISREG(settings_info.st_mode)
            or not os.access(SESSION_SETTINGS, os.R_OK)
            or not (directory / "settings.json").samefile(SESSION_SETTINGS)):
        raise ValueError("missing isolated session settings")
    with tempfile.TemporaryDirectory(prefix="sdlc-session-", dir=HOME_BASE) as home, private_config(home, "claude", directory) as config:
        (config / "CLAUDE.md").symlink_to(SESSION_INSTRUCTIONS)
        (config / "settings.json").symlink_to(SESSION_SETTINGS)
        claude_catalogues(config)
        snapshot_auth(config, "claude", directory)
        env = interactive_environment(home)
        env.update({"CLAUDE_CONFIG_DIR": str(config), "DISABLE_AUTOUPDATER": "1",
                    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
                    "CLAUDE_CODE_SKIP_PROMPT_HISTORY": "1",
                    "CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1",
                    "ENABLE_CLAUDEAI_MCP_SERVERS": "false",
                    "CLAUDE_CODE_DEBUG_LOGS_DIR": str(Path(home) / "debug.log")})
        try:
            return interactive_process(
                ["claude", "--setting-sources", "user", "--permission-mode", mode,
                 "--strict-mcp-config", "--mcp-config", '{"mcpServers":{}}'], env)
        finally:
            finish_snapshot(config, "claude", directory)



def run_claude(action, directory):
    path = directory / FILES["claude"]
    present = claude_credential_metadata(path, os.getuid())
    if action == "status" and not present:
        print(json.dumps({"state": "missing"}))
        return 0
    with tempfile.TemporaryDirectory(prefix="sdlc-auth-", dir=HOME_BASE) as home, private_config(home, "claude", directory) as config:
        if action == "login":
            snapshot_auth(config, "claude", directory, login=True)
        elif present:
            persist(config, FILES["claude"], opaque_cache(path, replacement_ok=True))
        path = config / FILES["claude"]
        # Restricted mode excludes cached user/project settings, including env
        # routing and apiKeyHelper; safe mode excludes user extensions and hooks.
        # Managed policy still applies. The CLI retains its native OAuth cache.
        env = {"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": home,
               "LANG": "C.UTF-8", "TERM": "xterm-256color",
               "CLAUDE_CONFIG_DIR": str(config), "CLAUDE_CODE_SAFE_MODE": "1",
               "CLAUDE_CODE_RESTRICTED": "1",
               "DISABLE_AUTOUPDATER": "1",
               "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}
        if action == "login":
            result = subprocess.run(["claude", "auth", "login"], env=env, cwd=home)
            if result.returncode:
                # Claude owns any cache changes, including a failed login.
                return 1
            if not claude_credential_metadata(path, os.getuid()):
                return 1
        result = subprocess.run(["claude", "auth", "status", "--json"],
                                env=env, cwd=home, capture_output=True,
                                text=True, timeout=30)
        state = classify_claude(result)
        if not claude_credential_metadata(path, os.getuid()) and state == "stored":
            state = "invalid"
        if action == "status":
            print(json.dumps({"state": state}))
            return 0
        if state == "stored":
            finish_snapshot(config, "claude", directory)

        return 0 if state == "stored" else 1


def interactive_mode(provider, mode):
    allowed = INTERACTIVE_MODES[provider]
    if mode is None:
        return allowed[0]
    if not isinstance(mode, str) or mode not in allowed:
        raise ValueError("unsupported interactive mode")
    return mode


def run(provider, action, directory=Path("/provider-auth"), mode=None):
    if action == "interactive":
        mode = interactive_mode(provider, mode)
    elif mode is not None:
        raise ValueError("interactive modes require an interactive session")
    filename = FILES[provider]
    os.umask(0o077)
    if action == "init":
        initialise(directory, filename)
        return 0
    info = directory.lstat()
    if (not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid()
            or stat.S_IMODE(info.st_mode) != 0o700):
        raise ValueError("unsafe storage directory")
    if provider == "synthetic":
        if action == "interactive":
            return synthetic_interactive(directory, mode)
        return synthetic_auth(action, directory)
    if action == "interactive":
        if provider == "claude":
            return claude_interactive(directory, mode)
        return codex_interactive(directory, mode)
    if provider == "claude":
        return run_claude(action, directory)
    data = credential(directory / filename, os.getuid(), allow_invalid=action == "login")
    if action == "status" and data is None:
        print(json.dumps({"state": "missing"}))
        return 0
    with tempfile.TemporaryDirectory(prefix="sdlc-auth-", dir=HOME_BASE) as home, private_config(home, provider, directory) as config:
        if action == "login":
            snapshot_auth(config, provider, directory, login=True)
        elif data is not None:
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
        finish_snapshot(config, provider, directory)
        return 0


def main():
    arguments = sys.argv[1:]
    if len(arguments) not in (2, 3):
        return 1
    provider, action = arguments[:2]
    mode = arguments[2] if len(arguments) == 3 else None
    if provider not in FILES or action not in ("init", "login", "status", "interactive", "logout"):
        return 1
    if action == "logout" and provider != "synthetic":
        return 1
    if action != "interactive" and mode is not None:
        return 1
    try:
        return run(provider, action, mode=mode)
    except Exception:
        # Exception strings, provider errors and JSON can contain secrets.
        if action == "status":
            print(json.dumps({"state": "invalid"}))
            return 0
        message = ("SDLC could not complete the interactive session safely."
                   if action == "interactive"
                   else "SDLC could not complete authentication safely.")
        print(message, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
