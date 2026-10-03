"""Offline TUI setup tests; native clients are mocked and caches are fake."""

import contextlib
import importlib.util
import io
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location(
    "provider_interactive", Path(__file__).resolve().parents[1]
    / "internal/providerauth/container.py")
auth = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(auth)


class InteractiveTests(unittest.TestCase):
    def setUp(self):
        previous = os.umask(0o077)
        self.addCleanup(os.umask, previous)
        storage = tempfile.TemporaryDirectory()
        self.addCleanup(storage.cleanup)
        self.root = Path(storage.name)
        self.cache = self.root / "cache"
        self.cache.mkdir(mode=0o700)
        self.home = self.root / "homes"
        self.home.mkdir(mode=0o700)
        self.workspace = self.root / "workspace"
        self.workspace.mkdir(mode=0o700)
        self.instructions = self.root / "instructions.md"
        self.instructions.write_text("Stop implementation for unanswered human questions.\n")
        self.settings = self.root / "session-settings.json"
        self.settings.write_text('{"skipDangerousModePermissionPrompt":true}\n')
        self.catalogues = self.root / "catalogues"
        for name in ("skills", "agents"):
            (self.catalogues / name).mkdir(parents=True, mode=0o700)
        for name, value in (("HOME_BASE", str(self.home)),
                            ("WORKSPACE", self.workspace),
                            ("SESSION_INSTRUCTIONS", self.instructions),
                            ("SESSION_SETTINGS", self.settings),
                            ("CLAUDE_CATALOGUES", self.catalogues)):
            item = patch.object(auth, name, value)
            item.start()
            self.addCleanup(item.stop)

    def codex_cache(self):
        path = self.cache / "auth.json"
        path.write_text('{"fake":"disposable-account"}')
        path.chmod(0o600)
        return path

    def claude_cache(self):
        path = self.cache / ".credentials.json"
        path.write_text("disposable opaque native account cache")
        path.chmod(0o600)
        os.link(self.instructions, self.cache / "CLAUDE.md")
        os.link(self.settings, self.cache / "settings.json")
        return path

    def installer(self, command, **kwargs):
        self.assertEqual(command[:7], ["bash", auth.CODEX_SKILL_INSTALLER,
                                      "--provider", "codex", "--prefix", "tjpeel", "--"])
        self.assertEqual(Path(command[7]), Path(kwargs["env"]["HOME"]) / ".agents/skills")
        self.assertEqual(kwargs["cwd"], kwargs["env"]["HOME"])
        self.assertTrue(kwargs["capture_output"])
        Path(command[7]).mkdir(parents=True, mode=0o700)
        return subprocess.CompletedProcess(command, 0)

    def test_codex_full_access_default_uses_shared_body_and_image_catalogue(self):
        path = self.codex_cache()
        homes = []

        def client(command, env):
            self.assertEqual(command, ["codex", "-c", 'cli_auth_credentials_store="file"',
                                       "--no-daemon", "--sandbox", "danger-full-access",
                                       "--ask-for-approval", "never"])
            home = Path(env["HOME"])
            homes.append(home)
            config = Path(env["CODEX_HOME"])
            self.assertEqual(config / "auth.json", home / ".codex/auth.json")
            self.assertEqual((config / "auth.json").readlink(), path)
            self.assertEqual((config / "AGENTS.md").readlink(), self.instructions)
            self.assertTrue((home / ".agents/skills").is_dir())
            self.assertEqual(env["PATH"], "/opt/dotnet:/usr/local/bin:/usr/bin:/bin")
            self.assertEqual(env["DOTNET_CLI_TELEMETRY_OPTOUT"], "1")
            self.assertNotIn("GITHUB_TOKEN", env)
            self.assertNotIn("OPENAI_API_KEY", env)
            self.assertNotIn("HTTP_PROXY", env)
            (config / "history.jsonl").write_text("disposable fake prompt")
            return 7

        with patch.dict(os.environ, {"GITHUB_TOKEN": "fake-disposable-token",
                                     "OPENAI_API_KEY": "fake-disposable-key",
                                     "HTTP_PROXY": "http://proxy.example.invalid"}), \
                patch.object(auth.subprocess, "run", side_effect=self.installer) as installer, \
                patch.object(auth, "interactive_process", side_effect=client) as native:
            self.assertEqual(auth.run("codex", "interactive", self.cache), 7)
        self.assertEqual(installer.call_count, 1)
        self.assertEqual(native.call_count, 1)
        self.assertTrue(all(not home.exists() for home in homes))
        self.assertEqual(list(self.cache.iterdir()), [path])

    def test_codex_native_refresh_writes_through_credential_link(self):
        path = self.codex_cache()

        def client(command, env):
            scratch = Path(env["CODEX_HOME"]) / "auth.json"
            scratch.write_bytes(b'{"fake":"disposable-native-refresh"}')
            self.assertEqual(path.read_bytes(), scratch.read_bytes())
            return 0

        with patch.object(auth.subprocess, "run", side_effect=self.installer), \
                patch.object(auth, "interactive_process", side_effect=client), \
                patch.object(auth, "persist", side_effect=AssertionError("copied linked cache")):
            self.assertEqual(auth.run("codex", "interactive", self.cache), 0)
        self.assertEqual(path.read_bytes(), b'{"fake":"disposable-native-refresh"}')

    def test_codex_safe_native_cache_replacement_persists_on_nonzero_exit(self):
        path = self.codex_cache()

        def client(command, env):
            scratch = Path(env["CODEX_HOME"]) / "auth.json"
            scratch.unlink()
            scratch.write_bytes(b'{"fake":"disposable-native-replacement"}')
            scratch.chmod(0o600)
            return 3

        with patch.object(auth.subprocess, "run", side_effect=self.installer), \
                patch.object(auth, "interactive_process", side_effect=client):
            self.assertEqual(auth.run("codex", "interactive", self.cache), 3)
        self.assertEqual(path.read_bytes(), b'{"fake":"disposable-native-replacement"}')
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_codex_native_logout_removes_validated_persistent_cache(self):
        path = self.codex_cache()

        def client(command, env):
            (Path(env["CODEX_HOME"]) / "auth.json").unlink()
            return 0

        with patch.object(auth.subprocess, "run", side_effect=self.installer), \
                patch.object(auth, "interactive_process", side_effect=client):
            self.assertEqual(auth.run("codex", "interactive", self.cache), 0)
        self.assertFalse(path.exists())

    def test_codex_rejects_unsafe_logout_target_without_deleting_it(self):
        path = self.codex_cache()

        def client(command, env):
            (Path(env["CODEX_HOME"]) / "auth.json").unlink()
            path.chmod(0o644)
            return 0

        with patch.object(auth.subprocess, "run", side_effect=self.installer), \
                patch.object(auth, "interactive_process", side_effect=client):
            with self.assertRaises(ValueError):
                auth.run("codex", "interactive", self.cache)
        self.assertTrue(path.exists())

    def test_codex_rejects_changed_link_and_permissive_replacement(self):
        for kind in ("link", "permissions"):
            with self.subTest(kind=kind):
                path = self.codex_cache()
                original = path.read_bytes()

                def client(command, env):
                    scratch = Path(env["CODEX_HOME"]) / "auth.json"
                    scratch.unlink()
                    if kind == "link":
                        scratch.symlink_to(self.root / "other-fake-cache")
                    else:
                        scratch.write_bytes(b'{"fake":"disposable-unsafe-replacement"}')
                        scratch.chmod(0o644)
                    return 0

                with patch.object(auth.subprocess, "run", side_effect=self.installer), \
                        patch.object(auth, "interactive_process", side_effect=client):
                    with self.assertRaises(ValueError):
                        auth.run("codex", "interactive", self.cache)
                self.assertEqual(path.read_bytes(), original)

    def test_missing_cache_or_instructions_stops_before_native_launch(self):
        with patch.object(auth.subprocess, "run") as installer, \
                patch.object(auth, "interactive_process") as native:
            for provider in ("codex", "claude"):
                with self.subTest(provider=provider):
                    with self.assertRaises(ValueError):
                        auth.run(provider, "interactive", self.cache)
            self.codex_cache()
            self.instructions.unlink()
            with self.assertRaises(FileNotFoundError):
                auth.run("codex", "interactive", self.cache)
            installer.assert_not_called()
            native.assert_not_called()

    def test_skill_installer_failure_stops_without_forwarding_output(self):
        path = self.codex_cache()
        result = subprocess.CompletedProcess([], 1, b"fake-private-output", b"fake-private-error")
        with patch.object(auth.subprocess, "run", return_value=result), \
                patch.object(auth, "interactive_process") as native:
            with self.assertRaisesRegex(ValueError, "could not prepare image skills"):
                auth.run("codex", "interactive", self.cache)
            native.assert_not_called()
        self.assertTrue(path.exists())

    def test_claude_full_access_default_uses_native_cache_and_shared_instructions(self):
        path = self.claude_cache()
        homes = []

        def client(command, env):
            self.assertEqual(command, ["claude", "--setting-sources", "user",
                                       "--permission-mode", "bypassPermissions",
                                       "--strict-mcp-config", "--mcp-config",
                                       '{"mcpServers":{}}'])
            homes.append(Path(env["HOME"]))
            self.assertEqual(env["CLAUDE_CONFIG_DIR"], str(self.cache))
            self.assertEqual(env["CLAUDE_CODE_SKIP_PROMPT_HISTORY"], "1")
            self.assertEqual(env["CLAUDE_CODE_DISABLE_AUTO_MEMORY"], "1")
            self.assertEqual(env["ENABLE_CLAUDEAI_MCP_SERVERS"], "false")
            self.assertEqual(env["CLAUDE_CODE_DEBUG_LOGS_DIR"], str(homes[-1] / "debug.log"))
            self.assertEqual(env["DISABLE_AUTOUPDATER"], "1")
            self.assertEqual(env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"], "1")
            for name in ("CLAUDE_CODE_SAFE_MODE", "CLAUDE_CODE_RESTRICTED",
                         "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN",
                         "CLAUDE_CODE_USE_BEDROCK", "GITHUB_TOKEN"):
                self.assertNotIn(name, env)
            self.assertTrue((self.cache / "CLAUDE.md").samefile(self.instructions))
            self.assertTrue((self.cache / "settings.json").samefile(self.settings))
            for name in ("skills", "agents"):
                self.assertEqual((self.cache / name).readlink(), self.catalogues / name)
            return 4

        with patch.dict(os.environ, {"ANTHROPIC_API_KEY": "fake-disposable-key",
                                     "CLAUDE_CODE_OAUTH_TOKEN": "fake-disposable-token",
                                     "CLAUDE_CODE_USE_BEDROCK": "1",
                                     "GITHUB_TOKEN": "fake-disposable-token"}), \
                patch.object(auth, "interactive_process", side_effect=client), \
                patch.object(auth, "credential", side_effect=AssertionError("cache read")), \
                patch.object(auth, "persist", side_effect=AssertionError("cache copy")), \
                patch.object(Path, "open", side_effect=AssertionError("opened native cache")):
            self.assertEqual(auth.run("claude", "interactive", self.cache), 4)
        self.assertEqual(path.read_text(), "disposable opaque native account cache")
        self.assertTrue(all(not home.exists() for home in homes))

    def test_claude_native_logout_is_preserved_and_unsafe_cache_change_is_rejected(self):
        path = self.claude_cache()

        def logout(command, env):
            path.unlink()
            return 0

        with patch.object(auth, "interactive_process", side_effect=logout):
            self.assertEqual(auth.run("claude", "interactive", self.cache), 0)
        self.assertFalse(path.exists())
        path.write_text("disposable fake native cache")
        path.chmod(0o600)

        def unsafe_change(command, env):
            path.chmod(0o644)
            return 0

        with patch.object(auth, "interactive_process", side_effect=unsafe_change):
            with self.assertRaises(ValueError):
                auth.run("claude", "interactive", self.cache)

    def test_claude_catalogue_links_reuse_only_curated_image_targets(self):
        self.claude_cache()
        auth.claude_catalogues(self.cache)
        auth.claude_catalogues(self.cache)
        for kind in ("directory", "symlink"):
            with self.subTest(kind=kind):
                path = self.cache / "skills"
                path.unlink()
                if kind == "directory":
                    path.mkdir()
                else:
                    path.symlink_to(self.root / "untrusted-catalogue")
                with self.assertRaisesRegex(ValueError, "conflicting catalogue path"):
                    auth.claude_catalogues(self.cache)
                if kind == "directory":
                    path.rmdir()
                else:
                    path.unlink()
                path.symlink_to(self.catalogues / "skills")

    def test_claude_cached_instructions_cannot_replace_shared_body(self):
        self.claude_cache()
        path = self.cache / "CLAUDE.md"
        path.unlink()
        path.write_text("disposable cached instructions")
        with patch.object(auth, "interactive_process") as native:
            with self.assertRaisesRegex(ValueError, "missing shared Claude instructions"):
                auth.run("claude", "interactive", self.cache)
            native.assert_not_called()

    def test_claude_cached_settings_cannot_replace_isolated_session_settings(self):
        self.claude_cache()
        path = self.cache / "settings.json"
        path.unlink()
        path.write_text('{"fake":"disposable-untrusted-settings"}')
        with patch.object(auth, "interactive_process") as native:
            with self.assertRaisesRegex(ValueError, "missing isolated session settings"):
                auth.run("claude", "interactive", self.cache)
            native.assert_not_called()

    def test_helper_errors_hide_account_details(self):
        output, error = io.StringIO(), io.StringIO()
        with patch.object(auth.sys, "argv", ["helper", "claude", "interactive"]), \
                patch.object(auth, "run", side_effect=ValueError("fake-private-account-detail")), \
                contextlib.redirect_stdout(output), contextlib.redirect_stderr(error):
            self.assertEqual(auth.main(), 1)
        self.assertEqual(output.getvalue(), "")
        self.assertEqual(error.getvalue(), "SDLC could not complete the interactive session safely.\n")

    def test_every_supported_provider_mode_reaches_native_client_without_fallback(self):
        self.codex_cache()
        self.claude_cache()
        for provider, flag, modes in (
                ("codex", "--ask-for-approval", ("never", "on-request")),
                ("claude", "--permission-mode", ("bypassPermissions", "default", "manual",
                                                  "acceptEdits", "plan", "auto", "dontAsk"))):
            for mode in modes:
                with self.subTest(provider=provider, mode=mode):
                    if provider == "claude":
                        self.settings.write_text(
                            '{"skipDangerousModePermissionPrompt":true}\n'
                            if mode == "bypassPermissions" else "{}\n")
                    def client(command, env):
                        self.assertEqual(command[command.index(flag) + 1], mode)
                        return 5

                    with patch.object(auth.subprocess, "run", side_effect=self.installer), \
                            patch.object(auth, "interactive_process", side_effect=client) as native:
                        self.assertEqual(auth.run(provider, "interactive", self.cache, mode=mode), 5)
                        native.assert_called_once()

    def test_invalid_modes_are_rejected_before_cache_access_or_native_launch(self):
        for provider in ("codex", "claude"):
            for mode in ("", "untrusted-mode", "never" if provider == "claude" else "manual"):
                with self.subTest(provider=provider, mode=mode), \
                        patch.object(Path, "lstat", side_effect=AssertionError("cache accessed")), \
                        patch.object(auth, "interactive_process") as native, \
                        patch.object(auth.subprocess, "run") as installer:
                    with self.assertRaisesRegex(ValueError, "unsupported interactive mode"):
                        auth.run(provider, "interactive", self.cache, mode=mode)
                    native.assert_not_called()
                    installer.assert_not_called()

    def test_auth_actions_reject_mode_arguments_before_cache_access(self):
        for provider in ("codex", "claude"):
            for action in ("init", "login", "status"):
                with self.subTest(provider=provider, action=action), \
                        patch.object(Path, "lstat", side_effect=AssertionError("cache accessed")), \
                        patch.object(auth.subprocess, "run") as native:
                    with self.assertRaisesRegex(ValueError, "interactive modes require"):
                        auth.run(provider, action, self.cache, mode="never")
                    native.assert_not_called()

    def test_main_accepts_interactive_mode_and_rejects_unexpected_arguments(self):
        for arguments in (("codex", "interactive"), ("codex", "interactive", "on-request"),
                          ("claude", "interactive", "manual")):
            with self.subTest(arguments=arguments), \
                    patch.object(auth.sys, "argv", ["helper", *arguments]), \
                    patch.object(auth, "run", return_value=9) as run:
                self.assertEqual(auth.main(), 9)
                run.assert_called_once_with(arguments[0], arguments[1],
                                            mode=arguments[2] if len(arguments) == 3 else None)
        for arguments in ((), ("codex",), ("codex", "interactive", "never", "extra"),
                          ("codex", "login", "never"), ("claude", "status", "manual"),
                          ("claude", "init", "bypassPermissions")):
            with self.subTest(arguments=arguments), \
                    patch.object(auth.sys, "argv", ["helper", *arguments]), \
                    patch.object(auth, "run") as run:
                self.assertEqual(auth.main(), 1)
                run.assert_not_called()


class InteractiveProcessTests(unittest.TestCase):
    def test_child_inherits_terminal_and_native_ctrl_c_while_term_is_forwarded(self):
        seen = {}
        original = {number: signal.getsignal(number)
                    for number in (signal.SIGTERM, signal.SIGINT)}

        class Child:
            def __init__(self, command, **kwargs):
                seen["command"] = command
                seen["options"] = kwargs
                seen["signals"] = []

            def send_signal(self, number):
                seen["signals"].append(number)

            def wait(self):
                signal.getsignal(signal.SIGINT)(signal.SIGINT, None)
                self.assert_term()
                return -signal.SIGTERM

            def assert_term(self):
                signal.getsignal(signal.SIGTERM)(signal.SIGTERM, None)

        with patch.object(auth.subprocess, "Popen", Child):
            self.assertEqual(auth.interactive_process(["native-cli"], {"fake": "environment"}), 143)
        self.assertEqual(seen["command"], ["native-cli"])
        self.assertEqual(seen["options"], {"env": {"fake": "environment"}, "cwd": auth.WORKSPACE})
        self.assertEqual(seen["signals"], [signal.SIGTERM])
        for number, handler in original.items():
            self.assertIs(signal.getsignal(number), handler)

    def test_signal_handlers_are_restored_when_native_start_fails(self):
        original = {number: signal.getsignal(number)
                    for number in (signal.SIGTERM, signal.SIGINT)}
        with patch.object(auth.subprocess, "Popen", side_effect=FileNotFoundError("fake-native-missing")):
            with self.assertRaises(FileNotFoundError):
                auth.interactive_process(["native-cli"], {})
        for number, handler in original.items():
            self.assertIs(signal.getsignal(number), handler)


if __name__ == "__main__":
    unittest.main()
