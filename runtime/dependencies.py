#!/usr/bin/env python3
"""Capture public build versions or inspect Debian candidates without installing."""
import argparse
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile
from pathlib import Path

INVENTORY = Path('/opt/sdlc/runtime-inventory.json')
MAXIMUM = 4 * 1024 * 1024
PACKAGE = re.compile(r'^[a-z0-9][a-z0-9+.-]*$')
ARCH = re.compile(r'^[a-z0-9][a-z0-9-]*$')
VERSION = re.compile(r'^[0-9][A-Za-z0-9.+:~_-]{0,127}$')
CODEX_PLATFORM_TAGS = frozenset(('linux-x64', 'linux-arm64', 'darwin-x64', 'darwin-arm64', 'win32-x64', 'win32-arm64'))
NPM_SOURCE = re.compile(r'(?:@[a-z0-9._-]+/)?[a-z0-9._-]+')


def run(args, timeout=15, allow_nonzero=False, apt_config=None):
    # Never print subprocess output or inherit injected proxies/configuration.
    env = {'PATH': os.environ.get('PATH', '/usr/local/bin:/usr/bin:/bin'),
           'HOME': '/tmp', 'LANG': 'C', 'LC_ALL': 'C',
           'DOTNET_ROOT': '/opt/dotnet', 'DOTNET_CLI_TELEMETRY_OPTOUT': '1',
           'DOTNET_NOLOGO': '1', 'DOTNET_SKIP_FIRST_TIME_EXPERIENCE': '1'}
    if apt_config is not None:
        env['APT_CONFIG'] = str(apt_config)
    result = subprocess.run(args, capture_output=True, text=True, env=env,
                            timeout=timeout, check=False)
    if (result.returncode and not allow_nonzero) or len(result.stdout.encode()) > MAXIMUM:
        raise ValueError('runtime dependency command failed')
    return result.stdout


def checked_version(value):
    if not isinstance(value, str) or not VERSION.fullmatch(value):
        raise ValueError('invalid dependency version')
    return value


def tool_version(args):
    match = re.search(r'\bv?(\d+\.\d+\.\d+(?:[-+][A-Za-z0-9.-]+)?)\b', run(args))
    if not match:
        raise ValueError('tool returned no recognizable version')
    return checked_version(match.group(1))


def packages():
    rows = run(['dpkg-query', '-W', '-f',
                '${db:Status-Abbrev}\t${binary:Package}\t${Version}\t${Architecture}\n'])
    result = []
    for row in rows.splitlines():
        fields = row.split('\t')
        if len(fields) != 4:
            raise ValueError('invalid installed package record')
        status, name, version, architecture = fields
        if len(status) < 2 or status[1] != 'i':
            continue
        name = name.split(':', 1)[0]
        if not PACKAGE.fullmatch(name) or not ARCH.fullmatch(architecture):
            raise ValueError('invalid installed package identity')
        result.append({'name': name, 'version': checked_version(version),
                       'architecture': architecture})
    if not result or len(result) > 10000:
        raise ValueError('invalid installed package count')
    return sorted(result, key=lambda item: (item['name'], item['architecture']))


def image_reference(dockerfile):
    references = re.findall(r'^FROM\s+(\S+)', dockerfile, re.MULTILINE | re.IGNORECASE)
    node = [value for value in references if value.startswith('node:')]
    docker = [value for value in references if value.startswith('docker:')]
    if len(node) != 1 or len(docker) != 1:
        raise ValueError('runtime image provenance is incomplete')
    match = re.fullmatch(r'node:([A-Za-z0-9_.-]+)@(sha256:[0-9a-f]{64})', node[0])
    if not match or not re.fullmatch(r'docker:[A-Za-z0-9_.-]+(?:@sha256:[0-9a-f]{64})?', docker[0]):
        raise ValueError('runtime image references must be fixed public references')
    return match.group(1), match.group(2)


def npm_inventory(global_tree, npm_version):
    if not isinstance(global_tree, dict):
        raise ValueError('npm global dependency inventory is unavailable')
    installed = global_tree.get('dependencies')
    if not isinstance(installed, dict) or not installed:
        raise ValueError('npm global dependency inventory is unavailable')
    # npm omits versions for absent optional platform dependencies. Missing
    # required dependencies also appear in the root problems list, even when
    # their leaf metadata is sparse, so check that list before skipping leaves.
    def check_problems(metadata):
        problems = metadata.get('problems', [])
        if not isinstance(problems, list) or any(not isinstance(problem, str) for problem in problems):
            raise ValueError('invalid npm problem metadata')
        if any(problem.startswith('missing:') for problem in problems):
            raise ValueError('npm inventory contains a missing required dependency')

    check_problems(global_tree)
    result = {('npm', '', npm_version): 'npm'}

    def walk(tree):
        for name, metadata in tree.items():
            if not NPM_SOURCE.fullmatch(name):
                raise ValueError('invalid npm package source')
            if not isinstance(metadata, dict):
                raise ValueError('invalid npm package metadata')
            check_problems(metadata)
            if metadata.get('missing') and not metadata.get('optional'):
                raise ValueError('npm inventory contains a missing required dependency')
            if not metadata.get('version'):
                # These are the two versionless leaf shapes emitted by npm ls
                # for absent optional packages, plus its explicit optional form.
                if not metadata or metadata == {'overridden': False} or (metadata.get('optional') and not metadata.get('dependencies')):
                    continue
                raise ValueError('npm package version is unavailable')
            version = checked_version(metadata['version'])
            source = metadata.get('name', name)
            if not isinstance(source, str) or not NPM_SOURCE.fullmatch(source):
                raise ValueError('invalid npm published source')
            track = ''
            if name.startswith('@openai/codex-'):
                track = name.removeprefix('@openai/codex-')
                if source != '@openai/codex' or track not in CODEX_PLATFORM_TAGS or not re.fullmatch(
                        r'[0-9]+\.[0-9]+\.[0-9]+-' + re.escape(track), version):
                    raise ValueError('unsupported Codex platform alias')
            elif source == '@openai/codex' and source != name:
                raise ValueError('unsupported Codex package alias')
            key = (source, track, version)
            if key not in result or name == source:
                result[key] = name
            children = metadata.get('dependencies', {})
            if not isinstance(children, dict):
                raise ValueError('invalid npm dependency tree')
            walk(children)

    walk(installed)
    for required in ('@openai/codex', '@anthropic-ai/claude-code'):
        if required not in installed or not installed[required].get('version'):
            raise ValueError('provider CLI package is absent from inventory')
    return {(name, source, track, version) for (source, track, version), name in result.items()}


def capture(skills_revision, agents_revision, dockerfile):
    for revision in (skills_revision, agents_revision):
        if not re.fullmatch(r'[0-9a-f]{40}', revision):
            raise ValueError('catalogue revision is not an immutable commit')
    tag, digest = image_reference(dockerfile)
    architecture = run(['dpkg', '--print-architecture']).strip()
    if architecture not in ('amd64', 'arm64'):
        raise ValueError('unsupported runtime architecture')
    os_release = Path('/etc/os-release').read_text()
    if not re.search(r'^ID=debian$', os_release, re.MULTILINE) or not re.search(
            r'^VERSION_CODENAME=bookworm$', os_release, re.MULTILINE):
        raise ValueError('runtime distribution is not Debian bookworm')
    dependencies = []

    def add(name, kind, source, version, track=None):
        item = {'name': name, 'kind': kind, 'source': source, 'version': version}
        if track:
            item['track'] = track
        dependencies.append(item)

    node = checked_version(run(['node', '--version']).strip().removeprefix('v'))
    add('Node.js', 'node', 'nodejs', node, node.split('.')[0])
    npm = checked_version(run(['npm', '--version']).strip())
    global_tree = json.loads(run(['npm', 'ls', '--global', '--all', '--long', '--json'], allow_nonzero=True))
    npm_packages = npm_inventory(global_tree, npm)
    if shutil.which('yarn'):
        npm_packages.add(('yarn', 'yarn', '', checked_version(run(['yarn', '--version']).strip())))
    for name, source, track, version in sorted(npm_packages):
        add(name, 'npm', source, version, track)
    for name, source, command in (
            ('GitHub CLI', 'cli/cli', ['gh', '--version']),
            ('Docker CLI', 'docker/cli', ['docker', '--version']),
            ('Docker Compose', 'docker/compose', ['docker', 'compose', 'version']),
            ('Docker Buildx', 'docker/buildx', ['docker', 'buildx', 'version'])):
        add(name, 'github-release', source, tool_version(command))
    sdk_rows = run(['dotnet', '--list-sdks']).splitlines()
    runtime_rows = run(['dotnet', '--list-runtimes']).splitlines()
    if not sdk_rows or not runtime_rows:
        raise ValueError('.NET inventory is unavailable')
    for row in sdk_rows:
        version = checked_version(row.split()[0])
        add('.NET SDK ' + version, 'dotnet-sdk', 'dotnet', version,
            '.'.join(version.split('.')[:2]))
    for row in runtime_rows:
        fields = row.split()
        if len(fields) < 2 or fields[0] not in ('Microsoft.NETCore.App', 'Microsoft.AspNetCore.App'):
            raise ValueError('unsupported .NET runtime')
        version = checked_version(fields[1])
        add(fields[0] + ' ' + version, 'dotnet-runtime', fields[0], version,
            '.'.join(version.split('.')[:2]))
    add('Skills catalogue', 'git', 'tjpeel/skills', skills_revision, 'main')
    add('Agents catalogue', 'git', 'tjpeel/agents', agents_revision, 'main')
    add('Node base image', 'image', 'library/node', digest, tag)
    return {'version': 1, 'platform': 'linux/' + {'amd64': 'amd64', 'arm64': 'arm64'}[architecture],
            'distribution': 'debian:bookworm', 'dependencies': dependencies,
            'packages': packages()}


def apt_options(directory):
    # Ignore image apt config hooks and use only Debian's signed public sources.
    # APT_CONFIG is read before the default configuration directories, so
    # image-specific apt hooks cannot run during the metadata refresh.
    (directory / 'apt.conf').write_text('Dir::Etc::parts "-";\nDir::Etc::main "-";\n')
    source = directory / 'sources.list'
    source.write_text(''.join(
        'deb [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] '
        + endpoint + ' ' + suite + ' main\n'
        for endpoint, suite in (
            ('https://deb.debian.org/debian', 'bookworm'),
            ('https://deb.debian.org/debian', 'bookworm-updates'),
            ('https://deb.debian.org/debian-security', 'bookworm-security'))))
    for name in ('lists/partial', 'archives/partial', 'logs'):
        (directory / name).mkdir(parents=True, exist_ok=True)
    values = {
        'Dir::Etc::sourcelist': str(source), 'Dir::Etc::sourceparts': '-',
        'Dir::Etc::parts': '-', 'Dir::Etc::main': '-',
        'Dir::State': str(directory), 'Dir::State::status': '/var/lib/dpkg/status',
        'Dir::State::lists': str(directory / 'lists'),
        'Dir::Cache': str(directory), 'Dir::Cache::archives': str(directory / 'archives'),
        'Dir::Cache::pkgcache': str(directory / 'pkgcache.bin'),
        'Dir::Cache::srcpkgcache': str(directory / 'srcpkgcache.bin'),
        'Dir::Log': str(directory / 'logs'), 'APT::Sandbox::User': 'root',
        'APT::Update::Error-Mode': 'any', 'Acquire::Retries': '0',
        'Acquire::http::Timeout': '10', 'Acquire::https::Timeout': '10',
        'Acquire::Languages': 'none',
    }
    return [argument for key, value in values.items() for argument in ('-o', key + '=' + value)]


def package_updates(inventory, directory):
    if not isinstance(inventory, dict) or inventory.get('version') != 1 or inventory.get('distribution') != 'debian:bookworm':
        raise ValueError('unsupported runtime package inventory')
    installed = inventory.get('packages')
    if not isinstance(installed, list) or not installed or len(installed) > 10000:
        raise ValueError('invalid installed package inventory')
    identities = []
    for item in installed:
        if not isinstance(item, dict):
            raise ValueError('invalid installed package record')
        name, arch = item.get('name', ''), item.get('architecture', '')
        if not PACKAGE.fullmatch(name) or not ARCH.fullmatch(arch):
            raise ValueError('invalid installed package identity')
        checked_version(item.get('version'))
        identities.append(name if arch == 'all' else name + ':' + arch)
    if len(set(identities)) != len(identities):
        raise ValueError('duplicate installed package identity')
    options = apt_options(directory)
    run(['apt-get', *options, 'update'], timeout=25, apt_config=directory / 'apt.conf')
    output = run(['apt-cache', *options, 'policy', *identities], timeout=5, apt_config=directory / 'apt.conf')
    candidates = {}
    available = {}
    current = None
    version = None
    for line in output.splitlines():
        if line and not line[0].isspace() and line.endswith(':'):
            current = line[:-1]
            version = None
        elif current and line.strip().startswith('Candidate:'):
            value = line.strip().split(':', 1)[1].strip()
            if value != '(none)':
                candidates[current] = checked_version(value)
        elif current:
            match = re.fullmatch(r'\s+(?:\*\*\*\s+)?([0-9]\S*)\s+[0-9]+', line)
            if match:
                version = checked_version(match.group(1))
            elif version and re.search(r'https://deb\.debian\.org/(?:debian|debian-security)\s', line) and ' Packages' in line:
                available.setdefault(current, set()).add(version)
    result = []
    for item, identity in zip(installed, identities):
        key = identity if identity in candidates else item['name']
        candidate = candidates.get(key, '')
        if candidate not in available.get(key, set()):
            candidate = ''
        update = False
        if candidate:
            comparison = subprocess.run(['dpkg', '--compare-versions', candidate, 'gt', item['version']],
                                        capture_output=True, timeout=2, check=False,
                                        env={'PATH': '/usr/bin:/bin', 'LANG': 'C', 'LC_ALL': 'C'})
            if comparison.returncode not in (0, 1):
                raise ValueError('Debian version comparison failed')
            update = comparison.returncode == 0
        result.append({'name': item['name'], 'installed': item['version'],
                       'candidate': candidate, 'architecture': item['architecture'],
                       'update': update})
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('mode', choices=('capture', 'package-updates'))
    parser.add_argument('--skills-revision')
    parser.add_argument('--agents-revision')
    args = parser.parse_args()
    try:
        if args.mode == 'capture':
            data = capture(args.skills_revision or '', args.agents_revision or '',
                           Path('/opt/sdlc/runtime.Dockerfile').read_text())
            encoded = json.dumps(data, sort_keys=True) + '\n'
            if len(encoded.encode()) > MAXIMUM:
                raise ValueError('runtime inventory is too large')
            INVENTORY.write_text(encoded)
        else:
            if INVENTORY.stat().st_size > MAXIMUM:
                raise ValueError('runtime inventory is too large')
            inventory = json.loads(INVENTORY.read_text())
            def expired(_signum, _frame):
                raise TimeoutError('package candidate check timed out')
            signal.signal(signal.SIGALRM, expired)
            signal.alarm(32)
            with tempfile.TemporaryDirectory(prefix='sdlc-apt-', dir='/tmp') as temporary:
                result = package_updates(inventory, Path(temporary))
            print(json.dumps(result, sort_keys=True))
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError):
        print('Runtime dependency inspection failed; rebuild the image or retry the public package check.',
              file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
