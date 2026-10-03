"""Offline credential boundary tests; fixtures are disposable fake credentials."""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location(
    "provider_auth", Path(__file__).resolve().parents[1] / "internal/providerauth/container.py")
auth = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(auth)


class ProviderAuthTests(unittest.TestCase):
    def setUp(self):
        previous = os.umask(0o077)
        self.addCleanup(os.umask, previous)
        home = tempfile.TemporaryDirectory()
        self.addCleanup(home.cleanup)
        home_patch = patch.object(auth, "HOME_BASE", home.name)
        home_patch.start()
        self.addCleanup(home_patch.stop)

    def test_rejects_symlink_hardlink_permissions_and_invalid_json(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            target = root / "target"
            target.write_text('{"fake": "disposable"}')
            target.chmod(0o600)
            link = root / "link"
            link.symlink_to(target)
            with self.assertRaises(OSError):
                auth.credential(link, os.getuid())
            link.unlink()
            os.link(target, link)
            with self.assertRaises(ValueError):
                auth.credential(target, os.getuid())
            link.unlink()
            target.chmod(0o644)
            with self.assertRaises(ValueError):
                auth.credential(target, os.getuid())
            target.chmod(0o600)
            target.write_text("not JSON")
            with self.assertRaises(ValueError):
                auth.credential(target, os.getuid())
            target.write_text("x" * (auth.LIMIT + 1))
            with self.assertRaises(ValueError):
                auth.credential(target, os.getuid())

    def test_status_excludes_provider_output_and_rejects_api_keys(self):
        def result(code, stdout="", stderr=""):
            return subprocess.CompletedProcess([], code, stdout, stderr)
        self.assertEqual(auth.classify(result(0, stderr="Logged in using ChatGPT\n")), "stored")
        self.assertEqual(auth.classify(result(1, stderr="Not logged in\n")), "missing")
        self.assertEqual(auth.classify(result(0, stderr="Logged in using an API key - fake")), "invalid")

    def test_login_failure_preserves_existing_credentials_and_isolates_environment(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            root.chmod(0o700)
            path = root / "auth.json"
            original = b'{"fake": "previous"}'
            path.write_bytes(original)
            path.chmod(0o600)
            def failed_login(command, **kwargs):
                self.assertEqual(command[-2:], ["login", "--device-auth"])
                self.assertNotIn("GITHUB_TOKEN", kwargs["env"])
                self.assertNotIn("OPENAI_API_KEY", kwargs["env"])
                self.assertNotEqual(kwargs["cwd"], temporary)
                (Path(kwargs["env"]["CODEX_HOME"]) / "auth.json").write_bytes(b'{"fake":"failed"}')
                return subprocess.CompletedProcess(command, 1)
            with patch.dict(os.environ, {"GITHUB_TOKEN": "fake", "OPENAI_API_KEY": "fake"}), patch.object(auth.subprocess, "run", side_effect=failed_login):
                self.assertEqual(auth.run("codex", "login", root), 1)
            self.assertEqual(path.read_bytes(), original)
            self.assertEqual(list(root.iterdir()), [path])

    def test_login_persists_only_credential_and_fresh_status_is_private(self):
        for provider, filename in (("codex", auth.FILES["codex"]),):
            with self.subTest(provider=provider), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                root.chmod(0o700)
                (root / filename).write_text("truncated {")
                (root / filename).chmod(0o600)
                homes = []
                fake = b'{"fake":"new-disposable-token"}'
                def provider_call(command, **kwargs):
                    home = kwargs["cwd"]
                    homes.append(home)
                    config = Path(kwargs["env"]["CODEX_HOME"])
                    if "status" not in command:
                        (config / filename).write_bytes(fake)
                        (config / "settings.json").write_text('{"fake":"untrusted-config"}')
                        return subprocess.CompletedProcess(command, 0)
                    self.assertEqual((config / filename).read_bytes(), fake)
                    return subprocess.CompletedProcess(command, 0, "", "Logged in using ChatGPT\n")
                with patch.object(auth.subprocess, "run", side_effect=provider_call):
                    self.assertEqual(auth.run(provider, "login", root), 0)
                    output = io.StringIO()
                    with contextlib.redirect_stdout(output):
                        self.assertEqual(auth.run(provider, "status", root), 0)
                self.assertEqual(json.loads(output.getvalue()), {"state": "stored"})
                self.assertEqual(list(root.iterdir()), [root / filename])
                self.assertEqual((root / filename).stat().st_mode & 0o777, 0o600)
                self.assertNotEqual(homes[0], homes[-1])
                self.assertTrue(all(not Path(home).exists() for home in homes))

    def test_exception_details_are_not_printed(self):
        with patch.object(auth.sys, "argv", ["helper", "codex", "status"]), patch.object(auth, "run", side_effect=ValueError("fake-secret-detail")):
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                self.assertEqual(auth.main(), 0)
        self.assertEqual(json.loads(output.getvalue()), {"state": "invalid"})

    def test_relogin_can_replace_corrupt_contents_without_accepting_links(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            path = root / "auth.json"
            path.write_text("truncated {")
            path.chmod(0o600)
            self.assertIsNone(auth.credential(path, os.getuid(), allow_invalid=True))
            auth.persist(root, "auth.json", b'{"fake":"repaired"}')
            self.assertEqual(auth.credential(path, os.getuid()), b'{"fake":"repaired"}')
            path.unlink()
            path.symlink_to(root / "another-file")
            with self.assertRaises(OSError):
                auth.persist(root, "auth.json", b'{"fake":"replacement"}')


class ClaudeAuthTests(unittest.TestCase):
    def setUp(self):
        previous = os.umask(0o077)
        self.addCleanup(os.umask, previous)
        home = tempfile.TemporaryDirectory()
        self.addCleanup(home.cleanup)
        home_patch = patch.object(auth, "HOME_BASE", home.name)
        home_patch.start()
        self.addCleanup(home_patch.stop)
        storage = tempfile.TemporaryDirectory()
        self.addCleanup(storage.cleanup)
        self.root = Path(storage.name)
        self.root.chmod(0o700)
        self.path = self.root / ".credentials.json"

    def result(self, code=0, **fields):
        record = {"loggedIn": True, "authMethod": "claude.ai",
                  "apiProvider": "firstParty", "email": "example@example.invalid",
                  "accountUuid": "fake-disposable-account",
                  "configDirectory": "fake-private-path"}
        record.update(fields)
        return subprocess.CompletedProcess([], code, json.dumps(record), "fake-private-error")

    def test_native_volume_cache_is_not_read_copied_or_parsed_by_helper(self):
        # SDLC treats the contents as opaque; only the mocked official client
        # decides whether this intentionally non-JSON cache represents a login.
        self.path.write_text("disposable opaque native cache")
        self.path.chmod(0o600)
        homes = []

        def provider_call(command, **kwargs):
            env = kwargs["env"]
            homes.append(kwargs["cwd"])
            self.assertEqual(env["CLAUDE_CONFIG_DIR"], str(self.root))
            self.assertNotEqual(env["HOME"], str(self.root))
            self.assertEqual(kwargs["cwd"], env["HOME"])
            self.assertEqual(list(Path(kwargs["cwd"]).iterdir()), [])
            self.assertEqual(env["CLAUDE_CODE_SAFE_MODE"], "1")
            self.assertEqual(env["CLAUDE_CODE_RESTRICTED"], "1")
            self.assertEqual(env["DISABLE_AUTOUPDATER"], "1")
            self.assertEqual(env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"], "1")
            self.assertEqual(set(env), {"PATH", "HOME", "LANG", "TERM",
                                       "CLAUDE_CONFIG_DIR", "CLAUDE_CODE_SAFE_MODE",
                                       "CLAUDE_CODE_RESTRICTED",
                                       "DISABLE_AUTOUPDATER",
                                       "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"})
            if command == ["claude", "auth", "login"]:
                self.assertNotIn("capture_output", kwargs)
                return subprocess.CompletedProcess(command, 0)
            self.assertEqual(command, ["claude", "auth", "status", "--json"])
            self.assertTrue(kwargs["capture_output"])
            self.assertEqual(kwargs["timeout"], 30)
            return self.result()

        with patch.dict(os.environ, {"ANTHROPIC_API_KEY": "fake-disposable-key",
                                     "CLAUDE_CODE_OAUTH_TOKEN": "fake-disposable-token",
                                     "CLAUDE_CODE_USE_BEDROCK": "1",
                                     "GITHUB_TOKEN": "fake-disposable-token"}), \
                patch.object(auth.subprocess, "run", side_effect=provider_call), \
                patch.object(auth, "credential", side_effect=AssertionError("cache read")), \
                patch.object(auth, "persist", side_effect=AssertionError("cache copy")), \
                patch.object(Path, "open", side_effect=AssertionError("file contents opened")):
            self.assertEqual(auth.run("claude", "login", self.root), 0)
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                self.assertEqual(auth.run("claude", "status", self.root), 0)
        self.assertEqual(json.loads(output.getvalue()), {"state": "stored"})
        self.assertEqual(self.path.read_text(), "disposable opaque native cache")
        self.assertNotEqual(homes[0], homes[-1])
        self.assertTrue(all(not Path(home).exists() for home in homes))

    def test_status_requires_native_subscription_fields_and_scrubs_other_modes(self):
        self.assertEqual(auth.classify_claude(self.result()), "stored")
        self.assertEqual(auth.classify_claude(self.result(
            1, loggedIn=False, authMethod="none")), "missing")
        for code, fields in (
                (1, {}), (0, {"loggedIn": "true"}),
                (0, {"authMethod": "oauth_token"}),
                (0, {"authMethod": "api_key"}),
                (0, {"authMethod": "api_key_helper"}),
                (0, {"authMethod": "third_party"}),
                (0, {"apiProvider": "bedrock"}),
                (0, {"apiProvider": None}),
                (0, {"loggedIn": False, "authMethod": "none"})):
            with self.subTest(code=code, fields=fields):
                self.assertEqual(auth.classify_claude(self.result(code, **fields)), "invalid")
        for output in ("not JSON", "[]", "null", "{}", "x" * (auth.LIMIT + 1)):
            with self.subTest(output=output[:20]):
                result = subprocess.CompletedProcess([], 0, output, "fake-private-error")
                self.assertEqual(auth.classify_claude(result), "invalid")

    def test_unsafe_native_cache_metadata_is_rejected_before_client_start(self):
        target = self.root / "target"
        target.write_text("disposable fake cache")
        target.chmod(0o600)
        with patch.object(auth.subprocess, "run") as client:
            self.path.symlink_to(target)
            with self.assertRaises(ValueError):
                auth.run("claude", "login", self.root)
            self.path.unlink()
            os.link(target, self.path)
            with self.assertRaises(ValueError):
                auth.run("claude", "status", self.root)
            self.path.unlink()
            self.path.write_text("disposable fake cache")
            self.path.chmod(0o644)
            with self.assertRaises(ValueError):
                auth.run("claude", "login", self.root)
            self.path.chmod(0o600)
            with self.assertRaises(ValueError):
                auth.claude_credential_metadata(self.path, os.getuid() + 1)
            self.path.unlink()
            self.path.mkdir(mode=0o600)
            with self.assertRaises(ValueError):
                auth.run("claude", "status", self.root)
            client.assert_not_called()

    def test_login_uses_native_cache_writes_and_keeps_native_configuration(self):
        def provider_call(command, **kwargs):
            if command == ["claude", "auth", "login"]:
                self.path.write_text("disposable fake native cache")
                self.path.chmod(0o600)
                (self.root / ".claude.json").write_text('{"fake": "native-account-state"}')
                return subprocess.CompletedProcess(command, 0)
            return self.result()
        with patch.object(auth.subprocess, "run", side_effect=provider_call), \
                patch.object(auth, "credential", side_effect=AssertionError("cache read")), \
                patch.object(auth, "persist", side_effect=AssertionError("cache copy")):
            self.assertEqual(auth.run("claude", "login", self.root), 0)
        self.assertEqual(sorted(path.name for path in self.root.iterdir()),
                         [".claude.json", ".credentials.json"])

    def test_failed_login_does_not_restore_or_manipulate_native_cache(self):
        self.path.write_text("disposable previous cache")
        self.path.chmod(0o600)

        def failed_login(command, **kwargs):
            self.assertEqual(command, ["claude", "auth", "login"])
            self.path.write_text("disposable client-owned changed cache")
            return subprocess.CompletedProcess(command, 1)

        with patch.object(auth.subprocess, "run", side_effect=failed_login) as client, \
                patch.object(auth, "persist", side_effect=AssertionError("cache restore")):
            self.assertEqual(auth.run("claude", "login", self.root), 1)
        self.assertEqual(client.call_count, 1)
        self.assertEqual(self.path.read_text(), "disposable client-owned changed cache")

    def test_native_client_decides_malformed_cache_status_without_helper_recovery(self):
        self.path.write_text("truncated {")
        self.path.chmod(0o600)
        result = subprocess.CompletedProcess([], 1, "not JSON", "fake-private-error")
        with patch.object(auth.subprocess, "run", return_value=result), \
                patch.object(auth, "credential", side_effect=AssertionError("cache parse")), \
                patch.object(auth, "persist", side_effect=AssertionError("cache recovery")):
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                self.assertEqual(auth.run("claude", "status", self.root), 0)
        self.assertEqual(json.loads(output.getvalue()), {"state": "invalid"})
        self.assertEqual(self.path.read_text(), "truncated {")

    def test_missing_cache_status_does_not_start_client(self):
        with patch.object(auth.subprocess, "run") as client:
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                self.assertEqual(auth.run("claude", "status", self.root), 0)
        self.assertEqual(json.loads(output.getvalue()), {"state": "missing"})
        client.assert_not_called()

    def test_client_created_unsafe_or_missing_cache_is_not_accepted(self):
        def unsafe_login(command, **kwargs):
            self.path.symlink_to(self.root / "missing")
            return subprocess.CompletedProcess(command, 0)
        with patch.object(auth.subprocess, "run", side_effect=unsafe_login) as client:
            with self.assertRaises(ValueError):
                auth.run("claude", "login", self.root)
            self.assertEqual(client.call_count, 1)
        self.path.unlink()
        with patch.object(auth.subprocess, "run", return_value=self.result()) as client:
            self.assertEqual(auth.run("claude", "login", self.root), 1)
            self.assertEqual(client.call_count, 1)


if __name__ == "__main__":
    unittest.main()
