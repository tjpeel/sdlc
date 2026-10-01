"""Offline checks for container publication guards and GitHub verification."""
import contextlib
import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
REPO = 'example-org/test-repo'
BRANCH = 'spike/docker-smoke'
LOGIN = 'example-user'
HEAD = 'a' * 40
PR_URL = f'https://github.com/{REPO}/pull/7'


def load(name, path):
    loader = importlib.machinery.SourceFileLoader(name, str(path))
    spec = importlib.util.spec_from_loader(name, loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


job = load('publication_job', ROOT / 'runtime/bin/sdlc-job')
launcher = load('publication_launcher', ROOT / 'scripts/sdlc.py')


def draft_pr():
    return dict(url=PR_URL, isDraft=True, headRefOid=HEAD, headRefName=BRANCH,
                baseRefName='main', author={'login': LOGIN})


class PublicationChecks(unittest.TestCase):
    def setUp(self):
        environment = mock.patch.dict(os.environ, {
            'SDLC_BASE_BRANCH': 'main', 'SDLC_GITHUB_LOGIN': LOGIN,
        }, clear=True)
        environment.start()
        self.addCleanup(environment.stop)
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.workspace = Path(self.temp.name) / 'repo'
        self.marker = self.workspace / job.SMOKE_FILE
        self.marker.parent.mkdir(parents=True)
        self.marker.write_bytes(job.SMOKE_CONTENT.encode('utf-8'))
        workspace = mock.patch.object(job, 'WORKSPACE', self.workspace)
        workspace.start()
        self.addCleanup(workspace.stop)

    def responses(self, remote_head=HEAD, prs=None, actor=LOGIN, changed=None):
        if prs is None:
            prs = [draft_pr()]
        if changed is None:
            changed = [job.SMOKE_FILE]

        def respond(*args, **kwargs):
            if args == ('git', 'rev-parse', 'HEAD'):
                return HEAD
            if args[:3] == ('git', 'diff', '--name-only'):
                return '\n'.join(changed)
            if args[:3] == ('gh', 'api', 'user'):
                return actor
            if args[:2] == ('gh', 'api'):
                return remote_head
            if args[:3] == ('gh', 'pr', 'list'):
                return json.dumps(prs)
            return ''

        return respond

    def main(self, runner, gate=None, verify=True, smoke=False, uid=1000, docker=True):
        args = ['sdlc-job', 'publish', '--branch', BRANCH, '--title', 'Smoke test',
                '--body', '/input/pr-body.md']
        if verify:
            args.append('--verify-published')
        if smoke:
            args.append('--smoke-checks')
        gate = gate or mock.Mock(return_value=[HEAD])
        with (mock.patch.object(sys, 'argv', args),
              mock.patch.object(job.Path, 'exists', autospec=True,
                                side_effect=lambda path: docker if str(path) == '/.dockerenv' else False),
              mock.patch.object(job.os, 'getuid', return_value=uid),
              mock.patch.object(job, 'initialize', return_value=(REPO, f'https://github.com/{REPO}.git')),
              mock.patch.object(job, 'valid_branch'), mock.patch.object(job, 'clean'),
              mock.patch.object(job, 'commits_to_publish', gate),
              mock.patch.object(job, 'run', runner)):
            job.main()

    def assert_no_publication(self, runner):
        for call in runner.call_args_list:
            self.assertNotEqual(call.args[:2], ('git', 'push'))
            self.assertNotEqual(call.args[:3], ('gh', 'pr', 'create'))

    def test_correct_branch_and_draft_pr_are_verified_inside_publish(self):
        events = []
        respond = self.responses()

        def record(*args, **kwargs):
            events.append(args)
            return respond(*args, **kwargs)

        def signed(branch):
            self.assertEqual(branch, BRANCH)
            events.append(('signature-gate',))
            return [HEAD]

        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            self.main(mock.Mock(side_effect=record), gate=signed)
        self.assertIn(f'Verified publication: {PR_URL} (HEAD {HEAD}).', output.getvalue())
        actor = next(i for i, args in enumerate(events) if args[:3] == ('gh', 'api', 'user'))
        gate = events.index(('signature-gate',))
        push = next(i for i, args in enumerate(events) if args[:2] == ('git', 'push'))
        create = next(i for i, args in enumerate(events) if args[:3] == ('gh', 'pr', 'create'))
        remote = next(i for i, args in enumerate(events)
                      if args[:3] == ('gh', 'api', f'repos/{REPO}/git/ref/heads/{BRANCH}'))
        prs = next(i for i, args in enumerate(events) if args[:3] == ('gh', 'pr', 'list'))
        self.assertLess(actor, gate)
        self.assertLess(gate, push)
        self.assertLess(push, create)
        self.assertLess(create, remote)
        self.assertLess(remote, prs)
        self.assertIn('--draft', events[create])
        self.assertIn('--state', events[prs])
        self.assertEqual(events[prs][events[prs].index('--state') + 1], 'open')

    def test_remote_branch_sha_mismatch_rejected(self):
        runner = mock.Mock(side_effect=self.responses(remote_head='b' * 40))
        with mock.patch.object(job, 'run', runner):
            with self.assertRaisesRegex(ValueError, 'branch SHA'):
                job.verify_publication(REPO, BRANCH)
        self.assertFalse(any(call.args[:3] == ('gh', 'pr', 'list')
                             for call in runner.call_args_list))

    def test_account_and_repository_casing_does_not_change_identity(self):
        prs = [{**draft_pr(), 'author': {'login': LOGIN.upper()}}]
        with (mock.patch.object(job, 'run', side_effect=self.responses(prs=prs)),
              contextlib.redirect_stdout(io.StringIO())):
            self.assertEqual(job.verify_publication(REPO.upper(), BRANCH), (PR_URL, HEAD))

    def test_missing_multiple_or_non_draft_pr_rejected(self):
        cases = ([], [draft_pr(), draft_pr()], [{**draft_pr(), 'isDraft': False}])
        for prs in cases:
            with self.subTest(prs=prs), mock.patch.object(job, 'run', side_effect=self.responses(prs=prs)):
                with self.assertRaises(ValueError):
                    job.verify_publication(REPO, BRANCH)

    def test_pr_branch_base_sha_actor_and_url_mismatches_rejected(self):
        cases = (
            {'headRefName': 'spike/other'}, {'baseRefName': 'other'},
            {'headRefOid': 'b' * 40}, {'author': {'login': 'different-user'}},
            {'author': None}, {'url': 'https://github.com/other/repo/pull/7'},
        )
        for changes in cases:
            prs = [{**draft_pr(), **changes}]
            with self.subTest(changes=changes), mock.patch.object(job, 'run', side_effect=self.responses(prs=prs)):
                with self.assertRaises(ValueError):
                    job.verify_publication(REPO, BRANCH)

    def test_wrong_actor_prevents_push_and_pr_creation(self):
        runner = mock.Mock(side_effect=self.responses(actor='different-user'))
        gate = mock.Mock(return_value=[HEAD])
        with self.assertRaisesRegex(ValueError, 'different GitHub account'):
            self.main(runner, gate=gate)
        gate.assert_not_called()
        self.assert_no_publication(runner)

    def test_failed_signature_gate_prevents_push_and_pr_creation(self):
        runner = mock.Mock(side_effect=self.responses())
        gate = mock.Mock(side_effect=subprocess.CalledProcessError(1, ['git', 'verify-commit', HEAD]))
        with self.assertRaises(subprocess.CalledProcessError):
            self.main(runner, gate=gate, smoke=True)
        self.assert_no_publication(runner)

    def test_smoke_acceptance_passes_after_signature_gate_before_push(self):
        events = []
        respond = self.responses()

        def record(*args, **kwargs):
            events.append(args)
            return respond(*args, **kwargs)

        def signed(branch):
            events.append(('signature-gate',))
            return [HEAD]

        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            self.main(mock.Mock(side_effect=record), gate=signed, smoke=True)
        self.assertIn(f'Smoke acceptance passed for commit {HEAD}.', output.getvalue())
        diff = next(i for i, args in enumerate(events) if args[:3] == ('git', 'diff', '--name-only'))
        push = next(i for i, args in enumerate(events) if args[:2] == ('git', 'push'))
        self.assertLess(events.index(('signature-gate',)), diff)
        self.assertLess(diff, push)
        self.assertEqual(events[diff][-1], 'refs/remotes/origin/main...HEAD')

    def test_wrong_smoke_contents_prevent_push_and_pr_creation(self):
        for content in (b'wrong\n', job.SMOKE_CONTENT.rstrip().encode('utf-8'),
                        job.SMOKE_CONTENT.replace('\n', '\r\n').encode('utf-8'), b'\xff'):
            with self.subTest(content=content), contextlib.redirect_stdout(io.StringIO()):
                self.marker.write_bytes(content)
                runner = mock.Mock(side_effect=self.responses())
                with self.assertRaises(ValueError):
                    self.main(runner, smoke=True)
                self.assert_no_publication(runner)

    def test_extra_or_missing_changed_file_prevents_push_and_pr_creation(self):
        for changed in ([job.SMOKE_FILE, 'README.md'], [], ['README.md']):
            with self.subTest(changed=changed), contextlib.redirect_stdout(io.StringIO()):
                runner = mock.Mock(side_effect=self.responses(changed=changed))
                with self.assertRaisesRegex(ValueError, 'change only'):
                    self.main(runner, smoke=True)
                self.assert_no_publication(runner)

    def test_wrong_uid_or_non_docker_prevents_push_and_pr_creation(self):
        for uid, docker in ((0, True), (1000, False)):
            with self.subTest(uid=uid, docker=docker), contextlib.redirect_stdout(io.StringIO()):
                runner = mock.Mock(side_effect=self.responses())
                with self.assertRaisesRegex(ValueError, 'Docker and user 1000'):
                    self.main(runner, smoke=True, uid=uid, docker=docker)
                self.assert_no_publication(runner)

    def test_multiple_or_no_proposed_commits_prevent_push_and_pr_creation(self):
        for commits in ([HEAD, 'b' * 40], []):
            with self.subTest(commits=commits), contextlib.redirect_stdout(io.StringIO()):
                runner = mock.Mock(side_effect=self.responses())
                gate = mock.Mock(return_value=commits)
                with self.assertRaisesRegex(ValueError, 'exactly one proposed commit'):
                    self.main(runner, gate=gate, smoke=True)
                self.assert_no_publication(runner)

    def test_missing_directory_or_symlink_marker_prevents_push_and_pr_creation(self):
        original = self.marker.read_bytes()
        for kind in ('missing', 'directory', 'symlink'):
            with self.subTest(kind=kind), contextlib.redirect_stdout(io.StringIO()):
                self.marker.unlink()
                if kind == 'directory':
                    self.marker.mkdir()
                elif kind == 'symlink':
                    target = self.workspace / 'outside-marker.md'
                    target.write_bytes(original)
                    self.marker.symlink_to(target)
                runner = mock.Mock(side_effect=self.responses())
                with self.assertRaisesRegex(ValueError, 'regular file'):
                    self.main(runner, smoke=True)
                self.assert_no_publication(runner)
                if self.marker.is_symlink():
                    self.marker.unlink()
                elif self.marker.is_dir():
                    self.marker.rmdir()
                self.marker.write_bytes(original)

    def test_normal_publication_does_not_add_remote_verification(self):
        runner = mock.Mock(side_effect=self.responses())
        with contextlib.redirect_stdout(io.StringIO()):
            self.main(runner, verify=False)
        self.assertTrue(any(call.args[:3] == ('gh', 'pr', 'create')
                            for call in runner.call_args_list))
        self.assertFalse(any(call.args[:3] == ('gh', 'pr', 'list')
                             for call in runner.call_args_list))
        self.assertFalse(any(call.args[:3] == ('git', 'diff', '--name-only')
                             for call in runner.call_args_list))

    def test_runtime_flag_rejected_for_other_actions_before_environment_or_repository_access(self):
        for action in ('init', 'exec', 'verify', 'results'):
            for flag in ('--verify-published', '--smoke-checks'):
                with (self.subTest(action=action, flag=flag),
                      mock.patch.object(sys, 'argv', ['sdlc-job', action, flag]),
                      mock.patch.object(job.Path, 'exists') as exists,
                      mock.patch.object(job, 'initialize') as initialize,
                      mock.patch.object(job, 'run') as runner,
                      contextlib.redirect_stderr(io.StringIO())):
                    with self.assertRaises(SystemExit) as exc:
                        job.main()
                    self.assertEqual(exc.exception.code, 2)
                    exists.assert_not_called()
                    initialize.assert_not_called()
                    runner.assert_not_called()


class LauncherPublicationChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        folder = Path(self.temp.name)
        self.profiles = folder / 'profiles.json'
        self.profiles.write_text(json.dumps({'smoke': {
            'github_login': LOGIN, 'git_name': 'Test User', 'git_email': 'test@example.invalid',
            'base_branch': 'main', 'repositories': [REPO],
            'signing_key_file': 'missing-test-key', 'github_token_file': 'missing-test-token',
        }}))
        self.body = folder / 'pr-body.md'
        self.body.write_text('Generated test change and checks.')

    def args(self, action, *extra):
        return ['sdlc.py', action, '--profiles', str(self.profiles), '--profile', 'smoke',
                '--repo', REPO, *extra]

    def test_verification_flags_are_forwarded_but_publish_remains_print_only(self):
        args = self.args('publish', '--branch', BRANCH, '--title', 'Smoke test',
                         '--body', str(self.body), '--verify-published', '--smoke-checks')
        output = io.StringIO()
        with (mock.patch.object(sys, 'argv', args),
              mock.patch.object(launcher, 'secret', side_effect=AssertionError('Secret read')),
              mock.patch.object(launcher.subprocess, 'run') as runner,
              contextlib.redirect_stdout(output)):
            launcher.main()
        self.assertIn('Print only: no secrets read, containers started or GitHub writes.', output.getvalue())
        command = shlex.split(output.getvalue().splitlines()[1])
        self.assertEqual(command[-2:], ['--verify-published', '--smoke-checks'])
        self.assertIn('sdlc-job', command)
        self.assertIn('publish', command)
        self.assertTrue(runner.call_args_list)
        self.assertTrue(all(call.args[0][:2] == ['git', 'check-ref-format']
                            for call in runner.call_args_list))

    def test_host_flag_rejected_for_other_actions_before_profile_or_command_access(self):
        for action in ('build', 'login', 'init', 'cli', 'exec', 'results', 'verify', 'ssh-up', 'ssh-down'):
            for flag in ('--verify-published', '--smoke-checks'):
                with (self.subTest(action=action, flag=flag),
                      mock.patch.object(sys, 'argv', self.args(action, flag)),
                      mock.patch.object(launcher, 'selected_profile') as selected,
                      mock.patch.object(launcher.subprocess, 'run') as runner,
                      mock.patch.object(launcher, 'secret') as secret,
                      contextlib.redirect_stderr(io.StringIO())):
                    with self.assertRaises(SystemExit) as exc:
                        launcher.main()
                    self.assertEqual(exc.exception.code, 2)
                    selected.assert_not_called()
                    runner.assert_not_called()
                    secret.assert_not_called()


if __name__ == '__main__':
    unittest.main()
