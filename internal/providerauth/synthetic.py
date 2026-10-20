"""Synthetic through the pinned, unmodified OpenCode client.

Loaded before container.py in the same namespace. HTTP below reaches only the
invocation's authenticated loopback OpenCode server, never an inference API.
"""
import base64
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

OPENCODE_CATALOGUES = Path('/opt/sdlc/opencode')
SYNTHETIC_MODEL = 'hf:moonshotai/Kimi-K3'
SYNTHETIC_RESPONSE_LIMIT = 16 * 1024 * 1024


def synthetic_environment(home, config, model=SYNTHETIC_MODEL, permission='allow', instructions=None):
    env = interactive_environment(home)
    env.update({'XDG_DATA_HOME': str(config.parent),
                'XDG_CONFIG_HOME': str(Path(home) / '.config'),
                'XDG_CACHE_HOME': str(Path(home) / '.cache'),
                'XDG_STATE_HOME': str(Path(home) / '.state'),
                'OPENCODE_DISABLE_AUTOUPDATE': 'true',
                'OPENCODE_DISABLE_PROJECT_CONFIG': 'true',
                'OPENCODE_PURE': 'true',
                'OPENCODE_DISABLE_DEFAULT_PLUGINS': 'true',
                'OPENCODE_DISABLE_CLAUDE_CODE': 'true',
                'OPENCODE_DISABLE_EXTERNAL_SKILLS': 'true',
                'OPENCODE_CONFIG_DIR': str(Path(home) / '.config' / 'opencode'),
                'OPENCODE_DISABLE_MODELS_FETCH': 'true',
                'OPENCODE_DISABLE_LSP_DOWNLOAD': 'true'})
    settings = {'enabled_providers': ['synthetic'], 'model': 'synthetic/' + model,
                'small_model': 'synthetic/' + model, 'share': 'disabled',
                'autoupdate': False, 'plugin': [], 'mcp': {}, 'lsp': False,
                'formatter': False, 'permission': permission if isinstance(permission, dict)
                else {'*': permission, 'question': 'deny'}}
    if instructions is not None:
        settings['instructions'] = [str(instructions)]
    env['OPENCODE_CONFIG_CONTENT'] = json.dumps(settings)
    return env


def synthetic_catalogues(home):
    target = Path(home) / '.config' / 'opencode'
    target.mkdir(parents=True, mode=0o700)
    for name in ('skills', 'agents'):
        source = OPENCODE_CATALOGUES / name
        if not source.is_dir():
            raise ValueError('missing pinned OpenCode catalogue')
        (target / name).symlink_to(source, target_is_directory=True)


def synthetic_status(config, env, home):
    # The native client interprets its credential store; SDLC never extracts keys.
    result = subprocess.run(['opencode', 'auth', 'list'], env=env, cwd=home,
                            capture_output=True, text=True, timeout=30)
    if result.returncode or len(result.stdout) + len(result.stderr) > LIMIT:
        return 'invalid'
    import re
    output = re.sub(r'\x1b\[[0-?]*[ -/]*[@-~]', '', result.stdout + result.stderr)
    return 'stored' if re.search(r'\bSynthetic\b', output, re.IGNORECASE) else 'missing'


def synthetic_auth(action, directory):
    data = credential(directory / FILES['synthetic'], os.getuid(), allow_invalid=action == 'login')
    if action in ('status', 'logout') and data is None:
        if action == 'status':
            print(json.dumps({'state': 'missing'}))
        return 0
    with tempfile.TemporaryDirectory(prefix='sdlc-opencode-auth-', dir=HOME_BASE) as home:
        config = Path(home) / 'data' / 'opencode'
        config.mkdir(parents=True, mode=0o700)
        env = synthetic_environment(home, config)
        if action == 'status':
            persist(config, FILES['synthetic'], data)
            print(json.dumps({'state': synthetic_status(config, env, home)}))
            return 0
        if action == 'logout':
            snapshot_auth(config, 'synthetic', directory)
            result = subprocess.run(['opencode', 'auth', 'logout', 'synthetic'],
                                     env=env, cwd=home, capture_output=True, timeout=30)
            if result.returncode or synthetic_status(config, env, home) != 'missing':
                raise ValueError('native Synthetic logout failed')
            finish_snapshot(config, 'synthetic', directory)
            return 0
        snapshot_auth(config, 'synthetic', directory, login=True)
        try:
            result = subprocess.run(['opencode', 'auth', 'login', '--provider', 'synthetic'],
                                    env=env, cwd=home)
            if result.returncode or synthetic_status(config, env, home) != 'stored':
                return 1
            # Reject permissive or redirected native credential files before persistence.
            if credential(config / FILES['synthetic'], os.getuid()) is None:
                return 1
            finish_snapshot(config, 'synthetic', directory)
            return 0
        finally:
            if (config / BASELINE).exists():
                # Use the common private recovery boundary on failed native login.
                with private_config(home, 'synthetic', directory) as recovery:
                    for name in (BASELINE, FILES['synthetic']):
                        data = opaque_cache(config / name)
                        if data is not None:
                            persist(recovery, name, data)


def synthetic_interactive(directory, mode):
    require_session_instructions()
    with tempfile.TemporaryDirectory(prefix='sdlc-opencode-', dir=HOME_BASE) as home:
        config = Path(home) / 'data' / 'opencode'
        config.mkdir(parents=True, mode=0o700)
        synthetic_catalogues(home)
        env = synthetic_environment(home, config, permission=mode, instructions=SESSION_INSTRUCTIONS)
        snapshot_auth(config, 'synthetic', directory)
        try:
            return interactive_process(['opencode'], env)
        finally:
            finish_snapshot(config, 'synthetic', directory)


class SyntheticServer:
    """One local native server, authenticated even inside its isolated container."""
    def __init__(self, env, cwd):
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            self.port = listener.getsockname()[1]
        password = secrets.token_urlsafe(32)
        env = dict(env, OPENCODE_SERVER_PASSWORD=password)
        encoded = base64.b64encode(('opencode:' + password).encode()).decode()
        self.authorization = str.join(' ', ('Basic', encoded))
        self.base = 'http://127.0.0.1:' + str(self.port)
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        self.process = subprocess.Popen(['opencode', 'serve', '--hostname', '127.0.0.1',
                                         '--port', str(self.port)], env=env, cwd=cwd,
                                        stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                        stderr=subprocess.DEVNULL, start_new_session=True)

    def request(self, method, path, value=None, timeout=None):
        data = None if value is None else json.dumps(value).encode()
        request = urllib.request.Request(self.base + path, data=data, method=method,
                    headers={'Authorization': self.authorization, 'Content-Type': 'application/json'})
        with self.opener.open(request, timeout=timeout) as response:
            raw = response.read(SYNTHETIC_RESPONSE_LIMIT + 1)
        if len(raw) > SYNTHETIC_RESPONSE_LIMIT:
            raise ValueError('native OpenCode response exceeds bound')
        return json.loads(raw)

    def ready(self):
        # Readiness is setup, not a model-work or repair budget.
        for _ in range(300):
            if self.process.poll() is not None:
                raise ValueError('native OpenCode server exited')
            try:
                result = self.request('GET', '/global/health', timeout=1)
                if result.get('healthy') is True:
                    self.version = result.get('version')
                    return
            except (OSError, ValueError):
                pass
            time.sleep(0.1)
        raise ValueError('native OpenCode server did not become ready')

    def close(self):
        try:
            os.killpg(self.process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            os.killpg(self.process.pid, signal.SIGKILL)
            self.process.wait()


class SyntheticMetrics:
    """Discard transcript text; retain numeric root-message checkpoints only."""
    def __init__(self, server, session_id, model):
        self.server, self.session_id, self.model = server, session_id, model
        self.messages, self.compactions = {}, set()
        self.connected = threading.Event()
        self.stop = threading.Event()
        self.response = None
        self.complete, self.mismatch = True, False
        self.drained_ok = False
        self.lock = threading.Lock()
        self.thread = threading.Thread(target=self.read, daemon=True)

    def observe(self, event):
        properties = event.get('properties', {})
        if event.get('type') == 'server.connected':
            self.connected.set()
        if event.get('type') == 'session.created':
            if properties.get('info', {}).get('parentID') == self.session_id:
                # Delegates are not included in root numeric checkpoints.
                self.complete = False
        if event.get('type') == 'session.compacted' and properties.get('sessionID') == self.session_id:
            identity = event.get('id')
            if isinstance(identity, str) and len(self.compactions) < 8192:
                self.compactions.add(identity)
        if event.get('type') != 'message.updated' or properties.get('sessionID') != self.session_id:
            return
        info = properties.get('info', {})
        if info.get('role') != 'assistant':
            return
        if info.get('providerID') != 'synthetic' or info.get('modelID') != self.model:
            self.mismatch = True
        identity = info.get('id')
        tokens = info.get('tokens')
        if not isinstance(identity, str) or not 1 <= len(identity) <= 256 or not isinstance(tokens, dict):
            self.complete = False
            return
        if not info.get('time', {}).get('completed'):
            return
        cache = tokens.get('cache', {})
        numeric = [tokens.get('input'), cache.get('read'), cache.get('write'),
                   tokens.get('output'), tokens.get('reasoning')]
        if any(type(value) is not int or not 0 <= value <= 1_000_000_000_000 for value in numeric):
            self.complete = False
            return
        if len(self.messages) >= 8192 and identity not in self.messages:
            self.complete = False
            return
        self.messages[identity] = numeric

    def read(self):
        request = urllib.request.Request(self.server.base + '/event',
                        headers={'Authorization': self.server.authorization})
        try:
            with self.server.opener.open(request, timeout=20) as response:
                self.response = response
                while not self.stop.is_set():
                    line = response.readline(8 * 1024 * 1024 + 1)
                    if not line or len(line) > 8 * 1024 * 1024:
                        if not self.stop.is_set():
                            self.complete = False
                        break
                    if line.startswith(b'data: '):
                        event = json.loads(line[6:])
                        with self.lock:
                            self.observe(event)
        except (OSError, ValueError):
            if not self.stop.is_set():
                self.complete = False

    def start(self):
        self.thread.start()
        if not self.connected.wait(timeout=5):
            self.complete = False

    def finish(self):
        self.stop.set()
        # Server shutdown closes its SSE connection before joining this reader.
        self.thread.join(timeout=2)
        if self.thread.is_alive():
            self.complete = False
        with self.lock:
            totals = [sum(row[i] for row in self.messages.values()) for i in range(5)]
            print(json.dumps({'type': 'sdlc.synthetic_metrics', 'provider': 'synthetic',
                  'client_version': getattr(self.server, 'version', None),
                  'model': self.model if self.messages and not self.mismatch else '', 'session_id': self.session_id,
                  'complete': self.complete and self.drained_ok and bool(self.messages),
                  'compactions': len(self.compactions),
                  'usage': dict(zip(('input_tokens', 'cached_input_tokens', 'cache_creation_input_tokens',
                                    'output_tokens', 'reasoning_output_tokens'), totals))}), flush=True)

    def drained(self, identity):
        # Seeing the final completed message on the ordered stream establishes
        # that its preceding root checkpoints have also been consumed.
        for _ in range(100):
            with self.lock:
                if identity in self.messages:
                    self.drained_ok = True
                    return
            if not self.thread.is_alive():
                break
            time.sleep(0.05)
        self.complete = False


def synthetic_headless(model, effort, resume, readonly, directory, session, prompt, schema, instructions, footprint=None):
    if effort != 'default' or os.environ.get('SDLC_NATIVE_SPEED', '') not in ('', 'normal'):
        raise ValueError('unsupported Synthetic reasoning or speed selection')
    if os.environ.get('SDLC_HEADROOM') == '1':
        raise ValueError('Synthetic requires its direct native route')
    os.umask(0o077)
    if credential(directory / FILES['synthetic'], os.getuid()) is None:
        raise ValueError('missing Synthetic authentication')
    config = session / 'synthetic' / 'opencode'
    for path in (session / 'synthetic', config):
        if path.is_symlink():
            raise ValueError('unsafe OpenCode session directory')
        path.mkdir(mode=0o700, exist_ok=True)
        if path.stat().st_uid != os.getuid():
            raise ValueError('unsafe OpenCode session directory')
        path.chmod(0o700)
    with tempfile.TemporaryDirectory(prefix='sdlc-opencode-', dir=HOME_BASE) as home:
        synthetic_catalogues(home)
        if footprint is not None:
            footprint('synthetic', OPENCODE_CATALOGUES / 'skills', OPENCODE_CATALOGUES / 'agents', instructions, schema, prompt)
        permission = {'*': 'deny', 'read': 'allow', 'glob': 'allow', 'grep': 'allow', 'list': 'allow'} if readonly else 'allow'
        env = synthetic_environment(home, config, model, permission=permission,
                                    instructions=instructions if instructions.exists() else None)
        snapshot_auth(config, 'synthetic', directory)
        server = None
        metrics = None
        handlers = {}
        def interrupted(signum, frame):
            raise InterruptedError('native OpenCode session interrupted')
        try:
            for number in (signal.SIGTERM, signal.SIGINT):
                handlers[number] = signal.signal(number, interrupted)
            server = SyntheticServer(env, WORKSPACE)
            server.ready()
            if resume:
                saved = server.request('GET', '/session/' + resume)
                if saved.get('id') != resume or saved.get('directory') != str(WORKSPACE):
                    raise ValueError('native OpenCode session does not match workspace')
                session_id = resume
            else:
                saved = server.request('POST', '/session', {'title': 'SDLC implementation'})
                session_id = saved['id']
            import re
            if not isinstance(session_id, str) or not re.fullmatch(r'ses_[A-Za-z0-9]{1,128}', session_id):
                raise ValueError('invalid native OpenCode session ID')
            # Initialization records observed identity only after the native response.
            print(json.dumps({'type': 'system', 'subtype': 'init', 'provider': 'synthetic', 'session_id': session_id}), flush=True)
            metrics = SyntheticMetrics(server, session_id, model)
            metrics.start()
            body = {'model': {'providerID': 'synthetic', 'modelID': model},
                    'agent': 'build', 'format': {'type': 'json_schema', 'schema': json.loads(schema.read_text())},
                    'parts': [{'type': 'text', 'text': prompt.read_text()}]}
            print(json.dumps({'type': 'sdlc.native_work_boundary', 'provider': 'synthetic', 'phase': 'start'}), flush=True)
            try:
                response = server.request('POST', '/session/' + session_id + '/message', body)
            finally:
                print(json.dumps({'type': 'sdlc.native_work_boundary', 'provider': 'synthetic', 'phase': 'end'}), flush=True)
            info = response.get('info', {})
            if info.get('sessionID') != session_id or info.get('role') != 'assistant':
                raise ValueError('native OpenCode response has the wrong session')
            if info.get('providerID') != 'synthetic' or info.get('modelID') != model:
                raise ValueError('native OpenCode reported a different inference selection')
            if metrics.mismatch:
                raise ValueError('native OpenCode changed its inference selection')
            if info.get('error') is not None:
                name = info['error'].get('name') if isinstance(info['error'], dict) else None
                category = {'APIError': 'native_api_error', 'MessageAbortedError': 'interrupted',
                            'StructuredOutputError': 'structured_output_failed',
                            'ProviderAuthError': 'authentication_failed'}.get(name, 'native_failure')
                print(json.dumps({'type': 'error', 'provider': 'synthetic', 'category': category}), flush=True)
                raise ValueError('native OpenCode returned an error')
            if not info.get('finish') or info.get('structured') is None:
                raise ValueError('native OpenCode did not complete structured output')
            metrics.drained(info.get('id'))
            with metrics.lock:
                if metrics.mismatch:
                    raise ValueError('native OpenCode changed its inference selection')
            print(json.dumps({'type': 'result', 'subtype': 'success', 'provider': 'synthetic', 'session_id': session_id,
                              'model': info['modelID'], 'is_error': False,
                              'structured_output': info['structured']}), flush=True)
            return 0
        finally:
            try:
                if metrics is not None:
                    metrics.stop.set()
                if server is not None:
                    server.close()
            finally:
                if metrics is not None:
                    metrics.finish()
                for number, previous in handlers.items():
                    signal.signal(number, previous)
                finish_snapshot(config, 'synthetic', directory)
