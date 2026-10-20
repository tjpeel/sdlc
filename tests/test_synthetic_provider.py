"""Offline OpenCode boundary tests. No provider service is contacted."""
import contextlib
import io
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1] / 'internal' / 'providerauth'
native = types.ModuleType('synthetic_fixture')
exec((ROOT / 'synthetic.py').read_text(), native.__dict__)
exec((ROOT / 'container.py').read_text(), native.__dict__)


class SyntheticTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        previous = os.umask(0o077)
        self.addCleanup(os.umask, previous)
        self.directory = self.root / 'auth'
        self.directory.mkdir(mode=0o700)
        self.data = json.dumps({'synthetic': {'type': 'api', 'key': 'fake-' + secrets.token_hex(16)}}).encode()
        native.persist(self.directory, 'auth.json', self.data)
        self.enterContext(patch.object(native, 'HOME_BASE', str(self.root)))

    def test_native_login_persists_only_selected_private_cache(self):
        replacement = json.dumps({'synthetic': {'type': 'api', 'key': 'fake-' + secrets.token_hex(16)}}).encode()
        commands = []
        def run(command, **kwargs):
            commands.append(command)
            env = kwargs['env']
            self.assertNotIn('ANTHROPIC_AUTH_TOKEN', env)
            self.assertNotIn('SYNTHETIC_API_KEY', env)
            self.assertTrue(env['OPENCODE_DISABLE_PROJECT_CONFIG'])
            config = Path(env['XDG_DATA_HOME']) / 'opencode'
            if command[2] == 'login':
                native.persist(config, 'auth.json', replacement)
            return subprocess.CompletedProcess(command, 0, '\x1b[32mSynthetic\x1b[0m api\n', '')
        with patch.object(native.subprocess, 'run', side_effect=run):
            self.assertEqual(native.synthetic_auth('login', self.directory), 0)
        self.assertEqual(commands, [['opencode', 'auth', 'login', '--provider', 'synthetic'], ['opencode', 'auth', 'list']])
        self.assertEqual((self.directory / 'auth.json').read_bytes(), replacement)
        self.assertEqual((self.directory / 'auth.json').stat().st_mode & 0o777, 0o600)

    def test_status_is_offline_and_leaves_persistent_state_unchanged(self):
        before = {p.name: p.read_bytes() for p in self.directory.iterdir()}
        with patch.object(native.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, 'synthetic api\n', '')), contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(native.synthetic_auth('status', self.directory), 0)
        self.assertEqual(json.loads(output.getvalue()), {'state': 'stored'})
        self.assertEqual({p.name: p.read_bytes() for p in self.directory.iterdir()}, before)

    def test_wrong_native_account_does_not_establish_synthetic_login(self):
        with patch.object(native.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, 'Another provider api\n', '')):
            self.assertEqual(native.synthetic_status(self.directory, {}, self.root), 'missing')

    def test_native_logout_removes_entry_without_exposing_key(self):
        commands = []
        def run(command, **kwargs):
            commands.append(command)
            if command[2] == 'logout':
                native.persist(Path(kwargs['env']['XDG_DATA_HOME']) / 'opencode', 'auth.json', b'{}')
            return subprocess.CompletedProcess(command, 0, '0 credentials\n', '')
        with patch.object(native.subprocess, 'run', side_effect=run):
            self.assertEqual(native.synthetic_auth('logout', self.directory), 0)
        self.assertEqual(commands, [['opencode', 'auth', 'logout', 'synthetic'], ['opencode', 'auth', 'list']])
        self.assertEqual((self.directory / 'auth.json').read_bytes(), b'{}')

    def test_root_metrics_replace_updates_and_exclude_delegate_totals(self):
        metrics = native.SyntheticMetrics(None, 'ses_root', 'hf:moonshotai/Kimi-K3')
        def message(identity, input_tokens, session='ses_root', model='hf:moonshotai/Kimi-K3'):
            return {'type': 'message.updated', 'properties': {'sessionID': session, 'info': {
                'id': identity, 'role': 'assistant', 'providerID': 'synthetic', 'modelID': model,
                'time': {'completed': 1}, 'tokens': {'input': input_tokens, 'output': 4,
                'reasoning': 1, 'cache': {'read': 3, 'write': 0}}}}}
        metrics.observe(message('msg_one', 8))
        metrics.observe(message('msg_one', 10))
        metrics.observe(message('msg_two', 5))
        metrics.observe(message('msg_child', 100, session='ses_child'))
        self.assertEqual(sum(row[0] for row in metrics.messages.values()), 15)
        metrics.observe({'type': 'session.created', 'properties': {'info': {'parentID': 'ses_root'}}})
        self.assertFalse(metrics.complete)
        metrics.observe(message('msg_wrong', 1, model='other-model'))
        self.assertTrue(metrics.mismatch)

    def test_strict_outcome_and_original_session_are_preserved(self):
        self._structured_probe(False)

    def test_readonly_execution_denies_mutation_and_delegation(self):
        self._structured_probe(True)

    def _structured_probe(self, readonly):
        session = self.root / 'session'
        session.mkdir()
        schema = self.root / 'schema.json'
        schema.write_text(json.dumps({'type': 'object', 'required': ['status']}))
        prompt = self.root / 'prompt'
        prompt.write_text('Fixture work')
        workspace = self.root / 'workspace'
        workspace.mkdir()
        requests = []
        environments = []
        class Server:
            def __init__(self, env, cwd): environments.append(env)
            def ready(self): pass
            def close(self): pass
            def request(self, method, path, value=None):
                requests.append((method, path, value))
                if path == '/session/ses_original':
                    return {'id': 'ses_original', 'directory': str(workspace)}
                return {'info': {'sessionID': 'ses_original', 'role': 'assistant',
                    'providerID': 'synthetic', 'modelID': 'hf:moonshotai/Kimi-K3',
                    'finish': 'stop', 'structured': {'status': 'implemented'}}}
        class Metrics:
            import threading
            lock = threading.Lock()
            mismatch = False
            def __init__(self, *args): pass
            stop = type("Stop", (), {"set": lambda self: None})()
            def drained(self, identity): pass
            def start(self): pass
            def finish(self): pass
        with patch.object(native, 'SyntheticServer', Server), patch.object(native, 'SyntheticMetrics', Metrics), patch.object(native, 'synthetic_catalogues'), patch.object(native, 'WORKSPACE', workspace), contextlib.redirect_stdout(io.StringIO()) as output:
            result = native.synthetic_headless('hf:moonshotai/Kimi-K3', 'default', 'ses_original', readonly, self.directory, session, prompt, schema, self.root / 'absent')
        self.assertEqual(result, 0)
        permissions = json.loads(environments[0]["OPENCODE_CONFIG_CONTENT"])["permission"]
        self.assertEqual(permissions["*"], "deny" if readonly else "allow")
        if readonly:
            self.assertEqual(permissions["read"], "allow")
            for tool in ("bash", "edit", "write", "task"):
                self.assertNotIn(tool, permissions)
        self.assertEqual(requests[0][:2], ('GET', '/session/ses_original'))
        body = requests[1][2]
        self.assertEqual(body['format'], {'type': 'json_schema', 'schema': json.loads(schema.read_text())})
        events = [json.loads(line) for line in output.getvalue().splitlines()]
        final = [e for e in events if e['type'] == 'result'][0]
        self.assertEqual(final['session_id'], 'ses_original')
        self.assertEqual(final['structured_output'], {'status': 'implemented'})
        self.assertEqual((self.directory / 'auth.json').read_bytes(), self.data)
        self.assertFalse((session / 'synthetic/opencode/auth.json').exists())

    def test_repository_redirect_in_session_storage_is_rejected(self):
        session = self.root / 'session'
        session.mkdir()
        (session / 'synthetic').symlink_to(self.root)
        with self.assertRaises(ValueError):
            native.synthetic_headless('hf:moonshotai/Kimi-K3', 'default', '', False, self.directory, session, self.root / 'prompt', self.root / 'schema', self.root / 'instructions')


if __name__ == '__main__':
    unittest.main()
