"""Demonstrate a failed boundary using a disposable marker, never a credential."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


class HostIdentityProbe(unittest.TestCase):
    def test_ignore_rules_and_owner_permissions_do_not_separate_same_uid_processes(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            secret_dir = root / ".secrets"
            secret_dir.mkdir(mode=0o700)
            marker = secret_dir / "disposable-marker.txt"
            marker.write_text("DISPOSABLE_MARKER_ONLY\n")
            marker.chmod(0o600)
            (root / ".gitignore").write_text(".secrets/\n")
            env = {"PATH": os.defpath, "HOME": str(root), "GIT_CONFIG_NOSYSTEM": "1",
                   "GIT_CONFIG_GLOBAL": os.devnull}
            subprocess.run(["git", "init", "-q", str(root)], env=env, check=True)
            ignored = subprocess.run(["git", "-C", str(root), "check-ignore", str(marker)],
                                     env=env, check=True, capture_output=True, text=True)
            self.assertIn("disposable-marker.txt", ignored.stdout)
            other_process = subprocess.run([
                sys.executable, "-I", "-c",
                "import os, pathlib, sys; p=pathlib.Path(sys.argv[1]); "
                "assert p.read_text() == 'DISPOSABLE_MARKER_ONLY\\n'; "
                "p.write_text('REPLACED_BY_SAME_UID\\n'); print(os.getuid())",
                str(marker)], env=env, check=True, capture_output=True, text=True)
            self.assertEqual(int(other_process.stdout.strip()), os.getuid())
            self.assertEqual(marker.read_text(), "REPLACED_BY_SAME_UID\n")


if __name__ == "__main__":
    unittest.main()
