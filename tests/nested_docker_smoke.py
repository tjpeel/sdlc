#!/usr/bin/env python3
"""Opt-in live Docker-in-Docker checks using only generated, public fixtures."""
import argparse
import os
from pathlib import Path
import subprocess
import sys
import uuid

ROOT = Path(__file__).resolve().parents[1]

# Run this inside the ordinary worker. The inner daemon sees the same workspace
# path and network namespace; neither image nor fixture receives credentials.
PROBE = r"""
import json, os
from pathlib import Path
import subprocess
import urllib.request

assert Path('/.dockerenv').exists() and os.getuid() == 1000
assert os.environ['DOCKER_HOST'] == 'unix:///run/job-docker/docker.sock'
for command in (['codex', '--version'], ['claude', '--version'], ['dotnet', '--version'],
                ['docker', 'compose', 'version'], ['docker', 'buildx', 'version']):
    subprocess.run(command, check=True)
for catalogue in ('/home/node/.agents/skills', '/etc/codex/agents',
                  '/home/node/.claude/skills', '/home/node/.claude/agents'):
    assert any(Path(catalogue).iterdir()), catalogue
folder = Path('/workspace/nested-smoke')
folder.mkdir()
marker = os.environ['SDLC_JOB_ID']
(folder / 'marker.txt').write_text(marker)
(folder / 'Dockerfile').write_text('FROM busybox:1.37.0\nCOPY marker.txt /site/index.html\n')
(folder / 'init.sh').write_text('cp /site/index.html /site/bind.txt\nexec httpd -f -p 8080 -h /site\n')
(folder / 'compose.yaml').write_text('''services:
  http:
    build: .
    entrypoint: [sh, /init.sh]
    ports: ["127.0.0.1:18085:8080"]
    volumes: ["./init.sh:/init.sh:ro"]
    healthcheck:
      test: [CMD, wget, -q, -O-, "http://127.0.0.1:8080/bind.txt"]
      interval: 1s
      timeout: 2s
      retries: 30
''')
command = ['docker', 'compose', '--project-name', 'nested-smoke', '-f', str(folder / 'compose.yaml')]
try:
    subprocess.run(command + ['up', '--build', '-d', '--wait'], check=True, cwd=folder)
    for route in ('/', '/bind.txt'):
        with urllib.request.urlopen('http://127.0.0.1:18085' + route, timeout=5) as response:
            assert response.read().decode() == marker
    print('Nested build, relative bind mount and worker localhost checks passed.')
    # An intentionally failing check must still tear down its fixture stack.
    failure = subprocess.run(['sh', '-c', 'exit 7'])
    assert failure.returncode == 7
finally:
    subprocess.run(command + ['down', '-v', '--remove-orphans'], check=True, cwd=folder)
assert not subprocess.check_output(command + ['ps', '-q'], text=True).strip()
subprocess.run(['dotnet', 'new', 'console', '--framework', 'net10.0', '--no-restore',
                '--output', str(folder / 'app')], check=True)
subprocess.run(['dotnet', 'run', '--project', str(folder / 'app'),
                '--property:RestoreIgnoreFailedSources=true'], check=True)
print('Fixture cleanup and .NET 10 execution passed.')
"""


def base_command(project):
    return ['docker', 'compose', '--project-name', project,
            '-f', str(ROOT / 'runtime/compose.yaml'),
            '-f', str(ROOT / 'runtime/compose.job.yaml'),
            '-f', str(ROOT / 'runtime/compose.docker-tests.yaml')]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--execute', action='store_true', help='Start disposable privileged daemons.')
    args = parser.parse_args(argv)
    if not args.execute:
        print('Print only. Build the runtime first, then add --execute for two disposable Docker-in-Docker probes.')
        return 0
    jobs = []
    try:
        # Start both daemons before either probe. Identical nested project names
        # and published ports must remain isolated across their namespaces.
        for index in range(2):
            job_id = uuid.uuid4().hex
            project = 'sdlc-nested-smoke-' + job_id[:12]
            env = os.environ.copy()
            # Never let host authentication enter this public-fixture test.
            for key in tuple(env):
                if key.startswith(('SDLC_', 'COMPOSE_')) or key in (
                        'GH_TOKEN', 'GITHUB_TOKEN', 'ANTHROPIC_API_KEY',
                        'CLAUDE_CODE_OAUTH_TOKEN', 'OPENAI_API_KEY', 'CODEX_API_KEY', 'SSH_AUTH_SOCK'):
                    env.pop(key)
            env.update(SDLC_JOB_ID=job_id, SDLC_REPOSITORY='example-org/example-repo',
                       SDLC_GITHUB_LOGIN='example-user', SDLC_GIT_NAME='Example User',
                       SDLC_GIT_EMAIL='example@example.invalid', SDLC_BASE_BRANCH='main')
            command = base_command(project)
            jobs.append((job_id, project, env, command))
            subprocess.run(command + ['up', '-d', '--wait', 'docker-engine'], env=env, check=True)
        workers = []
        try:
            for _, _, env, command in jobs:
                workers.append(subprocess.Popen(command + ['run', '--rm', '-T', '--no-deps',
                                                          'worker', 'python3', '-c', PROBE], env=env))
            codes = [process.wait(timeout=600) for process in workers]
            if any(codes):
                raise RuntimeError('A nested Docker probe failed.')
        finally:
            for process in workers:
                if process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
        print('Both Docker-in-Docker jobs passed with the same nested ports.')
        return 0
    finally:
        errors = []
        for job_id, project, env, command in reversed(jobs):
            stopped = subprocess.run(command + ['down', '--remove-orphans'], env=env)
            if stopped.returncode:
                errors.append(project)
                continue
            volumes = [f'sdlc-{job_id}-workspace', f'sdlc-{job_id}-docker-data',
                       f'sdlc-{job_id}-docker-socket']
            # Compose's default naming uses underscores for provider state.
            volumes += [f'{project}_{name}' for name in ('codex-state', 'claude-state', 't3-state')]
            existing = [name for name in volumes if subprocess.run(
                ['docker', 'volume', 'inspect', name], env=env, capture_output=True).returncode == 0]
            if existing and subprocess.run(['docker', 'volume', 'rm', *existing], env=env).returncode:
                errors.append(project)
        if errors:
            raise RuntimeError('Some smoke resources remain; inspect projects: ' + ', '.join(errors))


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        print(f'Nested Docker smoke failed: {error}', file=sys.stderr)
        sys.exit(1)
