"""Run native provider authentication and TUIs with isolated command homes."""

import json
import os
from pathlib import Path
import signal
import stat
import subprocess
import sys
import tempfile


FILES = {"codex": "auth.json", "claude": ".credentials.json"}
LIMIT = 1024 * 1024
HOME_BASE = "/home/node"
WORKSPACE = Path("/workspace")
SESSION_INSTRUCTIONS = Path("/session-instructions.md")
SESSION_SETTINGS = Path("/session-settings.json")
CODEX_SKILL_INSTALLER = "/opt/sdlc/catalogues/skills/scripts/install-codex-skills"
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
    """Retain only the native account cache, including native logout changes."""
    path = config / FILES["codex"]
    target = directory / FILES["codex"]
    try:
        info = path.lstat()
    except FileNotFoundError:
        # Native logout deletes the scratch auth path. Validate the persistent
        # target again before propagating that deletion.
        if credential(target, os.getuid()) is not None:
            target.unlink()
        return
    if stat.S_ISLNK(info.st_mode):
        if path.readlink() != target:
            raise ValueError("unexpected native cache link")
        credential(target, os.getuid())
        return
    data = credential(path, os.getuid())
    if data is None:
        raise ValueError("missing native credential file")
    persist(directory, FILES["codex"], data)


def codex_interactive(directory):
    target = directory / FILES["codex"]
    if credential(target, os.getuid()) is None:
        raise ValueError("missing credential file")
    require_session_instructions()
    with tempfile.TemporaryDirectory(prefix="sdlc-session-", dir=HOME_BASE) as home:
        config = Path(home) / ".codex"
        config.mkdir(mode=0o700)
        (config / "AGENTS.md").symlink_to(SESSION_INSTRUCTIONS)
        (config / FILES["codex"]).symlink_to(target)
        skills = Path(home) / ".agents" / "skills"
        env = interactive_environment(home)
        env["CODEX_HOME"] = str(config)
        result = subprocess.run(
            ["bash", CODEX_SKILL_INSTALLER, "--prefix", "tjpeel", str(skills)],
            env=env, cwd=home, capture_output=True)
        if result.returncode:
            raise ValueError("could not prepare image skills")
        command = ["codex", "-c", 'cli_auth_credentials_store="file"',
                   "--no-daemon", "--sandbox", "danger-full-access",
                   "--ask-for-approval", "on-request"]
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


def claude_interactive(directory):
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
        raise ValueError("missing empty session settings")
    claude_catalogues(directory)
    with tempfile.TemporaryDirectory(prefix="sdlc-session-", dir=HOME_BASE) as home:
        env = interactive_environment(home)
        env.update({"CLAUDE_CONFIG_DIR": str(directory), "DISABLE_AUTOUPDATER": "1",
                    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
                    "CLAUDE_CODE_SKIP_PROMPT_HISTORY": "1",
                    "CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1",
                    "ENABLE_CLAUDEAI_MCP_SERVERS": "false",
                    "CLAUDE_CODE_DEBUG_LOGS_DIR": str(Path(home) / "debug.log")})
        try:
            return interactive_process(
                ["claude", "--setting-sources", "user", "--permission-mode", "default",
                 "--strict-mcp-config", "--mcp-config", '{"mcpServers":{}}'], env)
        finally:
            claude_credential_metadata(path, os.getuid())


def run_claude(action, directory):
    path = directory / FILES["claude"]
    present = claude_credential_metadata(path, os.getuid())
    if action == "status" and not present:
        print(json.dumps({"state": "missing"}))
        return 0
    with tempfile.TemporaryDirectory(prefix="sdlc-auth-", dir=HOME_BASE) as home:
        # Restricted mode excludes cached user/project settings, including env
        # routing and apiKeyHelper; safe mode excludes user extensions and hooks.
        # Managed policy still applies. The CLI retains its native OAuth cache.
        env = {"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": home,
               "LANG": "C.UTF-8", "TERM": "xterm-256color",
               "CLAUDE_CONFIG_DIR": str(directory), "CLAUDE_CODE_SAFE_MODE": "1",
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
        return 0 if state == "stored" else 1


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
    if action == "interactive":
        if provider == "claude":
            return claude_interactive(directory)
        return codex_interactive(directory)
    if provider == "claude":
        return run_claude(action, directory)
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
    if provider not in FILES or action not in ("init", "login", "status", "interactive"):
        return 1
    try:
        return run(provider, action)
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
