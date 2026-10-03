#!/usr/bin/env python3
"""Public generated fixtures for the complete Colima worker runtime trial.

The agent responses and GitHub API are deterministic stubs. Git, SSH signatures,
the image, both provider CLIs, SDK and nested Docker are real. No model is called.
"""
import argparse
import hashlib
import importlib.util
import inspect
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[3]


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    loaded = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(loaded)
    return loaded


def prepare(directory, identity, implementation, review, ticket_text=None):
    smoke = module('container_ticket_smoke', 'tests/container_ticket_smoke.py')
    directory.mkdir()
    source = directory / 'original-checkout'
    source.mkdir()
    ticket = source / smoke.TICKET
    ticket.parent.mkdir(parents=True)
    ticket.write_text(ticket_text if ticket_text is not None else
                      '# Repair the example\n\nReplace the broken example with the repaired '
                      'text in example.txt. See [context](context.md).\n')
    (ticket.parent / 'context.md').write_text(
        'The expected contents of example.txt are `The repaired example.` followed by a newline.\n')
    snapshot = directory / 'job'
    manifest = smoke.snapshot_fixture(source, snapshot)
    descriptor = smoke.descriptor(identity, manifest, implementation, review)
    (snapshot / 'job.json').write_text(json.dumps(descriptor, indent=2) + '\n')
    for item in snapshot.rglob('*'):
        item.chmod(0o555 if item.is_dir() else 0o444)
    snapshot.chmod(0o555)


def runtime_checks():
    assert Path('/.dockerenv').is_file() and os.getuid() == 1000
    assert 'SSH_AUTH_SOCK' not in os.environ
    assert not any(os.environ.get(key) for key in (
        'OPENAI_API_KEY', 'CODEX_API_KEY', 'ANTHROPIC_API_KEY', 'CLAUDE_CODE_OAUTH_TOKEN'))
    for arguments in (['codex', '--version'], ['claude', '--version'], ['gh', '--version'],
                      ['dotnet', '--version'], ['docker', '--version'],
                      ['docker', 'compose', 'version'], ['docker', 'buildx', 'version']):
        subprocess.run(arguments, check=True, timeout=30)
    for name, arguments in (('Codex', ['codex', 'login', 'status']),
                            ('Claude', ['claude', 'auth', 'status'])):
        result = subprocess.run(arguments, capture_output=True, text=True, timeout=30)
        status = result.stdout + result.stderr
        if name == 'Claude':
            value = json.loads(result.stdout)
            assert value['loggedIn'] is False
        else:
            assert result.returncode != 0 and 'not logged in' in status.lower()
        print(f'{name} real CLI reports unauthenticated; no model request made.', flush=True)
    for state in (Path('/home/node/.codex'), Path('/home/node/.claude')):
        assert not (state / 'auth.json').exists()
    for catalogue in ('/home/node/.agents/skills', '/etc/codex/agents',
                      '/home/node/.claude/skills', '/home/node/.claude/agents'):
        files = list(Path(catalogue).rglob('*'))
        assert files, catalogue
        assert all(item.exists() for item in files), 'Broken catalogue resource'
        print(f'Bundled catalogue available: {catalogue}', flush=True)
    mount_info = [line.split() for line in Path('/proc/self/mountinfo').read_text().splitlines()]
    for destination in ('/run/secrets', '/run/sdlc'):
        assert any(parts[4] == destination and parts[parts.index('-') + 1] == 'tmpfs'
                   for parts in mount_info), destination
    for name in ('signing-key', 'github-token'):
        path = Path('/run/secrets') / name
        assert path.stat().st_uid == 1000 and stat.S_IMODE(path.stat().st_mode) == 0o400
    assert os.environ['GH_TOKEN'].startswith('fake-colima-full-')
    credential = subprocess.run(['git', 'credential', 'fill'],
                                input='protocol=https\nhost=github.com\n\n', text=True,
                                capture_output=True, check=True, timeout=30)
    assert 'password=' + os.environ['GH_TOKEN'] in credential.stdout
    print('Generated signing key and fake token were supplied at runtime into tmpfs.', flush=True)
    print('Real Git HTTPS credential helper returned the supplied fake token without a network call.', flush=True)


def nested_checks():
    nested = module('nested_docker_smoke', 'tests/nested_docker_smoke.py')
    # Keep the SDK restore offline: the generated console project has no packages.
    original = "subprocess.run(['dotnet', 'run', '--project', str(folder / 'app'),"
    replacement = ("(folder / 'app' / 'NuGet.Config').write_text("
                   "'<configuration><packageSources><clear /></packageSources></configuration>')\n"
                   + original)
    assert nested.PROBE.count(original) == 1
    exec(compile(nested.PROBE.replace(original, replacement), '<nested-public-probe>', 'exec'), {})


def admission_gate(job, seed):
    trial = os.environ.get('SDLC_HOST_CONTROL_TRIAL')
    if not trial:
        return
    remote = seed.parent / 'remote.git'
    ref = 'refs/heads/' + job['base_branch']
    base = subprocess.check_output(['/usr/bin/git', '--git-dir=' + str(remote), 'rev-parse', ref], text=True).strip()
    ticket = Path('/input/job') / job['ticket']
    admission = {'trial_id': trial, 'base_commit': base,
                 'ticket_sha256': hashlib.sha256(ticket.read_bytes()).hexdigest(), 'job_id': job['id']}
    Path('/run/sdlc/host-control-admission.json').write_text(json.dumps(admission) + '\n')
    deadline = time.monotonic() + 600
    while not Path('/run/sdlc/host-control-release').exists():
        if time.monotonic() > deadline:
            raise TimeoutError('Host-control release was not supplied within ten minutes.')
        time.sleep(0.25)
    # The fixture's base remote is private to this manager-owned VM. Verify it
    # still matches admission before the real runner resolves its base branch.
    current = subprocess.check_output(['/usr/bin/git', '--git-dir=' + str(remote), 'rev-parse', ref], text=True).strip()
    assert current == base


def ticket_checks():
    smoke = module('container_ticket_smoke', 'tests/container_ticket_smoke.py')
    inputs = Path('/input/job')
    for path in [inputs, *inputs.rglob('*')]:
        assert path.stat().st_uid == 0
        assert stat.S_IMODE(path.stat().st_mode) & 0o222 == 0
    # This trial copies root-owned inputs instead of binding the guest directory.
    # The existing smoke function requires a read-only mount; replace only that
    # precondition, retaining every ticket/signature/publication assertion.
    source = inspect.getsource(smoke.inside_container)
    assertion = "assert any(parts[4] == '/input/job' and 'ro' in parts[5].split(',') for parts in mounts)"
    replacement = ("assert not any(parts[4] == '/input/job' for parts in mounts), "
                   "'Ticket inputs must be copied, not mounted'")
    assert source.count(assertion) == 1, 'Upstream smoke precondition changed; review the adaptation'
    invocation = "command(['sdlc-job', 'run', '--job', '/input/job/job.json'], env=env)"
    assert source.count(invocation) == 1
    source = source.replace(invocation, 'admission_gate(job, seed)\n    ' + invocation)
    smoke.__dict__['admission_gate'] = admission_gate
    exec(compile(source.replace(assertion, replacement), '<copied-ticket-probe>', 'exec'), smoke.__dict__)
    smoke.inside_container()
    if os.environ.get('SDLC_HOST_CONTROL_TRIAL'):
        admission = json.loads(Path('/run/sdlc/host-control-admission.json').read_text())
        result = json.loads((Path('/workspace/results') / admission['job_id'] / 'result.json').read_text())
        assert result['base_commit'] == admission['base_commit']
        assert hashlib.sha256((Path('/input/job') / json.loads(Path('/input/job/job.json').read_text())['ticket']).read_bytes()).hexdigest() == admission['ticket_sha256']
    print('Provider implementation/review and GitHub responses above are simulated. '
          'Signatures, local Git push, exact reviewed SHA and runner stages are real.', flush=True)


def inside(run_tests):
    runtime_checks()
    if run_tests:
        subprocess.run([sys.executable, '-B', '-m', 'unittest', 'discover',
                        '-s', str(ROOT / 'tests'), '-v'], cwd=ROOT, check=True, timeout=300)
    nested_checks()
    ticket_checks()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument('--prepare', type=Path)
    action.add_argument('--inside', action='store_true')
    parser.add_argument('--identity')
    parser.add_argument('--implementation', choices=('codex', 'claude'))
    parser.add_argument('--review', choices=('codex', 'claude'))
    parser.add_argument('--run-tests', action='store_true')
    parser.add_argument('--ticket-text-file', type=Path)
    arguments = parser.parse_args()
    if arguments.prepare:
        if not all((arguments.identity, arguments.implementation, arguments.review)):
            parser.error('--prepare requires identity, implementation and review')
        text = arguments.ticket_text_file.read_text() if arguments.ticket_text_file else None
        prepare(arguments.prepare, arguments.identity, arguments.implementation, arguments.review, text)
    else:
        inside(arguments.run_tests)


if __name__ == '__main__':
    main()
