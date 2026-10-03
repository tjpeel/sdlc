#!/usr/bin/env python3
"""Check public bundle construction without Docker, VMs or host credentials."""

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import stat
import tarfile
import tempfile
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location("host_control_prepare", Path(__file__).with_name("prepare.py"))
prepare = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(prepare)
TRIAL_ID = "host-control-012345abcdef"


class PrepareTests(unittest.TestCase):
    def setUp(self):
        self.folder = tempfile.TemporaryDirectory(prefix="sdlc-host-control-prepare-test-")
        self.addCleanup(self.folder.cleanup)
        self.base = Path(self.folder.name)
        self.repo = self.base / "checkout"
        self.repo.mkdir()
        self.tracked = ["runtime/bin/sdlc-job", "scripts/example.py", "tests/test_example.py",
                        *prepare.TRACKED_FILES]
        for name in [*self.tracked, *prepare.FIXTURE_FILES]:
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("Public test source: " + name + "\n")
        (self.repo / "runtime/bin/sdlc-job").chmod(0o751)
        self.output = self.base / "bundle"
        self.index = mock.patch.object(prepare, "tracked_sources", return_value=self.tracked)
        self.index.start()
        self.addCleanup(self.index.stop)

    def bundle(self, **kwargs):
        return prepare.prepare_bundle(kwargs.pop("output_dir", self.output),
                                      repo=self.repo, trial_id=TRIAL_ID, **kwargs)

    def test_bundle_hash_manifest_modes_and_synthetic_ticket(self):
        result = self.bundle()
        self.assertEqual(result["trial_id"], TRIAL_ID)
        self.assertEqual(stat.S_IMODE(self.output.stat().st_mode), 0o700)
        self.assertEqual({path.name for path in self.output.iterdir()},
                         {"source.tar", "source.tar.sha256", "original-ticket.md"})
        for path in self.output.iterdir():
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        self.assertEqual((self.output / "original-ticket.md").read_text(), prepare.TICKET)
        payload = (self.output / "source.tar").read_bytes()
        self.assertEqual((self.output / "source.tar.sha256").read_text(),
                         hashlib.sha256(payload).hexdigest() + "  source.tar\n")
        prefix = TRIAL_ID + "-copy/"
        with tarfile.open(self.output / "source.tar") as archive:
            members = archive.getmembers()
            self.assertEqual(members[0].name, prefix.rstrip("/"))
            self.assertTrue(members[0].isdir())
            self.assertEqual(members[0].mode, 0o700)
            for member in members:
                self.assertEqual((member.uid, member.gid, member.uname, member.gname, member.mtime),
                                 (0, 0, "", "", 0))
                self.assertEqual(member.pax_headers, {})
            manifest = json.load(archive.extractfile(prefix + prepare.MANIFEST))
            self.assertEqual((manifest["version"], manifest["trial_id"]), (1, TRIAL_ID))
            expected = sorted([*self.tracked, *prepare.FIXTURE_FILES, prepare.PROTECTED_INPUT])
            self.assertEqual([item["path"] for item in manifest["files"]], expected)
            self.assertEqual(result["files"], len(expected))
            self.assertEqual({member.name for member in members[1:]},
                             {prefix + path for path in [*expected, prepare.MANIFEST]})
            for item in manifest["files"]:
                content = archive.extractfile(prefix + item["path"]).read()
                self.assertEqual(item["size"], len(content))
                self.assertEqual(item["sha256"], hashlib.sha256(content).hexdigest())
            self.assertEqual(archive.getmember(prefix + "runtime/bin/sdlc-job").mode, 0o755)
            self.assertEqual(archive.getmember(prefix + "scripts/example.py").mode, 0o644)
            self.assertEqual(archive.getmember(prefix + prepare.PROTECTED_INPUT).mode, 0o600)
            self.assertEqual(archive.extractfile(prefix + prepare.PROTECTED_INPUT).read(),
                             prepare.TICKET.encode())

    def test_untracked_sources_and_git_state_are_omitted(self):
        for name in ("runtime/untracked.py", "spikes/colima/README.md", ".git/config",
                     "profiles.local.json", ".secrets/generated-test-token"):
            path = self.repo / name
            path.parent.mkdir(exist_ok=True)
            path.write_text("Disposable excluded test content.\n")
        self.bundle()
        with tarfile.open(self.output / "source.tar") as archive:
            names = {member.name for member in archive.getmembers()}
            for excluded in ("runtime/untracked.py", "spikes/colima/README.md", ".git/config",
                             "profiles.local.json", ".secrets/generated-test-token"):
                self.assertNotIn(TRIAL_ID + "-copy/" + excluded, names)

    def test_existing_directory_and_dangling_symlink_are_refused(self):
        self.output.mkdir()
        with self.assertRaisesRegex(prepare.PreparationError, "new output directory"):
            self.bundle()
        self.output.rmdir()
        self.output.symlink_to(self.base / "missing-target")
        with self.assertRaisesRegex(prepare.PreparationError, "new output directory"):
            self.bundle()
        self.assertFalse((self.base / "missing-target").exists())

    def test_output_inside_checkout_or_symlink_to_checkout_is_refused(self):
        with self.assertRaisesRegex(prepare.PreparationError, "outside the source checkout"):
            self.bundle(output_dir=self.repo / "new-bundle")
        link = self.base / "checkout-link"
        link.symlink_to(self.repo, target_is_directory=True)
        with self.assertRaisesRegex(prepare.PreparationError, "outside the source checkout"):
            self.bundle(output_dir=link / "new-bundle")
        self.assertFalse((self.repo / "new-bundle").exists())

    def test_unsafe_tracked_paths_are_refused_before_output_creation(self):
        for path in ("../private.txt", "/private.txt", "runtime/../private.txt",
                     "runtime/.secrets/token", "runtime/__pycache__/example.pyc",
                     "scripts/profiles.local.json", "runtime/.codex/auth.json",
                     "spikes/colima/README.md", "runtime\\example.py"):
            with self.subTest(path=path), mock.patch.object(prepare, "tracked_sources", return_value=[path]):
                with self.assertRaises(prepare.PreparationError):
                    self.bundle()
                self.assertFalse(self.output.exists())

    def test_source_file_and_directory_symlinks_are_refused(self):
        source = self.repo / "scripts/example.py"
        source.unlink()
        source.symlink_to(self.repo / "tests/test_example.py")
        with self.assertRaisesRegex(prepare.PreparationError, "regular public source"):
            self.bundle()
        source.unlink()
        source.parent.rmdir()
        source.parent.symlink_to(self.repo / "tests", target_is_directory=True)
        with self.assertRaisesRegex(prepare.PreparationError, "symlink parent"):
            self.bundle()
        self.assertFalse(self.output.exists())

    def test_source_directory_and_missing_fixture_are_refused(self):
        source = self.repo / "scripts/example.py"
        source.unlink()
        source.mkdir()
        with self.assertRaisesRegex(prepare.PreparationError, "regular public file"):
            self.bundle()
        source.rmdir()
        source.write_text("Public test source.\n")
        (self.repo / "spikes/host-control/probe.py").unlink()
        with self.assertRaisesRegex(prepare.PreparationError, "regular public source"):
            self.bundle()
        self.assertFalse(self.output.exists())

    def test_archive_is_deterministic_for_same_public_bytes_and_trial_id(self):
        first, _ = prepare.source_archive(self.repo, TRIAL_ID)
        for name in self.tracked:
            os.utime(self.repo / name, (10000, 10000))
        second, _ = prepare.source_archive(self.repo, TRIAL_ID)
        self.assertEqual(first, second)

    def test_bad_trial_id_is_refused(self):
        with self.assertRaisesRegex(prepare.PreparationError, "Trial identifier"):
            prepare.prepare_bundle(self.output, repo=self.repo, trial_id="../copy")
        self.assertFalse(self.output.exists())


if __name__ == "__main__":
    unittest.main()
