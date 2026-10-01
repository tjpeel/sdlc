"""A fork PR must not block scheduled runtime dependency updates."""
import json
from pathlib import Path
import re
import shutil
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]


@unittest.skipUnless(shutil.which('jq'), 'Workflow jq check needs jq (installed on GitHub runners).')
class PendingRuntimePRChecks(unittest.TestCase):
    def count(self, prs):
        workflow = (ROOT / '.github/workflows/update-runtime-pins.yml').read_text()
        query = re.search(r"--jq '([^']+)'", workflow).group(1)
        result = subprocess.run(['jq', query], input=json.dumps(prs), text=True,
                                check=True, capture_output=True)
        return int(result.stdout)

    def test_matching_fork_pr_does_not_block_updates(self):
        self.assertEqual(self.count([
            {'headRefName': 'dependencies/runtime-pins-example', 'isCrossRepository': True},
        ]), 0)

    def test_matching_same_repository_pr_waits_for_review(self):
        self.assertEqual(self.count([
            {'headRefName': 'dependencies/runtime-pins-example', 'isCrossRepository': False},
            {'headRefName': 'unrelated-branch', 'isCrossRepository': False},
        ]), 1)


if __name__ == '__main__':
    unittest.main()
