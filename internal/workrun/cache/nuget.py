"""Package-only NuGet cache: validate archives, reconstruct payloads, never copy config.

Executed by the controller, never by a repository check. Seed mutations are
serialized by the controller's kernel lease. Session workers cannot mount seed.
"""
import base64
import hashlib
import json
import io
import os
import pathlib
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import uuid
import xml.etree.ElementTree as ET
import zipfile

SAFE = re.compile(r'[a-z0-9][a-z0-9._+-]*\Z')
LIMIT = 128 * 1024 * 1024
MAX_FILES = 20000


def read_regular(path):
    # Resolve every component using directory descriptors. lstat followed by an
    # ordinary open could follow a link swapped in by a concurrent restore.
    path = pathlib.Path(os.path.abspath(path))
    fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY)
    try:
        for part in path.parts[1:-1]:
            child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = child
        leaf = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=fd)
        with os.fdopen(leaf, "rb") as stream:
            st = os.fstat(stream.fileno())
            if not stat.S_ISREG(st.st_mode) or st.st_size > LIMIT:
                raise ValueError("unsafe package file")
            return stream.read(LIMIT + 1)
    finally:
        os.close(fd)


def regular(path):
    return stat.S_ISREG(path.lstat().st_mode)


def packages(root):
    if not root.exists() or root.is_symlink():
        return
    for identity in sorted(root.iterdir()):
        if not identity.is_dir() or identity.is_symlink() or not SAFE.fullmatch(identity.name):
            continue
        for version in sorted(identity.iterdir()):
            if version.is_dir() and not version.is_symlink() and SAFE.fullmatch(version.name):
                yield version


def normalized_version(version):
    base, sep, suffix = version.lower().partition("-")
    base = base.split("+")[0]
    numbers = base.split(".")
    if not all(n.isdigit() for n in numbers) or len(numbers) > 4:
        return version.lower()
    numbers = [str(int(n)) for n in numbers]
    while len(numbers) < 3:
        numbers.append("0")
    if len(numbers) == 4 and numbers[-1] == "0":
        numbers.pop()
    return ".".join(numbers) + (sep + suffix if sep else "")


def valid(package, payload=True):
    """A hash/marker alone does not establish complete or untampered extraction."""
    identity, version = package.parent.name, package.name
    archive = package / (identity + '.' + version + '.nupkg')
    hash_file = pathlib.Path(str(archive) + '.sha512')
    marker = package / '.nupkg.metadata'
    if package.is_symlink() or not all(regular(p) for p in (archive, hash_file, marker)):
        raise ValueError('incomplete package')
    # Reject every filesystem link, including extra files not present in the zip.
    for root, dirs, files in os.walk(package, followlinks=False):
        for name in dirs + files:
            mode = (pathlib.Path(root) / name).lstat().st_mode
            if not (stat.S_ISREG(mode) or stat.S_ISDIR(mode)):
                raise ValueError('nonregular package entry')
    if archive.stat().st_size > LIMIT:
        raise ValueError('oversized archive')
    data = read_regular(archive)
    digest = base64.b64encode(hashlib.sha512(data).digest()).decode()
    if read_regular(hash_file).decode().strip() != digest:
        raise ValueError('package hash differs')
    metadata = json.loads(read_regular(marker))
    if not isinstance(metadata, dict):
        raise ValueError('invalid package marker')
    content_hash = metadata.get('contentHash', digest)
    try:
        if not isinstance(content_hash, str) or len(base64.b64decode(content_hash, validate=True)) != 64:
            raise ValueError('invalid package content hash')
    except (ValueError, TypeError):
        raise ValueError('invalid package content hash')
    entries = []
    total = 0
    seen = set()
    nuspec = None
    with zipfile.ZipFile(io.BytesIO(data)) as z:
        if len(z.infolist()) > MAX_FILES:
            raise ValueError('too many package entries')
        # NuGet's marker uses package content hash, which can differ from the
        # complete archive SHA-512 after signing. Keep both fields distinct.
        # The sidecar hash above always checks the exact archive bytes.
        if content_hash != digest and '.signature.p7s' not in z.namelist():
            raise ValueError('unsigned package marker hash differs')
        for item in z.infolist():
            path = pathlib.PurePosixPath(item.filename)
            mode = item.external_attr >> 16
            if (path.is_absolute() or '..' in path.parts or '\\' in item.filename
                    or ':' in item.filename or str(path).rstrip('/') != item.filename.rstrip('/')
                    or not path.parts or any(p.startswith('.') and p in ('.', '..') for p in path.parts)
                    or stat.S_ISLNK(mode) or (stat.S_IFMT(mode) not in (0, stat.S_IFREG, stat.S_IFDIR))):
                raise ValueError('unsafe archive path')
            if item.is_dir():
                continue
            total += item.file_size
            if total > LIMIT:
                raise ValueError('oversized package payload')
            # NuGet omits OPC packaging metadata from the extracted cache.
            if (item.filename in ('_rels/.rels', '[Content_Types].xml')
                    or item.filename.startswith('package/services/metadata/core-properties/')):
                z.read(item)  # Still check archive CRC before excluding metadata.
                continue
            key = str(path).casefold()
            if key in seen or key in ('.nupkg.metadata', archive.name.casefold(), hash_file.name.casefold()):
                raise ValueError('duplicate or reserved archive path')
            seen.add(key)
            contents = z.read(item)
            if len(path.parts) == 1 and path.suffix.lower() == '.nuspec':
                if nuspec is not None:
                    raise ValueError('multiple package identities')
                nuspec = ET.fromstring(contents)
            if payload:
                target = package / path
                # NuGet normalizes the root nuspec filename to lower case.
                if len(path.parts) == 1 and path.suffix.lower() == '.nuspec':
                    target = package / item.filename.lower()
                if not regular(target) or read_regular(target) != contents:
                    raise ValueError('incomplete or modified extracted payload')
            entries.append((str(path), contents))
    if nuspec is None:
        raise ValueError('missing package identity')
    fields = {node.tag.rsplit('}', 1)[-1]: (node.text or '') for node in nuspec.iter()}
    if fields.get('id', '').lower() != identity or normalized_version(fields.get('version', '')) != version:
        raise ValueError('package identity differs')
    return data, digest, entries, content_hash


def reconstruct(target, data, digest, entries, content_hash):
    target.mkdir(parents=True)
    for name, contents in entries:
        path = pathlib.PurePosixPath(name)
        if len(path.parts) == 1 and path.suffix.lower() == '.nuspec':
            path = pathlib.PurePosixPath(name.lower())
        destination = target / path
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(contents)
    archive = target / (target.parent.name + '.' + target.name + '.nupkg')
    archive.write_bytes(data)
    pathlib.Path(str(archive) + '.sha512').write_text(digest)
    (target / '.nupkg.metadata').write_text(json.dumps({'version': 2, 'contentHash': content_hash, 'source': 'sdlc-package-cache'}))


def project_packages(workspace):
    """Read committed dependency declarations; never execute project tooling."""
    command = ['git', '--no-replace-objects', '-c', 'safe.directory='+str(workspace), '-c', 'core.hooksPath=/dev/null', '-c', 'core.fsmonitor=false', '-C', str(workspace)]
    env = {'PATH': os.environ['PATH'], 'HOME': '/tmp', 'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null'}
    tree = subprocess.check_output(command+['ls-tree', '-r', '-z', 'HEAD'], env=env)
    selected = {}
    batch = subprocess.Popen(command+['cat-file', '--batch'], stdin=subprocess.PIPE, stdout=subprocess.PIPE, env=env)
    try:
        for entry in tree.split(b'\0'):
            if not entry:
                continue
            metadata, path = entry.split(b'\t', 1)
            mode, kind, oid = metadata.split()
            name = os.fsdecode(path)
            if mode not in (b'100644', b'100755') or kind != b'blob':
                continue
            if not (name.endswith(('.csproj', '.props')) or name.endswith('/dotnet-tools.json') or name == 'dotnet-tools.json'):
                continue
            batch.stdin.write(oid+b'\n'); batch.stdin.flush()
            fields = batch.stdout.readline().split()
            size = int(fields[2])
            if size > 4*1024*1024:
                # Drain the object without retaining an unbounded project blob.
                remaining = size
                while remaining:
                    remaining -= len(batch.stdout.read(min(remaining, 65536)))
                batch.stdout.read(1)
                continue
            data = batch.stdout.read(size)
            if batch.stdout.read(1) != b'\n':
                raise ValueError('incomplete dependency blob')
            try:
                if name.endswith('.json'):
                    for identity, tool in json.loads(data).get('tools', {}).items():
                        selected.setdefault(identity.lower(), set()).add(str(tool.get('version', '')).lower())
                else:
                    for node in ET.fromstring(data).iter():
                        if node.tag.rsplit('}', 1)[-1] in ('PackageReference', 'PackageVersion'):
                            identity = node.get('Include') or node.get('Update')
                            if identity:
                                version = node.get('Version') or next((child.text or '' for child in node if child.tag.rsplit('}', 1)[-1] == 'Version'), '')
                                selected.setdefault(identity.lower(), set()).add(normalized_version(version))
            except (ValueError, ET.ParseError, AttributeError):
                continue
    finally:
        batch.stdin.close(); batch.stdout.close()
        batch.wait()
    return {identity: versions for identity, versions in selected.items() if SAFE.fullmatch(identity)}


def dependencies(entries):
    for name, data in entries:
        if '/' not in name and name.lower().endswith('.nuspec'):
            for node in ET.fromstring(data).iter():
                if node.tag.rsplit('}', 1)[-1] == 'dependency':
                    identity = node.get('id', '').lower()
                    if SAFE.fullmatch(identity):
                        yield identity


def export(source, destination, index_path, workspace=None, epoch=None):
    started = time.monotonic()
    try:
        saved = json.loads(index_path.read_text())
        index = saved.get('packages', {}) if saved.get('epoch') == epoch else {}
    except (OSError, ValueError):
        index = {}
    # Tests without a project inspect only their disposable tree. Production
    # always supplies committed dependency declarations and a seed epoch.
    selected = project_packages(workspace) if workspace else {p.name: set() for p in source.iterdir() if p.is_dir() and not p.is_symlink()}
    queue = list(selected)
    visited = set()
    count = size = rejected = processed = 0
    while queue:
        identity = queue.pop(0)
        if identity in visited or not SAFE.fullmatch(identity):
            continue
        visited.add(identity)
        root = source / identity
        if not root.is_dir() or root.is_symlink():
            continue
        versions = selected.get(identity, set())
        candidates = [p for p in root.iterdir() if p.is_dir() and not p.is_symlink() and SAFE.fullmatch(p.name)]
        pinned = {version for version in versions if SAFE.fullmatch(version)}
        if pinned:
            candidates = [p for p in candidates if p.name in pinned]
        candidates.sort(key=lambda p: (p.name not in versions, p.name))
        for package in candidates:
            key = identity + '/' + package.name
            archive = package / (identity + '.' + package.name + '.nupkg')
            try:
                st = archive.lstat()
                fingerprint = [st.st_size, st.st_mtime_ns, st.st_ctime_ns]
                previous = index.get(key, {})
                if previous.get('fingerprint') == fingerprint:
                    queue.extend(previous.get('dependencies', []))
                    continue
                if processed >= 64 or size + min(st.st_size, LIMIT) > LIMIT:
                    continue
                processed += 1
                data, digest, entries, content_hash = valid(package)
                reconstruct(destination / key, data, digest, entries, content_hash)
                deps = list(dependencies(entries))
                queue.extend(deps)
                index[key] = {'fingerprint': fingerprint, 'dependencies': deps}
                count += 1
                size += len(data)
            except (OSError, ValueError, zipfile.BadZipFile, ET.ParseError, RuntimeError):
                rejected += 1
    (destination / 'index.json').write_text(json.dumps({'epoch': epoch, 'packages': index}))
    print('NuGet host seed: %d packages validated, %d rejected; %.3fs' % (count, rejected, time.monotonic()-started), flush=True)


def fingerprint(path):
    st = path.lstat()
    return [st.st_dev, st.st_ino, st.st_ctime_ns, st.st_mtime_ns, st.st_size]


def promote(source, seed, initial_index=None):
    added = conflicts = rejected = unchanged = 0
    initial = json.loads(read_regular(initial_index)) if initial_index and initial_index.exists() else {}
    seed.mkdir(parents=True, exist_ok=True)
    for package in packages(source):
        target = seed / package.parent.name / package.name
        try:
            if target.exists():
                # Reused seed packages never need another payload validation:
                # no session payload is copied when this identity already exists.
                archive = package / (package.parent.name + '.' + package.name + '.nupkg')
                key = package.parent.name+'/'+package.name
                if initial.get(key) == fingerprint(archive):
                    unchanged += 1
                    continue
                digest = base64.b64encode(hashlib.sha512(read_regular(archive)).digest()).decode()
                old = read_regular(target / (target.parent.name + '.' + target.name + '.nupkg.sha512')).decode().strip()
                if old != digest:
                    conflicts += 1
                continue
            data, digest, entries, content_hash = valid(package)
            target.parent.mkdir(parents=True, exist_ok=True)
            stage = pathlib.Path(tempfile.mkdtemp(prefix='.import-', dir=target.parent))
            try:
                staged = stage / target.parent.name / target.name
                reconstruct(staged, data, digest, entries, content_hash)
                staged.rename(target)
                added += 1
            finally:
                shutil.rmtree(stage)
        except (OSError, ValueError, zipfile.BadZipFile, ET.ParseError, RuntimeError):
            rejected += 1
    print('NuGet seed: %d added, %d unchanged entries, %d immutable conflicts skipped, %d invalid packages skipped' % (added, unchanged, conflicts, rejected), flush=True)


def prepare(seed, incoming, home, cache):
    promote(incoming, seed)
    epoch = seed.parent / '.epoch'
    if not epoch.exists():
        epoch.write_text(str(uuid.uuid4()))
    # Cache source ancestors are controller-owned and never mounted in workers.
    # Only the child directories are mounted, preventing daemon-side symlink
    # redirection when a worker changes its writable HOME.
    cache.mkdir(parents=True, exist_ok=True)
    cache.chmod(0o700)
    packages_root = cache / 'packages'
    packages_root.mkdir()
    subprocess.run(['cp', '-a', '--reflink=auto', str(seed) + '/.', str(packages_root)], check=True)
    (cache / 'scratch').mkdir()
    for directory in (packages_root, cache / 'scratch'):
        for root, dirs, files in os.walk(directory):
            os.chown(root, 1000, 1000)
            for name in dirs + files:
                os.chown(pathlib.Path(root) / name, 1000, 1000)
    initial = {}
    for package in packages(packages_root):
        archive = package/(package.parent.name+'.'+package.name+'.nupkg')
        initial[package.parent.name+'/'+package.name] = fingerprint(archive)
    (cache / '.seed-index.json').write_text(json.dumps(initial))
    (home / '.nuget').mkdir(parents=True)
    # The source helper sets HOME permissions before transferring ownership.
    # Leave HOME root-owned here: neither helper has CAP_FOWNER to chmod it
    # after it belongs to the worker.
    home.chmod(0o700)
    os.chown(home / '.nuget', 1000, 1000)



if __name__ == '__main__':
    action, *args = sys.argv[1:]
    paths = [pathlib.Path(arg) for arg in args[:4]]
    if action == 'export' and len(args) > 4:
        export(*paths, epoch=args[4])
    else:
        {'export': export, 'prepare': prepare, 'promote': promote}[action](*paths)
