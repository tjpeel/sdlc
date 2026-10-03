#!/usr/bin/env python3
"""Explicit local-only probe. Uses fake OAuth data; never logs in or calls a model."""

import argparse
import base64
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import uuid


ROOT = Path(__file__).resolve().parents[1]
HELPER = (ROOT / "internal/providerauth/container.py").read_text()


def command(args, env=None, expected=0):
    result = subprocess.run(args, env=env, capture_output=True, text=True, timeout=60)
    if result.returncode != expected:
        raise RuntimeError("local authentication probe command failed; diagnostics withheld")
    return result.stdout


def fixture(provider):
    if provider == "claude":
        return {"claudeAiOauth": {"accessToken": "fake-access", "refreshToken": "fake-refresh",
                "expiresAt": 4102444800000, "scopes": ["user:inference"],
                "subscriptionType": "max", "rateLimitTier": "default_claude_max_5x"}}
    def encode(value):
        return base64.urlsafe_b64encode(json.dumps(value).encode()).decode().rstrip("=")
    fake_jwt = ".".join([encode({"alg": "none"}), encode({"sub": "fake", "exp": 4102444800,
                     "https://chatgpt.org/claims/account_id": "fake-account"}), "fake-signature"])
    return {"auth_mode": "chatgpt", "tokens": {"id_token": fake_jwt,
            "access_token": "fake-access", "refresh_token": "fake-refresh"}}


def container(image, volume, user="1000:1000", root=False):
    args = ["docker", "run", "--rm", "--pull", "never", "--network", "none",
            "--log-driver", "none", "--read-only", "--user", user, "--cap-drop", "ALL",
            "--security-opt", "no-new-privileges", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m,mode=1777",
            "--tmpfs", "/home/node/:rw,nosuid,nodev,noexec,size=64m,mode=0700,uid=1000,gid=1000"]
    if root:
        args += ["--cap-add", "CHOWN", "--cap-add", "FOWNER"]
    return args + ["--mount", "type=volume,src=" + volume + ",dst=/provider-auth,volume-nocopy",
                   "--entrypoint", "/usr/bin/python3", image]


def probe_claude_settings(image, volume, cli, env):
    marker = "/tmp/sdlc-auth-settings-marker"
    settings = {
        "apiKeyHelper": "touch " + marker + "; printf fake-settings-key",
        "env": {"ANTHROPIC_API_KEY": "fake-settings-key",
                "CLAUDE_CODE_OAUTH_TOKEN": "fake-settings-token",
                "CLAUDE_CODE_USE_BEDROCK": "1",
                "CLAUDE_CODE_SAFE_MODE": "0", "CLAUDE_CODE_RESTRICTED": "0"},
        "hooks": {"SessionStart": [{"hooks": [{"type": "command",
                                               "command": "touch " + marker}]}]},
    }
    write = "import os,pathlib,sys; os.umask(0o077); pathlib.Path('/provider-auth/settings.json').write_text(sys.argv[1])"
    command(container(image, volume) + ["-c", write, json.dumps(settings)])
    try:
        result = command([cli, "auth", "status", "--provider", "claude"], env=env)
        if "stored account login (offline check" not in result or "fake-settings" in result:
            raise RuntimeError("cached settings affected native account status")
        # Inspect the marker before the disposable tmpfs disappears. This uses
        # the production helper and the same read-only cache as CLI status.
        runner = HELPER.split('if __name__ == "__main__":')[0]
        runner += "\nrun('claude', 'status')\n"
        runner += "print(json.dumps({'customization_executed': Path('" + marker + "').exists()}))\n"
        readonly = container(image, volume)
        readonly[readonly.index("--mount") + 1] += ",readonly"
        records = [json.loads(line) for line in command(readonly + ["-c", runner]).splitlines()]
        if records != [{"state": "stored"}, {"customization_executed": False}]:
            raise RuntimeError("cached settings were not isolated from authentication")
    finally:
        remove = "from pathlib import Path; Path('/provider-auth/settings.json').unlink()"
        command(container(image, volume) + ["-c", remove])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cli", required=True, help="built SDLC executable")
    args = parser.parse_args()
    cli = str(Path(args.cli).resolve())
    status = command([cli, "runtime", "status"])
    match = re.search(r"^Image ID: (sha256:[0-9a-f]{64})$", status, re.MULTILINE)
    if not match:
        raise RuntimeError("build and record a shared runtime before probing")
    image = match.group(1)
    engine = command(["docker", "info", "--format", "{{.ID}}"] ).strip()
    identity = uuid.uuid4().hex
    volumes = []
    try:
        with tempfile.TemporaryDirectory(prefix="sdlc-auth-probe-") as temporary:
            state = Path(temporary)
            (state / "runtime.json").write_text(json.dumps({"version": 1, "engine_id": engine, "image_id": image}))
            (state / "auth-installation.json").write_text(json.dumps({"id": identity}))
            env = dict(os.environ, SDLC_STATE_DIR=temporary)
            command([cli, "auth", "status"], env=env, expected=1)
            for provider, filename in (("codex", "auth.json"), ("claude", ".credentials.json")):
                name = "sdlc-auth-" + identity + "-" + provider
                command(["docker", "volume", "create", "--driver", "local", "--label", "io.sdlc.managed=true",
                         "--label", "io.sdlc.kind=provider-auth", "--label", "io.sdlc.installation=" + identity,
                         "--label", "io.sdlc.provider=" + provider, name])
                volumes.append(name)
                initialise = container(image, name, user="0:0", root=True) + ["-c", HELPER, provider, "init"]
                command(initialise)
                missing = command(container(image, name) + ["-c", HELPER, provider, "status"])
                if json.loads(missing) != {"state": "missing"}:
                    raise RuntimeError("empty volume did not report missing login")
                write = "import os, pathlib, sys; os.umask(0o077); pathlib.Path('/provider-auth', sys.argv[1]).write_text(sys.argv[2])"
                command(container(image, name) + ["-c", write, filename, json.dumps(fixture(provider))])
                # Existing UID1000/0700 storage must work with the restricted root initializer.
                command(initialise)
                for _ in range(2):
                    result = command([cli, "auth", "status", "--provider", provider], env=env)
                    if "stored account login (offline check" not in result or "fake-access" in result:
                        raise RuntimeError("fresh CLI status did not safely load the fixture")
                if provider == "claude":
                    probe_claude_settings(image, name, cli, env)
                command(container(image, name) + ["-c", write, filename, "truncated {"])
                result = command([cli, "auth", "status", "--provider", provider], env=env, expected=1)
                if "could not be loaded" not in result and "no stored login" not in result:
                    raise RuntimeError("malformed credentials did not produce a safe failure")
                if provider == "codex":
                    repair = HELPER.split('if __name__ == "__main__":')[0] + "\npersist(Path('/provider-auth'), sys.argv[1], sys.argv[2].encode())\n"
                    command(container(image, name) + ["-c", repair, filename, json.dumps(fixture(provider))])
                else:
                    # Restore only this probe's fake fixture, without SDLC credential handling.
                    command(container(image, name) + ["-c", write, filename, json.dumps(fixture(provider))])
                command([cli, "auth", "status", "--provider", provider], env=env)
            command([cli, "auth", "status"], env=env)
            print("Local authentication probe passed for Codex and Claude using disposable fake credentials.")
    finally:
        for name in volumes:
            command(["docker", "volume", "rm", name])


if __name__ == "__main__":
    main()
