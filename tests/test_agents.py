"""Offline provider checks using disposable CLI executables and synthetic output."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import signal
import sys
import tempfile
import textwrap
import time
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('sdlc_agents', ROOT / 'runtime/bin/sdlc_agents.py')
agents = importlib.util.module_from_spec(spec)
spec.loader.exec_module(agents)
IMPLEMENTATION = {'status': 'complete', 'title': 'Repair example', 'body': 'Fixed and tested.'}
REVIEW = {'verdict': 'pass', 'findings': []}


class AgentChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.workspace = self.root / 'repo'
        self.workspace.mkdir()
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        workspace_patch = patch.object(agents, 'WORKSPACE', self.workspace)
        workspace_patch.start()
        self.addCleanup(workspace_patch.stop)
        env_patch = patch.dict(os.environ, {
            'PATH': str(self.bin) + os.pathsep + os.environ.get('PATH', ''),
            'STUB_ROOT': str(self.root),
        })
        env_patch.start()
        self.addCleanup(env_patch.stop)

    def stub(self, provider, output=None, exit_code=0, source=None):
        if source is None:
            (self.root / 'response.json').write_text(json.dumps(output), encoding='utf-8')
            source = '''
                import json, os
                from pathlib import Path
                import sys
                root = Path(os.environ['STUB_ROOT'])
                args = sys.argv[1:]
                prompt = sys.stdin.read()
                (root / 'invocation.json').write_text(json.dumps({
                    'args': args, 'cwd': os.getcwd(), 'prompt': prompt}))
                response = (root / 'response.json').read_text()
                if '--output-last-message' in args:
                    Path(args[args.index('--output-last-message') + 1]).write_text(response)
                    print(json.dumps({'type': 'turn.completed', 'test': 'FAKE_PRIVATE_EVENT'}))
                else:
                    print(response)
                print('FAKE_PRIVATE_DIAGNOSTIC', file=sys.stderr)
                sys.exit(STUB_EXIT_CODE)
            '''.replace('STUB_EXIT_CODE', str(exit_code))
        executable = self.bin / provider
        executable.write_text('#!' + sys.executable + '\n' + textwrap.dedent(source),
                              encoding='utf-8')
        executable.chmod(0o700)

    def run_agent(self, provider='codex', model=None, role='implementation', timeout=5):
        return agents.run_agent(provider, model, 'SYNTHETIC_PRIVATE_PROMPT',
                                self.root / 'results', role, timeout_seconds=timeout)

    def claude_envelope(self, value):
        return {'type': 'result', 'subtype': 'success', 'is_error': False,
                'structured_output': value, 'result': 'An unused narrative summary.'}

    def test_codex_uses_schema_stdin_and_fresh_session(self):
        self.stub('codex', IMPLEMENTATION)
        output = io.StringIO()
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
            result = self.run_agent(model='example-model')
        self.assertEqual(output.getvalue(), '')
        self.assertEqual(result, {'provider': 'codex', 'model': 'example-model',
                                  'role': 'implementation', **IMPLEMENTATION})
        invocation = json.loads((self.root / 'invocation.json').read_text())
        args = invocation['args']
        self.assertEqual(args[0], 'exec')
        self.assertIn('--dangerously-bypass-approvals-and-sandbox', args)
        self.assertIn('--json', args)
        self.assertEqual(args[args.index('-C') + 1], str(self.workspace))
        self.assertEqual(args[args.index('--model') + 1], 'example-model')
        self.assertNotIn('resume', args)
        self.assertNotIn('--last', args)
        self.assertNotIn('SYNTHETIC_PRIVATE_PROMPT', args)
        self.assertEqual(invocation['prompt'], 'SYNTHETIC_PRIVATE_PROMPT')
        self.assertEqual(Path(invocation['cwd']).resolve(), self.workspace.resolve())
        schema = json.loads(Path(args[args.index('--output-schema') + 1]).read_text())
        self.assertEqual(schema, agents.SCHEMAS['implementation'])
        self.assertEqual(json.loads((self.root / 'results/result.json').read_text()), result)
        self.assertIn('FAKE_PRIVATE_EVENT', (self.root / 'results/events.jsonl').read_text())
        self.assertIn('FAKE_PRIVATE_DIAGNOSTIC', (self.root / 'results/stderr.log').read_text())
        self.assertEqual((self.root / 'results').stat().st_mode & 0o777, 0o700)
        for path in (self.root / 'results').iterdir():
            self.assertEqual(path.stat().st_mode & 0o777, 0o600, path.name)

    def test_claude_uses_structured_output_and_keeps_customization(self):
        self.stub('claude', self.claude_envelope(IMPLEMENTATION))
        result = self.run_agent('claude')
        self.assertEqual(result['status'], 'complete')
        args = json.loads((self.root / 'invocation.json').read_text())['args']
        self.assertEqual(args[0], '-p')
        self.assertIn('--dangerously-skip-permissions', args)
        self.assertEqual(args[args.index('--output-format') + 1], 'json')
        self.assertEqual(json.loads(args[args.index('--json-schema') + 1]),
                         agents.SCHEMAS['implementation'])
        self.assertNotIn('--model', args)
        for forbidden in ('--resume', '--continue', '--bare', '--safe-mode',
                          '--disable-slash-commands', '--tools'):
            self.assertNotIn(forbidden, args)

    def test_claude_review_exposes_read_and_skill_tools_only(self):
        self.stub('claude', self.claude_envelope(REVIEW))
        result = self.run_agent('claude', 'example-review-model', 'review')
        self.assertEqual(result['verdict'], 'pass')
        args = json.loads((self.root / 'invocation.json').read_text())['args']
        self.assertEqual(args[args.index('--tools') + 1], 'Read,Glob,Grep,Skill')
        self.assertEqual(args[args.index('--disallowedTools') + 1], 'mcp__*')
        self.assertEqual(args[args.index('--model') + 1], 'example-review-model')
        self.assertNotIn('--resume', args)
        self.assertNotIn('--continue', args)

    def test_claude_failure_envelopes_do_not_use_narrative_result(self):
        variants = [
            {'type': 'result', 'subtype': 'success', 'is_error': False,
             'result': json.dumps(IMPLEMENTATION)},
            {**self.claude_envelope(IMPLEMENTATION), 'is_error': True},
            {**self.claude_envelope(IMPLEMENTATION), 'subtype': 'error_max_turns'},
            {**self.claude_envelope(IMPLEMENTATION), 'is_error': 'false'},
            {**self.claude_envelope(IMPLEMENTATION), 'structured_output': None},
            {**self.claude_envelope(IMPLEMENTATION), 'structured_output': json.dumps(IMPLEMENTATION)},
        ]
        for index, value in enumerate(variants):
            self.stub('claude', value)
            with self.subTest(index=index), self.assertRaises(agents.AgentError):
                agents.run_agent('claude', None, 'example prompt',
                                 self.root / f'results-{index}', 'implementation')

    def test_nonzero_exit_rejects_valid_output_and_keeps_errors_private(self):
        for provider in ('codex', 'claude'):
            value = IMPLEMENTATION if provider == 'codex' else self.claude_envelope(IMPLEMENTATION)
            self.stub(provider, value, exit_code=3)
            with self.subTest(provider=provider), self.assertRaises(agents.AgentError) as caught:
                agents.run_agent(provider, None, 'example prompt',
                                 self.root / f'results-{provider}', 'implementation')
            self.assertNotIn('FAKE_PRIVATE', str(caught.exception))
            self.assertNotIn('example prompt', str(caught.exception))

    def test_schema_rejects_unknown_values_types_and_empty_text(self):
        variants = [
            ('implementation', {**IMPLEMENTATION, 'status': 'in_progress'}),
            ('implementation', {**IMPLEMENTATION, 'title': '  '}),
            ('implementation', {**IMPLEMENTATION, 'body': None}),
            ('implementation', {**IMPLEMENTATION, 'body': ''}),
            ('implementation', {**IMPLEMENTATION, 'extra': True}),
            ('implementation', {'status': 'complete', 'title': 'Title'}),
            ('review', {'verdict': 'pass', 'findings': ['Unresolved issue.']}),
            ('review', {'verdict': 'approved', 'findings': []}),
            ('review', {'verdict': 'changes_requested', 'findings': 'Issue'}),
            ('review', {'verdict': 'changes_requested', 'findings': [3]}),
            ('review', {'verdict': 'changes_requested', 'findings': ['  ']}),
        ]
        for index, (role, value) in enumerate(variants):
            self.stub('codex', value)
            with self.subTest(index=index), self.assertRaises(agents.AgentError):
                agents.run_agent('codex', None, 'example prompt',
                                 self.root / f'results-{index}', role)

    def test_blocked_implementation_and_changes_requested_are_valid(self):
        self.stub('codex', {**IMPLEMENTATION, 'status': 'blocked'})
        self.assertEqual(self.run_agent()['status'], 'blocked')
        review = {'verdict': 'changes_requested', 'findings': ['Fix the missing boundary check.']}
        self.stub('claude', self.claude_envelope(review))
        result = agents.run_agent('claude', None, 'example prompt', self.root / 'review', 'review')
        self.assertEqual(result['findings'], review['findings'])

    def test_malformed_or_missing_json_is_rejected(self):
        for index, provider in enumerate(('codex', 'claude')):
            self.stub(provider, source='import sys\nsys.stdin.read()\nprint("not JSON")\n')
            with self.subTest(provider=provider), self.assertRaises(agents.AgentError):
                agents.run_agent(provider, None, 'example prompt',
                                 self.root / f'results-{index}', 'implementation')

    def test_arguments_are_validated_before_launch(self):
        with patch.object(agents.subprocess, 'Popen') as launch:
            for provider in ('other', None, []):
                with self.subTest(provider=provider), self.assertRaises(agents.AgentError):
                    self.run_agent(provider)
            for model in ('-option', 'two models', 'line\nbreak', 'control\x00',
                          'control\x7f', 'invisible\u200b', 3):
                with self.subTest(model=model), self.assertRaises(agents.AgentError):
                    self.run_agent(model=model)
            for timeout in (0, -1, True, float('nan'), float('inf'), '5'):
                with self.subTest(timeout=timeout), self.assertRaises(agents.AgentError):
                    self.run_agent(timeout=timeout)
            with self.assertRaises(agents.AgentError):
                self.run_agent(role='other')
            with self.assertRaises(agents.AgentError):
                agents.run_agent('codex', None, '  ', self.root / 'results', 'review')
            launch.assert_not_called()

    def test_results_cannot_be_written_into_checkout_or_reused(self):
        self.stub('codex', IMPLEMENTATION)
        with self.assertRaises(agents.AgentError):
            agents.run_agent('codex', None, 'example prompt', self.workspace / 'results', 'review')
        self.run_agent()
        with self.assertRaises(agents.AgentError):
            self.run_agent()

    def test_timeout_kills_children_that_ignore_termination(self):
        self.stub('codex', source='''
            import os, signal, subprocess, sys, time
            from pathlib import Path
            root = Path(os.environ['STUB_ROOT'])
            child = subprocess.Popen([sys.executable, '-c', ''' + repr('''
import os, signal, time
from pathlib import Path
signal.signal(signal.SIGTERM, signal.SIG_IGN)
path = Path(os.environ['STUB_ROOT']) / 'heartbeat'
while True:
    path.write_text(str(time.monotonic()))
    time.sleep(0.02)
''') + '''])
            (root / 'child-pid').write_text(str(child.pid))
            sys.stdin.read()
            time.sleep(30)
        ''')
        with self.assertRaisesRegex(agents.AgentError, 'time limit'):
            self.run_agent(timeout=0.4)
        self.assertTrue((self.root / 'child-pid').is_file())
        first = (self.root / 'heartbeat').read_text()
        time.sleep(0.15)
        self.assertEqual((self.root / 'heartbeat').read_text(), first)

    def test_interrupt_terminates_session_and_restores_signal_handlers(self):
        self.stub('codex', source='''
            import os, signal, sys, time
            from pathlib import Path
            root = Path(os.environ['STUB_ROOT'])
            (root / 'agent-pid').write_text(str(os.getpid()))
            sys.stdin.read()
            time.sleep(30)
        ''')
        original = signal.getsignal(signal.SIGTERM)
        communicate = agents.subprocess.Popen.communicate

        def interrupt(process, *args, **kwargs):
            # Exercise the installed signal handler without signalling the test runner.
            deadline = time.monotonic() + 2
            while not (self.root / 'agent-pid').exists() and time.monotonic() < deadline:
                time.sleep(0.01)
            signal.getsignal(signal.SIGTERM)(signal.SIGTERM, None)
            return communicate(process, *args, **kwargs)

        with patch.object(agents.subprocess.Popen, 'communicate', interrupt):
            with self.assertRaisesRegex(agents.AgentError, 'interrupted'):
                self.run_agent()
        self.assertEqual(signal.getsignal(signal.SIGTERM), original)
        pid = int((self.root / 'agent-pid').read_text())
        with self.assertRaises(ProcessLookupError):
            os.kill(pid, 0)


if __name__ == '__main__':
    unittest.main()
