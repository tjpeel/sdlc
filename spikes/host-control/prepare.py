#!/usr/bin/env python3
"""Prepare public sources and a synthetic ticket for the two-account host trial."""

import argparse
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import secrets
import shutil
import stat
import subprocess
import sys
import tarfile


ROOT = Path(__file__).resolve().parents[2]
TRACKED_PREFIXES = ("runtime", "scripts", "tests")
TRACKED_FILES = (
    ".github/dependency-files-filter.jq",
    ".github/dependency-pr-filter.jq",
    ".github/workflows/update-runtime-pins.yml",
)
FIXTURE_FILES = (
    "spikes/colima/run_trial.py",
    "spikes/colima/full_trial.sh",
    "spikes/colima/full_fixture.py",
    "spikes/colima/trial.sh",
    "spikes/colima/inner-trial.sh",
    "spikes/colima/compose.yaml",
    "spikes/colima/app/Program.cs",
    "spikes/colima/app/Trial.csproj",
    "spikes/colima/app/NuGet.Config",
    "spikes/colima/http/Dockerfile",
    "spikes/colima/http/index.html",
    "spikes/host-control/probe.py",
)
PROTECTED_INPUT = "spikes/host-control/protected-input.txt"
MANIFEST = ".sdlc-trial-sources.json"
TICKET = (
    "# Repair the example\n\n"
    "Replace the broken example with the repaired text in example.txt. "
    "See [context](context.md).\n"
)
FORBIDDEN_COMPONENTS = frozenset((
    ".git", ".secrets", ".cache", ".codex", ".claude", ".aws", ".ssh",
    "__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".tox",
    ".venv", "venv", "node_modules", "profiles.local.json", "auth.json",
    "credentials", "credentials.json", "credentials.db",
))


class PreparationError(RuntimeError):
    """A refused source or output location."""


def public_source_path(value):
    """Accept only normal relative paths from the source allowlist."""
    if not isinstance(value, str) or not value or "\\" in value or "\0" in value:
        return False
    path = PurePosixPath(value)
    if path.is_absolute() or path.as_posix() != value or any(
            part in ("", ".", "..") or part.lower() in FORBIDDEN_COMPONENTS
            or part.lower().startswith("profiles.local.")
            for part in path.parts):
        return False
    return (len(path.parts) > 1 and path.parts[0] in TRACKED_PREFIXES
            or value in TRACKED_FILES or value in FIXTURE_FILES)


def tracked_sources(repo):
    # Read the index only. Ambient Git directory overrides and user/system
    # configuration must not redirect the command or supply authentication.
    environment = {
        "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        "LC_ALL": "C",
        "GIT_CONFIG_NOSYSTEM": "1",
        "GIT_CONFIG_GLOBAL": os.devnull,
        "GIT_OPTIONAL_LOCKS": "0",
    }
    try:
        result = subprocess.run(
            ["git", "-C", str(repo), "ls-files", "-z", "--",
             *TRACKED_PREFIXES, *TRACKED_FILES],
            env=environment, check=True, stdout=subprocess.PIPE,
            stderr=subprocess.PIPE, timeout=30,
        )
    except (OSError, subprocess.SubprocessError) as error:
        raise PreparationError("Cannot read the public source index.") from error
    paths = [os.fsdecode(item) for item in result.stdout.split(b"\0") if item]
    if not paths:
        raise PreparationError("The checkout has no tracked public trial sources.")
    return paths


def read_public_source(repo, relative):
    if not public_source_path(relative):
        raise PreparationError("Source is outside the public allowlist: " + repr(relative))
    path = repo / relative
    # Refuse a directory symlink as well as a symlink in the file itself.
    for parent in path.parents:
        if parent == repo:
            break
        if parent.is_symlink():
            raise PreparationError("Source has a symlink parent: " + relative)
    descriptor = None
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
        metadata = os.fstat(descriptor)
        if not stat.S_ISREG(metadata.st_mode):
            raise PreparationError("Source must be a regular public file: " + relative)
        with os.fdopen(descriptor, "rb") as stream:
            descriptor = None
            data = stream.read()
    except OSError as error:
        raise PreparationError("Cannot read a regular public source: " + relative) from error
    finally:
        if descriptor is not None:
            os.close(descriptor)
    mode = 0o755 if metadata.st_mode & 0o111 else 0o644
    return data, mode


def archive_entry(archive, name, data, mode):
    info = tarfile.TarInfo(name)
    info.size = len(data)
    info.mode = mode
    info.uid = info.gid = 0
    info.uname = info.gname = ""
    info.mtime = 0
    archive.addfile(info, io.BytesIO(data))


def source_archive(repo, trial_id):
    if not re.fullmatch(r"host-control-[0-9a-f]{12}", trial_id):
        raise PreparationError("Trial identifier must be host-control-<12 lowercase hex digits>.")
    paths = sorted(set(tracked_sources(repo)) | set(FIXTURE_FILES))
    sources = {path: read_public_source(repo, path) for path in paths}
    sources[PROTECTED_INPUT] = (TICKET.encode("utf-8"), 0o600)
    manifest = {
        "version": 1,
        "trial_id": trial_id,
        "files": [
            {"path": path, "size": len(sources[path][0]),
             "sha256": hashlib.sha256(sources[path][0]).hexdigest()}
            for path in sorted(sources)
        ],
    }
    prefix = trial_id + "-copy/"
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w", format=tarfile.PAX_FORMAT) as archive:
        directory = tarfile.TarInfo(prefix)
        directory.type = tarfile.DIRTYPE
        directory.mode = 0o700
        directory.uid = directory.gid = 0
        directory.uname = directory.gname = ""
        directory.mtime = 0
        archive.addfile(directory)
        for path in sorted(sources):
            data, mode = sources[path]
            archive_entry(archive, prefix + path, data, mode)
        archive_entry(archive, prefix + MANIFEST,
                      (json.dumps(manifest, indent=2) + "\n").encode("utf-8"), 0o644)
    return buffer.getvalue(), manifest


def write_private_file(path, data):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "wb") as stream:
        os.fchmod(stream.fileno(), 0o600)
        stream.write(data)


def prepare_bundle(output_dir, repo=ROOT, trial_id=None):
    repo = Path(repo).resolve()
    requested = Path(output_dir).expanduser().absolute()
    output_dir = requested.resolve()
    if output_dir == repo or repo in output_dir.parents:
        raise PreparationError("Keep the bundle outside the source checkout.")
    # lexists also refuses dangling symlinks and other existing directory entries.
    if os.path.lexists(requested) or os.path.lexists(output_dir):
        raise PreparationError("Use a new output directory; existing locations are refused.")
    trial_id = trial_id or "host-control-" + secrets.token_hex(6)
    payload, manifest = source_archive(repo, trial_id)
    checksum = hashlib.sha256(payload).hexdigest()
    created = False
    try:
        output_dir.mkdir(mode=0o700)
        created = True
        output_dir.chmod(0o700)
        write_private_file(output_dir / "source.tar", payload)
        write_private_file(output_dir / "source.tar.sha256",
                           (checksum + "  source.tar\n").encode("ascii"))
        write_private_file(output_dir / "original-ticket.md", TICKET.encode("utf-8"))
    except OSError as error:
        if created:
            shutil.rmtree(output_dir)
        raise PreparationError("Cannot write the new private output directory.") from error
    return {"trial_id": trial_id, "files": len(manifest["files"]), "sha256": checksum}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path, required=True,
                        help="New private bundle directory outside the source checkout.")
    args = parser.parse_args(argv)
    try:
        result = prepare_bundle(args.output_dir)
    except PreparationError as error:
        print(str(error), file=sys.stderr)
        return 1
    print("Prepared trial: " + result["trial_id"])
    print("Wrote source.tar, source.tar.sha256 and the synthetic original-ticket.md "
          "to the requested private output directory.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
