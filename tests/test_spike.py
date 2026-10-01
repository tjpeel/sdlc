"""Offline acceptance checks. Generated test keys never leave a temporary folder."""
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]


def load(name, path):
    loader = importlib.machinery.SourceFileLoader(name, str(path))
    spec = importlib.util.spec_from_loader(name, loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


job = load('sdlc_job', ROOT / 'runtime/bin/sdlc-job')
launcher = load('sdlc_launcher', ROOT / 'scripts/sdlc.py')


class SigningChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.folder = Path(self.temp.name)
        self.repo = self.folder / 'repo'
        self.repo.mkdir()
        self.email = 'automation@example.invalid'
        self.environment = mock.patch.dict(os.environ, {
            'GIT_CONFIG_GLOBAL': os.devnull, 'GIT_CONFIG_NOSYSTEM': '1',
            'SDLC_GIT_EMAIL': self.email, 'SDLC_BASE_BRANCH': 'main',
        })
        self.environment.start()
        self.addCleanup(self.environment.stop)
        self.original_workspace = job.WORKSPACE
        self.original_signers = job.ALLOWED_SIGNERS
        self.original_program = job.SIGN_PROGRAM
        self.addCleanup(self.restore_job)
        job.WORKSPACE = self.repo
        job.SIGN_PROGRAM = str(ROOT / 'runtime/bin/ssh-sign-file')
        for name in ('expected', 'wrong'):
            self.command('ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(self.folder / name))
        signers = self.folder / 'signers'
        signers.write_text(f'{self.email} {(self.folder / "expected.pub").read_text()}')
        job.ALLOWED_SIGNERS = str(signers)
        self.git('init', '-q', '-b', 'main')
        for key, value in (('user.name', 'Automation'), ('user.email', self.email),
                           ('gpg.format', 'ssh'), ('gpg.ssh.program', job.SIGN_PROGRAM),
                           ('user.signingKey', str(self.folder / 'expected')),
                           ('commit.gpgsign', 'true'), ('core.hooksPath', '/dev/null')):
            self.git('config', key, value)
        self.commit('base', signed=False)
        self.git('update-ref', 'refs/remotes/origin/main', 'HEAD')
        self.git('switch', '-q', '-c', 'spike/ticket')

    def restore_job(self):
        job.WORKSPACE = self.original_workspace
        job.ALLOWED_SIGNERS = self.original_signers
        job.SIGN_PROGRAM = self.original_program

    def command(self, *args):
        return subprocess.run(args, cwd=self.repo, check=True, text=True,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout.strip()

    def git(self, *args):
        return self.command('git', *args)

    def commit(self, content, signed=True):
        (self.repo / 'example.txt').write_text(content)
        self.git('add', 'example.txt')
        self.git('-c', f'commit.gpgsign={str(signed).lower()}', 'commit', '-q', '-m', content)

    def test_all_proposed_commits_verify_with_expected_key(self):
        self.commit('one')
        self.commit('two')
        self.assertEqual(len(job.commits_to_publish('spike/ticket')), 2)

    def test_unsigned_earlier_commit_blocks_signed_head(self):
        self.commit('unsigned', signed=False)
        self.commit('signed head')
        with self.assertRaises(subprocess.CalledProcessError):
            job.commits_to_publish('spike/ticket')

    def test_wrong_signing_key_rejected(self):
        self.git('config', 'user.signingKey', str(self.folder / 'wrong'))
        self.commit('wrong key')
        with self.assertRaises(subprocess.CalledProcessError):
            job.commits_to_publish('spike/ticket')

    def test_different_committer_rejected(self):
        with mock.patch.dict(os.environ, {'GIT_COMMITTER_EMAIL': 'other@example.invalid'}):
            self.commit('different committer')
        with self.assertRaisesRegex(ValueError, 'committer'):
            job.commits_to_publish('spike/ticket')

    def test_base_branch_cannot_be_published(self):
        with self.assertRaisesRegex(ValueError, 'base branch'):
            job.commits_to_publish('main')

    def test_encrypted_key_fails_without_prompt(self):
        key = self.folder / 'encrypted'
        self.command('ssh-keygen', '-q', '-t', 'ed25519', '-N', 'test-passphrase', '-f', str(key))
        result = subprocess.run(['ssh-keygen', '-y', '-P', '', '-f', str(key)],
                                stdin=subprocess.DEVNULL, capture_output=True, timeout=5)
        self.assertNotEqual(result.returncode, 0)


class LauncherChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.folder = Path(self.temp.name)
        self.profiles = self.folder / 'profiles.json'
        self.profiles.write_text(json.dumps({'work': {
            'github_login': 'work-user', 'git_name': 'Engineer',
            'git_email': 'engineer@example.invalid', 'base_branch': 'main',
            'repositories': ['work-org/test-repo'],
            'signing_key_file': 'does-not-exist', 'github_token_file': 'does-not-exist',
        }}))
        self.body = self.folder / 'body.md'
        self.body.write_text('Reviewable changes and checks.')

    def call(self, *args):
        return subprocess.run([sys.executable, str(ROOT / 'scripts/sdlc.py'), *args,
                               '--profiles', str(self.profiles), '--profile', 'work',
                               '--repo', 'work-org/test-repo'], text=True, capture_output=True)

    def test_publish_defaults_to_print_without_docker_or_secret_access(self):
        # Docker is deliberately absent from PATH. Git remains available for ref validation.
        with mock.patch.dict(os.environ, {'PATH': '/usr/bin:/bin'}):
            result = self.call('publish', '--branch', 'spike/ticket', '--title', 'Example',
                               '--body', str(self.body))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('Print only', result.stdout)
        self.assertNotIn('GH_TOKEN=', result.stdout)

    def test_profile_cannot_select_repository_outside_allowlist(self):
        with self.assertRaisesRegex(ValueError, 'allowlist'):
            launcher.selected_profile(self.profiles, 'work', 'other-org/private')

    def test_invalid_branch_fails_before_container_start(self):
        result = self.call('verify', '--branch', 'spike/../bad', '--dry-run')
        self.assertNotEqual(result.returncode, 0)

    def test_secret_file_requires_private_permissions(self):
        key = self.folder / 'key'
        key.write_text('not a real credential')
        key.chmod(0o644)
        with self.assertRaisesRegex(ValueError, 'chmod 600'):
            launcher.secret(key)


if __name__ == '__main__':
    unittest.main()
