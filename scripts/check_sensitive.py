#!/usr/bin/env python3
"""Check Git file contents without printing potentially sensitive matches."""
import argparse
from dataclasses import dataclass
import hashlib
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys
from urllib.parse import unquote


@dataclass(frozen=True)
class Finding:
    path: str
    line: int
    category: str
    revision: str = ''


PATTERNS = (
    ('personal filesystem path', re.compile(
        r'/(?:Users|Volumes|media|mnt)/[^\s\x00\"\'`<>]+'
        r'|/home/' r'(?!(?:node|sdlc|runner)(?:/|[\s\"\'`]|$))[^\s\"\'`<>]+'
        r'|(?<![A-Za-z0-9])[A-Za-z]:[\\/]+[^\s\"\'`<>]+'
        r'|(?<![\\A-Za-z0-9])\\\\[A-Za-z0-9_.-]{2,}\\+[A-Za-z0-9_.-]+', re.IGNORECASE)),
    ('private key', re.compile(r'-{5}BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-{5}')),
    ('credential token', re.compile(
        r'\b(?:gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}'
        r'|(?:AKIA|ASIA)[A-Z0-9]{16}|sk-(?:proj-|ant-)?[A-Za-z0-9_-]{20,}'
        r'|xox[baprs]-[A-Za-z0-9-]{20,}|AIza[A-Za-z0-9_-]{35})\b')),
    ('URL credential', re.compile(r'\b[a-z][a-z0-9+.-]*://[^\s/@:]+:[^\s/@]+@', re.I)),
    ('vault reference', re.compile(r'\bop://(?!YOUR_|EXAMPLE_|<)[^\s\"\'`]+')),
)
EMAIL = re.compile(r'\b[A-Za-z0-9_.+-]+@([A-Za-z0-9.-]+\.[A-Za-z]{2,})\b')
EXAMPLE_DOMAINS = {'example.com', 'example.org', 'example.net', 'example.invalid', 'localhost.invalid'}
SECRET_NAME = r'(?:[A-Z][A-Z0-9]*_)*(?:token|password|passwd|secret|api_key|access_key|client_secret|authorization)'
ASSIGNMENT = re.compile(
    r'''(?i)(?<![\w"'])(?:["']''' + SECRET_NAME + r'''["']|''' + SECRET_NAME
    + r''')\s*[:=]\s*(["'])([^"'\r\n]+)\1''')
UNQUOTED_ASSIGNMENT = re.compile(
    r'''(?i)(?<![\w"'])''' + SECRET_NAME + r'''\s*[:=]\s*([^\s#,"'\r\n]+)''')
BEARER = re.compile(r'(?i)\bBearer\s+([A-Za-z0-9._~+/-]{8,})')
PLACEHOLDER = re.compile(r'^(?:fake(?:[-_]|$)|test(?:[-_]|$)|dummy(?:[-_]|$)|YOUR_|EXAMPLE_|PLACEHOLDER|<|\$)', re.I)
CONFIG_SUFFIXES = {'.yaml', '.yml', '.toml', '.ini', '.cfg', '.conf', '.sh'}
PRIVATE_DIRS = {'.secrets', '.ssh', '.aws', '.codex', '.t3', '.claude', '.state', 'tickets.local', 'results', 'logs', 'work'}
# Only the already-published, manually reviewed illustrative vault examples.
# This exact-content exception never applies to the index or working tree.
REVIEWED_HISTORY = {
    ('docs/options.md', 'd780b44563e52fecf680d7b6ff509ed0a3e54369fcf24b04b8251a79c8ad84a1', 'vault reference'),
}


def git(*args, cwd=None):
    return subprocess.run(['git', *args], cwd=cwd, check=True,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout


def private_filename(path):
    parts = PurePosixPath(path).parts
    name = parts[-1].lower()
    return (any(part.lower() in PRIVATE_DIRS for part in parts)
            or (name.startswith('.env') and name != '.env.example')
            or bool(re.fullmatch(r'profiles(?:\..+)?\.local\.json', name))
            or name.endswith(('.pem', '.key', '.p12', '.pfx', '.keystore', '.log'))
            or name.startswith(('id_rsa', 'id_ed25519', 'signing-key', 'github-token'))
            or name in {'auth.json', 'credentials', 'credentials.json', 'credentials.db', '.ds_store'}
            or bool(re.fullmatch(r'docker-.*-(?:ticket|pr-body)\.md', name)))


def scan(path, data, revision=''):
    findings = []
    if private_filename(path):
        findings.append(Finding(path, 0, 'local/private artifact filename', revision))
    try:
        source = data.decode('utf-8')
        if '\x00' in source:
            raise UnicodeError('binary content')
    except UnicodeError:
        findings.append(Finding(path, 0, 'binary file requires a separate publication review', revision))
        return findings
    for number, line in enumerate(source.splitlines(), 1):
        # Include URL-encoded and JSON-escaped paths in the same policy.
        decoded = unquote(line).replace('\\/', '/')
        for category, pattern in PATTERNS:
            if pattern.search(decoded):
                findings.append(Finding(path, number, category, revision))
        if any(match.group(1).lower() not in EXAMPLE_DOMAINS for match in EMAIL.finditer(decoded)):
            findings.append(Finding(path, number, 'non-example email address', revision))
        for match in ASSIGNMENT.finditer(decoded):
            if not PLACEHOLDER.match(match.group(2)):
                findings.append(Finding(path, number, 'literal credential assignment', revision))
                break
        if PurePosixPath(path).suffix.lower() in CONFIG_SUFFIXES or PurePosixPath(path).name.startswith('.env'):
            for match in UNQUOTED_ASSIGNMENT.finditer(decoded):
                value = match.group(1)
                if not PLACEHOLDER.match(value) and value.lower() not in {'null', 'none', 'true', 'false', 'bearer'}:
                    findings.append(Finding(path, number, 'unquoted credential assignment', revision))
                    break
        if any(not PLACEHOLDER.match(match.group(1)) for match in BEARER.finditer(decoded)):
            findings.append(Finding(path, number, 'credential (Bearer)', revision))
    return findings


def index_files(root):
    for entry in git('ls-files', '--stage', '-z', cwd=root).split(b'\0'):
        if not entry:
            continue
        metadata, raw_path = entry.split(b'\t', 1)
        mode, oid, stage = metadata.decode('ascii').split()
        if stage != '0':
            raise ValueError('Resolve the unmerged index before checking publication.')
        path = raw_path.decode('utf-8')
        if mode not in {'100644', '100755', '120000'}:
            raise ValueError(f'Unsupported Git entry requires review: {path!r}')
        yield path, git('cat-file', 'blob', oid, cwd=root)


def worktree_files(root):
    names = git('ls-files', '--cached', '--others', '--exclude-standard', '-z', cwd=root)
    for raw_path in sorted(set(names.split(b'\0')) - {b''}):
        path = raw_path.decode('utf-8')
        target = root / path
        if target.is_symlink():
            # Inspect the link target without reading files outside the checkout.
            import os
            yield path, os.fsencode(os.readlink(target))
        elif target.is_file():
            yield path, target.read_bytes()
        elif target.exists():
            raise ValueError(f'Unsupported working-tree entry requires review: {path!r}')


def history_findings(root):
    seen = set()
    for revision in git('rev-list', '--all', cwd=root).decode().splitlines():
        message = git('show', '-s', '--format=%B', revision, cwd=root)
        yield from scan('(commit message)', message, revision[:12])
        for entry in git('ls-tree', '-r', '-z', revision, cwd=root).split(b'\0'):
            if not entry:
                continue
            metadata, raw_path = entry.split(b'\t', 1)
            mode, kind, oid = metadata.decode('ascii').split()
            path = raw_path.decode('utf-8')
            if kind != 'blob':
                raise ValueError(f'Unsupported Git history entry requires review: {path!r}')
            identity = (path, oid)
            if identity not in seen:
                seen.add(identity)
                yield from scan_history_blob(path, git('cat-file', 'blob', oid, cwd=root), revision[:12])


def scan_history_blob(path, data, revision):
    digest = hashlib.sha256(data).hexdigest()
    return [finding for finding in scan(path, data, revision)
            if (path, digest, finding.category) not in REVIEWED_HISTORY]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument('--staged', action='store_true', help='Scan every index blob (the default).')
    mode.add_argument('--worktree', action='store_true', help='Scan tracked and non-ignored pending files.')
    mode.add_argument('--history', action='store_true', help='Scan all reachable commits and unique file versions.')
    args = parser.parse_args(argv)
    try:
        root = Path(git('rev-parse', '--show-toplevel').decode().strip())
        if args.history:
            findings = list(history_findings(root))
        else:
            files = worktree_files(root) if args.worktree else index_files(root)
            findings = [finding for path, data in files for finding in scan(path, data)]
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        # Git stderr can contain private paths; do not echo it.
        print(f'Sensitive-information check could not complete ({type(error).__name__}).', file=sys.stderr)
        return 2
    if findings:
        for finding in findings:
            location = f'{finding.path!r}:{finding.line}'
            if finding.revision:
                location = f'{finding.revision} {location}'
            print(f'{location}: {finding.category}', file=sys.stderr)
        print('Publication blocked. Remove private data or use explicit example placeholders. '
              'Matched contents are withheld. See docs/publication-safety.md.', file=sys.stderr)
        return 1
    print('Sensitive-information check passed.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
