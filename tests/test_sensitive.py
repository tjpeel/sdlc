"""Publication checks against real Git index and history fixtures."""
import importlib.util
import hashlib
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / 'scripts/check_sensitive.py'
spec = importlib.util.spec_from_file_location('sensitive_check', SCRIPT)
check = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = check
spec.loader.exec_module(check)


class PatternChecks(unittest.TestCase):
    def categories(self, content, path='example.txt'):
        return {finding.category for finding in check.scan(path, content.encode())}

    def test_personal_drives_homes_and_network_shares(self):
        paths = [
            '/' + 'Users/sample-person/Documents/private',
            '/' + 'Volumes/PersonalDisk/private',
            '/' + 'mnt/PersonalDisk/private',
            '/' + 'home/sample-person/private',
            chr(92).join(('C:', 'Users', 'sample-person', 'private')),
            'D' + ':/Personal/private',
            '\\' * 2 + 'private-server\\private-share',
            '%2f' + 'Users%2fsample-person%2fprivate',
            '\\/' + 'Users\\/sample-person\\/private',
        ]
        for path in paths:
            with self.subTest(path=path):
                self.assertIn('personal filesystem path', self.categories(path))

    def test_secret_formats_and_literal_assignments(self):
        values = [
            'gh' + 'p_' + 'a' * 36,
            'github_' + 'pat_' + 'b' * 40,
            'AK' + 'IA' + 'A' * 16,
            'sk' + '-proj-' + 'c' * 30,
            'xox' + 'b-' + '1' * 30,
            '-' * 5 + 'BEGIN OPENSSH PRIVATE KEY' + '-' * 5,
            'API_' + 'KEY = "plausible-credential-value"',
            "'GH_TOK" + "EN': 'plausible-credential-value'",
            'https://' + 'login:credential' + '@' + 'host.invalid/repo',
            'op' + '://private-vault/private-item/token',
        ]
        for value in values:
            with self.subTest(value=value):
                self.assertTrue(self.categories(value))

    def test_example_values_and_runtime_paths_are_allowed(self):
        content = ('/home/node/.config /home/sdlc/repo /home/runner/work '
                   '/workspace/repo /run/secrets/signing-key /tmp/generated '
                   '/ABSOLUTE/PATH/TO/PROJECT user@example.invalid '
                   'op://YOUR_VAULT/YOUR_ITEM/token\n'
                   "GH_TOKEN = 'fake-disposable-token'\n"
                   'API_KEY = "${EXTERNAL_KEY}"\n'
                   "assert 'password=' + os.environ['GH_TOKEN'] in result\n")
        self.assertFalse(self.categories(content))
        self.assertIn('non-example email address',
                      self.categories('person@' + 'private-company.invalid'))

    def test_private_artifacts_are_blocked_even_with_safe_content(self):
        for path in ('.secrets/token', 'profiles.local.json', 'nested/.env.production',
                     'nested/.codex/auth.json', '.sdlc/work/tickets/input.md',
                     'results/job.json', 'signing-key.pub', 'job.log'):
            with self.subTest(path=path):
                self.assertIn('local/private artifact filename', self.categories('placeholder', path))
        self.assertFalse(self.categories('API_KEY=YOUR_KEY', '.env.example'))

    def test_unquoted_config_credentials_and_bearer_headers(self):
        for path, content in (
                ('.env.example', 'API_' + 'KEY=plausible-credential-value'),
                ('config.yaml', 'client_' + 'secret: plausible-credential-value'),
                ('script.sh', 'export GH_' + 'TOKEN=plausible-credential-value'),
                ('example.md', 'Authorization: Bea' + 'rer plausible-credential-value')):
            with self.subTest(path=path):
                self.assertTrue(self.categories(content, path))
        for content in ('API_KEY=YOUR_KEY', 'TOKEN=${EXTERNAL_TOKEN}', 'TOKEN: null',
                        'TOKEN: fake-disposable-token', 'Authorization: Bearer YOUR_TOKEN'):
            with self.subTest(content=content):
                self.assertFalse(self.categories(content, 'config.yaml'))
        for path in ('credentials', 'nested/credentials.db'):
            self.assertIn('local/private artifact filename', self.categories('YOUR_TOKEN', path))

    def test_reviewed_history_exception_is_bound_to_exact_content_path_and_category(self):
        data = ('op' + '://old-example/old-item/token').encode()
        digest = hashlib.sha256(data).hexdigest()
        baseline = {('example.txt', digest, 'vault reference')}
        with mock.patch.object(check, 'REVIEWED_HISTORY', baseline):
            self.assertFalse(check.scan_history_blob('example.txt', data, 'revision'))
            self.assertTrue(check.scan_history_blob('other.txt', data, 'revision'))
            self.assertTrue(check.scan_history_blob('example.txt', data + b'\n', 'revision'))
            self.assertTrue(check.scan('example.txt', data))
            other_rule = b'gh' + b'p_' + b'a' * 36
            with mock.patch.object(check, 'REVIEWED_HISTORY',
                                   {('example.txt', hashlib.sha256(other_rule).hexdigest(), 'vault reference')}):
                self.assertTrue(check.scan_history_blob('example.txt', other_rule, 'revision'))


class GitChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name)
        self.env = {key: value for key, value in os.environ.items()
                    if not key.startswith('GIT_')}
        self.env.update(GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=os.devnull,
                        GIT_AUTHOR_NAME='Test User', GIT_AUTHOR_EMAIL='test@example.invalid',
                        GIT_COMMITTER_NAME='Test User', GIT_COMMITTER_EMAIL='test@example.invalid')
        self.git('init', '-q')

    def git(self, *args):
        return subprocess.run(['git', *args], cwd=self.repo, env=self.env, check=True,
                              capture_output=True)

    def run_check(self, mode='--staged'):
        return subprocess.run([sys.executable, str(SCRIPT), mode], cwd=self.repo,
                              env=self.env, capture_output=True, text=True)

    def write(self, path, value):
        target = self.repo / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(value)

    def test_staged_secret_is_blocked_despite_unstaged_cleanup_and_is_redacted(self):
        credential = 'gh' + 'p_' + 'a' * 36
        self.write('config.txt', credential)
        self.git('add', 'config.txt')
        self.write('config.txt', 'YOUR_TOKEN')
        result = self.run_check()
        self.assertEqual(result.returncode, 1)
        self.assertIn('credential token', result.stderr)
        self.assertNotIn(credential, result.stdout + result.stderr)
        self.assertEqual(self.run_check('--worktree').returncode, 0)

    def test_force_added_ignored_artifact_is_blocked(self):
        self.write('.gitignore', '.secrets/\n')
        self.write('.secrets/token', 'YOUR_TOKEN')
        self.git('add', '.gitignore')
        self.assertEqual(self.run_check('--worktree').returncode, 0)
        self.git('add', '-f', '.secrets/token')
        self.assertEqual(self.run_check().returncode, 1)

    def test_untracked_pending_file_is_checked(self):
        self.write('pending.txt', '/' + 'Users/sample-person/private')
        self.assertEqual(self.run_check().returncode, 0)
        self.assertEqual(self.run_check('--worktree').returncode, 1)

    def test_historical_deleted_secret_is_detected(self):
        self.write('old.txt', 'gh' + 'p_' + 'a' * 36)
        self.git('add', 'old.txt')
        self.git('-c', 'commit.gpgsign=false', 'commit', '-qm', 'Add fixture')
        self.git('rm', 'old.txt')
        self.git('-c', 'commit.gpgsign=false', 'commit', '-qm', 'Remove fixture')
        self.assertEqual(self.run_check().returncode, 0)
        self.assertEqual(self.run_check('--history').returncode, 1)

    def test_symlink_target_is_checked_without_following_it(self):
        (self.repo / 'link').symlink_to('/' + 'Users/sample-person/private')
        self.git('add', 'link')
        self.assertEqual(self.run_check().returncode, 1)
        self.assertEqual(self.run_check('--worktree').returncode, 1)

    def test_binary_and_submodule_entries_fail_closed(self):
        (self.repo / 'blob').write_bytes(b'\x00\xff')
        self.git('add', 'blob')
        self.assertEqual(self.run_check().returncode, 1)
        self.git('rm', '--cached', 'blob')
        self.write('source.txt', 'placeholder')
        self.git('add', 'source.txt')
        self.git('-c', 'commit.gpgsign=false', 'commit', '-qm', 'Create fixture')
        oid = self.git('rev-parse', 'HEAD').stdout.decode().strip()
        self.git('update-index', '--add', '--cacheinfo', f'160000,{oid},submodule')
        self.assertEqual(self.run_check().returncode, 2)

    def test_pre_commit_hook_rejects_private_content(self):
        self.write('scripts/check_sensitive.py', SCRIPT.read_text())
        hook = self.repo / '.githooks/pre-commit'
        hook.parent.mkdir()
        hook.write_text((ROOT / '.githooks/pre-commit').read_text())
        hook.chmod(0o755)
        self.git('config', 'core.hooksPath', '.githooks')
        self.write('example.txt', 'user@example.invalid')
        self.git('add', '.')
        self.git('-c', 'commit.gpgsign=false', 'commit', '-qm', 'Safe fixture')
        self.write('example.txt', '/' + 'Volumes/PrivateDisk/notes')
        self.git('add', 'example.txt')
        result = subprocess.run(['git', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'Private fixture'],
                                cwd=self.repo, env=self.env, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('personal filesystem path', result.stderr)


if __name__ == '__main__':
    unittest.main()
