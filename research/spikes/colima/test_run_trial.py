#!/usr/bin/env python3
"""Check prepared-source admission and transfer without starting a VM."""

import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location("colima_run_trial", Path(__file__).with_name("run_trial.py"))
trial = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(trial)
TRIAL_ID = "host-control-012345abcdef"
PROTECTED_INPUT = "research/spikes/host-control/protected-input.txt"


class PreparedSourceTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="sdlc-colima-source-test-")
        self.addCleanup(self.directory.cleanup)
        self.repo = Path(self.directory.name).resolve() / (TRIAL_ID + "-copy")
        self.repo.mkdir()
        self.fixture = self.repo / "research/spikes/colima"
        self.files = ["research/spikes/colima/" + name for name in trial.FILES]
        self.files += ["runtime/bin/sdlc-job", "scripts/example.py", "tests/test_example.py", "research/container-spike/sdlc.py",
                       ".github/workflows/update-runtime-pins.yml",
                       "research/spikes/colima/full_trial.sh", "research/spikes/colima/full_fixture.py", PROTECTED_INPUT]
        for name in self.files:
            self.write(name, "Generated public source: " + name + "\n")
        self.write_manifest()
        patcher = mock.patch.object(trial, "FIXTURE", self.fixture)
        patcher.start()
        self.addCleanup(patcher.stop)

    def write(self, name, contents):
        path = self.repo / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(contents)
        return path

    def write_manifest(self, names=None):
        value = {"version": 1, "trial_id": TRIAL_ID, "files": []}
        for name in self.files if names is None else names:
            data = (self.repo / name).read_bytes()
            value["files"].append({"path": name, "size": len(data),
                                   "sha256": hashlib.sha256(data).hexdigest()})
        self.write(".sdlc-trial-sources.json", json.dumps(value))
        return value

    def test_generated_manifest_admits_expected_public_sources(self):
        expected = self.write_manifest()
        paths, metadata = trial.bundle_sources(self.repo)
        self.assertEqual(paths, [Path(name) for name in self.files])
        self.assertEqual(metadata, expected)

    def test_source_change_is_rejected_before_transfer(self):
        self.write("runtime/bin/sdlc-job", "Changed after admission.\n")
        with self.assertRaisesRegex(RuntimeError, "hash changed"):
            trial.fixture_archive(full_worker=True, host_control=True)

    def test_source_file_symlink_is_rejected_even_for_identical_bytes(self):
        source = self.repo / "scripts/example.py"
        substitute = self.write("outside-source.py", source.read_text())
        source.unlink()
        source.symlink_to(substitute)
        with self.assertRaisesRegex(RuntimeError, "without symlink ancestors"):
            trial.fixture_archive(full_worker=True, host_control=True)

    def test_symlink_parent_is_rejected_even_for_identical_bytes(self):
        source = self.repo / "scripts/example.py"
        substitute = self.write("replacement/example.py", source.read_text())
        source.unlink()
        source.parent.rmdir()
        source.parent.symlink_to(substitute.parent, target_is_directory=True)
        with self.assertRaisesRegex(RuntimeError, "without symlink ancestors"):
            trial.fixture_archive(full_worker=True, host_control=True)

    def test_unlisted_required_fixture_is_rejected(self):
        self.write_manifest([name for name in self.files if name != "research/spikes/colima/http/index.html"])
        with self.assertRaisesRegex(RuntimeError, "missing a required fixture"):
            trial.fixture_archive(full_worker=True, host_control=True)

    def test_host_control_ticket_must_be_in_admitted_manifest(self):
        self.write_manifest([name for name in self.files if name != PROTECTED_INPUT])
        with self.assertRaisesRegex(RuntimeError, "protected input|ticket|fixture"):
            trial.fixture_archive(full_worker=True, host_control=True)

    def test_transfer_includes_only_explicit_admitted_files(self):
        excluded = ["research/spikes/colima/http/unlisted.html", "research/spikes/colima/app/unlisted.cs",
                    "runtime/unlisted.py", "runtime/.codex/auth.json", ".git/config",
                    "profiles.local.json", ".secrets/generated-test-token"]
        for name in excluded:
            self.write(name, "Generated content excluded from transfer.\n")
        with mock.patch.object(trial.subprocess, "check_output", side_effect=AssertionError("Git must not be consulted")):
            payload = trial.fixture_archive(full_worker=True, host_control=True)
        with tarfile.open(fileobj=io.BytesIO(payload)) as archive:
            expected = set(trial.FILES)
            expected.update("full-source/" + name for name in self.files
                            if not name.startswith("research/spikes/colima/")
                            or name in ("research/spikes/colima/full_trial.sh", "research/spikes/colima/full_fixture.py"))
            self.assertEqual({member.name for member in archive.getmembers()}, expected)
            self.assertTrue(all(member.isfile() for member in archive.getmembers()))
            for member in archive.getmembers():
                self.assertNotIn(b"Generated content excluded from transfer.", archive.extractfile(member).read())


if __name__ == "__main__":
    unittest.main()
