"""Let the official gh client manage its private native configuration."""
import base64
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import sys

CONFIG = Path('/github-auth')
SIGNING_KEYS_OUTPUT_LIMIT = 1024 * 1024
SIGNING_KEY_LIMIT = 8192


def environment(login=False):
    # An allowlist prevents image/host token and routing overrides taking priority.
    env = {'PATH': '/usr/local/bin:/usr/bin:/bin', 'HOME': '/home/node',
           'LANG': 'C.UTF-8', 'TERM': 'xterm-256color',
           'GH_CONFIG_DIR': str(CONFIG), 'GH_HOST': 'github.com',
           'GH_NO_UPDATE_NOTIFIER': '1', 'GH_NO_EXTENSION_UPDATE_NOTIFIER': '1',
           'GH_BROWSER': '/bin/true', 'BROWSER': '/bin/true'}
    if not login:
        env['GH_PROMPT_DISABLED'] = '1'
    return env


def checked_bytes(path, info, limit):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as stream:
        opened = os.fstat(stream.fileno())
        if ((opened.st_dev, opened.st_ino) != (info.st_dev, info.st_ino)
                or not stat.S_ISREG(opened.st_mode) or opened.st_nlink != 1
                or opened.st_uid != os.getuid()
                or stat.S_IMODE(opened.st_mode) != 0o600):
            raise ValueError('configuration file changed while opening')
        data = stream.read(limit + 1)
        if len(data) > limit:
            raise ValueError('oversized configuration file')
        return data


def storage(directory):
    info = directory.lstat()
    if (not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid()
            or stat.S_IMODE(info.st_mode) != 0o700):
        raise ValueError('unsafe configuration directory')
    present = False
    for path in directory.iterdir():
        info = path.lstat()
        if (path.name not in ('hosts.yml', 'config.yml')
                or not stat.S_ISREG(info.st_mode) or info.st_nlink != 1
                or info.st_uid != os.getuid()
                or stat.S_IMODE(info.st_mode) != 0o600
                or info.st_size > 1024 * 1024):
            raise ValueError('unsafe configuration file')
        if path.name == 'config.yml' and checked_bytes(path, info, 20) != b'git_protocol: https\n':
            raise ValueError('unsupported gh configuration')
        if path.name == 'hosts.yml' and info.st_size > 0:
            # Recognize gh's empty logout document without parsing or exporting
            # any credentials. Reads are bounded and never follow a link.
            present = checked_bytes(path, info, 1024 * 1024).strip() not in (b'', b'{}')
    return present


def initialise(directory):
    info = directory.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid not in (0, 1000):
        raise ValueError('unsafe configuration directory')
    os.chown(directory, 1000, 1000)
    os.chmod(directory, 0o700)


def login_process(command, env):
    child = None
    terminating = False

    def terminate(signum, frame):
        nonlocal terminating
        terminating = True
        if child is not None:
            try:
                child.send_signal(signum)
            except ProcessLookupError:
                pass

    handlers = {number: signal.getsignal(number)
                for number in (signal.SIGTERM, signal.SIGINT)}
    signal.signal(signal.SIGTERM, terminate)
    signal.signal(signal.SIGINT, lambda signum, frame: None)
    try:
        child = subprocess.Popen(command, env=env, cwd='/home/node')
        if terminating:
            child.send_signal(signal.SIGTERM)
        return child.wait()
    finally:
        for number, handler in handlers.items():
            signal.signal(number, handler)


def valid_repository(repository):
    if not isinstance(repository, str) or '/' not in repository:
        return False
    parts = repository.split('/')
    return (len(parts) == 2 and re.fullmatch(r'[A-Za-z0-9-]{1,39}', parts[0])
            and not parts[0].startswith('-') and not parts[0].endswith('-')
            and '--' not in parts[0] and re.fullmatch(r'[A-Za-z0-9_.-]{1,100}', parts[1])
            and parts[1] not in ('.', '..'))


def valid_login(login):
    return (isinstance(login, str) and re.fullmatch(r'[A-Za-z0-9-]{1,39}', login)
            and not login.startswith('-') and not login.endswith('-') and '--' not in login)


def valid_signing_key(key):
    if (not isinstance(key, str) or not key or len(key) > SIGNING_KEY_LIMIT
            or any(ord(char) < 32 or ord(char) > 126 for char in key)):
        return False
    fields = key.split()
    if (len(fields) < 2 or not re.fullmatch(
            r'(ssh-(rsa|ed25519)|ecdsa-sha2-nistp(256|384|521)|sk-(ssh-ed25519|ecdsa-sha2-nistp256)@openssh.com)', fields[0])):
        return False
    try:
        decoded = base64.b64decode(fields[1], validate=True)
        return bool(decoded) and base64.b64encode(decoded).decode('ascii') == fields[1]
    except ValueError:
        return False


def run(action, directory=CONFIG, repository=None, login=None):
    if action == 'repository' and not valid_repository(repository):
        return 1
    if action != 'repository' and repository is not None:
        return 1
    if action == 'signing-keys' and not valid_login(login):
        return 1
    if action != 'signing-keys' and login is not None:
        return 1
    os.umask(0o077)
    if action == 'init':
        initialise(directory)
        return 0
    present = storage(directory)
    if action == 'status':
        # Presence is an offline storage check, never an account validity claim.
        print(json.dumps({'state': 'stored' if present else 'missing'}))
        return 0
    if action == 'identity':
        if not present:
            return 1
        result = subprocess.run(
            ['gh', 'api', '--hostname', 'github.com', 'user', '--jq', '{id: .id, login: .login}'],
            env=environment(), cwd='/home/node', capture_output=True, timeout=30)
        if result.returncode or len(result.stdout) > 1024:
            return 1
        record = json.loads(result.stdout)
        if (not isinstance(record, dict) or type(record.get('id')) is not int
                or record['id'] <= 0 or not isinstance(record.get('login'), str)
                or not record['login'] or len(record['login']) > 100):
            return 1
        print(json.dumps({'id': record['id'], 'login': record['login']}))
        return 0
    if action == 'signing-keys':
        if not present:
            return 1
        keys = []
        for page in range(1, 11):
            result = subprocess.run(
                ['gh', 'api', '--hostname', 'github.com',
                 'users/' + login + '/ssh_signing_keys?per_page=100&page=' + str(page),
                 '--jq', '[.[] | .key]'],
                env=environment(), cwd='/home/node', capture_output=True, timeout=30)
            if result.returncode or len(result.stdout) > SIGNING_KEYS_OUTPUT_LIMIT:
                return 1
            record = json.loads(result.stdout)
            if (not isinstance(record, list) or len(record) > 100
                    or not all(valid_signing_key(key) for key in record)):
                return 1
            keys.extend(record)
            encoded = json.dumps(keys)
            if len(encoded.encode('utf-8')) > SIGNING_KEYS_OUTPUT_LIMIT:
                return 1
            if len(record) < 100:
                print(encoded)
                return 0
        return 1
    if action == 'repository':
        if not present:
            return 1
        result = subprocess.run(
            ['gh', 'api', '--hostname', 'github.com', 'repos/' + repository,
             '--jq', '{id:.id,name:.full_name,push:.permissions.push}'],
            env=environment(), cwd='/home/node', capture_output=True, timeout=30)
        if result.returncode or len(result.stdout) > 1024:
            return 1
        record = json.loads(result.stdout)
        if (not isinstance(record, dict) or type(record.get('id')) is not int
                or record['id'] <= 0 or record.get('push') is not True
                or not valid_repository(record.get('name'))
                or record['name'].lower() != repository.lower()):
            return 1
        print(json.dumps({'id': record['id'], 'name': record['name'], 'push': True}))
        return 0
    if action == 'verify':
        if not present:
            state = 'missing'
        else:
            result = subprocess.run(
                ['gh', 'auth', 'status', '--hostname', 'github.com', '--active'],
                env=environment(), cwd='/home/node', capture_output=True, timeout=30)
            state = 'verified' if result.returncode == 0 else 'failed'
        print(json.dumps({'state': state}))
        return 0
    if action == 'login':
        if present:
            return 1
        result = login_process(
            ['gh', 'auth', 'login', '--hostname', 'github.com', '--git-protocol',
             'https', '--web', '--insecure-storage', '--skip-ssh-key'], environment(True))
        if result != 0:
            return 1
        # Retain a fixed noncredential configuration, excluding native aliases,
        # extensions, custom editors and other executable user configuration.
        config = directory / 'config.yml'
        info = config.lstat() if config.exists() or config.is_symlink() else None
        if info is not None and (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1
                                 or info.st_uid != os.getuid()):
            raise ValueError('unsafe configuration file')
        config.unlink(missing_ok=True)
        fd = os.open(config, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'wb') as stream:
            stream.write(b'git_protocol: https\n')
        return 0 if storage(directory) else 1
    if action == 'logout':
        if not present:
            return 0
        # Native logout owns credential removal. Prompts and account details are
        # suppressed; ambiguous multi-account caches fail instead of choosing.
        result = subprocess.run(['gh', 'auth', 'logout', '--hostname', 'github.com'],
                                env=environment(), cwd='/home/node',
                                capture_output=True, timeout=30)
        storage(directory)
        return 0 if result.returncode == 0 else 1
    return 1


def main():
    if len(sys.argv) not in (2, 3) or sys.argv[1] not in ('init', 'login', 'status', 'verify', 'identity', 'repository', 'signing-keys', 'logout'):
        return 1
    action = sys.argv[1]
    argument = sys.argv[2] if len(sys.argv) == 3 else None
    if (action in ('repository', 'signing-keys')) != (argument is not None):
        return 1
    try:
        return run(action, repository=argument if action == 'repository' else None,
                   login=argument if action == 'signing-keys' else None)
    except Exception:
        if action in ('status', 'verify'):
            print(json.dumps({'state': 'invalid'}))
            return 0
        print('SDLC could not complete GitHub authentication safely.', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
