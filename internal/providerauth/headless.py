"""Native headless clients; loaded alongside container.py without its main."""

import json
import os
from pathlib import Path
import signal
import stat
import subprocess
import sys
import tempfile
import tomllib

SESSION = Path("/session")
PROMPT = Path("/prompt.txt")
SCHEMA = Path("/schema.json")
CODEX_AGENTS = Path("/etc/codex/agents")
MAXIMUM_CODEX_TRUST_RECORD = 4096
CLAUDE_REFRESH_BASE = ".native-refresh-base.json"


def codex_trust_metadata(info):
    return (stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid()
            and info.st_nlink == 1 and stat.S_IMODE(info.st_mode) == 0o600
            and 0 < info.st_size <= MAXIMUM_CODEX_TRUST_RECORD)


def validate_codex_workspace_trust(path):
    """Allow only the native client's private trust marker for this workspace."""
    try:
        before = path.lstat()
        if not codex_trust_metadata(before):
            raise ValueError("unsafe native trust record")
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(descriptor, "rb") as stream:
            opened = os.fstat(stream.fileno())
            if (not codex_trust_metadata(opened)
                    or not os.path.samestat(before, opened)):
                raise ValueError("changed native trust record")
            data = stream.read(MAXIMUM_CODEX_TRUST_RECORD + 1)
            after = os.fstat(stream.fileno())
        current = path.lstat()
        if (len(data) > MAXIMUM_CODEX_TRUST_RECORD
                or not codex_trust_metadata(after)
                or not codex_trust_metadata(current)
                or not os.path.samestat(before, after)
                or not os.path.samestat(before, current)
                or any((info.st_size, info.st_mtime_ns, info.st_ctime_ns)
                       != (before.st_size, before.st_mtime_ns, before.st_ctime_ns)
                       for info in (opened, after, current))):
            raise ValueError("changed native trust record")
        record = tomllib.loads(data.decode("utf-8"))
        if record != {"projects": {"/workspace": {"trust_level": "trusted"}}}:
            raise ValueError("unexpected native trust record")
    except (OSError, ValueError, UnicodeError):
        raise ValueError("unexpected session customization") from None


def safe_directory(path, shared=False):
    if path.exists() or path.is_symlink():
        info = path.lstat()
        if (not stat.S_ISDIR(info.st_mode)
                or (not shared and info.st_uid != os.getuid())
                or not os.access(path, os.R_OK | os.W_OK | os.X_OK)):
            raise ValueError("unsafe native session directory")
    else:
        path.mkdir(mode=0o700)
    if not shared:
        os.chmod(path, 0o700)


def auth_link(config, provider, directory):
    if (config / CLAUDE_REFRESH_BASE).exists():
        raise ValueError("retained legacy native refresh needs inspection before migration")
    path = native.snapshot_auth(config, provider, directory)
    return path, directory / native.FILES[provider]


def finish_auth(config, provider, directory):
    native.finish_snapshot(config, provider, directory)


def persist_claude_refresh(path, directory):
    # Retained legacy copies without a transaction baseline require inspection.
    try:
        native.finish_snapshot(path.parent, "claude", directory)
    except OSError as error:
        if error.errno == 62 or error.errno == 40:
            raise ValueError("unsafe native cache") from None
        raise


def native_command(provider, model, effort, resume, readonly):
    speed = os.environ.get("SDLC_NATIVE_SPEED", "")
    if speed not in ("", "normal", "fast"):
        raise ValueError("unsupported native speed")
    if provider == "claude" and speed == "fast" and model not in ("claude-opus-5-5", "claude-opus-5", "claude-opus-4-8"):
        raise ValueError("unsupported Claude fast model")
    if provider == "codex":
        command = ["codex", "exec"]
        if resume:
            command += ["resume"]
        command += ["--json", "--output-schema", str(SCHEMA), "--model", model,
                    "--ignore-rules",
                    "-c", 'cli_auth_credentials_store="file"',
                    "-c", 'approval_policy="never"',
                    "-c", 'sandbox_mode="danger-full-access"',
                    "-c", "model_reasoning_effort=" + json.dumps(effort),
                    "-c", "mcp_servers={}", "-c", "features.hooks=false",
                    "-c", "features.plugins=false"]
        if speed == "fast":
            command += ["-c", 'service_tier="priority"']
        command += [resume, "-"] if resume else ["-"]
        return command
    settings = {"disableAllHooks": True, "enabledPlugins": {},
                "skipDangerousModePermissionPrompt": True}
    if speed:
        settings["fastMode"] = speed == "fast"
    command = ["claude", "-p", "--output-format", "stream-json", "--verbose",
               "--include-partial-messages", "--forward-subagent-text",
               "--model", model, "--effort", effort,
               "--json-schema", json.dumps(json.loads(SCHEMA.read_text())),
               # User scope discovers the guarded pinned catalogues. Project
               # and local settings stay excluded; cached customization is
               # rejected before this command can start.
               "--setting-sources", "user", "--settings", json.dumps(settings),
               "--strict-mcp-config", "--mcp-config", '{"mcpServers":{}}',
               "--permission-mode", "bypassPermissions"]
    if resume:
        command += ["--resume", resume]
    return command


def apply_headroom_route(provider, command, env):
    # SDLC supplies this marker only after starting its private sidecar. Never
    # copy a URL or authentication override from the host environment.
    if os.environ.get("SDLC_HEADROOM") != "1":
        return
    if provider == "codex":
        command += ["-c", 'openai_base_url="http://127.0.0.1:8787/v1"']
        env["OPENAI_BASE_URL"] = "http://127.0.0.1:8787/v1"
    elif provider == "claude":
        env["ANTHROPIC_BASE_URL"] = "http://127.0.0.1:8787"
    else:
        raise ValueError("unsupported Headroom provider")


def process(command, env):
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

    handlers = {number: signal.getsignal(number)
                for number in (signal.SIGTERM, signal.SIGINT)}
    for number in handlers:
        signal.signal(number, terminate)
    try:
        with PROMPT.open("rb") as prompt:
            child = subprocess.Popen(command, env=env, cwd=native.WORKSPACE,
                                     stdin=prompt)
            if terminating:
                child.send_signal(signal.SIGTERM)
            code = child.wait()
            return code if code >= 0 else 128 - code
    finally:
        for number, handler in handlers.items():
            signal.signal(number, handler)


def headless(provider, model, effort, resume, readonly, directory=Path("/provider-auth")):
    os.umask(0o077)
    info = directory.lstat()
    if (not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid()
            or stat.S_IMODE(info.st_mode) != 0o700):
        raise ValueError("unsafe authentication directory")
    if provider == "codex":
        if native.credential(directory / native.FILES[provider], os.getuid()) is None:
            raise ValueError("missing native authentication")
    elif not native.claude_credential_metadata(directory / native.FILES[provider], os.getuid()):
        raise ValueError("missing native authentication")
    safe_directory(SESSION, shared=True)
    config = SESSION / provider
    safe_directory(config)
    instructions = Path('/instructions.md')
    if instructions.exists():
        name = 'AGENTS.md' if provider == 'codex' else 'CLAUDE.md'
        target = config / name
        if target.is_symlink() and target.readlink() == instructions:
            pass
        elif target.exists() or target.is_symlink():
            raise ValueError('unexpected native instructions')
        else:
            target.symlink_to(instructions)
    # Native transcripts and the exact workspace-trust marker survive. Settings
    # are supplied on every launch; project extensions are hidden by mounts.
    for name in ("config.toml", "settings.json", "hooks.json", "plugins",
                 "rules", "commands", "output-styles", "workflows", "mcp.json",
                 "CLAUDE.local.md"):
        path = config / name
        if path.exists() or path.is_symlink():
            if provider == "codex" and name == "config.toml":
                validate_codex_workspace_trust(path)
                continue
            raise ValueError("unexpected session customization")
    with tempfile.TemporaryDirectory(prefix="sdlc-headless-", dir=native.HOME_BASE) as home:
        env = native.interactive_environment(home)
        if provider == "codex":
            env["CODEX_HOME"] = str(config)
            skills = Path(home) / ".agents" / "skills"
            result = subprocess.run(
                ["bash", native.CODEX_SKILL_INSTALLER, "--provider", "codex",
                 "--prefix", "tjpeel", "--", str(skills)],
                env=env, cwd=home, capture_output=True)
            if result.returncode:
                raise ValueError("could not prepare image skills")
            # Agent configuration files live at their native pinned image path.
            if not CODEX_AGENTS.is_dir():
                raise ValueError("missing image agents")
            agents = config / "agents"
            if agents.is_symlink() and agents.readlink() == CODEX_AGENTS:
                pass
            elif agents.exists() or agents.is_symlink():
                raise ValueError("unexpected image agents")
            else:
                agents.symlink_to(CODEX_AGENTS, target_is_directory=True)
        else:
            native.claude_catalogues(config)
            env.update({"CLAUDE_CONFIG_DIR": str(config), "DISABLE_AUTOUPDATER": "1",
                        "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
                        "CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1",
                        # Foreground delegates complete before native structured
                        # output can end the parent turn with work still pending.
                        "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS": "1",
                        "ENABLE_CLAUDEAI_MCP_SERVERS": "false"})
        command = native_command(provider, model, effort, resume, readonly)
        apply_headroom_route(provider, command, env)
        auth_link(config, provider, directory)
        try:
            subscription.capture(provider, 'before', env)
            subscription.boundary(provider, 'start')
            try:
                code = process(command, env)
            finally:
                subscription.boundary(provider, 'end')
                subscription.capture(provider, 'after', env)
        finally:
            finish_auth(config, provider, directory)
        print(json.dumps({"type": "sdlc.native_finalization", "provider": provider,
                          "exit_code": code}), flush=True)
        return code


def main():
    if len(sys.argv) != 6 or sys.argv[1] not in native.FILES or sys.argv[5] not in ("true", "false"):
        return 1
    try:
        return headless(*sys.argv[1:5], sys.argv[5] == "true")
    except Exception:
        print("SDLC could not complete the native headless session safely.", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
