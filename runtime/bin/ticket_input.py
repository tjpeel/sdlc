"""Bounded Markdown ticket snapshots, kept outside the source checkout."""
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
from urllib.parse import unquote, urlsplit

MAX_FILE_BYTES = 256 * 1024
MAX_TOTAL_BYTES = 1024 * 1024
MAX_FILES = 32
TICKET_PATTERN = re.compile(r'\.sdlc/work/tickets/[A-Za-z0-9][A-Za-z0-9_-]*/ticket-[A-Za-z0-9][A-Za-z0-9_.-]*\.md')
PROTECTED = {'.git', '.secrets', '.state', '.codex', '.claude', 'profiles.local.json'}


def ticket_path(value):
    text = str(value)
    if not TICKET_PATTERN.fullmatch(text):
        raise ValueError('Ticket must be a repository-relative .sdlc/work/tickets/<number>/ticket-*.md path.')
    return text


def read_markdown(root, relative):
    """Open every path component without following links; reject multiply linked files."""
    parts = PurePosixPath(relative).parts
    if (not parts or parts[:2] != ('.sdlc', 'work') or any(p in ('.', '..') or p in PROTECTED for p in parts)
            or not relative.endswith('.md') or '\\' in relative or any(c in relative for c in '\0\r\n')):
        raise ValueError('Ticket context must be regular Markdown under .sdlc/work.')
    flags = os.O_RDONLY | os.O_NOFOLLOW
    descriptors = []
    try:
        descriptor = os.open(str(root), flags | os.O_DIRECTORY)
        descriptors.append(descriptor)
        for component in parts[:-1]:
            descriptor = os.open(component, flags | os.O_DIRECTORY, dir_fd=descriptor)
            descriptors.append(descriptor)
        descriptor = os.open(parts[-1], flags | os.O_NONBLOCK, dir_fd=descriptor)
        descriptors.append(descriptor)
        info = os.fstat(descriptor)
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size > MAX_FILE_BYTES:
            raise ValueError('Ticket context must be a bounded regular file without links.')
        chunks = []
        remaining = MAX_FILE_BYTES + 1
        while remaining:
            block = os.read(descriptor, min(remaining, 65536))
            if not block:
                break
            chunks.append(block)
            remaining -= len(block)
        content = b''.join(chunks)
        if len(content) > MAX_FILE_BYTES:
            raise ValueError('Ticket context exceeds the per-file limit.')
        content.decode('utf-8')
        return content
    except (OSError, UnicodeError) as exc:
        raise ValueError('Ticket context could not be read safely as UTF-8 Markdown.') from exc
    finally:
        for descriptor in reversed(descriptors):
            os.close(descriptor)


def markdown_links(content):
    # Inline/image links and reference definitions. Destinations containing spaces
    # must use Markdown's <...> syntax. Remote links are never downloaded.
    text = content.decode('utf-8')
    inline = re.findall(r'!?\[[^\]\n]*\]\(\s*(<[^>\n]+>|[^\s)]+)', text)
    references = re.findall(r'^\s{0,3}\[[^\]\n]+\]:\s*(<[^>\n]+>|[^\s]+)', text, re.MULTILINE)
    return [value.strip('<>') for value in inline + references]


def context_link(source, link):
    parsed = urlsplit(link)
    if parsed.scheme or parsed.netloc or not parsed.path:
        return None
    path = unquote(parsed.path)
    if '\\' in path or '\0' in path:
        raise ValueError('Ticket context link contains an unsafe path.')
    # Resolve relative references without granting access outside .sdlc/work.
    target_parts = [] if path.startswith('/') else list(PurePosixPath(source).parent.parts)
    for part in path.split('/'):
        if part in ('', '.'):
            continue
        if part == '..':
            if not target_parts:
                raise ValueError('Ticket context link escapes the repository.')
            target_parts.pop()
        else:
            target_parts.append(part)
    if any(part in PROTECTED for part in target_parts):
        raise ValueError('Ticket context links to protected local state.')
    relative = '/'.join(target_parts)
    if target_parts[:2] != ['.sdlc', 'work']:
        # Documentation/code links are read from the fresh clone, never the host.
        return None
    if not relative.endswith('.md'):
        raise ValueError('Local work context links must target Markdown files.')
    return relative


def snapshot(root, ticket, destination):
    ticket = ticket_path(ticket)
    root = Path(root).absolute()
    pending = [ticket]
    seen = set()
    entries = []
    total = 0
    destination = Path(destination)
    destination.mkdir(mode=0o755, parents=True, exist_ok=False)
    destination.chmod(0o755)
    while pending:
        relative = pending.pop(0)
        if relative in seen:
            continue
        if len(seen) >= MAX_FILES:
            raise ValueError('Ticket context exceeds the file-count limit.')
        seen.add(relative)
        content = read_markdown(root, relative)
        total += len(content)
        if total > MAX_TOTAL_BYTES:
            raise ValueError('Ticket context exceeds the total-size limit.')
        target = destination / relative
        target.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
        for directory in (target.parent, *target.parent.parents):
            directory.chmod(0o755)
            if directory == destination:
                break
        with target.open('xb') as output:
            output.write(content)
        target.chmod(0o444)
        entries.append({'path': relative, 'size': len(content), 'sha256': hashlib.sha256(content).hexdigest()})
        for link in markdown_links(content):
            linked = context_link(relative, link)
            if linked and linked not in seen:
                pending.append(linked)
    manifest = {'version': 1, 'ticket': ticket, 'files': entries}
    (destination / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    (destination / 'manifest.json').chmod(0o444)
    return manifest
