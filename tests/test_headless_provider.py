"""Offline headless client tests with disposable caches and mocked processes."""

import importlib.util
import io
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1] / "internal/providerauth"


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


native = load("headless_auth", "container.py")
headless = load("headless_test", "headless.py")
headless.native = native
ID = "00000000-0000-4000-8000-000000000001"
CODEX_WORKSPACE_TRUST = '[projects."/workspace"]\ntrust_level = "trusted"\n'


class HeadlessTests(unittest.TestCase):
    def setUp(self):
        previous = os.umask(0o077)
        self.addCleanup(os.umask, previous)
        storage = tempfile.TemporaryDirectory()
        self.addCleanup(storage.cleanup)
        self.root = Path(storage.name)
        self.cache = self.root / "cache"
        self.cache.mkdir(mode=0o700)
        self.session = self.root / "session"
        self.session.mkdir(mode=0o777)
        self.home = self.root / "home"
        self.home.mkdir()
        self.workspace = self.root / "workspace"
        self.workspace.mkdir()
        self.prompt = self.root / "prompt"
        self.prompt.write_text("Synthetic prompt $(do-not-execute) `literal`.\n")
        self.schema = self.root / "schema"
        self.schema.write_text('{"type":"object"}')
        self.agents = self.root / "codex-agents"
        self.agents.mkdir()
        self.catalogues = self.root / "catalogues"
        for name in ("skills", "agents"):
            (self.catalogues / name).mkdir(parents=True)
        for module, name, value in (
                (headless, "SESSION", self.session),
                (headless, "PROMPT", self.prompt),
                (headless, "SCHEMA", self.schema),
                (headless, "CODEX_AGENTS", self.agents),
                (native, "HOME_BASE", str(self.home)),
                (native, "WORKSPACE", self.workspace),
                (native, "CLAUDE_CATALOGUES", self.catalogues)):
            change = patch.object(module, name, value)
            change.start()
            self.addCleanup(change.stop)
        for provider, filename in native.FILES.items():
            # Claude fixtures deliberately are not JSON: this path must never
            # inspect or parse native subscription credentials.
            contents = b'{"synthetic":true}' if provider == "codex" else b"fake-native-cache"
            path = self.cache / filename
            path.write_bytes(contents)
            path.chmod(0o600)

    def run_client(self, provider, resume="", readonly=False, effect=None):
        invocations = []

        def launch(command, env, cwd, stdin):
            self.assertEqual(cwd, self.workspace)
            self.assertEqual(stdin.read(), self.prompt.read_bytes())
            self.assertNotIn(self.prompt.read_text(), command)
            self.assertNotIn("HOST_TEST_SECRET", env)
            self.assertTrue(env["PATH"].startswith("/opt/dotnet:"))
            self.assertNotIn("CLAUDE_CODE_SAFE_MODE", env)
            self.assertNotIn("CLAUDE_CODE_RESTRICTED", env)
            config = Path(env.get("CODEX_HOME", env.get("CLAUDE_CONFIG_DIR")))
            path = config / native.FILES[provider]
            self.assertTrue(path.is_symlink())
            self.assertEqual(path.readlink(), self.cache / native.FILES[provider])
            (config / "native-transcript.jsonl").write_text('{"session_id":"' + ID + '"}\n')
            invocations.append((command, env, config))
            if effect:
                effect(path)
            class Child:
                def wait(self):
                    return 0
            return Child()

        with patch.dict(os.environ, {"HOST_TEST_SECRET": "fake-host-secret"}), \
                patch.object(subprocess, "run", return_value=subprocess.CompletedProcess([], 0)), \
                patch.object(subprocess, "Popen", side_effect=launch):
            code = headless.headless(provider, "example-model", "high", resume,
                                     readonly, self.cache)
        self.assertEqual(code, 0)
        self.assertEqual(len(invocations), 1)
        command, env, config = invocations[0]
        self.assertFalse((config / native.FILES[provider]).exists())
        self.assertFalse((config / native.FILES[provider]).is_symlink())
        return command, env, config

    def test_codex_fresh_and_specific_resume_keep_native_session(self):
        def native_trust_record(path):
            # The unmodified native client records this on the first launch.
            target = path.parent / "config.toml"
            target.write_text(CODEX_WORKSPACE_TRUST)
            target.chmod(0o600)
        command, env, config = self.run_client("codex", effect=native_trust_record)
        self.assertEqual(command[:2], ["codex", "exec"])
        for argument in ("--json", "--output-schema", "--ignore-rules",
                         'approval_policy="never"', "features.hooks=false",
                         "features.plugins=false", "mcp_servers={}"):
            self.assertIn(argument, command)
        self.assertNotIn("--ignore-user-config", command)
        self.assertEqual(command[command.index("--model") + 1], "example-model")
        self.assertIn('model_reasoning_effort="high"', command)
        self.assertEqual((config / "agents").readlink(), self.agents)
        native_config = config / "config.toml"
        self.assertEqual(len(native_config.read_bytes()), 48)
        before = native_config.stat()
        command, _, next_config = self.run_client("codex", ID)
        self.assertEqual(config, next_config)
        self.assertEqual(command[:3], ["codex", "exec", "resume"])
        self.assertEqual(command[-2:], [ID, "-"])
        self.assertNotIn("--last", command)
        self.assertTrue((config / "native-transcript.jsonl").exists())
        self.assertEqual(native_config.read_text(), CODEX_WORKSPACE_TRUST)
        self.assertEqual(before.st_ino, native_config.stat().st_ino)

    def test_codex_workspace_trust_rejects_other_config_shapes_before_client(self):
        for contents in (
                "", "invalid TOML", b"\xff", "projects = []\n",
                '[projects."/another-workspace"]\ntrust_level = "trusted"\n',
                '[projects."/workspace"]\ntrust_level = "untrusted"\n',
                '[projects."/workspace"]\ntrust_level = true\n',
                '[projects."/workspace"]\ntrust_level = "trusted"\nmodel = "example-model"\n',
                CODEX_WORKSPACE_TRUST + '[projects."/another-workspace"]\ntrust_level = "trusted"\n',
                'model = "example-model"\n' + CODEX_WORKSPACE_TRUST,
                'model_provider = "example-provider"\n' + CODEX_WORKSPACE_TRUST,
                'cli_auth_credentials_store = "keyring"\n' + CODEX_WORKSPACE_TRUST,
                'auth = {type = "test"}\n' + CODEX_WORKSPACE_TRUST,
                'features = {hooks = true}\n' + CODEX_WORKSPACE_TRUST,
                'features = {plugins = true}\n' + CODEX_WORKSPACE_TRUST,
                CODEX_WORKSPACE_TRUST + '[mcp_servers.example]\ncommand = "fake-command"\n',
                CODEX_WORKSPACE_TRUST + '[model_providers.example]\nname = "Example"\n'):
            with self.subTest(contents=contents):
                config = self.session / "codex"
                config.mkdir(exist_ok=True, mode=0o700)
                path = config / "config.toml"
                path.write_bytes(contents if isinstance(contents, bytes) else contents.encode())
                path.chmod(0o600)
                with patch.object(subprocess, "Popen") as client, self.assertRaises(ValueError):
                    headless.headless("codex", "example-model", "high", ID, False, self.cache)
                client.assert_not_called()
                path.unlink()

    def test_codex_workspace_trust_rejects_unsafe_file_metadata_before_client(self):
        for cause in ("symlink", "hardlink", "public", "directory", "oversized", "foreign owner"):
            with self.subTest(cause=cause):
                config = self.session / "codex"
                config.mkdir(exist_ok=True, mode=0o700)
                path = config / "config.toml"
                target = self.root / "native-trust.toml"
                target.write_text(CODEX_WORKSPACE_TRUST)
                target.chmod(0o600)
                if cause == "symlink":
                    path.symlink_to(target)
                elif cause == "hardlink":
                    os.link(target, path)
                elif cause == "directory":
                    path.mkdir(mode=0o700)
                else:
                    path.write_text(CODEX_WORKSPACE_TRUST + ("#" * 4096 if cause == "oversized" else ""))
                    path.chmod(0o644 if cause == "public" else 0o600)
                with patch.object(subprocess, "Popen") as client:
                    if cause == "foreign owner":
                        # Disposable file owner remains unchanged; test metadata
                        # against a different expected uid without privileges.
                        with patch.object(os, "getuid", return_value=os.getuid() + 1), self.assertRaises(ValueError):
                            headless.validate_codex_workspace_trust(path)
                    else:
                        with self.assertRaises(ValueError):
                            headless.headless("codex", "example-model", "high", ID, False, self.cache)
                    client.assert_not_called()
                if path.is_dir():
                    path.rmdir()
                else:
                    path.unlink()
                self.assertEqual(target.read_text(), CODEX_WORKSPACE_TRUST)
                target.unlink()

    def test_codex_workspace_trust_exception_does_not_apply_to_claude(self):
        config = self.session / "claude"
        config.mkdir(mode=0o700)
        (config / "config.toml").write_text(CODEX_WORKSPACE_TRUST)
        with patch.object(subprocess, "Popen") as client, self.assertRaises(ValueError):
            headless.headless("claude", "example-model", "high", ID, False, self.cache)
        client.assert_not_called()

    def test_claude_streaming_uses_isolated_settings_and_native_subscription(self):
        command, _, config = self.run_client("claude", ID, readonly=True)
        for argument in ("-p", "stream-json", "--verbose", "--include-partial-messages",
                         "--forward-subagent-text", "--strict-mcp-config", "--json-schema"):
            self.assertIn(argument, command)
        self.assertEqual(command[command.index("--setting-sources") + 1], "user")
        settings = json.loads(command[command.index("--settings") + 1])
        self.assertTrue(settings["disableAllHooks"])
        self.assertEqual(settings["enabledPlugins"], {})
        self.assertEqual(command[-2:], ["--resume", ID])
        self.assertNotIn("--bare", command)
        self.assertEqual((config / "skills").readlink(), self.catalogues / "skills")
        self.assertEqual((config / "agents").readlink(), self.catalogues / "agents")
        self.assertEqual((self.cache / native.FILES["claude"]).read_bytes(), b"fake-native-cache")

    def test_native_atomic_auth_refresh_does_not_leave_credentials_in_session(self):
        for provider in native.FILES:
            def replace(path):
                path.unlink()
                contents = b'{"synthetic_refresh":true}' if provider == "codex" else b"fake-refreshed-native-cache"
                path.write_bytes(contents)
                path.chmod(0o600)
            self.run_client(provider, effect=replace)
            expected = b'{"synthetic_refresh":true}' if provider == "codex" else b"fake-refreshed-native-cache"
            self.assertEqual((self.cache / native.FILES[provider]).read_bytes(), expected)

    def test_cached_customizations_stop_before_native_client(self):
        for name in ("config.toml", "settings.json", "hooks.json", "plugins",
                     "rules", "commands", "output-styles", "workflows", "mcp.json",
                     "CLAUDE.local.md"):
            with self.subTest(name=name):
                config = self.session / "codex"
                config.mkdir(exist_ok=True)
                path = config / name
                path.write_text("untrusted customization")
                with patch.object(subprocess, "Popen") as client, self.assertRaises(ValueError):
                    headless.headless("codex", "example-model", "high", "", False, self.cache)
                client.assert_not_called()
                path.unlink()

    def test_claude_user_scope_rejects_customization_before_client(self):
        config = self.session / "claude"
        config.mkdir(mode=0o700)
        for name in ("settings.json", "hooks.json", "plugins", "rules",
                     "commands", "output-styles", "workflows", "mcp.json",
                     "CLAUDE.local.md", "agents", "skills"):
            with self.subTest(name=name):
                path = config / name
                path.write_text("untrusted customization")
                with patch.object(subprocess, "Popen") as client, self.assertRaises(ValueError):
                    headless.headless("claude", "example-model", "high", "", False, self.cache)
                client.assert_not_called()
                path.unlink()
                # Catalogue preparation may establish the other expected link
                # before rejecting this conflicting path.
                for catalogue in ("agents", "skills"):
                    linked = config / catalogue
                    if linked.is_symlink():
                        linked.unlink()
        for name in ("agents", "skills"):
            with self.subTest(link=name):
                path = config / name
                path.symlink_to(self.root, target_is_directory=True)
                with patch.object(subprocess, "Popen") as client, self.assertRaises(ValueError):
                    headless.headless("claude", "example-model", "high", "", False, self.cache)
                client.assert_not_called()
                path.unlink()
                for catalogue in ("agents", "skills"):
                    linked = config / catalogue
                    if linked.is_symlink():
                        linked.unlink()

    def test_missing_auth_does_not_start_client(self):
        for provider, filename in native.FILES.items():
            (self.cache / filename).unlink()
            with patch.object(subprocess, "Popen") as client, self.assertRaises(ValueError):
                headless.headless(provider, "example-model", "high", "", False, self.cache)
            client.assert_not_called()

    def test_exception_removes_session_auth_link(self):
        with patch.object(subprocess, "Popen", side_effect=OSError("synthetic failure")), \
                self.assertRaises(OSError):
            headless.headless("claude", "example-model", "high", "", False, self.cache)
        path = self.session / "claude" / native.FILES["claude"]
        self.assertFalse(path.exists())
        self.assertFalse(path.is_symlink())

    def test_process_forwards_stop_and_restores_signal_handlers(self):
        handlers = {}
        sent = []
        class Child:
            def wait(self):
                handlers[signal.SIGTERM](signal.SIGTERM, None)
                return -signal.SIGTERM
            def send_signal(self, number):
                sent.append(number)
        def set_handler(number, handler):
            handlers[number] = handler
        original = {number: signal.getsignal(number)
                    for number in (signal.SIGTERM, signal.SIGINT)}
        with patch.object(signal, "signal", side_effect=set_handler), \
                patch.object(subprocess, "Popen", return_value=Child()):
            code = headless.process(["fake"], {})
        self.assertEqual(code, 128 + signal.SIGTERM)
        self.assertEqual(sent, [signal.SIGTERM])
        self.assertEqual(handlers, original)


if __name__ == "__main__":
    unittest.main()
