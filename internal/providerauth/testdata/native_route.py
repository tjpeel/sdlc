import base64
import json
import os
import pathlib
import subprocess
import tempfile
import threading
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

captured = []

class Handler(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    def log_message(self, *args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', '0'))))
        captured.append((self.path, dict(self.headers), body))
        self.send_response(429)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(json.dumps({'error': {'type': 'rate_limit_error', 'message': 'offline fixture quota'}}).encode())

    def do_GET(self):
        if self.path == '/v1/responses':
            captured.append((self.path, dict(self.headers), {}))
        self.send_response(404)
        self.send_header('Content-Length', '0')
        self.end_headers()

server = ThreadingHTTPServer(('127.0.0.1', 8787), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()

def encode(value):
    return base64.urlsafe_b64encode(json.dumps(value).encode()).decode().rstrip('=')

with tempfile.TemporaryDirectory() as directory:
    home = pathlib.Path(directory)
    config = home / 'codex'
    config.mkdir(mode=0o700)
    workspace = home / 'workspace'
    workspace.mkdir()
    fake_jwt = '.'.join((encode({'alg': 'none'}), encode({'sub': 'fake', 'exp': 4102444800,
        'https://api.openai.com/auth': {'chatgpt_account_id': 'fake-account', 'chatgpt_plan_type': 'plus'}}), 'fake-signature'))
    auth = {'auth_mode': 'chatgpt', 'tokens': {'id_token': fake_jwt, 'access_token': 'fake-access', 'refresh_token': 'fake-refresh', 'account_id': 'fake-account'}, 'last_refresh': datetime.now(timezone.utc).isoformat()}
    path = config / 'auth.json'
    path.write_text(json.dumps(auth))
    path.chmod(0o600)
    env = {'PATH': os.environ['PATH'], 'HOME': str(home), 'CODEX_HOME': str(config), 'OPENAI_BASE_URL': 'http://127.0.0.1:8787/v1'}
    version = subprocess.check_output(['codex', '--version'], env=env, text=True).strip()
    assert version == 'codex-cli 0.160.0', version
    command = ['codex', 'exec', '--skip-git-repo-check', '--json', '--model', 'gpt-5.4', '--ignore-rules',
        '-c', 'cli_auth_credentials_store="file"', '-c', 'approval_policy="never"', '-c', 'sandbox_mode="danger-full-access"',
        '-c', 'openai_base_url="http://127.0.0.1:8787/v1"', '-c', 'mcp_servers={}', '-c', 'features.hooks=false', '-c', 'features.plugins=false', '-']
    try:
        result = subprocess.run(command, input='Offline fixture. Return fixture only.', cwd=workspace, env=env, capture_output=True, text=True, timeout=25)
    except subprocess.TimeoutExpired as error:
        result = subprocess.CompletedProcess(command, -1, (error.stdout or b'').decode(), (error.stderr or b'').decode())
    assert captured, ('native client did not send a request to the fake inference endpoint', result.returncode, result.stdout[-3000:], result.stderr[-3000:])
    path, headers, body = captured[0]
    assert path == '/v1/responses', path
    headers = {k.lower(): v for k, v in headers.items()}
    assert headers.get('authorization') == ' '.join(('Bearer', 'fake-access'))
    assert headers.get('chatgpt-account-id') == 'fake-account', list(headers)
    assert headers.get('upgrade') == 'websocket' or body.get('model') == 'gpt-5.4'
    print(json.dumps({'native': version, 'path': path, 'requests': len(captured), 'saved_account_forwarding': True, 'transport': headers.get('upgrade', 'http'), 'network': 'none'}))
server.shutdown()
