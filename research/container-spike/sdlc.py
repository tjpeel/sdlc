#!/usr/bin/env python3
"""Host launcher: explicit profile + repository; credentials stay out of argv."""
import argparse
import contextlib
import functools
import signal
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import shutil
import tempfile
import uuid
import unicodedata

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'runtime/bin'))
from ticket_input import snapshot, ticket_path


def valid_model(model):
    if (not isinstance(model, str) or not model or model.startswith('-')
            or any(c.isspace() or unicodedata.category(c).startswith('C') for c in model)):
        raise ValueError('Model must be a nonempty name without whitespace, control characters or a leading dash.')
    return model


def selected_profile(filename, name, repo):
    profiles = json.loads(filename.read_text())
    if not isinstance(profiles, dict):
        raise ValueError('Profiles must be a JSON object.')
    if name not in profiles:
        raise ValueError(f'Unknown profile: {name}')
    profile = profiles[name]
    if (not isinstance(profile, dict) or not isinstance(profile.get('repositories'), list)
            or any(not isinstance(repository, str) for repository in profile['repositories'])):
        raise ValueError('Profile must contain a repository allowlist.')
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



@contextlib.contextmanager
def interruption_guard(cleanup=False):
    def interrupted(signum, frame):
        raise InterruptedError('Job interrupted; cleanup will run.')
    previous = {}
    for signum in (signal.SIGTERM, signal.SIGHUP, signal.SIGINT):
        previous[signum] = signal.signal(signum, signal.SIG_IGN if cleanup else interrupted)
    try:
        yield
    finally:
        for signum, handler in previous.items():
            signal.signal(signum, handler)


def guarded_job(function):
    @functools.wraps(function)
    def guarded(*arguments, **keywords):
        with interruption_guard():
            return function(*arguments, **keywords)
    return guarded

def unattended_settings(profile, args):
    roles = {}
    for key, flag, model_flag, default in (
        ('implementation', 'implementer', 'implementation_model', 'codex'),
        ('review', 'reviewer', 'review_model', 'claude'),
    ):
        configured = profile.get(key, {})
        if not isinstance(configured, dict) or set(configured) - {'provider', 'model'}:
            raise ValueError(f'Invalid {key} provider configuration.')
        provider = getattr(args, flag) or configured.get('provider', default)
        if provider not in ('codex', 'claude'):
            raise ValueError('Provider must be codex or claude.')
        model = getattr(args, model_flag)
        if model is None:
            model = configured.get('model')
        if model is not None:
            valid_model(model)
        roles[key] = {'provider': provider, 'model': model}
    for field in ('checks', 'cleanup'):
        values = profile.get(field, [] if field == 'cleanup' else None)
        if (not isinstance(values, list) or (field == 'checks' and not values)
                or any(not isinstance(value, str) or not value.strip()
                       or any(c in value for c in '\0\r') for value in values)):
            raise ValueError(f'Profile {field} must contain shell command strings; checks cannot be empty.')
        roles[field] = values
    for field, default, minimum, maximum in (
        ('max_review_rounds', 2, 1, 3), ('agent_timeout_seconds', 3600, 60, 14400),
    ):
        value = profile.get(field, default)
        if type(value) is not int or not minimum <= value <= maximum:
            raise ValueError(f'Profile {field} must be an integer between {minimum} and {maximum}.')
        roles[field] = value
    return roles


@guarded_job
def launch_unattended(args, profile, profile_file, command, env, project):
    """Hold one profile lock through snapshot, credentials and Docker teardown."""
    if not args.ticket:
        raise ValueError('--ticket is required for run.')
    ticket = ticket_path(args.ticket)
    settings = unattended_settings(profile, args)
    job_id = uuid.uuid4().hex
    temporary = Path(tempfile.gettempdir()) / f'sdlc-{job_id}'
    input_directory = temporary / 'input'
    env['SDLC_JOB_ID'] = job_id
    command += ['-f', str(ROOT / 'runtime/compose.job.yaml')]
    test_env = profile.get('test_env_file')
    if test_env is not None:
        if not isinstance(test_env, str) or not test_env.strip() or any(c in test_env for c in '\0\r\n'):
            raise ValueError('test_env_file must be a private environment file path.')
        env['SDLC_TEST_ENV_FILE'] = str((profile_file.parent / test_env).resolve())
        command += ['-f', str(ROOT / 'runtime/compose.test-env.yaml')]
    if args.docker_tests:
        command += ['-f', str(ROOT / 'runtime/compose.docker-tests.yaml')]
    worker = command + ['run', '--rm', '-T', '--no-deps', '--volume',
                        f'{input_directory}:/input/job:ro', 'worker', 'sdlc-job', 'run',
                        '--job', '/input/job/job.json']
    start = command + ['up', '-d', '--wait', '--wait-timeout', '180', 'docker-engine']
    stop = command + ['down', '--remove-orphans']
    print(f'Job ID: {job_id}; profile: {args.profile}; repository: {args.repo}; Docker project: {project}', flush=True)
    print('Implementation: ' + settings['implementation']['provider'] + '; review: ' + settings['review']['provider'], flush=True)
    if args.dry_run or not args.execute:
        print(f'Input snapshot: {input_directory} (ticket and bounded linked work Markdown)')
        if args.docker_tests:
            print(shlex.join(start))
        print(shlex.join(worker))
        print(shlex.join(stop))
        if args.docker_tests:
            print(shlex.join(['docker', 'volume', 'rm', f'sdlc-{job_id}-docker-data',
                              f'sdlc-{job_id}-docker-socket']))
        print('Print only: no secrets read, containers started or GitHub writes. Add --execute to run.')
        return
    # All launcher actions use the project namespace shared by the actual
    # provider volumes, including selections loaded from another profile file.
    lock_id = project
    locks = Path(tempfile.gettempdir()) / 'sdlc-project-locks'
    locks.mkdir(mode=0o700, exist_ok=True)
    with (locks / lock_id).open('a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError('Another launcher command owns this Docker project.')
        active = subprocess.run(['docker', 'ps', '--filter', f'label=com.docker.compose.project={project}',
                                 '--format', '{{.Names}}'], env=env, check=True, capture_output=True, text=True)
        if active.stdout.strip():
            raise ValueError('A container already owns this profile. Stop SSH mode first.')
        temporary.mkdir(mode=0o700)
        started = False
        failure = None
        try:
            manifest = snapshot(args.repo_root, ticket, input_directory)
            descriptor = {'version': 1, 'id': job_id, 'repository': args.repo, 'branch': args.branch,
                          'base_branch': profile['base_branch'], 'ticket': ticket,
                          'manifest': manifest, **settings}
            descriptor_path = input_directory / 'job.json'
            descriptor_path.write_text(json.dumps(descriptor, indent=2) + '\n')
            descriptor_path.chmod(0o444)
            resolve = lambda key: (profile_file.parent / profile[key]).resolve()
            env['SDLC_SIGNING_KEY'] = secret(resolve('signing_key_file'))
            env['SDLC_GITHUB_TOKEN'] = secret(resolve('github_token_file')).strip()
            if test_env is not None:
                secret(Path(env['SDLC_TEST_ENV_FILE']))
            started = True
            if args.docker_tests:
                subprocess.run(start, env=env, check=True)
            subprocess.run(worker, env=env, check=True)
        except BaseException as exc:
            failure = exc
        finally:
            with interruption_guard(cleanup=True):
                stopped = False
                try:
                    if started:
                        subprocess.run(stop, env=env, check=True, timeout=180)
                        stopped = True
                except BaseException as exc:
                    failure = failure or exc
                    print('Docker teardown failed; inspect this job before removing retained resources.', file=sys.stderr)
                if stopped and args.docker_tests:
                    try:
                        subprocess.run(['docker', 'volume', 'rm', f'sdlc-{job_id}-docker-data',
                                        f'sdlc-{job_id}-docker-socket'], env=env, check=True, timeout=180)
                    except BaseException as exc:
                        failure = failure or exc
                        print('Job Docker volume removal failed; inspect retained resources.', file=sys.stderr)
                try:
                    shutil.rmtree(temporary)
                except BaseException as exc:
                    failure = failure or exc
                print(f'Job {job_id}: workspace and results volumes retained.', flush=True)
                print('Inspect result: ' + shlex.join([sys.executable, str(ROOT / 'research/container-spike/sdlc.py'), 'results',
                      '--profiles', str(profile_file), '--profile', args.profile, '--repo', args.repo,
                      '--job-id', job_id]), flush=True)
        if failure is not None:
            raise failure



def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('build', 'login', 'auth-status', 'init', 'cli', 'exec', 'results',
                                          'verify', 'publish', 'ssh-up', 'ssh-down', 'run'))
    parser.add_argument('--profiles', type=Path, default=ROOT / 'profiles.local.json')
    parser.add_argument('--profile', required=True)
    parser.add_argument('--repo', required=True)
    parser.add_argument('--model', type=valid_model,
                        help='Interactive provider model; overrides the profile Codex model for Codex actions.')
    parser.add_argument('--branch')
    parser.add_argument('--job-id', help='Inspect a preserved unattended job with results.')
    parser.add_argument('--ticket', help='Ticket path (repository-relative for run).')
    parser.add_argument('--repo-root', type=Path, default=Path.cwd())
    parser.add_argument('--provider', choices=('codex', 'claude'),
                        help='Provider for login, auth-status or cli (default: codex).')
    parser.add_argument('--implementer', choices=('codex', 'claude'))
    parser.add_argument('--reviewer', choices=('codex', 'claude'))
    parser.add_argument('--implementation-model', type=valid_model)
    parser.add_argument('--review-model', type=valid_model)
    parser.add_argument('--docker-tests', action='store_true',
                        help='Opt in to a separate privileged Docker engine for integration tests.')
    parser.add_argument('--body', type=Path)
    parser.add_argument('--title')
    parser.add_argument('--dry-run', action='store_true', help='Print commands without reading secrets.')
    parser.add_argument('--execute', action='store_true', help='Required to run an unattended job or publish a draft PR.')
    parser.add_argument('--verify-published', action='store_true',
                        help='After publishing, verify the remote branch and draft PR.')
    parser.add_argument('--smoke-checks', action='store_true',
                        help='Before publishing, check the Docker smoke ticket acceptance criteria.')
    args = parser.parse_args()
    if args.provider is not None and args.action not in ('login', 'auth-status', 'cli'):
        parser.error('--provider is only valid for login, auth-status or cli; run uses --implementer and --reviewer')
    args.provider = args.provider or 'codex'
    if args.job_id and (args.action != 'results' or not re.fullmatch(r'[0-9a-f]{32}', args.job_id)):
        parser.error('--job-id requires results and a 32-character lowercase hex job ID')
    if args.docker_tests and args.action != 'run':
        parser.error('--docker-tests is only valid for run')
    if args.verify_published and args.action != 'publish':
        parser.error('--verify-published is only valid for publish')
    if args.smoke_checks and args.action != 'publish':
        parser.error('--smoke-checks is only valid for publish')
    profile_file = args.profiles.resolve()
    profile = selected_profile(profile_file, args.profile, args.repo)
    model = args.model if args.model is not None else (
        profile.get('model', '') if args.provider == 'codex' else '')
    if profile['base_branch'].startswith(('-', 'codex/')):
        parser.error('Invalid base branch in profile.')
    subprocess.run(['git', 'check-ref-format', '--branch', profile['base_branch']],
                   check=True, stdout=subprocess.DEVNULL)
    if args.action in ('exec', 'verify', 'publish', 'run'):
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
    credentials = args.action not in ('build', 'login', 'auth-status', 'results', 'ssh-down')
    if credentials:
        command += ['-f', str(ROOT / 'runtime/compose.credentials.yaml')]
    if args.action.startswith('ssh-'):
        command += ['-f', str(ROOT / 'runtime/compose.ssh.yaml')]
    env = os.environ.copy()
    # Avoid inherited account switching, shell agent sockets and Compose overrides.
    for key in ('GH_TOKEN', 'GITHUB_TOKEN', 'GH_HOST', 'SSH_AUTH_SOCK',
                'SDLC_SIGNING_KEY', 'SDLC_GITHUB_TOKEN', 'COMPOSE_FILE',
                'COMPOSE_PROJECT_NAME', 'COMPOSE_PROFILES', 'SDLC_MODEL', 'SDLC_JOB_ID',
                'DOCKER_HOST', 'DOCKER_CONTEXT', 'ANTHROPIC_API_KEY', 'CLAUDE_CODE_OAUTH_TOKEN',
                'OPENAI_API_KEY', 'SDLC_TEST_ENV_FILE'):
        env.pop(key, None)
    env.update({
        'SDLC_REPOSITORY': args.repo, 'SDLC_GITHUB_LOGIN': profile['github_login'],
        'SDLC_GIT_NAME': profile['git_name'], 'SDLC_GIT_EMAIL': profile['git_email'],
        'SDLC_BASE_BRANCH': profile['base_branch'],
        # Claude's explicit interactive model must not configure machine Codex defaults.
        'SDLC_MODEL': model if args.provider == 'codex' else '',
    })
    if args.action == 'results' and args.job_id:
        env['SDLC_JOB_ID'] = args.job_id
        command += ['-f', str(ROOT / 'runtime/compose.job.yaml')]
    resolve = lambda key: (profile_file.parent / profile[key]).resolve()
    if args.action == 'run':
        launch_unattended(args, profile, profile_file, command, env, project)
        return
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
                path = Path(path).resolve()
                if not path.is_file() or any(c in str(path) for c in ':\r\n'):
                    raise ValueError(f'Invalid input file: {path}')
                command += ['--volume', f'{path}:{target}:ro']
        command += ['worker']
        if args.action == 'login':
            command += (['codex', 'login', '--device-auth'] if args.provider == 'codex'
                        else ['claude', 'auth', 'login'])
        elif args.action == 'auth-status':
            command += (['codex', 'login', 'status'] if args.provider == 'codex'
                        else ['claude', 'auth', 'status', '--text'])
        elif args.action == 'cli':
            interactive = (['codex', '--dangerously-bypass-approvals-and-sandbox', '-C', '/workspace/repo']
                           if args.provider == 'codex' else ['claude', '--dangerously-skip-permissions'])
            if model:
                interactive += ['--model', model]
            working_directory = 'cd /workspace/repo && ' if args.provider == 'claude' else ''
            command += ['bash', '-lc', 'sdlc-job init && ' + working_directory + 'exec ' + shlex.join(interactive)]
        else:
            command += ['sdlc-job', args.action]
            if args.job_id:
                command += ['--job-id', args.job_id]
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
          f'model: {model or ("Codex default" if args.provider == "codex" else "Claude default")}', flush=True)
    if args.dry_run or (args.action == 'publish' and not args.execute):
        print(shlex.join(command))
        print('Print only: no secrets read, containers started or GitHub writes.')
        return
    lock_id = project
    locks = Path(tempfile.gettempdir()) / 'sdlc-project-locks'
    locks.mkdir(mode=0o700, exist_ok=True)
    with (locks / lock_id).open('a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError('Another launcher command owns this Docker project.')
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
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as exc:
        print(f'Launcher failed: {exc}', file=sys.stderr)
        sys.exit(1)
