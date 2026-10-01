#!/usr/bin/env python3
"""Update the runtime's pinned tool versions and catalogue revisions."""
import argparse
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
PIN_NAMES = ('CODEX_VERSION', 'GH_VERSION', 'SKILLS_REVISION', 'AGENTS_REVISION')
VERSION_PATTERN = r'(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)'
SHA_PATTERN = r'[0-9a-f]{40}'
NPM_URL = 'https://registry.npmjs.org/@openai/codex/latest'
MAX_RESPONSE_BYTES = 1024 * 1024


class UpdateError(ValueError):
    """An invalid pin or upstream response prevented an update."""


def validate_pin(name, value):
    pattern = VERSION_PATTERN if name.endswith('_VERSION') else SHA_PATTERN
    if not isinstance(value, str) or re.fullmatch(pattern, value) is None:
        raise UpdateError(f'Invalid {name}; expected a stable x.y.z version or lowercase 40-character SHA.')
    return value


def read_pins(contents):
    """Return validated values and their positions without normalising the file."""
    pins = {}
    for name in PIN_NAMES:
        definitions = list(re.finditer(rf'(?m)^[ \t]*(?i:ARG)[ \t]+{name}\b[^\r\n]*', contents))
        if len(definitions) != 1:
            raise UpdateError(f'Expected exactly one ARG {name} definition; found {len(definitions)}.')
        definition = definitions[0]
        assignment = re.fullmatch(rf'[ \t]*(?i:ARG)[ \t]+{name}=(?P<value>\S+)[ \t]*', definition.group())
        if assignment is None:
            raise UpdateError(f'Invalid ARG {name} assignment.')
        value = validate_pin(name, assignment.group('value'))
        start = definition.start() + assignment.start('value')
        pins[name] = (value, start, start + len(value))
    return pins


def parse_json(raw, source):
    try:
        payload = json.loads(raw)
    except (json.JSONDecodeError, UnicodeDecodeError, TypeError) as error:
        raise UpdateError(f'Invalid JSON from {source}.') from error
    if not isinstance(payload, dict):
        raise UpdateError(f'Expected a JSON object from {source}.')
    return payload


def github_json(endpoint):
    try:
        response = subprocess.run(
            ['gh', 'api', '--method', 'GET', endpoint],
            check=True, capture_output=True, text=True, encoding='utf-8', timeout=30,
        )
    except (OSError, subprocess.SubprocessError, UnicodeDecodeError) as error:
        # gh errors can contain authentication details; keep them out of logs.
        raise UpdateError(f'Unable to fetch GitHub metadata for {endpoint}.') from error
    return parse_json(response.stdout, f'GitHub {endpoint}')


def npm_json():
    request = urllib.request.Request(
        NPM_URL, headers={'Accept': 'application/json', 'User-Agent': 'sdlc-runtime-pin-updater'},
    )
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            raw = response.read(MAX_RESPONSE_BYTES + 1)
    except (OSError, urllib.error.URLError) as error:
        raise UpdateError('Unable to fetch the Codex npm release.') from error
    if len(raw) > MAX_RESPONSE_BYTES:
        raise UpdateError('Codex npm response is too large.')
    return parse_json(raw, 'the Codex npm registry')


def fetch_candidates():
    npm = npm_json()
    if npm.get('name') != '@openai/codex':
        raise UpdateError('Unexpected package in the Codex npm response.')
    codex = validate_pin('CODEX_VERSION', npm.get('version'))

    release = github_json('repos/cli/cli/releases/latest')
    tag = release.get('tag_name')
    if (release.get('draft') is not False or release.get('prerelease') is not False
            or not isinstance(tag, str) or not tag.startswith('v')):
        raise UpdateError('GitHub CLI response must describe a stable release.')
    gh = validate_pin('GH_VERSION', tag[1:])

    candidates = {'CODEX_VERSION': codex, 'GH_VERSION': gh}
    for name, repo in (('SKILLS_REVISION', 'skills'), ('AGENTS_REVISION', 'agents')):
        payload = github_json(f'repos/tjpeel/{repo}/commits/main')
        candidates[name] = validate_pin(name, payload.get('sha'))
    return candidates


def atomic_replace(path, contents, original):
    mode = stat.S_IMODE(path.stat().st_mode)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(dir=path.parent, prefix=f'.{path.name}.', delete=False) as output:
            temporary = Path(output.name)
            os.fchmod(output.fileno(), mode)
            output.write(contents.encode('utf-8'))
            output.flush()
            os.fsync(output.fileno())
        if path.read_bytes() != original:
            raise UpdateError('Dockerfile changed during the update; retry from its current contents.')
        os.replace(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def load_dockerfile(path):
    if path.is_symlink() or not path.is_file():
        raise UpdateError('Dockerfile must be a regular file, not a symlink.')
    original = path.read_bytes()
    try:
        contents = original.decode('utf-8')
    except UnicodeDecodeError as error:
        raise UpdateError('Dockerfile must contain UTF-8 text.') from error
    return original, contents, read_pins(contents)


def update_pins(path, fetcher=None):
    original, contents, pins = load_dockerfile(path)
    candidates = (fetcher or fetch_candidates)()
    if not isinstance(candidates, dict) or set(candidates) != set(PIN_NAMES):
        raise UpdateError('Expected all four runtime pin candidates.')
    # Validate every candidate before choosing replacements or writing the file.
    for name in PIN_NAMES:
        validate_pin(name, candidates[name])

    changes = []
    replacements = []
    for name in PIN_NAMES:
        old, start, end = pins[name]
        new = candidates[name]
        if name.endswith('_VERSION'):
            if tuple(map(int, new.split('.'))) <= tuple(map(int, old.split('.'))):
                continue
        elif new == old:
            continue
        changes.append((name, old, new))
        replacements.append((start, end, new))
    for start, end, value in sorted(replacements, reverse=True):
        contents = contents[:start] + value + contents[end:]
    if changes:
        atomic_replace(path, contents, original)
    return changes


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument('--check', action='store_true', help='Validate current pins without fetching or writing.')
    mode.add_argument('--write', action='store_true', help='Fetch and update pins (the default).')
    parser.add_argument('--dockerfile', type=Path, default=ROOT / 'runtime/Dockerfile')
    args = parser.parse_args(argv)
    try:
        if args.check:
            load_dockerfile(args.dockerfile)
            print('Runtime pins are valid.')
        else:
            changes = update_pins(args.dockerfile)
            for name, old, new in changes:
                print(f'{name}: {old} -> {new}')
            if not changes:
                print('No runtime pin updates.')
    except (OSError, UpdateError) as error:
        print(f'Runtime pin update failed: {error}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
