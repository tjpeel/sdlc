"""Offline checks for the connected test's execution and failure boundaries."""
import contextlib
import importlib.util
import io
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock


spec = importlib.util.spec_from_file_location('github_smoke', Path(__file__).with_name('github_smoke.py'))
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class ConnectedSmokeChecks(unittest.TestCase):
    def setUp(self):
        folder = tempfile.TemporaryDirectory()
        self.addCleanup(folder.cleanup)
        self.folder = Path(folder.name)
        self.profile = self.folder / 'profiles.local.json'
        self.body = self.folder / 'pr-body.md'
        self.ticket = self.folder / 'ticket.md'
        for path in (self.profile, self.body, self.ticket):
            path.write_text('Synthetic test input.')
        self.args = ['--profiles', str(self.profile), '--profile', 'test',
                     '--repo', 'example/test-repo', '--branch', 'spike/test',
                     '--title', 'Test container publication', '--body', str(self.body)]

    def call(self, args, side_effect=None):
        with mock.patch.object(smoke.subprocess, 'run', side_effect=side_effect) as run:
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                code = smoke.main(args)
        return code, run

    def test_plan_does_not_run_any_command_or_require_input_files(self):
        self.profile.unlink()
        self.body.unlink()
        code, run = self.call(self.args)
        self.assertEqual(code, 0)
        run.assert_not_called()

    def test_existing_branch_verifies_then_publishes_without_codex(self):
        code, run = self.call(self.args + ['--execute'])
        self.assertEqual(code, 0)
        actions = [call.args[0][2] for call in run.call_args_list]
        self.assertEqual(actions, ['verify', 'publish'])
        publish = run.call_args_list[-1].args[0]
        self.assertIn('--execute', publish)
        self.assertIn('--smoke-checks', publish)
        self.assertIn('--verify-published', publish)

    def test_fresh_ticket_failure_stops_before_verification_or_publication(self):
        failure = subprocess.CalledProcessError(1, ['codex'])
        code, run = self.call(self.args + ['--ticket', str(self.ticket), '--execute'], failure)
        self.assertEqual(code, 1)
        self.assertEqual(run.call_count, 1)
        self.assertEqual(run.call_args.args[0][2], 'exec')

    def test_signature_verification_failure_prevents_publication(self):
        failure = subprocess.CalledProcessError(1, ['verify'])
        code, run = self.call(self.args + ['--execute'], failure)
        self.assertEqual(code, 1)
        self.assertEqual(run.call_count, 1)
        self.assertEqual(run.call_args.args[0][2], 'verify')

    def test_fresh_ticket_completes_before_verification_and_publication(self):
        code, run = self.call(self.args + ['--ticket', str(self.ticket), '--execute'])
        self.assertEqual(code, 0)
        self.assertEqual([call.args[0][2] for call in run.call_args_list],
                         ['exec', 'verify', 'publish'])

    def test_model_override_is_forwarded_to_container_launcher(self):
        code, run = self.call(self.args + ['--ticket', str(self.ticket),
                                         '--model', 'chosen-model', '--execute'])
        self.assertEqual(code, 0)
        for call in run.call_args_list:
            command = call.args[0]
            self.assertEqual(command[command.index('--model') + 1], 'chosen-model')

    def test_remote_publication_failure_does_not_report_success(self):
        failure = subprocess.CalledProcessError(1, ['publish'])
        code, run = self.call(self.args + ['--execute'], [None, failure])
        self.assertEqual(code, 1)
        self.assertEqual(run.call_count, 2)

    def test_missing_pr_body_stops_before_any_execution(self):
        self.body.unlink()
        with self.assertRaises(SystemExit) as raised:
            self.call(self.args + ['--ticket', str(self.ticket), '--execute'])
        self.assertEqual(raised.exception.code, 2)


if __name__ == '__main__':
    unittest.main()
