"""Disposable adversarial probes for the input-capture spike."""
import os
from pathlib import Path
import tempfile
import unittest

import input_snapshot


class InputSnapshotChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "repo"
        (self.root / ".tickets/BATCH-001").mkdir(parents=True)
        (self.root / ".specifications").mkdir()
        self.ticket = self.root / ".tickets/BATCH-001/01-change.md"
        self.ticket.write_text("**Status:** ready\n**Blocked by:** None\n"
                               "[Contract](../../.specifications/contract.md)\n"
                               "[Guidance](../../CONTRIBUTING.md)\n")
        (self.root / ".specifications/contract.md").write_text("Synthetic contract.\n")
        self.outside = Path(self.temp.name) / "outside.md"
        self.outside.write_text("DISPOSABLE_OUTSIDE_MARKER\n")

    def capture(self):
        return input_snapshot.capture(self.root, [".tickets/BATCH-001/01-change.md"])

    def test_linked_ignored_inputs_captured_and_source_changes_do_not_change_snapshot(self):
        files, manifest, refs, digest = self.capture()
        self.assertEqual(set(files), {".tickets/BATCH-001/01-change.md", ".specifications/contract.md"})
        self.assertEqual(refs, ("CONTRIBUTING.md",))
        self.ticket.write_text("Changed after capture.\n")
        self.assertIn(b"**Status:** ready", files[".tickets/BATCH-001/01-change.md"])
        with self.assertRaises(TypeError):
            files["extra.md"] = b"Mutation"
        with self.assertRaises(TypeError):
            manifest[".tickets/BATCH-001/01-change.md"]["sha256"] = "Mutation"
        self.assertNotEqual(digest, self.capture()[-1])
        self.assertEqual(len(manifest), 2)

    def test_direct_and_linked_symlinks_are_rejected(self):
        contract = self.root / ".specifications/contract.md"
        contract.unlink()
        contract.symlink_to(self.outside)
        with self.assertRaises(OSError):
            self.capture()
        self.ticket.unlink()
        self.ticket.symlink_to(self.outside)
        with self.assertRaises(OSError):
            self.capture()

    def test_symlink_directory_is_rejected(self):
        contract = self.root / ".specifications/contract.md"
        contract.unlink()
        contract.parent.rmdir()
        (self.root / ".specifications").symlink_to(self.outside.parent, target_is_directory=True)
        with self.assertRaises(OSError):
            self.capture()

    def test_hardlinked_file_is_rejected(self):
        self.ticket.unlink()
        os.link(self.outside, self.ticket)
        with self.assertRaisesRegex(ValueError, "hard links"):
            self.capture()

    def test_invalid_seed_is_rejected_before_opening_registered_root(self):
        baseline_fd = os.open(os.devnull, os.O_RDONLY)
        os.close(baseline_fd)
        for _ in range(32):
            with self.assertRaises(ValueError):
                input_snapshot.capture(self.root, ["../../outside.md"])
        next_fd = os.open(os.devnull, os.O_RDONLY)
        try:
            self.assertEqual(next_fd, baseline_fd, "Invalid requests leaked file descriptors")
        finally:
            os.close(next_fd)

    def test_seed_list_is_bounded_before_capture(self):
        def oversized_request():
            for _ in range(input_snapshot.MAX_FILES + 1):
                yield ".tickets/BATCH-001/01-change.md"
        with self.assertRaisesRegex(ValueError, "seed input files"):
            input_snapshot.capture(self.root, oversized_request())

    def test_protected_directory_nested_inside_an_input_root_is_rejected(self):
        hidden = self.root / ".tickets/.secrets/marker.md"
        hidden.parent.mkdir()
        hidden.write_text("DISPOSABLE_MARKER_ONLY\n")
        with self.assertRaisesRegex(ValueError, "protected state"):
            input_snapshot.capture(self.root, [".tickets/.secrets/marker.md"])

    def test_absolute_escape_encoded_escape_and_secret_root_rejected(self):
        for target in (str(self.outside), "../../../outside.md", "../../../%6futside.md",
                       "../../.secrets/credential.md", "file:///outside.md"):
            with self.subTest(target=target):
                self.ticket.write_text(f"[Unsafe]({target})\n")
                with self.assertRaises(ValueError):
                    self.capture()

    def test_fifo_rejected_without_blocking(self):
        self.ticket.unlink()
        os.mkfifo(self.ticket)
        with self.assertRaisesRegex(ValueError, "regular file"):
            self.capture()

    def test_oversized_and_non_utf8_inputs_rejected(self):
        self.ticket.write_bytes(b"x" * (input_snapshot.MAX_FILE_BYTES + 1))
        with self.assertRaisesRegex(ValueError, "size limit"):
            self.capture()
        self.ticket.write_bytes(b"\xff")
        with self.assertRaises(UnicodeDecodeError):
            self.capture()

    def test_status_and_dependency_fields_preserved_for_completed_and_superseded_tickets(self):
        self.ticket.write_text("**Status:** implemented\n**Blocked by:** None\n")
        second = self.ticket.parent / "02-follow-up.md"
        second.write_text("**Status:** superseded\n**Blocked by:** [First](01-change.md)\n")
        files, _, _, _ = input_snapshot.capture(self.root, [
            ".tickets/BATCH-001/01-change.md", ".tickets/BATCH-001/02-follow-up.md"])
        self.assertEqual([t["status"] for t in input_snapshot.ticket_metadata(files)],
                         ["implemented", "superseded"])
        self.assertEqual(input_snapshot.ticket_metadata(files)[1]["dependencies"], ["01-change.md"])


if __name__ == "__main__":
    unittest.main()
