#!/usr/bin/env python3
"""Opt-in unattended container acceptance with local Git and provider/API stubs.

No model call or GitHub write occurs. Fixtures, keys, containers and volumes are
disposable; only the resources named by this invocation are removed.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import textwrap
import uuid

ROOT = Path(__file__).resolve().parents[1]
REPOSITORY = 'example/offline-test'
TICKET = '.sdlc/work/tickets/123/ticket-1.md'
EXPECTED = 'The repaired example.\n'
CONTROL = Path('/workspace/offline-control')
REAL_GIT = '/usr/bin/git'

GIT_STUB = '''
import json, os, sys
from pathlib import Path
root = Path(os.environ['SDLC_STUB_CONTROL'])
args = sys.argv[1:]
position = 0
while position < len(args) and args[position] in ('-c', '-C', '--git-dir', '--work-tree'):
    position += 2
operation = args[position] if position < len(args) else ''
if operation in ('clone', 'fetch', 'ls-remote', 'push'):
    with (root / 'git-operations.jsonl').open('a') as stream:
        stream.write(json.dumps({'operation': operation}) + '\\n')
    args = ['-c', 'url.' + str(root / 'remote.git') + '.insteadOf=' +
            'https://github.com/example/offline-test.git', *args]
os.execv('/usr/bin/git', ['/usr/bin/git', *args])
'''

AGENT_STUB = '''
import hashlib, json, os, sys
from pathlib import Path
import subprocess
root = Path(os.environ['SDLC_STUB_CONTROL'])
provider = Path(sys.argv[0]).name
args = sys.argv[1:]
prompt = sys.stdin.read()
job = json.loads(Path('/input/job/job.json').read_text())
workspace = Path('/workspace/repo')
assert Path.cwd() == workspace
assert not any(value in args for value in ('resume', '--resume', '--continue', '--bare'))
if provider == 'codex':
    assert args[0] == 'exec' and '--json' in args
    schema = json.loads(Path(args[args.index('--output-schema') + 1]).read_text())
else:
    assert args[0] == '-p' and '--dangerously-skip-permissions' in args
    assert args[args.index('--output-format') + 1] == 'json'
    schema = json.loads(args[args.index('--json-schema') + 1])
role = 'implementation' if 'status' in schema['properties'] else 'review'
assert provider == job[role]['provider']
assert args[args.index('--model') + 1] == job[role]['model']
snapshot = Path('/workspace/results') / job['id'] / 'input'
ticket = Path('/input/job') / job['ticket']
assert str(ticket) in prompt and ticket.is_file()
assert 'Replace the broken example' in ticket.read_text()
assert (Path('/input/job') / '.sdlc/work/tickets/123/context.md').is_file()
assert (workspace / 'AGENTS.md').is_file()
assert list((Path.home() / '.agents/skills').glob('*/SKILL.md'))
assert list(Path('/etc/codex/agents').glob('*.toml'))
assert list((Path.home() / '.claude/skills').glob('*/SKILL.md'))
assert list((Path.home() / '.claude/agents').glob('*.md'))
def git(*arguments):
    return subprocess.run(['/usr/bin/git', *arguments], cwd=workspace,
                          check=True, capture_output=True, text=True).stdout.strip()
before = git('rev-parse', 'HEAD')
if role == 'implementation':
    assert not git('status', '--porcelain')
    assert (workspace / 'example.txt').read_text() == 'The broken example.\\n'
    (workspace / 'example.txt').write_text('The repaired example.\\n')
    git('add', '--', 'example.txt')
    git('commit', '-S', '-q', '-m', 'Repair example from supplied ticket')
    git('verify-commit', 'HEAD')
    result = {'status': 'complete', 'title': 'Repair the supplied example',
              'body': 'Replaces the broken example with the requested text. '
                      'The configured fixture check and independent review passed.'}
else:
    if provider == 'claude':
        assert args[args.index('--tools') + 1] == 'Read,Glob,Grep,Skill'
        assert args[args.index('--disallowedTools') + 1] == 'mcp__*'
    assert (workspace / 'example.txt').read_text() == 'The repaired example.\\n'
    diff = (snapshot.parent / 'review-diff-1.patch').read_text()
    assert '-The broken example.' in diff and '+The repaired example.' in diff
    assert git('diff', '--name-only', 'origin/main...HEAD') == 'example.txt'
    assert not git('status', '--porcelain')
    git('verify-commit', 'HEAD')
    after = git('rev-parse', 'HEAD')
    assert before == after
    (root / 'review.json').write_text(json.dumps({
        'provider': provider, 'reviewed_head': before, 'post_review_head': after,
        'ticket_sha256': hashlib.sha256(ticket.read_bytes()).hexdigest()}))
    result = {'verdict': 'pass', 'findings': []}
with (root / 'agent-calls.jsonl').open('a') as stream:
    stream.write(json.dumps({'provider': provider, 'role': role}) + '\\n')
if provider == 'codex':
    Path(args[args.index('--output-last-message') + 1]).write_text(json.dumps(result))
    print(json.dumps({'type': 'turn.completed', 'offline_stub': True}))
else:
    print(json.dumps({'type': 'result', 'subtype': 'success', 'is_error': False,
                      'structured_output': result}))
'''

GH_STUB = '''
import json, os, sys
from pathlib import Path
import subprocess
from urllib.parse import unquote
root = Path(os.environ['SDLC_STUB_CONTROL'])
args = sys.argv[1:]
job = json.loads(Path('/input/job/job.json').read_text())
repo = 'example/offline-test'
assert job['repository'] == repo
def option(name):
    return args[args.index(name) + 1]
def remote_head(branch):
    return subprocess.run(['/usr/bin/git', '--git-dir=' + str(root / 'remote.git'),
                           'rev-parse', 'refs/heads/' + branch], check=True,
                          capture_output=True, text=True).stdout.strip()
record = root / 'pull-requests.json'
if args[:2] == ['api', 'user']:
    assert option('--jq') == '.login'
    print('example')
elif args[0] == 'api' and args[1].startswith('repos/' + repo + '/git/ref/heads/'):
    assert option('--jq') == '.object.sha'
    branch = unquote(args[1].split('/git/ref/heads/', 1)[1])
    assert branch == job['branch']
    print(remote_head(branch))
elif args[:2] == ['pr', 'create']:
    assert option('--repo') == repo and '--draft' in args
    assert option('--head') == job['branch'] and option('--base') == job['base_branch']
    assert option('--title') and Path(option('--body-file')).read_text().strip()
    assert not record.exists(), 'The runner attempted duplicate PR creation'
    head = remote_head(job['branch'])
    assert head == json.loads((root / 'review.json').read_text())['reviewed_head']
    pr = {'url': 'https://github.com/' + repo + '/pull/1', 'isDraft': True,
          'headRefOid': head, 'headRefName': job['branch'],
          'baseRefName': job['base_branch'], 'author': {'login': 'example'}}
    record.write_text(json.dumps([pr]))
    print(pr['url'])
elif args[:2] == ['pr', 'list']:
    assert option('--repo') == repo and option('--head') == job['branch']
    assert option('--base') == job['base_branch'] and option('--state') == 'open'
    print(record.read_text() if record.exists() else '[]')
else:
    raise SystemExit('Unsupported offline GitHub stub operation')
'''


def command(arguments, **kwargs):
    return subprocess.run(arguments, text=True, check=True, **kwargs)


def json_lines(path):
    return [json.loads(line) for line in path.read_text().splitlines() if line]


def checkout_fingerprint(checkout):
    """Include ignored inputs, Git metadata and file modes in the host comparison."""
    digest = hashlib.sha256()
    for path in sorted(checkout.rglob('*')):
        digest.update(str(path.relative_to(checkout)).encode() + b'\0')
        digest.update(str(path.stat().st_mode).encode() + b'\0')
        if path.is_file():
            digest.update(path.read_bytes())
    return digest.hexdigest()


def write_stub(path, source):
    # Executable source is generated at runtime; no host interpreter path is committed.
    path.write_text('#!/usr/bin/python3\n' + textwrap.dedent(source))
    path.chmod(0o700)


def inside_container():
    if not Path('/.dockerenv').is_file() or os.getuid() != 1000:
        raise RuntimeError('The fixture runner requires the non-root worker container.')
    mounts = [line.split() for line in Path('/proc/self/mountinfo').read_text().splitlines()]
    assert any(parts[4] == '/input/job' and 'ro' in parts[5].split(',') for parts in mounts)
    job = json.loads(Path('/input/job/job.json').read_text())
    CONTROL.mkdir(mode=0o700)
    seed = CONTROL / 'seed'
    seed.mkdir()
    (seed / 'example.txt').write_text('The broken example.\n')
    (seed / '.gitignore').write_text('.sdlc/\n')
    (seed / 'AGENTS.md').write_text(
        'Read the supplied ticket and linked context. Preserve the configured Git identity, '
        'sign commits, and run python3 fixture_checks.py after changing example.txt.\n')
    (seed / 'fixture_checks.py').write_text(
        "from pathlib import Path\n"
        "assert Path('example.txt').read_text() == 'The repaired example.\\n'\n"
        "print('Synthetic acceptance check passed.')\n")
    command([REAL_GIT, 'init', '-q', '-b', 'main', str(seed)])
    command([REAL_GIT, '-C', str(seed), 'add', '.'])
    command([REAL_GIT, '-C', str(seed), 'commit', '-S', '-q', '-m', 'Create offline fixture'])
    remote = CONTROL / 'remote.git'
    command([REAL_GIT, 'clone', '-q', '--bare', '--', str(seed), str(remote)])
    stubs = CONTROL / 'bin'
    stubs.mkdir(mode=0o700)
    for name, source in (('git', GIT_STUB), ('gh', GH_STUB),
                         ('codex', AGENT_STUB), ('claude', AGENT_STUB)):
        write_stub(stubs / name, source)
    env = os.environ.copy()
    env.update(PATH=str(stubs) + os.pathsep + env['PATH'], SDLC_STUB_CONTROL=str(CONTROL))
    command(['sdlc-job', 'run', '--job', '/input/job/job.json'], env=env)
    results = Path('/workspace/results') / job['id']
    metadata = json.loads((results / 'result.json').read_text())
    assert metadata['status'] == 'published' and metadata['stage'] == 'complete'
    review = json.loads((CONTROL / 'review.json').read_text())
    head = command([REAL_GIT, '--git-dir=' + str(remote), 'rev-parse',
                    'refs/heads/' + job['branch']], capture_output=True).stdout.strip()
    assert head == review['reviewed_head'] == review['post_review_head'] == metadata['commit']
    command([REAL_GIT, '--git-dir=' + str(remote), 'verify-commit', head], capture_output=True)
    prs = json.loads((CONTROL / 'pull-requests.json').read_text())
    assert len(prs) == 1 and prs[0]['isDraft'] is True and prs[0]['headRefOid'] == head
    calls = json_lines(CONTROL / 'agent-calls.jsonl')
    assert calls == [{'provider': job['implementation']['provider'], 'role': 'implementation'},
                     {'provider': job['review']['provider'], 'role': 'review'}]
    for role in ('implementation', 'review'):
        result = json.loads((results / f'{role}-1/result.json').read_text())
        assert result['provider'] == job[role]['provider'] and result['model'] == job[role]['model']
    saved_job = json.loads((results / 'input/job.json').read_text())
    assert saved_job == job
    saved_manifest = json.loads((results / 'input/manifest.json').read_text())
    assert saved_manifest == job['manifest']
    for item in saved_manifest['files']:
        path = results / 'input' / item['path']
        assert path.stat().st_size == item['size']
        assert hashlib.sha256(path.read_bytes()).hexdigest() == item['sha256']
    assert review['ticket_sha256'] == hashlib.sha256(
        (results / 'input' / TICKET).read_bytes()).hexdigest()
    check_results = json.loads((results / 'checks-1.json').read_text())
    assert all(item['status'] == 'passed' for item in check_results)
    assert 'Synthetic acceptance check passed.' in (results / 'checks-1-1.log').read_text()
    assert (CONTROL / 'cleanup-complete').is_file()
    operations = [item['operation'] for item in json_lines(CONTROL / 'git-operations.jsonl')]
    assert operations.count('clone') == 1 and operations.count('push') == 1
    assert 'fetch' in operations and 'ls-remote' in operations
    changed = command([REAL_GIT, '-C', '/workspace/repo', 'diff', '--name-only',
                       'origin/main...HEAD'], capture_output=True).stdout.strip()
    assert changed == 'example.txt'
    assert not Path('/workspace/repo/.sdlc').exists()
    print('Offline container ticket passed: '
          f'{job["implementation"]["provider"]} -> {job["review"]["provider"]}; '
          'real signed commit, local push, one draft PR, saved inputs and cleanup verified.')


def snapshot_fixture(source, destination):
    spec = importlib.util.spec_from_file_location('ticket_input', ROOT / 'runtime/bin/ticket_input.py')
    collector = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(collector)
    return collector.snapshot(source, TICKET, destination)


def descriptor(identity, manifest, implementation, review):
    return {'version': 1, 'id': identity, 'repository': REPOSITORY,
            'branch': f'123-offline-{implementation}', 'base_branch': 'main',
            'ticket': TICKET, 'manifest': manifest,
            'implementation': {'provider': implementation, 'model': f'example-{implementation}-model'},
            'review': {'provider': review, 'model': f'example-{review}-model'},
            'checks': ['python3 fixture_checks.py'],
            'cleanup': ['touch /workspace/offline-control/cleanup-complete'],
            'max_review_rounds': 1, 'agent_timeout_seconds': 60}


def host_runs(image):
    with tempfile.TemporaryDirectory(prefix='sdlc-offline-ticket-') as folder:
        folder = Path(folder)
        key = folder / 'signing-key'
        command(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(key)])
        source = folder / 'original-checkout'
        source.mkdir()
        (source / '.gitignore').write_text('.sdlc/\n')
        (source / 'README.md').write_text('Disposable offline ticket source.\n')
        command(['git', 'init', '-q', '-b', 'main', str(source)])
        command(['git', '-C', str(source), 'add', '.'])
        command(['git', '-C', str(source), '-c', 'user.name=Offline Fixture',
                 '-c', 'user.email=offline@example.invalid', '-c', 'commit.gpgsign=false',
                 '-c', 'core.hooksPath=/dev/null', 'commit', '-q', '-m', 'Create disposable ticket source'])
        ticket = source / TICKET
        ticket.parent.mkdir(parents=True)
        ticket.write_text('# Repair the example\n\nReplace the broken example with the repaired '
                          'text in example.txt. See [context](context.md).\n')
        (ticket.parent / 'context.md').write_text(
            'The expected contents of example.txt are `The repaired example.` followed by a newline.\n')
        original = checkout_fingerprint(source)
        for implementation, review in (('codex', 'claude'), ('claude', 'codex')):
            identity = uuid.uuid4().hex
            project = 'sdlc-offline-' + identity[:12]
            snapshot = folder / identity
            manifest = snapshot_fixture(source, snapshot)
            job = descriptor(identity, manifest, implementation, review)
            (snapshot / 'job.json').write_text(json.dumps(job, indent=2) + '\n')
            (snapshot / 'job.json').chmod(0o444)
            override = folder / f'{identity}.compose.yaml'
            # JSON strings are valid YAML scalars for user-configurable paths.
            override.write_text('services:\n  worker:\n'
                                '    image: ' + json.dumps(image) + '\n'
                                '    network_mode: none\n'
                                '    volumes:\n'
                                '      - type: bind\n'
                                '        source: ' + json.dumps(str(snapshot)) + '\n'
                                '        target: /input/job\n'
                                '        read_only: true\n'
                                '      - type: bind\n'
                                '        source: ' + json.dumps(str(Path(__file__).resolve())) + '\n'
                                '        target: /smoke/harness.py\n'
                                '        read_only: true\n')
            env = os.environ.copy()
            for name in tuple(env):
                if name.startswith(('SDLC_', 'COMPOSE_')) or name in (
                        'GH_TOKEN', 'GITHUB_TOKEN', 'ANTHROPIC_API_KEY',
                        'CLAUDE_CODE_OAUTH_TOKEN', 'OPENAI_API_KEY', 'CODEX_API_KEY', 'SSH_AUTH_SOCK'):
                    env.pop(name)
            env.update(SDLC_REPOSITORY=REPOSITORY, SDLC_GITHUB_LOGIN='example',
                       SDLC_GIT_NAME='Offline Fixture', SDLC_GIT_EMAIL='offline@example.invalid',
                       SDLC_BASE_BRANCH='main', SDLC_MODEL='', SDLC_JOB_ID=identity,
                       SDLC_SIGNING_KEY=key.read_text(), SDLC_GITHUB_TOKEN='fake-offline-token')
            compose = ['docker', 'compose', '--project-name', project,
                       '-f', str(ROOT / 'runtime/compose.yaml'),
                       '-f', str(ROOT / 'runtime/compose.credentials.yaml'),
                       '-f', str(ROOT / 'runtime/compose.job.yaml'), '-f', str(override)]
            try:
                command(compose + ['run', '--rm', '-T', '--no-deps', 'worker',
                                   'python3', '/smoke/harness.py', '--inside'], env=env)
                assert checkout_fingerprint(source) == original, 'Original ticket checkout changed'
            finally:
                command(compose + ['down', '--volumes', '--remove-orphans'], env=env)
        print('Both provider orders passed. Original ticket checkout is unchanged; '
              'all named fixture resources were removed. No model or GitHub request was made.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--execute', action='store_true', help='Run the disposable Docker acceptance checks')
    parser.add_argument('--image', default='sdlc:local', help='Use an already built worker image')
    parser.add_argument('--inside', action='store_true', help=argparse.SUPPRESS)
    args = parser.parse_args()
    if args.inside:
        inside_container()
    elif args.execute:
        def interrupted(number, frame):
            raise KeyboardInterrupt('Container ticket smoke interrupted')
        handlers = {number: signal.signal(number, interrupted)
                    for number in (signal.SIGTERM, signal.SIGHUP)}
        try:
            host_runs(args.image)
        finally:
            for number, previous in handlers.items():
                signal.signal(number, previous)
    else:
        print('Container ticket acceptance plan: run both provider orders with a read-only '
              'ticket snapshot, disposable SSH signing key, real local Git remote, provider/API '
              'stubs and networking disabled. Each run removes its own containers and volumes.\n'
              'Execute with: python3 tests/container_ticket_smoke.py --execute')


if __name__ == '__main__':
    main()
