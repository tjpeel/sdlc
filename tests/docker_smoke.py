#!/usr/bin/env python3
"""Opt-in container/SSH test using disposable keys and a fake GitHub token.

Build the image first. This does not log into Codex, call a model, contact the
GitHub API, clone, push or create a PR. It removes only its own Docker resources.
"""
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]


def main():
    project = 'sdlc-smoke-' + uuid.uuid4().hex[:10]
    with tempfile.TemporaryDirectory(prefix='sdlc-smoke-') as folder:
        folder = Path(folder)
        for name in ('signing', 'control'):
            subprocess.run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '',
                            '-f', str(folder / name)], check=True)
        with socket.socket() as probe:
            probe.bind(('127.0.0.1', 0))
            port = probe.getsockname()[1]
        env = os.environ.copy()
        env.update({
            'SDLC_REPOSITORY': 'example/offline-test', 'SDLC_GITHUB_LOGIN': 'example',
            'SDLC_GIT_NAME': 'Smoke Test', 'SDLC_GIT_EMAIL': 'smoke@example.invalid',
            'SDLC_BASE_BRANCH': 'main', 'SDLC_SIGNING_KEY': (folder / 'signing').read_text(),
            'SDLC_MODEL': 'example-smoke-model',
            'SDLC_GITHUB_TOKEN': 'fake-offline-token',
            'SDLC_SSH_PUBLIC_KEY_FILE': str(folder / 'control.pub'), 'SDLC_SSH_PORT': str(port),
        })
        compose = ['docker', 'compose', '--project-name', project,
                   '-f', str(ROOT / 'runtime/compose.yaml'),
                   '-f', str(ROOT / 'runtime/compose.credentials.yaml')]
        ssh_compose = compose + ['-f', str(ROOT / 'runtime/compose.ssh.yaml')]
        def call(args, **kwargs):
            return subprocess.run(args, env=env, text=True, check=True, **kwargs)
        try:
            command = r"""
set -eu
test "$(id -u)" = 1000
test -z "${SSH_AUTH_SOCK:-}"
codex --version
gh --version
python3 - <<'PY'
import os, pathlib, tomllib
skills = pathlib.Path.home() / '.agents/skills'
assert list(skills.glob('*/SKILL.md')), 'No bundled skills found'
agents = list(pathlib.Path('/etc/codex/agents').glob('*.toml'))
assert agents, 'No bundled agents found'
assert 'read_low' in {tomllib.loads(agent.read_text())['name'] for agent in agents}
assert tomllib.loads(pathlib.Path('/etc/codex/config.toml').read_text())['model'] == 'example-smoke-model'
for skill in skills.iterdir():
    for resource in skill.iterdir():
        assert resource.exists(), f'Broken skill resource: {resource}'
print('Bundled skills, agents, resources and model defaults are available.')
PY
python3 -B -m unittest discover -s /spike/tests -v
git init -q -b main /workspace/repo
printf 'offline smoke test\n' > /workspace/repo/example.txt
git -C /workspace/repo add example.txt
git -C /workspace/repo commit -q -m 'Create smoke test commit'
git -C /workspace/repo verify-commit HEAD
python3 - <<'PY'
import os, subprocess
p=subprocess.run(['git','credential','fill'], input='protocol=https\nhost=github.com\n\n', text=True, capture_output=True, check=True)
assert 'password='+os.environ['GH_TOKEN'] in p.stdout
print('HTTPS credential helper uses the supplied test token without network access.')
PY
"""
            call(compose + ['run', '--rm', '-T', '--volume', f'{ROOT}:/spike:ro',
                            'worker', 'bash', '-lc', command])
            call(ssh_compose + ['up', '-d', 'worker'])
            container = call(ssh_compose + ['ps', '-q', 'worker'], capture_output=True).stdout.strip()
            for _ in range(10):
                result = subprocess.run(['docker', 'exec', container, 'cat',
                                         '/var/lib/sdlc-ssh/ssh_host_ed25519_key.pub'],
                                        env=env, capture_output=True, text=True)
                if result.returncode == 0:
                    break
                time.sleep(0.5)
            else:
                call(ssh_compose + ['logs', '--tail', '30', 'worker'])
                raise RuntimeError('SSH host-key setup failed: ' + result.stderr)
            host_public = result.stdout.strip().split()
            known_hosts = folder / 'known_hosts'
            known_hosts.write_text(f'[127.0.0.1]:{port} {host_public[0]} {host_public[1]}\n')
            ssh = ['ssh', '-F', '/dev/null', '-p', str(port), '-i', str(folder / 'control'),
                   '-o', 'BatchMode=yes', '-o', 'IdentityAgent=none', '-o', 'IdentitiesOnly=yes',
                   '-o', 'StrictHostKeyChecking=yes', '-o', f'UserKnownHostsFile={known_hosts}',
                   '-o', 'ConnectTimeout=3', 'node@127.0.0.1']
            ready = False
            for _ in range(10):
                result = subprocess.run(ssh + ['true'], env=env, capture_output=True, text=True)
                if result.returncode == 0:
                    ready = True
                    break
                time.sleep(0.5)
            if not ready:
                call(ssh_compose + ['logs', '--tail', '30', 'worker'])
                raise RuntimeError('SSH never became ready: ' + result.stderr)
            call(ssh + ['test -n "$GH_TOKEN" && test "$CODEX_HOME" = /home/node/.codex '
                        '&& test "$SDLC_MODEL" = example-smoke-model '
                        '&& test -r "$HOME/.agents/skills/tjpeel-writing-unslop/SKILL.md" '
                        '&& test -r /etc/codex/agents/tjpeel-read-low.toml '
                        '&& test -z "${SSH_AUTH_SOCK:-}" && codex --version '
                        '&& git -C /workspace/repo verify-commit HEAD'])
            call(ssh + ['bash -lc \'test -n "$GH_TOKEN" && test "$(id -u)" = 1000\''])
            call(ssh + ['sdlc-job results'])
            print('Docker smoke test passed: non-root tools, file signing, token helper, '
                  'bundled skills and agents, model defaults, ordinary SSH commands '
                  'and login shells. No model/GitHub request made.')
        finally:
            call(ssh_compose + ['down', '--volumes', '--remove-orphans'])


if __name__ == '__main__':
    main()
