"""Actual Git SSH signing shim and constrained, local protocol fixture signer."""
import base64
from dataclasses import dataclass, field
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import socket
import stat
import struct
import subprocess
import tempfile
import threading
import time

SSH_KEYGEN = '/usr/bin/ssh-keygen'
MAX_PAYLOAD = 16384
MAX_FRAME = 32768
OID = re.compile(r'[0-9a-f]{40}')
IDENTITY = 'Synthetic Developer <developer@example.invalid>'


class Rejected(ValueError):
    pass


def sha256(raw):
    return hashlib.sha256(raw).hexdigest()


def commit_payload(tree, parent, identity, message, timestamp):
    if not OID.fullmatch(tree) or not OID.fullmatch(parent):
        raise Rejected('Invalid approved tree or parent')
    return (f'tree {tree}\nparent {parent}\nauthor {identity} {timestamp} +0000\n'
            f'committer {identity} {timestamp} +0000\n\n').encode() + message


def service_environment(home):
    return {'PATH': '/usr/bin:/bin', 'HOME': str(home), 'LC_ALL': 'C',
            'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_SYSTEM': '/dev/null',
            'GIT_CONFIG_GLOBAL': '/dev/null', 'GIT_ATTR_NOSYSTEM': '1',
            'GIT_CEILING_DIRECTORIES': str(home.resolve().parent)}


@dataclass
class SigningJob:
    job_id: str
    repository: str
    capability: str
    identity: str
    expires_at: float
    revoked: bool = False
    approvals: dict = field(default_factory=dict)


class Signer:
    """Coordinator-owned state; clients cannot create jobs or approvals."""
    def __init__(self, root, key):
        self.root = Path(root)
        self.home = self.root / 'home'
        self.home.mkdir(parents=True)
        self.key = Path(key)
        self.jobs, self.events, self.signing_environments = {}, [], []
        self.lock = threading.Lock()

    def register(self, job):
        if job.job_id in self.jobs or len(job.capability) < 32:
            raise Rejected('Invalid coordinator registration')
        self.jobs[job.job_id] = job

    def authorize(self, job_id, tree, parent, message, timestamp=1700000000):
        """Fixture review admission for one increment, repeatable within a job."""
        job = self.jobs[job_id]
        payload = commit_payload(tree, parent, job.identity, message, timestamp)
        if not message.endswith(b'\n') or len(payload) > MAX_PAYLOAD:
            raise Rejected('Invalid approved message')
        with self.lock:
            job.approvals[sha256(payload)] = {'tree': tree, 'parent': parent,
                                            'message_sha256': sha256(message), 'signature': None}
        return payload

    def revoke(self, job_id):
        with self.lock:
            self.jobs[job_id].revoked = True

    def handle(self, raw):
        try:
            request = json.loads(raw)
            if set(request) != {'version', 'job_id', 'repository', 'capability', 'payload'}:
                raise Rejected('Unexpected protocol fields')
            job = self.jobs.get(request['job_id'])
            if (request['version'] != 1 or not job or job.revoked or time.time() >= job.expires_at
                    or request['repository'] != job.repository
                    or not isinstance(request['capability'], str)
                    or not hmac.compare_digest(request['capability'], job.capability)):
                raise Rejected('Job capability is unavailable')
            payload = base64.b64decode(request['payload'], validate=True)
            if not 0 < len(payload) <= MAX_PAYLOAD:
                raise Rejected('Payload exceeds protocol limit')
            headers, message = payload.split(b'\n\n', 1)
            lines = headers.decode('utf-8').splitlines()
            if len(lines) != 4 or not lines[0].startswith('tree ') or not lines[1].startswith('parent '):
                raise Rejected('Expected unsigned single-parent Git commit')
            tree, parent = lines[0][5:], lines[1][7:]
            if not OID.fullmatch(tree) or not OID.fullmatch(parent):
                raise Rejected('Invalid Git tree or parent')
            for label, line in zip(('author', 'committer'), lines[2:]):
                if not re.fullmatch(re.escape(label + ' ' + job.identity) + r' [0-9]{1,12} \+0000', line):
                    raise Rejected('Fixed job identity differs')
            with self.lock:
                # Check mutable expiry/revocation again at the signing boundary.
                if job.revoked or time.time() >= job.expires_at:
                    raise Rejected('Job capability is unavailable')
                approval = job.approvals.get(sha256(payload))
                if not approval or (tree, parent, sha256(message)) != (
                        approval['tree'], approval['parent'], approval['message_sha256']):
                    raise Rejected('Commit has not been authorised')
                if approval['signature'] is None:
                    approval['signature'] = self._sign(payload)
                signature = approval['signature']
                self.events.append({'job_id': job.job_id, 'payload_sha256': sha256(payload), 'signed': True})
            return {'version': 1, 'ok': True, 'signature': base64.b64encode(signature).decode()}
        except (Rejected, AttributeError, KeyError, OSError, RecursionError,
                subprocess.SubprocessError, TypeError, UnicodeError, ValueError):
            self.events.append({'signed': False})
            return {'version': 1, 'ok': False, 'error': 'Signing request rejected'}

    def _sign(self, payload):
        env = service_environment(self.home)
        self.signing_environments.append(env)
        with tempfile.TemporaryDirectory(dir=self.root, prefix='sign-') as directory:
            source = Path(directory) / 'commit'
            source.write_bytes(payload)
            source.chmod(0o600)
            subprocess.run([SSH_KEYGEN, '-Y', 'sign', '-n', 'git', '-f', str(self.key), str(source)],
                           input=b'', env=env, cwd=self.home, check=True,
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=5)
            signature = source.with_suffix('.sig').read_bytes()
            if not signature.startswith(b'-----BEGIN SSH SIGNATURE-----\n'):
                raise Rejected('Unexpected signer output')
            return signature

    def serve(self, connection):
        try:
            while True:
                raw = receive_frame(connection, active_timeout=2)
                send_frame(connection, json.dumps(self.handle(raw)).encode())
        except (EOFError, OSError, Rejected):
            return


def read_exact(connection, length, deadline=None):
    data = bytearray()
    while len(data) < length:
        if deadline is not None:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError('Incomplete protocol frame')
            connection.settimeout(remaining)
        chunk = connection.recv(length - len(data))
        if not chunk:
            raise EOFError
        data.extend(chunk)
    return bytes(data)


def receive_frame(connection, active_timeout=None):
    # Idle sessions may wait for another increment. Once a frame begins, a
    # partial header or body has a bounded receive deadline in the service.
    first = read_exact(connection, 1)
    previous = connection.gettimeout()
    deadline = time.monotonic() + active_timeout if active_timeout is not None else None
    try:
        length = struct.unpack('!I', first + read_exact(connection, 3, deadline))[0]
        if not 0 < length <= MAX_FRAME:
            raise Rejected('Invalid protocol frame size')
        return read_exact(connection, length, deadline)
    finally:
        connection.settimeout(previous)


def send_frame(connection, data):
    if not 0 < len(data) <= MAX_FRAME:
        raise Rejected('Invalid protocol frame size')
    connection.sendall(struct.pack('!I', len(data)) + data)


def client_request(connection, job_id, repository, capability, payload):
    request = {'version': 1, 'job_id': job_id, 'repository': repository,
               'capability': capability, 'payload': base64.b64encode(payload).decode()}
    send_frame(connection, json.dumps(request).encode())
    return json.loads(receive_frame(connection))


def read_temporary(root, name, limit):
    # Git-created inputs must be direct regular files in the registered tmp root.
    path = Path(name)
    if not path.is_absolute() or path.parent.resolve() != root.resolve():
        raise Rejected('Signing file is outside the registered temporary root')
    directory = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        fd = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size > limit:
                raise Rejected('Signing file must be bounded and regular')
            with os.fdopen(fd, 'rb', closefd=False) as source:
                data = source.read(limit + 1)
            if len(data) > limit:
                raise Rejected('Signing file exceeds input limit')
            return data
        finally:
            os.close(fd)
    finally:
        os.close(directory)


def shim_main(arguments, client):
    # Git may add -U for its temporary public-key selector; this shim uses the
    # constrained endpoint instead of an SSH agent. No other OpenSSH mode exists.
    if len(arguments) == 8 and arguments[-2] == '-U':
        arguments = arguments[:-2] + arguments[-1:]
    if len(arguments) != 7 or arguments[:5] != ['-Y', 'sign', '-n', 'git', '-f']:
        raise Rejected('Only Git SSH commit signing is supported')
    root = Path(client['temporary_root'])
    selector = read_temporary(root, arguments[5], 4096).strip()
    if selector != client['public_key'].encode().strip():
        raise Rejected('Unexpected public signing key')
    payload = read_temporary(root, arguments[6], MAX_PAYLOAD)
    descriptor = os.environ.get('SDLC_SIGN_FD', '')
    if not re.fullmatch(r'[3-9][0-9]*|[1-9][0-9]+', descriptor):
        raise Rejected('Missing preconnected signer descriptor')
    with socket.socket(fileno=os.dup(int(descriptor))) as connection:
        connection.settimeout(5)
        result = client_request(connection, client['job_id'], client['repository'], client['capability'], payload)
    if set(result) != {'version', 'ok', 'signature'} or result['version'] != 1 or result['ok'] is not True:
        raise Rejected('Signing request rejected')
    signature = base64.b64decode(result['signature'], validate=True)
    if len(signature) > 4096 or not signature.startswith(b'-----BEGIN SSH SIGNATURE-----\n'):
        raise Rejected('Invalid signature response')
    # Do not follow a preplanted signature-output link or overwrite any file.
    directory = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        fd = os.open(Path(arguments[6]).name + '.sig',
                     os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=directory)
    finally:
        os.close(directory)
    with os.fdopen(fd, 'wb') as destination:
        destination.write(signature)
