#!/usr/bin/env python3
"""Local-only native setup checks; every container has networking disabled."""

import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import uuid


ROOT = Path(__file__).resolve().parents[1]
HELPER = (ROOT / "internal/providerauth/container.py").read_text().split(
    'if __name__ == "__main__":')[0]


def command(args, *, expected=0, timeout=60):
    if args[:2] == ["docker", "run"]:
        if "--network" not in args or args[args.index("--network") + 1] != "none":
            raise RuntimeError("the probe requires network-disabled containers")
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
    if result.returncode != expected:
        raise RuntimeError("local interactive probe failed; diagnostics withheld")
    return result.stdout


def container(image, volume, instructions=None, *, root=False, managed=None):
    args = ["docker", "run", "--rm", "--pull", "never", "--network", "none",
            "--log-driver", "none", "--read-only", "--user", "0:0" if root else "1000:1000",
            "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
            "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m,mode=1777",
            "--tmpfs", "/home/node/:rw,nosuid,nodev,noexec,size=64m,mode=0700,uid=1000,gid=1000",
            "--tmpfs", "/workspace:rw,nosuid,nodev,size=1g,mode=0700,uid=1000,gid=1000",
            "--mount", "type=volume,src=" + volume + ",dst=/provider-auth,volume-nocopy"]
    if root:
        args += ["--cap-add", "CHOWN", "--cap-add", "FOWNER"]
    if managed:
        args += ['--mount', 'type=bind,src=' + str(managed)
                 + ',dst=/etc/claude-code/managed-settings.json,readonly']
    if instructions:
        args += ["--mount", "type=bind,src=" + str(instructions) + ",dst=/session-instructions.md,readonly",
                 "--mount", "type=bind,src=" + str(instructions.parent / 'settings.json')
                 + ",dst=/session-settings.json,readonly",
                 "--mount", "type=bind,src=" + str(instructions) + ",dst=/provider-auth/CLAUDE.md,readonly",
                 "--mount", "type=bind,src=" + str(instructions.parent / 'settings.json')
                 + ",dst=/provider-auth/settings.json,readonly",
                 "--tmpfs", "/provider-auth/plugins:rw,nosuid,nodev,noexec,size=64m,mode=0700,uid=1000,gid=1000",
                 "--tmpfs", "/provider-auth/projects:rw,nosuid,nodev,noexec,size=256m,mode=0700,uid=1000,gid=1000"]
        for directory in ('rules', 'commands', 'output-styles', 'workflows', 'agent-memory'):
            args += ['--tmpfs', '/provider-auth/' + directory
                     + ':rw,nosuid,nodev,noexec,size=64m,mode=0700,uid=1000,gid=1000']
    return args + ["--entrypoint", "/usr/bin/python3", image]


CODEX = r'''
def native(command, env):
    config = Path(env["CODEX_HOME"])
    target = Path('/provider-auth/auth.json')
    assert (config / 'auth.json').readlink() == target
    assert (config / 'AGENTS.md').samefile(SESSION_INSTRUCTIONS)
    assert list((Path(env['HOME']) / '.agents/skills').glob('*/SKILL.md'))
    assert command[command.index('--ask-for-approval') + 1] == approval
    assert command[command.index('--sandbox') + 1] == 'danger-full-access'
    help_result = subprocess.run(command + ['--help'], capture_output=True,
                                 text=True, env=env, timeout=30)
    assert help_result.returncode == 0
    login = subprocess.run(['codex', '-c', 'cli_auth_credentials_store="file"',
                            'login', '--with-api-key'], input='fake-disposable-probe-key\n',
                           capture_output=True, text=True, env=env, timeout=30)
    assert login.returncode == 0
    assert (config / 'auth.json').is_symlink()
    assert json.loads(target.read_text())['OPENAI_API_KEY'] == 'fake-disposable-probe-key'
    if logout:
        result = subprocess.run(['codex', '-c', 'cli_auth_credentials_store="file"', 'logout'],
                                capture_output=True, text=True, env=env, timeout=30)
        assert result.returncode == 0
        assert not (config / 'auth.json').exists()
        assert target.exists()
    return 0

os.umask(0o077)
logout = sys.argv[1] == 'logout'
mode = sys.argv[2]
approval = 'never' if mode == 'default' else mode
target = Path('/provider-auth/auth.json')
if not target.exists():
    target.write_text('{"fake":"disposable-cache"}')
    target.chmod(0o600)
interactive_process = native
assert run('codex', 'interactive', mode=None if mode == 'default' else mode) == 0
assert target.exists() is (not logout)
assert not list(Path(HOME_BASE).glob('sdlc-session-*'))
print(json.dumps({'native_save': True, 'native_logout_bookkeeping': logout}))
'''


CLAUDE = r'''
import fcntl
import pty
import re
import select
import struct
import termios
import time

def native(command, env):
    assert command[:4] == ['claude', '--setting-sources', 'user', '--permission-mode']
    assert command[4] == permission
    assert '--strict-mcp-config' in command
    assert json.loads(command[command.index('--mcp-config') + 1]) == {'mcpServers': {}}
    assert Path('/provider-auth/CLAUDE.md').samefile(SESSION_INSTRUCTIONS)
    assert list(Path('/provider-auth/skills').glob('*/SKILL.md'))
    assert list(Path('/provider-auth/agents').glob('*.md'))
    expected_settings = {'skipDangerousModePermissionPrompt': True} if permission == 'bypassPermissions' else {}
    assert json.loads(Path('/provider-auth/settings.json').read_text()) == expected_settings
    assert not Path('/provider-auth/plugins/disposable-cached-plugin').exists()
    assert not Path('/provider-auth/rules/disposable-cached-rule.md').exists()
    assert not Path('/provider-auth/agent-memory/disposable-cached-memory.md').exists()
    # auth is a native subcommand; --mcp-config's variadic arguments otherwise
    # consume its words. The full startup flags are checked by the TUI below.
    result = subprocess.run(command[:5] + ['auth', 'status', '--json'],
                            capture_output=True, text=True, env=env, timeout=30)
    status = json.loads(result.stdout)
    assert result.returncode == 0 and status['authMethod'] == 'claude.ai'
    assert status['apiProvider'] == 'firstParty'
    assert not Path(marker).exists()
    # Start the native interactive UI without submitting a model prompt. Its
    # built-in context screens are inspected over a private, disposable PTY.
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 50, 120, 0, 0))
    def terminal():
        os.setsid()
        fcntl.ioctl(0, termios.TIOCSCTTY, 0)
    process = subprocess.Popen(command + ['--agent', 'read_medium',
                                          '--debug-file', '/tmp/native-claude-debug.log'],
                               stdin=slave, stdout=slave, stderr=slave, cwd=WORKSPACE,
                               env=env, preexec_fn=terminal)
    os.close(slave)
    output = bytearray()
    def clean_output():
        return re.sub(r'\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\x1b.', '',
                      output.decode(errors='replace'))
    def read_for(seconds):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            ready, _, _ = select.select([master], [], [], 0.1)
            if ready:
                try:
                    chunk = os.read(master, 65536)
                except OSError:
                    break
                if not chunk:
                    break
                output.extend(chunk)
    try:
        read_for(4)
        assert process.poll() is None, 'the native offline TUI did not start'
        if case == 'managed-policy':
            clean = ''.join(clean_output().split()).lower()
            assert 'bypasspermissionsmodewasdisabledbysettings' in clean
            assert 'bypasspermissionson' not in clean
            assert 'automodeon' in clean
        else:
            if permission == 'manual':
                os.write(master, b'/memory\r')
                read_for(2)
                os.write(master, b'\x1b')
                read_for(0.5)
                os.write(master, b'/context\r')
                read_for(2)
                os.write(master, b'\x1b')
                read_for(0.5)
                os.write(master, b'/skills\r')
                read_for(2)
    finally:
        process.terminate()
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=3)
        os.close(master)
    assert not Path(marker).exists()
    debug_path = Path('/tmp/native-claude-debug.log')
    debug = debug_path.read_text() if debug_path.exists() else ''
    clean = clean_output()
    print(json.dumps({'native_status': True, 'customization_executed': False,
                      'ui': clean, 'debug': debug, 'permission': permission,
                      'managed_denied': case == 'managed-policy'}))
    if case != 'managed-policy':
        label = {'bypassPermissions': 'bypass permissions on', 'manual': 'manual mode on',
                 'plan': 'plan mode on'}[permission]
        assert ''.join(label.split()) in ''.join(clean.split()).lower(), (
            'the requested native permission mode was not displayed')
    return 0

os.umask(0o077)
marker = '/tmp/sdlc-interactive-settings-marker'
mode = sys.argv[1]
case = sys.argv[2]
permission = 'bypassPermissions' if mode == 'default' else mode
interactive_process = native
assert run('claude', 'interactive', mode=None if mode == 'default' else mode) == 0
assert not list(Path(HOME_BASE).glob('sdlc-session-*'))
'''


CLAUDE_SETUP = r'''
os.umask(0o077)
marker = '/tmp/sdlc-interactive-settings-marker'
cache = Path('/provider-auth')
cache.joinpath('.credentials.json').write_text(json.dumps({
    'claudeAiOauth': {'accessToken': 'fake-disposable-access',
        'refreshToken': 'fake-disposable-refresh', 'expiresAt': 4102444800000,
        'scopes': ['user:inference'], 'subscriptionType': 'max',
        'rateLimitTier': 'default_claude_max_5x'}}))
cache.joinpath('settings.json').write_text(json.dumps({
    'apiKeyHelper': 'touch ' + marker + '; printf fake-disposable-settings-key',
    'env': {'ANTHROPIC_API_KEY': 'fake-disposable-settings-key',
            'CLAUDE_CODE_USE_BEDROCK': '1'},
    'hooks': {'SessionStart': [{'hooks': [{'type': 'command', 'command': 'touch ' + marker}]}]}}))
cache.joinpath('.claude.json').write_text(json.dumps({
    'hasCompletedOnboarding': True, 'theme': 'dark',
    'mcpServers': {'disposable-cached-server': {
        'command': '/bin/sh', 'args': ['-c', 'touch ' + marker]}},
    'projects': {'/workspace': {'hasTrustDialogAccepted': True}}}))
cache.joinpath('plugins').mkdir(mode=0o700)
cache.joinpath('plugins/disposable-cached-plugin').write_text('disposable cached plugin')
for directory, filename in (('rules', 'disposable-cached-rule.md'),
                            ('agent-memory', 'disposable-cached-memory.md')):
    cache.joinpath(directory).mkdir(mode=0o700)
    cache.joinpath(directory, filename).write_text('Disposable cached context must be hidden.\n')
'''


def probe_cli_cancellation(cli, image, state, volume, recorded_names, mode):
    """Cancel an attached native CLI; Docker gets no network or real auth."""
    import fcntl
    import pty
    import select
    import signal
    import struct
    import termios
    import time

    docker = shutil.which('docker')
    wrappers = state / 'bin'
    wrappers.mkdir(mode=0o700, exist_ok=True)
    wrapper = wrappers / 'docker'
    wrapper.write_text('#!/usr/bin/env python3\n' +
        'import json,os,sys\n' +
        'args=sys.argv[1:]\n' +
        "if args and args[0]=='run':\n" +
        "    assert '--network' in args\n" +
        "    args[args.index('--network')+1]='none'\n" +
        "    if '--name' in args:\n" +
        '        with open(' + repr(str(recorded_names)) + ", 'a') as output:\n" +
        "            output.write(args[args.index('--name')+1]+'\\n')\n" +
        'os.execv(' + repr(docker) + ', [' + repr(docker) + ', *args])\n')
    wrapper.chmod(0o700)
    engine = command(['docker', 'info', '--format', '{{.ID}}']).strip()
    identity = volume.removeprefix('sdlc-auth-').removesuffix('-claude')
    (state / 'runtime.json').write_text(json.dumps({
        'version': 1, 'engine_id': engine, 'image_id': image}))
    (state / 'auth-installation.json').write_text(json.dumps({'id': identity}))
    env = dict(os.environ, SDLC_STATE_DIR=str(state),
               PATH=str(wrappers) + os.pathsep + os.environ['PATH'])
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 50, 120, 0, 0))
    original = termios.tcgetattr(slave)

    def terminal():
        os.setsid()
        fcntl.ioctl(0, termios.TIOCSCTTY, 0)
        os.umask(0o077)

    # Keep a disposable controlling-session leader alive after the CLI exits.
    # On macOS, otherwise that exit hangs up the PTY before its settings can
    # be compared, even when Docker restored them correctly.
    keeper = ("import signal,subprocess,sys; "
              "child=subprocess.Popen([sys.argv[1],'interactive','--provider','claude',*sys.argv[2:]]); "
              "print('probe-cli-pid='+str(child.pid),flush=True); "
              "print('probe-cli-exit='+str(child.wait()),flush=True); signal.pause()")
    override = [] if mode == 'default' else ['--permission-mode', mode]
    process = subprocess.Popen([sys.executable, '-c', keeper, str(cli), *override],
                               env=env, stdin=slave, stdout=slave, stderr=slave,
                               preexec_fn=terminal)
    output = bytearray()
    def drain_until(marker, seconds):
        deadline = time.monotonic() + seconds
        while marker not in output and process.poll() is None and time.monotonic() < deadline:
            ready, _, _ = select.select([master], [], [], 0.1)
            if ready:
                try:
                    output.extend(os.read(master, 65536))
                except OSError:
                    break
        return marker in output

    def compact_output():
        clean = re.sub(rb'\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\x1b.', b'', output)
        return b''.join(clean.split()).lower()

    try:
        deadline = time.monotonic() + 15
        ready_label = b'manualmodeon' if mode == 'manual' else b'bypasspermissionson'
        while time.monotonic() < deadline and ready_label not in compact_output():
            ready, _, _ = select.select([master], [], [], 0.1)
            if ready:
                output.extend(os.read(master, 65536))
            if process.poll() is not None:
                break
        assert ready_label in compact_output(), 'the native offline TUI did not start'
        assert termios.tcgetattr(slave) != original, 'Docker did not attach a raw TTY'
        pid = re.search(rb'probe-cli-pid=([0-9]+)', output)
        assert pid, 'the CLI test child did not report its process'
        os.kill(int(pid.group(1)), signal.SIGTERM)
        # Keep reading the attached PTY: Docker must flush native shutdown
        # output before it can restore the terminal and exit.
        assert drain_until(b'probe-cli-exit=', 45), 'attached CLI cancellation timed out'
        assert b'probe-cli-exit=1' in output
        assert termios.tcgetattr(slave) == original, 'Docker did not restore terminal settings'
        names = recorded_names.read_text().splitlines()
        assert any(name.startswith('sdlc-interactive-') for name in names)
        for name in names:
            assert not command(['docker', 'ps', '--all', '--filter',
                                'name=^/' + name + '$', '--format', '{{.ID}}']).strip()
        assert not list(state.glob('.interactive-*'))
        print('Attached CLI ' + mode + ' cancellation restored its terminal and removed its containers.')
    finally:
        if process.poll() is None:
            process.terminate()
            process.wait(timeout=5)
        if recorded_names.exists():
            for name in recorded_names.read_text().splitlines():
                remaining = command(['docker', 'ps', '--all', '--filter',
                                     'name=^/' + name + '$', '--format', '{{.ID}}']).strip()
                if remaining:
                    command(['docker', 'rm', '--force', name])
        try:
            termios.tcsetattr(slave, termios.TCSANOW, original)
        except termios.error:
            pass
        os.close(master)
        os.close(slave)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--cli', required=True, type=Path, help='built SDLC executable')
    args = parser.parse_args()
    status = command([str(args.cli.resolve()), 'runtime', 'status'])
    matched = re.search(r'^Image ID: (sha256:[0-9a-f]{64})$', status, re.MULTILINE)
    if not matched:
        raise RuntimeError('build and record a shared runtime before probing')
    image = matched.group(1)
    identity = uuid.uuid4().hex
    volumes = []
    recorded_names = None
    try:
        with tempfile.TemporaryDirectory(prefix='sdlc-interactive-probe-') as temporary:
            instructions = Path(temporary) / 'instructions.md'
            instructions.write_text('Stop implementation until a human answers any question.\n')
            instructions.chmod(0o444)
            settings = instructions.parent / 'settings.json'
            settings.write_text('{}\n')
            settings.chmod(0o444)
            for provider in ('codex', 'claude'):
                name = 'sdlc-interactive-probe-' + identity + '-' + provider
                command(['docker', 'volume', 'create', '--driver', 'local', name])
                volumes.append(name)
                command(container(image, name, root=True) + [
                    '-c', HELPER + '\nrun(sys.argv[1], "init")\n', provider])
                setup = container(image, name, instructions)
                if provider == 'codex':
                    pins = dict(re.findall(r'^ARG (CODEX|CLAUDE)_VERSION=([^\n]+)$',
                                           (ROOT / 'runtime/Dockerfile').read_text(), re.MULTILINE))
                    versions = json.loads(command(setup + ['-c',
                        "import json,subprocess; print(json.dumps({name:subprocess.run([name,'--version'],"
                        "capture_output=True,text=True,check=True).stdout for name in ('codex','claude')}))"]))
                    for provider_name, pin in (('codex', pins['CODEX']), ('claude', pins['CLAUDE'])):
                        assert re.search(r'(?<![0-9.])' + re.escape(pin) + r'(?![0-9.])',
                                         versions[provider_name])
                    for action, mode in (('save', 'default'), ('save', 'on-request'), ('logout', 'default')):
                        result = json.loads(command(setup + ['-c', HELPER + CODEX, action, mode]))
                        assert result['native_save']
                else:
                    command(container(image, name) + ['-c', HELPER + CLAUDE_SETUP])
                    for mode in ('default', 'manual', 'plan'):
                        settings.chmod(0o600)
                        settings.write_text(json.dumps({'skipDangerousModePermissionPrompt': True}
                                                       if mode == 'default' else {}) + '\n')
                        settings.chmod(0o444)
                        result = json.loads(command(setup + ['-c', HELPER + CLAUDE, mode, 'startup']))
                        assert result['native_status'] and not result['customization_executed']
                        if mode == 'manual':
                            skills = re.search(r'Loaded ([1-9][0-9]*) unique skills', result['debug'])
                            assert skills and 'No skills found' not in result['ui']
                            assert 'Loaded 1 CLAUDE.md/rules files:' in result['debug']
                            assert '[User] /provider-auth/CLAUDE.md' in result['debug']
                            assert '@read_medium' in result['ui']
                            print('Claude loaded ' + skills.group(1) + ' image skills with shared instructions.')
                    managed = instructions.parent / 'managed-settings.json'
                    managed.write_text(json.dumps({'permissions': {'disableBypassPermissionsMode': 'disable'}}))
                    managed.chmod(0o444)
                    settings.chmod(0o600)
                    settings.write_text(json.dumps({'skipDangerousModePermissionPrompt': True}) + '\n')
                    settings.chmod(0o444)
                    denied = json.loads(command(container(image, name, instructions, managed=managed)
                        + ['-c', HELPER + CLAUDE, 'default', 'managed-policy']))
                    assert denied['managed_denied']
                    print('Native managed policy disabled bypass mode and selected auto mode.')
            if args.cli:
                state = Path(temporary) / 'state with spaces,comma'
                state.mkdir(mode=0o700)
                recorded_names = state / 'container-names.txt'
                name = 'sdlc-auth-' + identity + '-claude'
                command(['docker', 'volume', 'create', '--driver', 'local',
                         '--label', 'io.sdlc.managed=true', '--label', 'io.sdlc.kind=provider-auth',
                         '--label', 'io.sdlc.installation=' + identity,
                         '--label', 'io.sdlc.provider=claude', name])
                volumes.append(name)
                command(container(image, name, root=True) + [
                    '-c', HELPER + '\nrun("claude", "init")\n'])
                command(container(image, name) + ['-c', HELPER + CLAUDE_SETUP])
                for mode in ('default', 'manual'):
                    probe_cli_cancellation(args.cli.resolve(), image, state, name, recorded_names, mode)
            print('Local native interactive setup checks passed with disposable fake data.')
    finally:
        if recorded_names and recorded_names.exists():
            for name in recorded_names.read_text().splitlines():
                remaining = command(['docker', 'ps', '--all', '--filter',
                                     'name=^/' + name + '$', '--format', '{{.ID}}']).strip()
                if remaining:
                    command(['docker', 'rm', '--force', name])
        for name in volumes:
            command(['docker', 'volume', 'rm', name])


if __name__ == '__main__':
    main()
