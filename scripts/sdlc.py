#!/usr/bin/env python3
"""Host launcher: explicit profile + repository; credentials stay out of argv."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import unicodedata

ROOT = Path(__file__).resolve().parents[1]


def valid_model(model):
    if (not isinstance(model, str) or not model or model.startswith('-')
            or any(c.isspace() or unicodedata.category(c).startswith('C') for c in model)):
        raise ValueError('Model must be a nonempty name without whitespace, control characters or a leading dash.')
    return model


def selected_profile(filename, name, repo):
    profiles = json.loads(filename.read_text())
    if name not in profiles:
        raise ValueError(f'Unknown profile: {name}')
    profile = profiles[name]
    if not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repo):
        raise ValueError('Use a github.com OWNER/REPO name.')
    if repo not in profile['repositories']:
        raise ValueError('Repository is not in the selected profile allowlist.')
    for field in ('github_login', 'git_name', 'git_email', 'base_branch'):
        value = profile[field]
        if not isinstance(value, str) or not value or any(c in value for c in '\r\n\0'):
            raise ValueError(f'Invalid profile field: {field}')
    if not re.fullmatch(r'[A-Za-z0-9-]+', profile['github_login']):
        raise ValueError('Invalid GitHub login.')
    if any(c.isspace() for c in profile['git_email']):
        raise ValueError('Use one verified GitHub email address.')
    if 'model' in profile:
        valid_model(profile['model'])
    return profile


def secret(filename):
    if not filename.is_file():
        raise ValueError(f'Missing secret file: {filename}')
    if filename.stat().st_mode & 0o077:
        raise ValueError(f'Secret file must be private (chmod 600): {filename}')
    value = filename.read_text()
    if not value.strip():
        raise ValueError(f'Empty secret file: {filename}')
    return value


def build_arguments(env):
    arguments = []
    for name in ('SKILLS_REVISION', 'AGENTS_REVISION'):
        value = env.get(f'SDLC_{name}')
        if value is not None:
            if not re.fullmatch(r'[0-9a-f]{40}', value):
                raise ValueError(f'SDLC_{name} must be a full lowercase commit SHA.')
            arguments += ['--build-arg', f'{name}={value}']
    return arguments


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('build', 'login', 'init', 'cli', 'exec', 'results',
                                          'verify', 'publish', 'ssh-up', 'ssh-down'))
    parser.add_argument('--profiles', type=Path, default=ROOT / 'profiles.local.json')
    parser.add_argument('--profile', required=True)
    parser.add_argument('--repo', required=True)
    parser.add_argument('--model', type=valid_model,
                        help='Codex model; overrides the selected profile model.')
    parser.add_argument('--branch')
    parser.add_argument('--ticket', type=Path)
    parser.add_argument('--body', type=Path)
    parser.add_argument('--title')
    parser.add_argument('--dry-run', action='store_true', help='Print commands without reading secrets.')
    parser.add_argument('--execute', action='store_true', help='Required to actually publish a draft PR.')
    parser.add_argument('--verify-published', action='store_true',
                        help='After publishing, verify the remote branch and draft PR.')
    parser.add_argument('--smoke-checks', action='store_true',
                        help='Before publishing, check the Docker smoke ticket acceptance criteria.')
    args = parser.parse_args()
    if args.verify_published and args.action != 'publish':
        parser.error('--verify-published is only valid for publish')
    if args.smoke_checks and args.action != 'publish':
        parser.error('--smoke-checks is only valid for publish')
    profile_file = args.profiles.resolve()
    profile = selected_profile(profile_file, args.profile, args.repo)
    model = args.model if args.model is not None else profile.get('model', '')
    if profile['base_branch'].startswith(('-', 'codex/')):
        parser.error('Invalid base branch in profile.')
    subprocess.run(['git', 'check-ref-format', '--branch', profile['base_branch']],
                   check=True, stdout=subprocess.DEVNULL)
    if args.action in ('exec', 'verify', 'publish'):
        if not args.branch or args.branch.startswith(('-', 'codex/')):
            parser.error('--branch must be explicit and must not start with codex/')
        # git check-ref-format has no repository or network dependency.
        subprocess.run(['git', 'check-ref-format', '--branch', args.branch],
                       check=True, stdout=subprocess.DEVNULL)
    if args.action == 'exec' and not args.ticket:
        parser.error('--ticket is required')
    if args.action == 'publish' and (not args.title or not args.body):
        parser.error('--title and --body are required')

    identity = '|'.join((args.repo, profile['github_login'], profile['git_email']))
    suffix = hashlib.sha256(identity.encode()).hexdigest()[:12]
    name = re.sub(r'[^a-z0-9-]', '-', args.profile.lower()).strip('-') or 'profile'
    project = f'sdlc-{name[:30]}-{suffix}'
    command = ['docker', 'compose', '--project-name', project,
               '-f', str(ROOT / 'runtime/compose.yaml')]
    credentials = args.action not in ('build', 'login', 'results', 'ssh-down')
    if credentials:
        command += ['-f', str(ROOT / 'runtime/compose.credentials.yaml')]
    if args.action.startswith('ssh-'):
        command += ['-f', str(ROOT / 'runtime/compose.ssh.yaml')]
    env = os.environ.copy()
    # Avoid inherited account switching, shell agent sockets and Compose overrides.
    for key in ('GH_TOKEN', 'GITHUB_TOKEN', 'GH_HOST', 'SSH_AUTH_SOCK',
                'SDLC_SIGNING_KEY', 'SDLC_GITHUB_TOKEN', 'COMPOSE_FILE',
                'COMPOSE_PROJECT_NAME', 'COMPOSE_PROFILES', 'SDLC_MODEL'):
        env.pop(key, None)
    env.update({
        'SDLC_REPOSITORY': args.repo, 'SDLC_GITHUB_LOGIN': profile['github_login'],
        'SDLC_GIT_NAME': profile['git_name'], 'SDLC_GIT_EMAIL': profile['git_email'],
        'SDLC_BASE_BRANCH': profile['base_branch'],
        'SDLC_MODEL': model,
    })
    resolve = lambda key: (profile_file.parent / profile[key]).resolve()
    if args.action.startswith('ssh-'):
        port = int(profile.get('ssh_port', 2222))
        if not 1024 <= port <= 65535:
            raise ValueError('SSH port must be between 1024 and 65535.')
        env['SDLC_SSH_PORT'] = str(port)
        env['SDLC_SSH_PUBLIC_KEY_FILE'] = str(resolve('ssh_public_key_file'))

    if args.action == 'build':
        command += ['build', *build_arguments(env), 'worker']
    elif args.action == 'ssh-up':
        command += ['up', '-d', 'worker']
    elif args.action == 'ssh-down':
        command += ['down']
    else:
        command += ['run', '--rm']
        if args.action not in ('cli', 'login'):
            command += ['-T']
        for field, target in (('ticket', '/input/ticket.md'), ('body', '/input/pr-body.md')):
            path = getattr(args, field)
            if path:
                path = path.resolve()
                if not path.is_file() or any(c in str(path) for c in ':\r\n'):
                    raise ValueError(f'Invalid input file: {path}')
                command += ['--volume', f'{path}:{target}:ro']
        command += ['worker']
        if args.action == 'login':
            command += ['codex', 'login', '--device-auth']
        elif args.action == 'cli':
            codex = ['codex', '--dangerously-bypass-approvals-and-sandbox', '-C', '/workspace/repo']
            if model:
                codex += ['--model', model]
            command += ['bash', '-lc', 'sdlc-job init && exec ' + shlex.join(codex)]
        else:
            command += ['sdlc-job', args.action]
            if args.branch:
                command += ['--branch', args.branch]
            if args.ticket:
                command += ['--ticket', '/input/ticket.md']
            if args.body:
                command += ['--body', '/input/pr-body.md', '--title', args.title]
            if args.verify_published:
                command += ['--verify-published']
            if args.smoke_checks:
                command += ['--smoke-checks']

    print(f'Profile: {args.profile}; repository: {args.repo}; Docker project: {project}; '
          f'model: {model or "Codex default"}', flush=True)
    if args.dry_run or (args.action == 'publish' and not args.execute):
        print(shlex.join(command))
        print('Print only: no secrets read, containers started or GitHub writes.')
        return
    locks = ROOT / '.state/locks'
    locks.mkdir(parents=True, exist_ok=True)
    with (locks / project).open('w') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError('Another launcher command owns this workspace.')
        if args.action not in ('build', 'ssh-down'):
            active = subprocess.run(
                ['docker', 'ps', '--filter', f'label=com.docker.compose.project={project}',
                 '--format', '{{.Names}}'], env=env, check=True, capture_output=True, text=True)
            if active.stdout.strip():
                raise ValueError('A container already owns this workspace. Stop SSH mode first.')
        if credentials:
            env['SDLC_SIGNING_KEY'] = secret(resolve('signing_key_file'))
            env['SDLC_GITHUB_TOKEN'] = secret(resolve('github_token_file')).strip()
        # subprocess argv contains only paths and ordinary metadata, never secret values.
        subprocess.run(command, env=env, check=True)


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as exc:
        print(f'Launcher failed: {exc}', file=sys.stderr)
        sys.exit(1)
