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
            self.assertIn(path, list(root.iterdir()))

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
                self.assertEqual({p.name for p in root.iterdir()}, {filename, ".native-auth.lock", ".native-auth-epoch"})
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
            with self.assertRaises((OSError, ValueError)):
                auth.persist(root, "auth.json", b'{"fake":"replacement"}')


    def test_overlapping_cache_snapshots_preserve_updates_and_conflicting_copy(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            root.chmod(0o700)
            target = root / "auth.json"
            target.write_bytes(b'{"fake":"initial"}')
            target.chmod(0o600)
            configs = [root / name for name in ("one", "two", "unchanged")]
            for config in configs:
                config.mkdir(mode=0o700)
                auth.snapshot_auth(config, "codex", root)
            (configs[0] / "auth.json").write_bytes(b'{"fake":"first-refresh"}')
            auth.finish_snapshot(configs[0], "codex", root)
            auth.finish_snapshot(configs[2], "codex", root)
            self.assertEqual(target.read_bytes(), b'{"fake":"first-refresh"}')
            (configs[1] / "auth.json").write_bytes(b'{"fake":"conflicting-refresh"}')
            with self.assertRaises(ValueError):
                auth.finish_snapshot(configs[1], "codex", root)
            self.assertEqual(target.read_bytes(), b'{"fake":"first-refresh"}')
            self.assertEqual((configs[1] / "auth.json").read_bytes(), b'{"fake":"conflicting-refresh"}')
            self.assertTrue((configs[1] / auth.BASELINE).exists())

    def test_snapshot_copy_failure_and_cleanup_interruption_never_logout(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            root.chmod(0o700)
            target = root / "auth.json"
            target.write_bytes(b'{"fake":"initial"}')
            target.chmod(0o600)
            config = root / "session"
            config.mkdir(mode=0o700)
            original = os.replace
            def fail_copy(source, destination):
                if Path(destination) == config / "auth.json":
                    raise OSError("offline copy failure")
                return original(source, destination)
            with patch.object(os, "replace", side_effect=fail_copy), self.assertRaises(OSError):
                auth.snapshot_auth(config, "codex", root)
            self.assertFalse((config / auth.BASELINE).exists())
            auth.snapshot_auth(config, "codex", root)
            original_unlink = Path.unlink
            def fail_cleanup(path, *args, **kwargs):
                if path == config / "auth.json":
                    raise OSError("offline cleanup interruption")
                return original_unlink(path, *args, **kwargs)
            with patch.object(Path, "unlink", fail_cleanup), self.assertRaises(OSError):
                auth.finish_snapshot(config, "codex", root)
            with self.assertRaises(ValueError):
                auth.snapshot_auth(config, "codex", root)
            self.assertEqual(target.read_bytes(), b'{"fake":"initial"}')

    def test_login_supersedes_same_epoch_refresh_but_newer_login_wins(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            root.chmod(0o700)
            target = root / "auth.json"
            target.write_bytes(b'{"fake":"initial"}')
            target.chmod(0o600)
            login, worker, newer = [root / name for name in ("login", "worker", "newer")]
            for config in (login, worker, newer):
                config.mkdir(mode=0o700)
            auth.snapshot_auth(login, "codex", root, login=True)
            auth.snapshot_auth(worker, "codex", root)
            (worker / "auth.json").write_bytes(b'{"fake":"old-account-refresh"}')
            auth.finish_snapshot(worker, "codex", root)
            (login / "auth.json").write_bytes(b'{"fake":"explicit-login"}')
            auth.finish_snapshot(login, "codex", root)
            self.assertEqual(target.read_bytes(), b'{"fake":"explicit-login"}')
            auth.snapshot_auth(login, "codex", root, login=True)
            auth.snapshot_auth(newer, "codex", root, login=True)
            (login / "auth.json").write_bytes(b'{"fake":"superseded-login"}')
            with self.assertRaises(ValueError):
                auth.finish_snapshot(login, "codex", root)
            self.assertEqual(target.read_bytes(), b'{"fake":"explicit-login"}')


    def test_both_native_refreshes_retain_private_copy_on_transfer_failure(self):
        for provider, filename in auth.FILES.items():
            with self.subTest(provider=provider), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                root.chmod(0o700)
                target = root / filename
                target.write_bytes(b"opaque-original-cache")
                target.chmod(0o600)
                config = root / "session"
                config.mkdir(mode=0o700)
                auth.snapshot_auth(config, provider, root)
                candidate = config / filename
                candidate.write_bytes(b"opaque-refreshed-cache")
                original = os.replace
                def fail_transfer(source, destination):
                    if Path(destination) == target:
                        raise OSError("offline transfer failure")
                    return original(source, destination)
                with patch.object(os, "replace", side_effect=fail_transfer), self.assertRaises(OSError):
                    auth.finish_snapshot(config, provider, root)
                self.assertEqual(target.read_bytes(), b"opaque-original-cache")
                self.assertEqual(candidate.read_bytes(), b"opaque-refreshed-cache")
                self.assertTrue((config / auth.BASELINE).exists())
                auth.finish_snapshot(config, provider, root)
                self.assertEqual(target.read_bytes(), b"opaque-refreshed-cache")

    def test_malformed_baseline_cannot_grant_login_overwrite(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            root.chmod(0o700)
            target = root / "auth.json"
            target.write_bytes(b"original")
            target.chmod(0o600)
            config = root / "session"
            config.mkdir(mode=0o700)
            auth.snapshot_auth(config, "codex", root)
            baseline = json.loads((config / auth.BASELINE).read_bytes())
            baseline["login"] = "false"
            (config / auth.BASELINE).write_text(json.dumps(baseline))
            (config / "auth.json").write_bytes(b"refreshed")
            target.write_bytes(b"newer-login")
            with self.assertRaises(ValueError):
                auth.finish_snapshot(config, "codex", root)
            self.assertEqual(target.read_bytes(), b"newer-login")


    def test_explicit_login_can_replace_empty_and_oversized_private_cache(self):
        for contents in (b"", b"x" * (auth.LIMIT + 1)):
            with self.subTest(size=len(contents)), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                root.chmod(0o700)
                target = root / "auth.json"
                target.write_bytes(contents)
                target.chmod(0o600)
                def client(command, **kwargs):
                    if "status" not in command:
                        (Path(kwargs["env"]["CODEX_HOME"]) / "auth.json").write_bytes(b'{"fake":"relogin"}')
                        return subprocess.CompletedProcess(command, 0)
                    return subprocess.CompletedProcess(command, 0, "", "Logged in using ChatGPT")
                with patch.object(auth.subprocess, "run", side_effect=client):
                    self.assertEqual(auth.run("codex", "login", root), 0)
                self.assertEqual(target.read_bytes(), b'{"fake":"relogin"}')


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

    def test_native_login_and_status_use_private_opaque_snapshot(self):
        self.path.write_bytes(b"opaque-native-cache")
        self.path.chmod(0o600)
        def client(command, **kwargs):
            config = Path(kwargs["env"]["CLAUDE_CONFIG_DIR"])
            self.assertNotEqual(config, self.root)
            self.assertFalse((config / auth.FILES["claude"]).is_symlink())
            self.assertEqual((config / auth.FILES["claude"]).read_bytes(), b"opaque-native-cache")
            return subprocess.CompletedProcess(command, 0) if "login" in command else self.result()
        with patch.object(auth.subprocess, "run", side_effect=client):
            self.assertEqual(auth.run("claude", "login", self.root), 0)
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                self.assertEqual(auth.run("claude", "status", self.root), 0)
            self.assertEqual(json.loads(output.getvalue()), {"state": "stored"})
        self.assertEqual(self.path.read_bytes(), b"opaque-native-cache")

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

    def test_login_persists_only_private_native_cache(self):
        def client(command, **kwargs):
            config = Path(kwargs["env"]["CLAUDE_CONFIG_DIR"])
            if "login" in command:
                (config / auth.FILES["claude"]).write_bytes(b"new-opaque-cache")
                (config / "settings.json").write_text("{}")
                return subprocess.CompletedProcess(command, 0)
            return self.result()
        with patch.object(auth.subprocess, "run", side_effect=client):
            self.assertEqual(auth.run("claude", "login", self.root), 0)
        self.assertEqual(self.path.read_bytes(), b"new-opaque-cache")
        self.assertFalse((self.root / "settings.json").exists())

    def test_failed_login_preserves_canonical_and_invalidates_old_refresh(self):
        self.path.write_bytes(b"old-opaque-cache")
        self.path.chmod(0o600)
        config = self.root / "session"
        config.mkdir(mode=0o700)
        auth.snapshot_auth(config, "claude", self.root)
        def client(command, **kwargs):
            private = Path(kwargs["env"]["CLAUDE_CONFIG_DIR"]) / auth.FILES["claude"]
            private.write_bytes(b"failed-login-cache")
            return subprocess.CompletedProcess(command, 1)
        with patch.object(auth.subprocess, "run", side_effect=client):
            self.assertEqual(auth.run("claude", "login", self.root), 1)
        self.assertEqual(self.path.read_bytes(), b"old-opaque-cache")
        (config / auth.FILES["claude"]).write_bytes(b"old-session-refresh")
        with self.assertRaises(ValueError):
            auth.finish_snapshot(config, "claude", self.root)
        self.assertEqual((config / auth.FILES["claude"]).read_bytes(), b"old-session-refresh")

    def test_native_client_decides_malformed_cache_status_without_helper_recovery(self):
        self.path.write_text("truncated {")
        self.path.chmod(0o600)
        result = subprocess.CompletedProcess([], 1, "not JSON", "fake-private-error")
        with patch.object(auth.subprocess, "run", return_value=result), \
                patch.object(auth, "credential", side_effect=AssertionError("cache parse")), \
                patch.object(auth, "classify_claude", wraps=auth.classify_claude):
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

    def test_private_login_cache_must_be_safe_and_present(self):
        for unsafe in (True, False):
            def client(command, **kwargs):
                if "login" in command:
                    path = Path(kwargs["env"]["CLAUDE_CONFIG_DIR"]) / auth.FILES["claude"]
                    path.unlink(missing_ok=True)
                    if unsafe:
                        path.symlink_to(self.root / "absent")
                    return subprocess.CompletedProcess(command, 0)
                return self.result()
            with patch.object(auth.subprocess, "run", side_effect=client):
                if unsafe:
                    with self.assertRaises(ValueError):
                        auth.run("claude", "login", self.root)
                else:
                    self.assertEqual(auth.run("claude", "login", self.root), 1)


if __name__ == "__main__":
    unittest.main()
