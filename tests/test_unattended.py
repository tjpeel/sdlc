"""Offline ticket transfer and unattended publication ordering checks."""
import contextlib
import hashlib
import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import shlex
import signal
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
REPO = 'example-org/test-repo'
BRANCH = 'tickets/123'
JOB_ID = 'a' * 32
HEAD = 'b' * 40
BASE = 'c' * 40
TICKET = '.sdlc/work/tickets/123/ticket-123.md'


def load(name, path):
    loader = importlib.machinery.SourceFileLoader(name, str(path))
    spec = importlib.util.spec_from_loader(name, loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


launcher = load('unattended_launcher', ROOT / 'scripts/sdlc.py')
job = load('unattended_job', ROOT / 'runtime/bin/sdlc-job')
collector = load('unattended_collector', ROOT / 'runtime/bin/ticket_input.py')


class TicketInputChecks(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name) / 'repo'
        self.ticket = self.root / TICKET
        self.ticket.parent.mkdir(parents=True)
        self.ticket.write_text('Synthetic ticket.\n')
        self.destination = Path(self.temporary.name) / 'private' / 'input'

    def snapshot(self):
        return collector.snapshot(self.root, TICKET, self.destination)

    def test_snapshot_preserves_linked_context_and_never_copies_local_code(self):
        context = self.root / '.sdlc/work/context.md'
        context.write_text('Bounded context.\n')
        (self.root / 'README.md').write_text('Host documentation must not be copied.\n')
        self.ticket.write_text('Read [context](../../context.md) and [docs](../../../../README.md).\n')
        manifest = self.snapshot()
        self.assertEqual({entry['path'] for entry in manifest['files']}, {TICKET, '.sdlc/work/context.md'})
        self.assertEqual((self.destination / TICKET).read_bytes(), self.ticket.read_bytes())
        self.assertFalse((self.destination / 'README.md').exists())
        for entry in manifest['files']:
            self.assertEqual(entry['sha256'], hashlib.sha256((self.destination / entry['path']).read_bytes()).hexdigest())
        self.assertEqual((self.destination / TICKET).stat().st_mode & 0o777, 0o444)
        self.assertEqual(self.destination.stat().st_mode & 0o777, 0o755)

    def test_ticket_path_rejects_absolute_traversal_and_noncanonical_input(self):
        for value in ('/tmp/ticket.md', '.sdlc/work/tickets/123/../123/ticket-123.md',
                      '.sdlc//work/tickets/123/ticket-123.md', '.sdlc/./work/tickets/123/ticket-123.md',
                      '.sdlc/work/tickets/123/ticket.md'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                collector.ticket_path(value)

    def test_symlink_hardlink_directory_and_oversize_input_are_rejected(self):
        content = self.ticket.read_bytes()
        for kind in ('symlink', 'hardlink', 'directory', 'oversize'):
            with self.subTest(kind=kind):
                self.ticket.unlink()
                other = self.root / 'outside.md'
                other.write_bytes(content)
                if kind == 'symlink':
                    self.ticket.symlink_to(other)
                elif kind == 'hardlink':
                    os.link(other, self.ticket)
                elif kind == 'directory':
                    self.ticket.mkdir()
                else:
                    self.ticket.write_bytes(b'x' * (collector.MAX_FILE_BYTES + 1))
                with self.assertRaises(ValueError):
                    collector.read_markdown(self.root, TICKET)
                if self.ticket.is_dir():
                    self.ticket.rmdir()
                else:
                    self.ticket.unlink()
                self.ticket.write_bytes(content)
                other.unlink()

    def test_fifo_is_rejected_without_waiting_for_a_writer(self):
        self.ticket.unlink()
        os.mkfifo(self.ticket)
        with self.assertRaises(ValueError):
            collector.read_markdown(self.root, TICKET)

    def test_symlinked_parent_and_protected_work_links_are_rejected(self):
        target = self.root / 'real-tickets'
        self.ticket.parent.rename(target)
        self.ticket.parent.symlink_to(target, target_is_directory=True)
        with self.assertRaises(ValueError):
            collector.read_markdown(self.root, TICKET)
        for link in ('../../.secrets/credentials.md', '../../../../.git/config', '../../result.json'):
            with self.subTest(link=link), self.assertRaises(ValueError):
                collector.context_link(TICKET, link)

    def test_snapshot_remains_readable_inside_worker_with_restrictive_host_umask(self):
        previous = os.umask(0o077)
        try:
            self.snapshot()
        finally:
            os.umask(previous)
        current = (self.destination / TICKET).parent
        while current != self.destination.parent:
            self.assertEqual(current.stat().st_mode & 0o777, 0o755)
            current = current.parent

    def test_link_cycles_are_bounded_and_binary_markdown_rejected(self):
        self.ticket.write_text('[self](ticket-123.md)\n')
        self.assertEqual(len(self.snapshot()['files']), 1)
        self.ticket.write_bytes(b'\xff')
        with self.assertRaises(ValueError):
            collector.read_markdown(self.root, TICKET)


class LauncherUnattendedChecks(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.repo = self.directory / 'repo'
        (self.repo / TICKET).parent.mkdir(parents=True)
        (self.repo / TICKET).write_text('Synthetic work ticket.\n')
        self.profiles = self.directory / 'profiles.json'
        self.profile = {'github_login': 'example-user', 'git_name': 'Example User',
                        'git_email': 'test@example.invalid', 'base_branch': 'main', 'repositories': [REPO],
                        'signing_key_file': 'missing-key', 'github_token_file': 'missing-token',
                        'checks': ['python3 -m unittest'], 'cleanup': ['docker compose down']}

    def invoke(self, *extra, runner=None):
        self.profiles.write_text(json.dumps({'example': self.profile}))
        arguments = ['sdlc.py', 'run', '--profiles', str(self.profiles), '--profile', 'example',
                     '--repo', REPO, '--repo-root', str(self.repo), '--branch', BRANCH, '--ticket', TICKET, *extra]
        output = io.StringIO()
        runner = runner or mock.Mock(return_value=subprocess.CompletedProcess([], 0, stdout=''))
        with (mock.patch.object(sys, 'argv', arguments), mock.patch.object(launcher.subprocess, 'run', runner),
              mock.patch.object(launcher, 'secret', return_value='fake-disposable-secret') as secret,
              contextlib.redirect_stdout(output)):
            launcher.main()
        return output.getvalue(), runner, secret

    def test_run_is_print_only_by_default_with_concrete_provider_and_docker_commands(self):
        output, runner, secret = self.invoke('--docker-tests', '--implementer', 'claude', '--reviewer', 'codex')
        secret.assert_not_called()
        self.assertFalse(any(c.args[0][0] == 'docker' for c in runner.call_args_list))
        self.assertIn('Implementation: claude; review: codex', output)
        self.assertIn('up -d --wait --wait-timeout 180 docker-engine', output)
        self.assertIn('compose.job.yaml', output)
        self.assertIn('compose.docker-tests.yaml', output)
        self.assertIn('--no-deps', output)
        self.assertIn('--job /input/job/job.json', output)
        self.assertIn('down --remove-orphans', output)

    def test_checks_and_provider_settings_fail_closed(self):
        for change in ({'checks': []}, {'checks': 'npm test'}, {'cleanup': [None]},
                       {'max_review_rounds': True}, {'max_review_rounds': 4},
                       {'implementation': {'provider': 'other'}}, {'review': {'model': '-bad'}},
                       {'agent_timeout_seconds': 1}):
            with self.subTest(change=change):
                original = dict(self.profile)
                self.profile.update(change)
                with self.assertRaises(ValueError):
                    self.invoke()
                self.profile = original

    def test_execution_snapshots_outside_repo_and_always_stops_failed_docker_job(self):
        commands = []
        mounted = []
        def execute(arguments, **kwargs):
            commands.append(arguments)
            if '--volume' in arguments:
                value = arguments[arguments.index('--volume') + 1]
                directory = Path(value.split(':/input/job:ro')[0])
                mounted.append(directory)
                self.assertNotIn(self.repo, directory.parents)
                descriptor = json.loads((directory / 'job.json').read_text())
                self.assertEqual(descriptor['ticket'], TICKET)
                self.assertEqual(descriptor['implementation']['provider'], 'codex')
                self.assertEqual(descriptor['review']['provider'], 'claude')
                self.assertEqual((directory / 'job.json').stat().st_mode & 0o777, 0o444)
                self.assertEqual(directory.parent.stat().st_mode & 0o777, 0o700)
                raise subprocess.CalledProcessError(1, arguments)
            return subprocess.CompletedProcess(arguments, 0, stdout='')
        with self.assertRaises(subprocess.CalledProcessError):
            self.invoke('--execute', '--docker-tests', runner=mock.Mock(side_effect=execute))
        self.assertTrue(any(c[-2:] == ['down', '--remove-orphans'] for c in commands))
        self.assertTrue(any(c[:3] == ['docker', 'volume', 'rm'] for c in commands))
        self.assertFalse(mounted[0].exists())

    def test_private_test_environment_file_is_explicitly_forwarded(self):
        self.profile['test_env_file'] = 'synthetic-test.env'
        environment_file = self.directory / self.profile['test_env_file']
        environment_file.write_text('EXAMPLE_TEST_VALUE=disposable-private-value\n')
        environment_file.chmod(0o600)
        output, runner, secret = self.invoke('--execute')
        self.assertIn(mock.call(environment_file.resolve()), secret.call_args_list)
        docker_calls = [call for call in runner.call_args_list if call.args[0][:2] == ['docker', 'compose']]
        self.assertTrue(docker_calls)
        self.assertTrue(all(str(ROOT / 'runtime/compose.test-env.yaml') in call.args[0] for call in docker_calls))
        self.assertTrue(all(call.kwargs['env']['SDLC_TEST_ENV_FILE'] == str(environment_file.resolve())
                            for call in docker_calls))
        self.assertNotIn('disposable-private-value', output)

    def test_cancellation_runs_cleanup_and_repeated_signals_do_not_interrupt_it(self):
        commands = []
        previous = signal.getsignal(signal.SIGTERM)
        def execute(arguments, **kwargs):
            commands.append(arguments)
            if '--volume' in arguments:
                os.kill(os.getpid(), signal.SIGTERM)
            if arguments[-2:] == ['down', '--remove-orphans']:
                os.kill(os.getpid(), signal.SIGTERM)
            return subprocess.CompletedProcess(arguments, 0, stdout='')
        with self.assertRaises(InterruptedError):
            self.invoke('--execute', runner=mock.Mock(side_effect=execute))
        self.assertTrue(any(c[-2:] == ['down', '--remove-orphans'] for c in commands))
        self.assertEqual(signal.getsignal(signal.SIGTERM), previous)

    def test_project_lock_prevents_secret_access_and_docker_lifecycle(self):
        self.profiles.write_text(json.dumps({'example': self.profile}))
        identity = '|'.join((REPO, self.profile['github_login'], self.profile['git_email']))
        lock_id = 'sdlc-example-' + hashlib.sha256(identity.encode()).hexdigest()[:12]
        locks = Path(tempfile.gettempdir()) / 'sdlc-project-locks'
        locks.mkdir(mode=0o700, exist_ok=True)
        with (locks / lock_id).open('a') as held:
            launcher.fcntl.flock(held, launcher.fcntl.LOCK_EX | launcher.fcntl.LOCK_NB)
            with mock.patch.object(launcher, 'secret') as secret:
                with self.assertRaisesRegex(ValueError, 'owns this Docker project'):
                    self.invoke('--execute')
                secret.assert_not_called()
            alternate = self.directory / 'alternate-profiles.json'
            alternate.write_text(json.dumps({'example': self.profile}))
            legacy_arguments = ['sdlc.py', 'login', '--profiles', str(alternate), '--profile', 'example',
                                '--repo', REPO]
            with (mock.patch.object(sys, 'argv', legacy_arguments),
                  mock.patch.object(launcher.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, stdout='')) as runner,
                  contextlib.redirect_stdout(io.StringIO())):
                with self.assertRaisesRegex(ValueError, 'owns this Docker project'):
                    launcher.main()
                self.assertFalse(any(call.args[0][0] == 'docker' for call in runner.call_args_list))


class AtomicBranchChecks(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.remote = self.directory / 'remote.git'
        self.workspace = self.directory / 'repo'
        self.results = self.directory / 'results'
        self.results.mkdir()
        environment = mock.patch.dict(os.environ, {'GIT_CONFIG_GLOBAL': os.devnull,
                                                   'GIT_CONFIG_NOSYSTEM': '1'}, clear=False)
        environment.start()
        self.addCleanup(environment.stop)
        self.git('init', '--bare', '-q', '-b', 'main', str(self.remote), cwd=self.directory)
        self.git('clone', '-q', str(self.remote), str(self.workspace), cwd=self.directory)
        self.git('config', 'user.name', 'Disposable Test User')
        self.git('config', 'user.email', 'test@example.invalid')
        self.git('config', 'commit.gpgsign', 'false')
        (self.workspace / 'example.txt').write_text('Synthetic base.\n')
        self.git('add', 'example.txt')
        self.git('commit', '-q', '-m', 'Add synthetic base')
        self.git('push', '-q', 'origin', 'main')
        self.base = self.git('rev-parse', 'HEAD')
        self.git('switch', '-q', '-c', BRANCH)
        (self.workspace / 'example.txt').write_text('Synthetic ticket change.\n')
        self.git('add', 'example.txt')
        self.git('commit', '-q', '-m', 'Handle synthetic ticket')
        self.head = self.git('rev-parse', 'HEAD')

    def git(self, *arguments, cwd=None):
        result = subprocess.run(['git', *arguments], cwd=cwd or self.workspace,
                                check=True, capture_output=True, text=True)
        return result.stdout.strip()

    def test_absent_branch_is_created_at_exact_reviewed_sha(self):
        with mock.patch.object(job, 'WORKSPACE', self.workspace):
            job.push_new_branch(str(self.remote), BRANCH, self.head, self.results)
        self.assertEqual(self.git('--git-dir', str(self.remote), 'rev-parse', f'refs/heads/{BRANCH}'), self.head)

    def test_branch_created_during_implementation_is_never_fast_forwarded(self):
        self.git('--git-dir', str(self.remote), 'update-ref', f'refs/heads/{BRANCH}', self.base)
        with mock.patch.object(job, 'WORKSPACE', self.workspace):
            with self.assertRaises(ValueError):
                job.push_new_branch(str(self.remote), BRANCH, self.head, self.results)
        self.assertEqual(self.git('--git-dir', str(self.remote), 'rev-parse', f'refs/heads/{BRANCH}'), self.base)


class DescriptorChecks(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name) / 'repo'
        source = self.root / TICKET
        source.parent.mkdir(parents=True)
        source.write_text('Synthetic bounded input.\n')
        self.input = Path(self.temporary.name) / 'input'
        manifest = collector.snapshot(self.root, TICKET, self.input)
        self.descriptor = {'version': 1, 'id': JOB_ID, 'repository': REPO, 'branch': BRANCH,
                           'base_branch': 'main', 'ticket': TICKET, 'manifest': manifest,
                           'implementation': {'provider': 'codex', 'model': None},
                           'review': {'provider': 'claude', 'model': 'example-model'},
                           'checks': ['synthetic checks'], 'cleanup': [],
                           'max_review_rounds': 2, 'agent_timeout_seconds': 60}
        self.path = self.input / 'job.json'
        environment = mock.patch.dict(os.environ, {'SDLC_JOB_ID': JOB_ID, 'SDLC_REPOSITORY': REPO,
                                                   'SDLC_BASE_BRANCH': 'main'}, clear=True)
        environment.start()
        self.addCleanup(environment.stop)

    def load(self):
        self.path.write_text(json.dumps(self.descriptor))
        return job.load_job(self.path)

    def test_descriptor_validates_snapshot_hashes_and_selected_identity(self):
        self.assertEqual(self.load(), self.descriptor)
        with mock.patch.dict(os.environ, {'SDLC_JOB_ID': '0' * 32}):
            with self.assertRaisesRegex(ValueError, 'identity'):
                self.load()
        (self.input / TICKET).chmod(0o600)
        (self.input / TICKET).write_text('Changed after manifest.\n')
        with self.assertRaisesRegex(ValueError, 'hash'):
            self.load()

    def test_validated_inputs_and_execution_settings_are_retained_privately(self):
        descriptor = self.load()
        result = Path(self.temporary.name) / 'results'
        result.mkdir()
        saved = job.persist_inputs(descriptor, self.input, result)
        self.assertEqual((saved / TICKET).read_bytes(), (self.input / TICKET).read_bytes())
        self.assertEqual(json.loads((saved / 'job.json').read_text()), descriptor)
        self.assertEqual((saved / 'job.json').stat().st_mode & 0o777, 0o600)
        self.assertEqual(saved.stat().st_mode & 0o777, 0o700)

    def test_descriptor_rejects_malformed_limits_providers_manifest_and_extra_fields(self):
        cases = ({'version': True}, {'agent_timeout_seconds': True}, {'max_review_rounds': 4},
                 {'checks': []}, {'cleanup': {}}, {'branch': None},
                 {'review': {'provider': 'other', 'model': None}},
                 {'implementation': {'provider': 'codex', 'model': '-bad'}},
                 {'extra': 'unexpected'}, {'manifest': {'version': 1, 'ticket': TICKET, 'files': []}})
        original = dict(self.descriptor)
        for changes in cases:
            with self.subTest(changes=changes):
                self.descriptor = dict(original, **changes)
                with self.assertRaises(ValueError):
                    self.load()

    def test_agent_boundary_rejects_blocked_and_contradictory_reviews(self):
        settings = {'provider': 'codex', 'model': None}
        responses = [
            ('implementation', {'status': 'blocked', 'title': 'Blocked', 'body': 'Incomplete'}),
            ('review', {'verdict': 'pass', 'findings': ['An actionable problem.']}),
            ('review', {'verdict': 'changes_requested', 'findings': []}),
        ]
        for role, fields in responses:
            response = dict(settings, role=role, **fields)
            with self.subTest(role=role, fields=fields), mock.patch('sdlc_agents.run_agent', return_value=response):
                with self.assertRaises(ValueError):
                    job.invoke_agent(settings, 'Synthetic prompt', self.input / role, role, 60)


class PipelineChecks(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.workspace = self.directory / 'repo'
        self.workspace.mkdir()
        self.results = self.directory / 'results'
        self.job_path = self.directory / 'input/job.json'
        self.descriptor = {'version': 1, 'id': JOB_ID, 'repository': REPO, 'branch': BRANCH,
                           'base_branch': 'main', 'ticket': TICKET,
                           'implementation': {'provider': 'codex', 'model': None},
                           'review': {'provider': 'claude', 'model': None},
                           'checks': ['synthetic checks'], 'cleanup': ['synthetic cleanup'],
                           'max_review_rounds': 2, 'agent_timeout_seconds': 60}
        self.events = []
        self.prompts = []
        self.proposal = {'provider': 'codex', 'model': None, 'role': 'implementation',
                         'status': 'complete', 'title': 'Handle example ticket', 'body': 'Change and checks.'}

    def execute(self, reviews=None, failure=None, mutation=False, cleanup_failure=False, mutate_saved_input=False):
        reviews = iter(reviews or [{'verdict': 'pass', 'findings': []}])
        state_calls = 0
        def agent(settings, prompt, results, role, timeout):
            self.events.append(role)
            self.prompts.append((role, prompt))
            if mutate_saved_input and role == 'implementation':
                saved_ticket = self.directory / 'saved-input' / TICKET
                saved_ticket.parent.mkdir(parents=True, exist_ok=True)
                saved_ticket.write_text('Altered diagnostic copy acceptance criteria.\n')
            if role == 'implementation':
                if failure == 'implementation':
                    raise ValueError('Synthetic agent failure.')
                return self.proposal
            return dict(provider='claude', model=None, role='review', **next(reviews))
        def configured(descriptor, results, key, number):
            self.events.append(key)
            if failure == key or (key == 'cleanup' and cleanup_failure):
                raise ValueError('Synthetic configured command failure.')
            return []
        def fingerprint(results):
            nonlocal state_calls
            state_calls += 1
            return (HEAD, '', 'changed' if mutation and state_calls == 2 else 'stable')
        def command(arguments, results, label, **kwargs):
            self.events.append(tuple(arguments[:3]))
            if arguments[:3] == ['git', 'remote', 'get-url']:
                return f'https://github.com/{REPO}.git'
            if arguments[:3] == ['gh', 'api', 'user']:
                return 'example-user'
            return ''
        original_path = Path
        def runtime_path(value):
            return self.results if str(value) == '/workspace/results' else original_path(value)
        with (mock.patch.object(job, 'Path', side_effect=runtime_path),
              mock.patch.object(job, 'WORKSPACE', self.workspace),
              mock.patch.object(job, 'load_job', return_value=dict(self.descriptor)),
              mock.patch.object(job, 'persist_inputs', return_value=self.directory / 'saved-input'),
              mock.patch.object(job, 'fresh_checkout', return_value=(REPO, f'https://github.com/{REPO}.git', BASE)),
              mock.patch.object(job, 'invoke_agent', side_effect=agent),
              mock.patch.object(job, 'checked_head', return_value=(HEAD, [HEAD])),
              mock.patch.object(job, 'configured_commands', side_effect=configured),
              mock.patch.object(job, 'workspace_fingerprint', side_effect=fingerprint),
              mock.patch.object(job, 'pipeline_command', side_effect=command),
              mock.patch.object(job, 'verify_publication', return_value=(f'https://github.com/{REPO}/pull/1', HEAD)),
              mock.patch.dict(os.environ, {'SDLC_GITHUB_LOGIN': 'example-user'}),
              contextlib.redirect_stdout(io.StringIO())):
            job.unattended_job(self.job_path)
        return json.loads((self.results / JOB_ID / 'result.json').read_text())

    def test_success_checks_reviews_cleans_then_publishes_exact_reviewed_commit_once(self):
        result = self.execute()
        self.assertEqual(result['status'], 'published')
        push = self.events.index(('git', 'push', f'--force-with-lease=refs/heads/{BRANCH}:'))
        self.assertLess(self.events.index('implementation'), self.events.index('checks'))
        self.assertLess(self.events.index('checks'), self.events.index('review'))
        self.assertLess(self.events.index('review'), self.events.index('cleanup'))
        self.assertLess(self.events.index('cleanup'), push)
        self.assertEqual(self.events.count(('gh', 'pr', 'create')), 1)
        self.assertEqual(result['commit'], HEAD)

    def test_agents_always_use_original_input_after_diagnostic_copy_is_mutated(self):
        self.execute(mutate_saved_input=True)
        original = self.job_path.parent / TICKET
        saved_ticket = self.directory / 'saved-input' / TICKET
        self.assertIn('Altered diagnostic copy', saved_ticket.read_text())
        self.assertEqual([role for role, _ in self.prompts], ['implementation', 'review'])
        for role, prompt in self.prompts:
            self.assertIn(str(original), prompt)
            self.assertIn(str(self.job_path.parent), prompt)
            self.assertNotIn(str(self.directory / 'saved-input'), prompt)

    def test_review_findings_trigger_new_implementation_checks_and_independent_review(self):
        result = self.execute(reviews=[{'verdict': 'changes_requested', 'findings': ['Handle boundary case.']},
                                       {'verdict': 'pass', 'findings': []}])
        self.assertEqual(result['review_rounds'], 2)
        self.assertEqual([event for event in self.events if isinstance(event, str)],
                         ['implementation', 'checks', 'review', 'implementation', 'checks', 'review', 'cleanup'])

    def test_failures_and_review_mutation_never_publish_and_always_cleanup(self):
        cases = ({'failure': 'implementation'}, {'failure': 'checks'}, {'mutation': True},
                 {'cleanup_failure': True}, {'reviews': [
                     {'verdict': 'changes_requested', 'findings': ['Still broken.']},
                     {'verdict': 'changes_requested', 'findings': ['Still broken.']}]})
        for index, options in enumerate(cases):
            with self.subTest(options=options):
                self.events = []
                self.descriptor['id'] = f'{index:032x}'
                # Each run uses an exclusive results directory.
                with self.assertRaises(ValueError):
                    self.execute(**options)
                self.assertIn('cleanup', self.events)
                self.assertNotIn(('git', 'push', f'--force-with-lease=refs/heads/{BRANCH}:'), self.events)
                self.assertNotIn(('gh', 'pr', 'create'), self.events)

    def test_worker_rejects_existing_clone_without_reset_or_network(self):
        with (mock.patch.object(job, 'WORKSPACE', self.workspace),
              mock.patch.dict(os.environ, {'SDLC_REPOSITORY': REPO}),
              mock.patch.object(job, 'pipeline_command') as command):
            with self.assertRaisesRegex(ValueError, 'existing checkout'):
                job.fresh_checkout(self.descriptor, self.results)
            command.assert_not_called()


if __name__ == '__main__':
    unittest.main()
