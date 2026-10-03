"""Synthetic provider-scoped credentials and real worker Git push over stdio.

No sockets, live provider, real credential, or operating-system isolation.
Provider storage and entry points represent separately protected services.
"""
import base64
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import time


GIT = str(Path(shutil.which('git')).resolve())
PYTHON = sys.executable
MODULE = str(Path(__file__).resolve())
ZERO = '0' * 40
REPO = re.compile(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,79}\.git')
TOKEN_FIELDS = {'v', 'job', 'repo', 'scope', 'iat', 'exp', 'generation', 'jti'}
MAX_REQUEST = 8192
QUARANTINE_ENV = ('GIT_OBJECT_DIRECTORY', 'GIT_ALTERNATE_OBJECT_DIRECTORIES', 'GIT_QUARANTINE_PATH')


class Rejected(ValueError):
    pass


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()


def encode(raw):
    return base64.urlsafe_b64encode(raw).decode().rstrip('=')


def decode(text):
    return base64.b64decode(text + '=' * (-len(text) % 4), altchars=b'-_', validate=True)


def clean_env(home):
    home = Path(home).resolve()
    return {'PATH': '/usr/bin:/bin', 'HOME': str(home), 'LC_ALL': 'C',
            'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null',
            'GIT_CONFIG_SYSTEM': '/dev/null', 'GIT_TERMINAL_PROMPT': '0',
            'GIT_NO_REPLACE_OBJECTS': '1', 'GIT_ATTR_NOSYSTEM': '1',
            'GIT_CEILING_DIRECTORIES': str(home.parent)}


def git(home, repository, *args, data=None, extra=None, check=True):
    env = clean_env(home)
    if extra:
        env.update(extra)
    command = [GIT, '-c', 'core.hooksPath=/dev/null', '-c', 'core.fsmonitor=false',
               '-c', 'core.attributesFile=/dev/null', '-c', 'commit.gpgsign=false']
    if repository:
        command += ['--git-dir', str(repository)]
    return subprocess.run(command + list(args), input=data, env=env, cwd=home,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=check)


class Provider:
    """Controller API only. Workers receive a token and job renewal capability."""
    def __init__(self, root):
        self.root = Path(root).resolve()
        self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.home = self.root / 'home'
        self.home.mkdir(exist_ok=True)
        self.state = self.root / 'issuer.json'
        if not self.state.exists():
            self._save({'master': encode(secrets.token_bytes(32)), 'jobs': {}, 'revoked': []})

    def _load(self):
        return json.loads(self.state.read_bytes())

    def _save(self, value):
        temporary = self.state.with_suffix('.next')
        temporary.write_bytes(canonical(value))
        temporary.chmod(0o600)
        os.replace(temporary, self.state)

    def register(self, job_id, repo, branch, public_key, job_seconds=120, token_seconds=30):
        """Trusted controller chooses one repo/ref and finite job lifetime."""
        if (not isinstance(job_id, str) or not 1 <= len(job_id) <= 120
                or not REPO.fullmatch(repo) or not branch.startswith('refs/heads/job/')
                or git(self.home, None, 'check-ref-format', branch, check=False).returncode):
            raise Rejected('Invalid registered repository or job branch')
        if not 1 <= token_seconds <= 60 or not 1 <= job_seconds <= 300:
            raise Rejected('Invalid finite credential lifetime')
        state = self._load()
        if job_id in state['jobs']:
            raise Rejected('Job already registered')
        if any(j['repo'] == repo and j['branch'] == branch for j in state['jobs'].values()):
            raise Rejected('Branch already belongs to a registered job')
        capability = encode(secrets.token_bytes(32))
        policy_id = hashlib.sha256(job_id.encode()).hexdigest()
        policies = self.root / 'policies'
        policies.mkdir(exist_ok=True)
        policy = policies / (policy_id + '.json')
        state['jobs'][job_id] = {'repo': repo, 'scope': ['git:read', 'git:push'],
                                 'branch': branch, 'policy': str(policy),
                                 'expires': int(time.time()) + job_seconds,
                                 'token_seconds': token_seconds, 'generation': 0,
                                 'capability_hash': hashlib.sha256(capability.encode()).hexdigest()}
        self._save(state)
        remote = self.root / 'repos' / repo
        remote.parent.mkdir(exist_ok=True)
        if not remote.exists():
            git(self.home, None, 'init', '--bare', str(remote))
        allowed = policies / (policy_id + '.signers')
        allowed.write_text('worker@example.invalid ' + public_key.strip() + '\n')
        policy.write_bytes(canonical({'repo_path': str(remote), 'home': str(self.home),
                                      'allowed_ref': branch, 'allowed_signers': str(allowed)}))
        hooks = self.root / 'hooks' / repo
        hooks.mkdir(parents=True, exist_ok=True)
        hook = hooks / 'pre-receive'
        hook.write_text('#!' + PYTHON + '\nimport os\nenv = ' + repr(clean_env(self.home)) + '\n' +
                        'for name in ' + repr(QUARANTINE_ENV) + ':\n' +
                        '    if name in os.environ: env[name] = os.environ[name]\n' +
                        'os.execve(' + repr(PYTHON) + ', ' +
                        repr([PYTHON, MODULE, 'receive-policy']) +
                        ' + [os.environ["FIXTURE_RECEIVE_POLICY"]], env)\n')
        hook.chmod(0o700)
        return capability

    def renew(self, request):
        """Worker-facing API: no caller repo, permissions or TTL argument exists."""
        if not isinstance(request, dict) or set(request) != {'job', 'capability'}:
            raise Rejected('Renewal requires the registered job capability only')
        state = self._load()
        job = state['jobs'].get(request['job'])
        if not job or not isinstance(request['capability'], str):
            raise Rejected('Unknown job or invalid capability')
        actual = hashlib.sha256(request['capability'].encode()).hexdigest()
        if not hmac.compare_digest(actual, job['capability_hash']):
            raise Rejected('Invalid job capability')
        now = int(time.time())
        if now >= job['expires']:
            raise Rejected('Registered job expired')
        job['generation'] += 1
        payload = {'v': 1, 'job': request['job'], 'repo': job['repo'],
                   'scope': job['scope'], 'iat': now,
                   'exp': min(now + job['token_seconds'], job['expires']),
                   'generation': job['generation'], 'jti': encode(secrets.token_bytes(16))}
        raw = canonical(payload)
        signature = hmac.digest(decode(state['master']), raw, 'sha256')
        self._save(state)
        return 'v1.' + encode(raw) + '.' + encode(signature)

    def revoke(self, token):
        """Trusted provider revokes a credential independently of worker URLs."""
        state = self._load()
        state['revoked'].append(json.loads(decode(token.split('.')[1]))['jti'])
        self._save(state)

    def finish(self, job_id):
        """Trusted controller ends renewal and all outstanding tokens for a job."""
        state = self._load()
        state['jobs'][job_id]['expires'] = 0
        self._save(state)

    def authenticate(self, token, repo, service):
        try:
            if not isinstance(token, str) or len(token) > MAX_REQUEST or not REPO.fullmatch(repo):
                raise Rejected('Invalid credential request')
            version, body, signature = token.split('.')
            raw = decode(body)
            state = self._load()
            if version != 'v1' or not hmac.compare_digest(
                    hmac.digest(decode(state['master']), raw, 'sha256'), decode(signature)):
                raise Rejected('Invalid credential')
            value = json.loads(raw)
            if set(value) != TOKEN_FIELDS or value['v'] != 1 or value['repo'] != repo:
                raise Rejected('Credential repository mismatch')
            job = state['jobs'].get(value['job'])
            required = {'git-upload-pack': 'git:read', 'git-receive-pack': 'git:push'}.get(service)
            now = int(time.time())
            if (not job or not required or required not in value['scope']
                    or value['scope'] != job['scope'] or value['repo'] != job['repo']
                    or not 1 <= value['generation'] <= job['generation']
                    or now >= value['exp'] or now >= job['expires']
                    or value['exp'] > job['expires'] or value['jti'] in state['revoked']):
                raise Rejected('Credential expired, revoked or outside registered job')
            return self.root / 'repos' / repo, Path(job['policy'])
        except (KeyError, TypeError, ValueError) as exc:
            if isinstance(exc, Rejected):
                raise
            raise Rejected('Invalid credential') from exc

    def write_worker_helper(self, directory):
        """Client is only a bridge. Server authenticates every Git connection."""
        directory = Path(directory)
        directory.mkdir(parents=True, exist_ok=True)
        helper = directory / 'git-remote-fixture'
        helper.write_text('#!' + PYTHON + '\nimport os\nos.execve(' + repr(PYTHON) + ', ' +
                          repr([PYTHON, MODULE, 'remote-helper', str(self.root)]) +
                          ' + __import__("sys").argv[1:], dict(os.environ))\n')
        helper.chmod(0o700)
        return helper


def remote_helper(root, name, url):
    # The URL is untrusted and token is supplied by the worker. Neither selects
    # the issuer, master secret, repo root, policy or receive-pack executable.
    while True:
        line = sys.stdin.buffer.readline(MAX_REQUEST + 1)
        if len(line) > MAX_REQUEST:
            raise Rejected('Helper request too large')
        if line == b'capabilities\n':
            sys.stdout.buffer.write(b'connect\n\n')
            sys.stdout.buffer.flush()
        elif line.startswith(b'connect '):
            service = line[8:].strip().decode('ascii')
            repo = url.removeprefix('fixture::')
            sys.stdout.buffer.write(b'\n')
            sys.stdout.buffer.flush()
            env = {'PATH': '/usr/bin:/bin', 'FAKE_REPOSITORY_TOKEN': os.environ.get('FAKE_REPOSITORY_TOKEN', '')}
            # Modeled provider service boundary; same-UID launch is not isolation.
            os.execve(PYTHON, [PYTHON, MODULE, 'serve', root, repo, service], env)
        elif not line:
            return
        else:
            raise Rejected('Unsupported Git helper command')


def serve(root, repo, service):
    provider = Provider(root)
    repository, policy = provider.authenticate(os.environ.get('FAKE_REPOSITORY_TOKEN', ''), repo, service)
    env = clean_env(provider.home)
    env['FIXTURE_RECEIVE_POLICY'] = str(policy)
    command = [GIT, '-c', 'core.fsmonitor=false', '-c', 'core.attributesFile=/dev/null',
               '-c', 'receive.denyNonFastForwards=true', '-c', 'receive.denyDeletes=true',
               '-c', 'receive.fsckObjects=true', '-c',
               'core.hooksPath=' + str(provider.root / 'hooks' / repo),
               service.removeprefix('git-'), str(repository)]
    # The raw issuer key/token/job capability is absent from backend environment.
    os.chdir(provider.home)
    os.execve(GIT, command, env)


def receive_policy(filename):
    policy = json.loads(Path(filename).read_bytes())
    # These variables originate in the trusted receive-pack process, whose
    # launch environment is rebuilt by serve(). Incoming objects stay isolated
    # until this hook accepts them, so checks must read the quarantine store.
    objects = (Path(policy['repo_path']) / 'objects').resolve()
    incoming = Path(os.environ.get('GIT_OBJECT_DIRECTORY', '')).resolve()
    alternate = os.environ.get('GIT_ALTERNATE_OBJECT_DIRECTORIES', '')
    quarantine = Path(os.environ.get('GIT_QUARANTINE_PATH', '')).resolve()
    if (incoming.parent != objects or not incoming.name.startswith('tmp_objdir-incoming-')
            or not incoming.is_dir() or quarantine != incoming
            or Path(alternate).resolve() != objects):
        raise Rejected('Unexpected receive quarantine')
    extra = {name: os.environ[name] for name in QUARANTINE_ENV}
    lines = sys.stdin.buffer.read(MAX_REQUEST + 1).splitlines()
    if len(lines) != 1:
        raise Rejected('Exactly one registered job ref may be updated')
    old, new, ref = lines[0].decode('ascii').split()
    if ref != policy['allowed_ref'] or new == ZERO:
        raise Rejected('Ref is protected or outside registered receive policy')
    if old != ZERO and git(policy['home'], policy['repo_path'], 'merge-base', '--is-ancestor',
                           old, new, extra=extra, check=False).returncode:
        raise Rejected('Non-fast-forward update rejected')
    new_commits = git(policy['home'], policy['repo_path'], 'rev-list', new, '--not', '--all',
                      extra=extra).stdout.splitlines()
    # A tip already present elsewhere still needs the current job's signing
    # identity. Its existing ancestors need not be reintroduced or re-signed.
    for commit in set(new_commits) | {new.encode()}:
        result = git(policy['home'], policy['repo_path'], '-c', 'gpg.format=ssh',
                     '-c', 'gpg.ssh.program=/usr/bin/ssh-keygen',
                     '-c', 'gpg.ssh.allowedSignersFile=' + policy['allowed_signers'],
                     'verify-commit', commit.decode(), extra=extra, check=False)
        if result.returncode:
            raise Rejected('New commits require the fixture signing identity')


def worker_git(workspace, *args, token=None, helper_directory=None, check=True, data=None):
    """Actual worker process; no provider Git wrapper sends the push for it."""
    workspace = Path(workspace).resolve()
    env = clean_env(workspace)
    if helper_directory:
        env['PATH'] = str(Path(helper_directory).resolve()) + ':/usr/bin:/bin'
    if token is not None:
        env['FAKE_REPOSITORY_TOKEN'] = token
    return subprocess.run([GIT, '-C', str(workspace)] + list(args), input=data,
                          env=env, cwd=workspace, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, check=check)


def worker_push(workspace, repo, branch, token, helper_directory, source='HEAD', force=False):
    """Push a worker-produced OID unchanged to the modeled provider endpoint."""
    options = ['--force'] if force else []
    return worker_git(workspace, 'push', *options, 'fixture::' + repo, source + ':' + branch,
                      token=token, helper_directory=helper_directory, check=False)


def main():
    mode = sys.argv[1]
    if mode == 'remote-helper':
        remote_helper(*sys.argv[2:])
    elif mode == 'serve':
        serve(*sys.argv[2:])
    elif mode == 'receive-policy':
        receive_policy(*sys.argv[2:])
    elif mode == 'renew':
        raw = sys.stdin.buffer.read(MAX_REQUEST + 1)
        if len(raw) > MAX_REQUEST:
            raise Rejected('Renewal request too large')
        print(Provider(sys.argv[2]).renew(json.loads(raw)))
    else:
        raise Rejected('Unknown service operation')


if __name__ == '__main__':
    try:
        main()
    except Exception:
        # Never disclose request bytes, bearer tokens or issuer state in errors.
        print('remote: synthetic provider rejected operation', file=sys.stderr)
        sys.exit(1)
