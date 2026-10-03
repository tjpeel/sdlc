import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("decision_install", Path(__file__).with_name("install.py"))
install = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(install)


class InstallationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name).resolve()
        self.prefix = self.root / "package"

    def tearDown(self):
        self.temporary.cleanup()

    def test_standalone_cli_contains_only_allowlisted_sources(self):
        entry = install.install(self.prefix)
        result = subprocess.run([sys.executable, "-B", str(entry), "--help"], cwd=self.root,
                                env={"PATH": os.defpath}, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("simulation", result.stdout.lower())
        files = {str(path.relative_to(self.prefix)) for path in self.prefix.rglob("*") if path.is_file()}
        self.assertEqual(files, {"bin/sdlc", "package.json", *[
            "share/sdlc-decision-spike/" + path for path in install.FILES]})
        manifest = json.loads((self.prefix / "package.json").read_text())
        self.assertTrue(manifest["simulation"])
        self.assertEqual(set(manifest["files_sha256"]), set(install.FILES))
        for path in self.prefix.rglob("*"):
            self.assertFalse(path.stat().st_mode & 0o077)

    def test_existing_prefix_is_not_replaced(self):
        self.prefix.mkdir()
        marker = self.prefix / "retain"
        marker.write_text("retain")
        with self.assertRaises(FileExistsError):
            install.install(self.prefix)
        self.assertEqual(marker.read_text(), "retain")

    def test_symlink_parent_and_source_checkout_refused(self):
        link = self.root / "linked"
        link.symlink_to(self.root, target_is_directory=True)
        with self.assertRaises(ValueError):
            install.install(link / "package")
        with self.assertRaises(ValueError):
            install.install(install.SOURCE_ROOT / "unwanted-installation")

    def test_other_repository_prefix_refused(self):
        (self.root / ".git").mkdir()
        with self.assertRaises(ValueError):
            install.install(self.prefix)
        self.assertFalse(self.prefix.exists())

    def test_nonregular_source_does_not_create_prefix(self):
        original = install.FILES
        install.FILES = ("runtime/bin",)
        try:
            with self.assertRaises(ValueError):
                install.install(self.prefix)
            self.assertFalse(self.prefix.exists())
        finally:
            install.FILES = original


if __name__ == "__main__":
    unittest.main()
