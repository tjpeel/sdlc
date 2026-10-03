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
        for provider, filename in auth.FILES.items():
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


if __name__ == "__main__":
    unittest.main()
