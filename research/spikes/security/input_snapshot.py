"""Experimental local Markdown input capture. Not an installed security boundary.

The caller owns the registered source root and destination. Capture is limited
to Markdown inside .tickets and .specifications, with protected state paths
rejected. This does not detect credentials pasted into a document or prove file
provenance. Repository documentation references remain references to resolve
against the job clone. The link parser supports only inline links without titles.
"""
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import posixpath
import re
import stat
from types import MappingProxyType
from urllib.parse import unquote, urlsplit


LINK = re.compile(r"(?<!!)\[[^\]]*\]\(([^\s)]+)\)")
ALLOWED_ROOTS = frozenset({".tickets", ".specifications"})
PROTECTED_PARTS = frozenset({".git", ".secrets", ".ssh", ".codex", ".t3"})
MAX_FILES = 128
MAX_FILE_BYTES = 1_048_576
MAX_TOTAL_BYTES = 4_194_304


def _input_path(value):
    path = PurePosixPath(value)
    if path.is_absolute() or not path.parts or path.parts[0] not in ALLOWED_ROOTS:
        raise ValueError("Input path is outside the enrolled input roots")
    if any(part in {".", ".."} for part in path.parts) or "\x00" in value or "\\" in value:
        raise ValueError("Unsafe input path")
    if any(part in PROTECTED_PARTS for part in path.parts):
        raise ValueError("Input path names a protected state directory")
    if path.suffix != ".md":
        raise ValueError("Only Markdown job inputs are supported by this spike")
    return path


def _read_relative(root_fd, relative):
    """Open each component relative to an enrolled fd, never following a symlink."""
    relative = _input_path(str(relative))
    directory = os.dup(root_fd)
    try:
        for part in relative.parts[:-1]:
            next_directory = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW,
                                     dir_fd=directory)
            os.close(directory)
            directory = next_directory
        fd = os.open(relative.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK,
                     dir_fd=directory)
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
                raise ValueError("Input must be a regular file with no hard links")
            if info.st_size > MAX_FILE_BYTES:
                raise ValueError("Input file exceeds the spike size limit")
            with os.fdopen(fd, "rb", closefd=False) as stream:
                content = stream.read(MAX_FILE_BYTES + 1)
            if len(content) > MAX_FILE_BYTES:
                raise ValueError("Input file exceeds the spike size limit")
            content.decode("utf-8")
            return content
        finally:
            os.close(fd)
    finally:
        os.close(directory)


def capture(source_root, ticket_files):
    """Return an immutable in-memory capture and digest; emit no document contents.

    This freezes the bytes read, not an atomic snapshot of a concurrently edited
    source tree. A production controller must own and protect the stored result.
    """
    source_root = Path(source_root)
    pending = []
    for value in ticket_files:
        if len(pending) >= MAX_FILES:
            raise ValueError("Too many seed input files")
        pending.append(str(_input_path(value)))
    root_fd = os.open(source_root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    files = {}
    references = set()
    total = 0
    try:
        while pending:
            relative = pending.pop()
            if relative in files:
                continue
            if len(files) >= MAX_FILES:
                raise ValueError("Too many input files")
            content = _read_relative(root_fd, relative)
            total += len(content)
            if total > MAX_TOTAL_BYTES:
                raise ValueError("Input pack exceeds the spike size limit")
            files[relative] = content
            for target in LINK.findall(content.decode("utf-8")):
                parsed = urlsplit(target)
                if parsed.scheme in {"http", "https"}:
                    continue
                if parsed.scheme or parsed.netloc:
                    raise ValueError("Unsupported local input reference")
                decoded = unquote(parsed.path)
                if not decoded:
                    continue
                if PurePosixPath(decoded).is_absolute() or "\\" in decoded or "\x00" in decoded:
                    raise ValueError("Unsafe local input reference")
                linked = posixpath.normpath(str(PurePosixPath(relative).parent / decoded))
                path = PurePosixPath(linked)
                if path.is_absolute() or path.parts[0] == "..":
                    raise ValueError("Local input reference escapes the registered repository")
                if any(part in PROTECTED_PARTS for part in path.parts):
                    raise ValueError("Local input reference names a protected state directory")
                if path.parts[0] in ALLOWED_ROOTS:
                    pending.append(str(_input_path(linked)))
                else:
                    references.add(linked)
    finally:
        os.close(root_fd)
    manifest = {name: {"sha256": hashlib.sha256(data).hexdigest(), "bytes": len(data)}
                for name, data in sorted(files.items())}
    digest = hashlib.sha256(json.dumps(manifest, sort_keys=True).encode()).hexdigest()
    frozen_manifest = {name: MappingProxyType(fields) for name, fields in manifest.items()}
    return MappingProxyType(files), MappingProxyType(frozen_manifest), tuple(sorted(references)), digest


def ticket_metadata(files):
    """Read explicit status/dependency fields; this is not a ticket scheduler."""
    result = []
    for name, content in sorted(files.items()):
        path = PurePosixPath(name)
        if path.parts[0] != ".tickets" or not re.match(r"\d+-", path.name):
            continue
        text = content.decode("utf-8")
        status = re.search(r"^\*\*Status:\*\*\s*(.+)$", text, re.MULTILINE)
        blocked = re.search(r"^\*\*Blocked by:\*\*\s*(.+)$", text, re.MULTILINE)
        result.append({"file": name, "status": status.group(1).strip() if status else None,
                       "dependencies": LINK.findall(blocked.group(1)) if blocked else []})
    return result
