"""Offline checks for provider selection, model isolation and launcher arguments."""
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


def load(name, path):
    loader = importlib.machinery.SourceFileLoader(name, str(path))
    spec = importlib.util.spec_from_loader(name, loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


launcher = load('model_launcher', ROOT / 'scripts/sdlc.py')
job = load('model_job', ROOT / 'runtime/bin/sdlc-job')


class LauncherModelChecks(unittest.TestCase):
    def setUp(self):
        folder = tempfile.TemporaryDirectory()
        self.addCleanup(folder.cleanup)
        self.folder = Path(folder.name)
        self.profiles = self.folder / 'profiles.json'
        self.profile = {
            'github_login': 'example-user', 'git_name': 'Test User',
            'git_email': 'test@example.invalid', 'base_branch': 'main',
            'repositories': ['example-org/test-repo'],
            'signing_key_file': 'test-key', 'github_token_file': 'test-token',
            'ssh_public_key_file': 'test-key.pub',
        }
        self.ticket = self.folder / 'ticket.md'
        self.ticket.write_text('Synthetic ticket.')

    def call(self, action, *extra):
        self.profiles.write_text(json.dumps({'example': self.profile}))
        argv = ['sdlc.py', action, '--profiles', str(self.profiles),
                '--profile', 'example', '--repo', 'example-org/test-repo', *extra]
        output = io.StringIO()
        with (mock.patch.object(sys, 'argv', argv),
              mock.patch.object(launcher, 'ROOT', self.folder),
              mock.patch.object(launcher, 'secret', return_value='fake-test-secret') as secret,
              mock.patch.object(launcher.subprocess, 'run',
                                return_value=subprocess.CompletedProcess([], 0, stdout='')) as run,
              mock.patch.dict(os.environ, {'SDLC_MODEL': 'inherited-model',
                                           'GH_TOKEN': 'fake-inherited-token',
                                           'GITHUB_TOKEN': 'fake-inherited-token',
                                           'SDLC_SIGNING_KEY': 'fake-inherited-key',
                                           'SDLC_GITHUB_TOKEN': 'fake-inherited-token',
                                           'OPENAI_API_KEY': 'fake-inherited-api-key',
                                           'ANTHROPIC_API_KEY': 'fake-inherited-api-key',
                                           'CLAUDE_CODE_OAUTH_TOKEN': 'fake-inherited-oauth-token'}),
              contextlib.redirect_stdout(output)):
            launcher.main()
            if '--dry-run' in extra or action in ('login', 'auth-status'):
                secret.assert_not_called()
        compose = [call for call in run.call_args_list if call.args[0][:2] == ['docker', 'compose']]
        return compose[0] if compose else None, output.getvalue(), run

    def test_explicit_flag_overrides_profile_model(self):
        self.profile['model'] = 'profile-model'
        command, output, _ = self.call('cli', '--model', 'chosen-model')
        self.assertEqual(command.kwargs['env']['SDLC_MODEL'], 'chosen-model')
        self.assertIn('model: chosen-model', output)
        self.assertNotIn('fake-test-secret', output)
        self.assertNotIn('fake-inherited-token', output)

    def test_profile_model_is_used_for_each_instantiation(self):
        self.profile['model'] = 'profile-model'
        for action, extra in (
                ('cli', ()), ('exec', ('--branch', 'spike/test', '--ticket', str(self.ticket))),
                ('ssh-up', ())):
            with self.subTest(action=action):
                command, _, _ = self.call(action, *extra)
                self.assertEqual(command.kwargs['env']['SDLC_MODEL'], 'profile-model')

    def test_old_profile_clears_inherited_model_and_uses_codex_default(self):
        for action in ('cli', 'ssh-up'):
            with self.subTest(action=action):
                command, output, _ = self.call(action)
                self.assertEqual(command.kwargs['env']['SDLC_MODEL'], '')
                self.assertIn('model: Codex default', output)
                self.assertNotIn('inherited-model', output)
                if action == 'cli':
                    self.assertNotIn('--model', shlex.split(command.args[0][-1]))

    def test_interactive_model_is_one_safely_quoted_argument(self):
        model = "test;$(touch${IFS}/tmp/unused-test-model)'name"
        command, _, _ = self.call('cli', '--model', model)
        shell = command.args[0][-1]
        self.assertTrue(shell.startswith('sdlc-job init && exec '))
        argv = shlex.split(shell.removeprefix('sdlc-job init && exec '))
        self.assertEqual(argv[argv.index('--model') + 1], model)
        self.assertEqual(argv.count('--model'), 1)
        self.assertEqual(argv[0], 'codex')

    def test_codex_is_default_and_explicit_interactive_provider(self):
        self.profile['model'] = 'profile-model'
        for extra in ((), ('--provider', 'codex')):
            with self.subTest(extra=extra):
                command, _, _ = self.call('cli', *extra)
                argv = shlex.split(command.args[0][-1].removeprefix('sdlc-job init && exec '))
                self.assertEqual(argv, ['codex', '--dangerously-bypass-approvals-and-sandbox',
                                        '-C', '/workspace/repo', '--model', 'profile-model'])
                self.assertNotIn('-T', command.args[0])

    def test_claude_starts_fresh_in_repository_without_profile_codex_model(self):
        self.profile['model'] = 'profile-codex-model'
        command, output, _ = self.call('cli', '--provider', 'claude')
        shell = command.args[0][-1]
        prefix = 'sdlc-job init && cd /workspace/repo && exec '
        self.assertTrue(shell.startswith(prefix))
        self.assertEqual(shlex.split(shell.removeprefix(prefix)),
                         ['claude', '--dangerously-skip-permissions'])
        self.assertEqual(command.kwargs['env']['SDLC_MODEL'], '')
        self.assertNotIn('-T', command.args[0])
        self.assertIn('model: Claude default', output)
        self.assertNotIn('profile-codex-model', output)

    def test_claude_explicit_model_is_quoted_without_configuring_codex(self):
        self.profile['model'] = 'profile-codex-model'
        model = "claude-test;$(touch${IFS}/tmp/unused-test-model)'name"
        command, output, _ = self.call('cli', '--provider', 'claude', '--model', model)
        argv = shlex.split(command.args[0][-1].removeprefix(
            'sdlc-job init && cd /workspace/repo && exec '))
        self.assertEqual(argv, ['claude', '--dangerously-skip-permissions', '--model', model])
        self.assertEqual(command.kwargs['env']['SDLC_MODEL'], '')
        self.assertIn('model: ' + model, output)

    def test_login_keeps_provider_authentication_commands(self):
        for extra, expected in (
                ((), ['codex', 'login', '--device-auth']),
                (('--provider', 'codex'), ['codex', 'login', '--device-auth']),
                (('--provider', 'claude'), ['claude', 'auth', 'login'])):
            with self.subTest(extra=extra):
                command, _, _ = self.call('login', *extra)
                self.assertEqual(command.args[0][-len(expected):], expected)
                self.assertNotIn('-T', command.args[0])
                self.assertNotIn(str(self.folder / 'runtime/compose.credentials.yaml'), command.args[0])

    def test_auth_status_uses_selected_provider_without_credentials_or_tty(self):
        self.profile.pop('signing_key_file')
        self.profile.pop('github_token_file')
        for extra, expected in (
                ((), ['codex', 'login', 'status']),
                (('--provider', 'codex'), ['codex', 'login', 'status']),
                (('--provider', 'claude'), ['claude', 'auth', 'status', '--text'])):
            with self.subTest(extra=extra):
                command, output, _ = self.call('auth-status', *extra)
                self.assertEqual(command.args[0][-len(expected):], expected)
                self.assertIn('-T', command.args[0])
                self.assertNotIn(str(self.folder / 'runtime/compose.credentials.yaml'), command.args[0])
                for name in ('GH_TOKEN', 'GITHUB_TOKEN', 'SDLC_SIGNING_KEY', 'SDLC_GITHUB_TOKEN',
                             'OPENAI_API_KEY', 'ANTHROPIC_API_KEY', 'CLAUDE_CODE_OAUTH_TOKEN'):
                    self.assertNotIn(name, command.kwargs['env'])
                self.assertNotIn('fake-inherited', output)

    def test_explicit_provider_fails_for_unsupported_actions_before_profile_access(self):
        for action in ('build', 'init', 'exec', 'results', 'verify', 'publish',
                       'ssh-up', 'ssh-down', 'run'):
            for provider in ('codex', 'claude'):
                argv = ['sdlc.py', action, '--profile', 'example',
                        '--repo', 'example-org/test-repo', '--provider', provider]
                with (self.subTest(action=action, provider=provider),
                      mock.patch.object(sys, 'argv', argv),
                      mock.patch.object(launcher, 'selected_profile') as selected,
                      mock.patch.object(launcher.subprocess, 'run') as run,
                      mock.patch.object(launcher, 'secret') as secret,
                      contextlib.redirect_stderr(io.StringIO())):
                    with self.assertRaises(SystemExit) as raised:
                        launcher.main()
                    self.assertEqual(raised.exception.code, 2)
                    selected.assert_not_called()
                    run.assert_not_called()
                    secret.assert_not_called()

    def test_auth_status_respects_project_lock_before_docker_or_secret_access(self):
        self.profiles.write_text(json.dumps({'example': self.profile}))
        for provider in ('codex', 'claude'):
            argv = ['sdlc.py', 'auth-status', '--profiles', str(self.profiles),
                    '--profile', 'example', '--repo', 'example-org/test-repo',
                    '--provider', provider]
            with (self.subTest(provider=provider),
                  mock.patch.object(sys, 'argv', argv),
                  mock.patch.object(launcher.fcntl, 'flock', side_effect=BlockingIOError),
                  mock.patch.object(launcher.subprocess, 'run') as run,
                  mock.patch.object(launcher, 'secret') as secret,
                  contextlib.redirect_stdout(io.StringIO())):
                with self.assertRaisesRegex(ValueError, 'owns this Docker project'):
                    launcher.main()
                secret.assert_not_called()
                self.assertFalse(any(call.args[0][0] == 'docker' for call in run.call_args_list))

    def test_model_changes_do_not_change_workspace_volume_identity(self):
        first, _, _ = self.call('cli', '--model', 'first-model')
        second, _, _ = self.call('cli', '--model', 'second-model')
        self.assertEqual(first.args[0][3], second.args[0][3])

    def test_provider_changes_do_not_change_workspace_volume_identity(self):
        codex, _, _ = self.call('cli')
        claude, _, _ = self.call('cli', '--provider', 'claude')
        status, _, _ = self.call('auth-status', '--provider', 'claude')
        self.assertEqual(codex.args[0][3], claude.args[0][3])
        self.assertEqual(claude.args[0][3], status.args[0][3])

    def test_preview_reports_model_without_running_docker_or_reading_secrets(self):
        command, output, run = self.call('cli', '--model', 'chosen-model', '--dry-run')
        self.assertIsNone(command)
        self.assertIn('model: chosen-model', output)
        self.assertIn('--model chosen-model', output)
        self.assertFalse(any(call.args[0][0] == 'docker' for call in run.call_args_list))

    def test_invalid_models_are_rejected_in_flags_and_profiles(self):
        invalid_strings = ('', '-model', 'two models', 'model\nname', 'model\tname',
                           'model\0name', 'model\x7fname', 'model\x85name', 'model\u200bname')
        for model in invalid_strings:
            with self.subTest(flag=model), contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as raised:
                    self.call('cli', '--model=' + model)
                self.assertEqual(raised.exception.code, 2)
        for model in (*invalid_strings, None, False, 123, [], {}):
            with self.subTest(profile=model):
                self.profile['model'] = model
                with self.assertRaisesRegex(ValueError, 'Model'):
                    self.call('cli')


class RuntimeModelChecks(unittest.TestCase):
    def setUp(self):
        folder = tempfile.TemporaryDirectory()
        self.addCleanup(folder.cleanup)
        self.folder = Path(folder.name)
        self.ticket = self.folder / 'ticket.md'
        self.ticket.write_text('Synthetic ticket.')
        self.environment_file = self.folder / 'environment.json'

    def execute(self, model=None, stored_model=None):
        environment = {'SDLC_BASE_BRANCH': 'main'}
        if model is not None:
            environment['SDLC_MODEL'] = model
        if stored_model is not None:
            self.environment_file.write_text(json.dumps({'SDLC_MODEL': stored_model}))

        def runtime_path(path):
            if path == '/run/sdlc/environment.json':
                return self.environment_file
            if path == '/workspace/results':
                return self.folder / 'results'
            return Path(path)

        process = mock.Mock(stdin=io.StringIO(), stdout=io.StringIO(), stderr=io.StringIO())
        process.wait.return_value = 0
        argv = ['sdlc-job', 'exec', '--branch', 'spike/test', '--ticket', str(self.ticket)]
        with (mock.patch.object(sys, 'argv', argv),
              mock.patch.dict(os.environ, environment, clear=True),
              mock.patch.object(job, 'Path', side_effect=runtime_path),
              mock.patch.object(job, 'initialize', return_value=('example-org/test-repo', '')),
              mock.patch.object(job, 'clean'), mock.patch.object(job, 'valid_branch'),
              mock.patch.object(job, 'run'),
              mock.patch.object(job.subprocess, 'Popen', return_value=process) as popen,
              contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO())):
            job.main()
        return popen.call_args.args[0]

    def test_exec_receives_selected_model_as_an_argv_argument(self):
        model = 'chosen-model;$(unused-test-command)'
        command = self.execute(model)
        self.assertEqual(command[command.index('--model') + 1], model)
        self.assertEqual(command.count('--model'), 1)
        self.assertEqual(command[-1], '-')

    def test_exec_without_model_does_not_override_codex_default(self):
        for model in (None, ''):
            with self.subTest(model=model):
                self.assertNotIn('--model', self.execute(model))

    def test_stored_instantiation_model_overrides_shell_environment(self):
        command = self.execute('stale-shell-model', stored_model='container-model')
        self.assertEqual(command[command.index('--model') + 1], 'container-model')

    def test_invalid_runtime_model_is_rejected_before_repository_access(self):
        for model in ('-model', 'two models', 'model\nname', 'model\x7fname', 'model\u200bname'):
            with (self.subTest(model=model), mock.patch.dict(os.environ, {'SDLC_MODEL': model}),
                  mock.patch.object(sys, 'argv', ['sdlc-job', 'init']),
                  mock.patch.object(job.Path, 'exists', return_value=False),
                  mock.patch.object(job, 'initialize') as initialize):
                with self.assertRaisesRegex(ValueError, 'Model'):
                    job.main()
                initialize.assert_not_called()


if __name__ == '__main__':
    unittest.main()
