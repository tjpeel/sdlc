"""Automatic merges must exclude untested heads, forks and unrelated changes."""
import copy
import json
from pathlib import Path
import shutil
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]
REPO = 'example/dependency-test'
SHA = 'a' * 40
BRANCH = 'dependencies/runtime-pins-123'


@unittest.skipUnless(shutil.which('jq'), 'Merge policy needs jq (installed on GitHub runners).')
class DependencyMergePolicy(unittest.TestCase):
    def query(self, filename, value, **variables):
        arguments = ['jq', '-c']
        for name, content in variables.items():
            arguments += ['--arg', name, content]
        result = subprocess.run(arguments + ['-f', str(ROOT / '.github' / filename)],
                                input=json.dumps(value), text=True, capture_output=True, check=True)
        return json.loads(result.stdout) if result.stdout.strip() else None

    def pr(self):
        return {'number': 1, 'state': 'open', 'draft': False,
                'user': {'login': 'github-actions[bot]', 'type': 'Bot'},
                'head': {'ref': BRANCH, 'sha': SHA, 'repo': {'full_name': REPO}},
                'base': {'ref': 'main', 'repo': {'full_name': REPO}}}

    def select(self, prs, branch=BRANCH):
        return self.query('dependency-pr-filter.jq', prs, repo=REPO, base='main',
                          sha=SHA, branch=branch)

    def files_allowed(self, files, author='github-actions[bot]'):
        return self.query('dependency-files-filter.jq', files, author=author)

    def test_matching_bot_prs_are_selected(self):
        self.assertEqual(self.select([self.pr()]), 1)
        pr = self.pr()
        pr['user']['login'] = 'dependabot[bot]'
        pr['head']['ref'] = 'dependabot/docker/runtime/node-24'
        self.assertEqual(self.select([pr], branch=pr['head']['ref']), 1)

    def test_untrusted_or_untested_prs_are_excluded(self):
        mutations = [('state', 'closed'), ('draft', True), ('user.login', 'example-user'),
                     ('user.type', 'User'), ('head.sha', 'b' * 40),
                     ('head.ref', 'unrelated'), ('head.repo.full_name', 'example/fork'),
                     ('base.ref', 'release'), ('base.repo.full_name', 'example/other')]
        for field, value in mutations:
            with self.subTest(field=field):
                pr = self.pr()
                cursor = pr
                parts = field.split('.')
                for part in parts[:-1]:
                    cursor = cursor[part]
                cursor[parts[-1]] = value
                self.assertIsNone(self.select([pr]))

    def test_bot_author_requires_its_expected_branch(self):
        pr = self.pr()
        pr['head']['ref'] = 'dependencies/runtime-pins-not-a-run-id'
        self.assertIsNone(self.select([pr], branch=pr['head']['ref']))
        pr['user']['login'] = 'dependabot[bot]'
        pr['head']['ref'] = BRANCH
        self.assertIsNone(self.select([pr]))

    def test_ambiguous_prs_fail_closed(self):
        self.assertIsNone(self.select([self.pr(), copy.deepcopy(self.pr())]))

    def test_runtime_pin_changes_are_allowed(self):
        patch = ('@@ -3,2 +3,2 @@\n-ARG CODEX_VERSION=0.159.2\n'
                 '+ARG CODEX_VERSION=0.159.3\n-ARG SKILLS_REVISION=' + 'a' * 40
                 + '\n+ARG SKILLS_REVISION=' + 'b' * 40)
        self.assertTrue(self.files_allowed([
            {'filename': 'runtime/Dockerfile', 'status': 'modified', 'patch': patch}]))

    def test_other_runtime_changes_fail_closed(self):
        for patch in (None, '', '+RUN arbitrary-command', '+ARG CODEX_VERSION=latest',
                      '+ARG AGENTS_REVISION=main', '+ARG CODEX_VERSION=0.159.3;echo injected'):
            with self.subTest(patch=patch):
                self.assertFalse(self.files_allowed([
                    {'filename': 'runtime/Dockerfile', 'status': 'modified', 'patch': patch}]))
        self.assertFalse(self.files_allowed([
            {'filename': 'scripts/sdlc.py', 'status': 'modified', 'patch': '+print(1)'}]))

    def test_dependabot_paths_and_statuses_are_restricted(self):
        for filename in ('runtime/Dockerfile', '.github/workflows/validate.yml',
                         '.github/workflows/update-runtime-pins.yaml'):
            with self.subTest(filename=filename):
                self.assertTrue(self.files_allowed([
                    {'filename': filename, 'status': 'modified'}], author='dependabot[bot]'))
        for filename, status in (('scripts/sdlc.py', 'modified'),
                                 ('.github/workflows/validate.yml', 'removed'),
                                 ('runtime/Dockerfile', 'renamed'),
                                 ('.github/workflows/nested/unsafe.yml', 'modified')):
            with self.subTest(filename=filename, status=status):
                self.assertFalse(self.files_allowed([
                    {'filename': filename, 'status': status}], author='dependabot[bot]'))
        self.assertFalse(self.files_allowed([], author='dependabot[bot]'))
        self.assertFalse(self.files_allowed([
            {'filename': 'runtime/Dockerfile', 'status': 'modified'}], author='example-user'))


if __name__ == '__main__':
    unittest.main()
