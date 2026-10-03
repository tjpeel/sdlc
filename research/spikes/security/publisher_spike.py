"""Offline publication protocol spike. Python stdlib, Git, and OpenSSH only.

Trust: Policy, Job, helper storage and candidate review are coordinator owned.
Untrusted: candidate bytes and every worker checkout/configuration. No checkout,
worker Git invocation, project code execution, arbitrary command or signing API.
"""
import base64
from dataclasses import dataclass, replace
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import stat
import subprocess


GIT = str(Path(shutil.which('git')).resolve())
SSH_KEYGEN = '/usr/bin/ssh-keygen'
OID = re.compile(r'[0-9a-f]{40}')
NAME = re.compile(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,79}')
MAX_CANDIDATE = 8 * 1024 * 1024


class Rejected(ValueError):
    pass


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def object_oid(kind, data):
    return hashlib.sha1(f'{kind} {len(data)}\0'.encode() + data).hexdigest()


def clean_environment(home):
    # An allowlist discards GIT_CONFIG_COUNT, GIT_CONFIG_PARAMETERS, GIT_DIR,
    # GIT_WORK_TREE, alternates, tracing, preload variables, and SSH_AUTH_SOCK.
    # Git compares ceilings with physical cwd paths; temporary paths may pass
    # through host symlinks, so a merely absolute spelling is insufficient.
    home = Path(home).resolve()
    return {
        'PATH': '/usr/bin:/bin', 'HOME': str(home), 'LC_ALL': 'C',
        'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_SYSTEM': '/dev/null',
        'GIT_CONFIG_GLOBAL': '/dev/null', 'GIT_ATTR_NOSYSTEM': '1',
        'GIT_TEMPLATE_DIR': str(home / 'empty-template'),
        'GIT_TERMINAL_PROMPT': '0', 'GIT_NO_REPLACE_OBJECTS': '1',
        # Git ignores a ceiling equal to its current directory. The physical
        # immediate parent is an actual ancestor and stops upward discovery.
        'GIT_CEILING_DIRECTORIES': str(home.parent),
        'GIT_ALLOW_PROTOCOL': 'file', 'GIT_DEFAULT_HASH': 'sha1',
    }


def ensure_neutral_home(home):
    if os.path.lexists(home / '.git') or (
            (home / 'HEAD').is_file() and (home / 'objects').is_dir() and (home / 'refs').is_dir()):
        raise Rejected('Neutral helper home must not be a Git repository')


def git(home, repository, *args, data=None, extra=None, check=True):
    ensure_neutral_home(home)
    env = clean_environment(home)
    if extra:
        env.update(extra)
    command = [GIT, '-c', 'core.hooksPath=/dev/null',
               '-c', 'core.attributesFile=/dev/null', '-c', 'commit.gpgSign=false',
               '-c', 'protocol.ext.allow=never', '-c', 'protocol.file.allow=always',
               '-c', 'core.fsmonitor=false']
    if repository:
        command += ['--git-dir', str(repository)]
    # Commands without --git-dir cannot discover caller or ancestor .git/config.
    return subprocess.run(command + list(args), input=data, env=env, cwd=home,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=check)


def text_git(home, repository, *args, **kw):
    return git(home, repository, *args, **kw).stdout.decode().strip()


@dataclass(frozen=True)
class Policy:
    repository: str
    remote: Path
    base_branch: str
    author: str


@dataclass(frozen=True)
class Job:
    job_id: str
    repository: str
    base_branch: str
    base_oid: str
    head_branch: str
    author: str
    candidate_name: str
    candidate_sha256: str
    reviewed_trees: tuple
    committed_at: str = '2026-10-01T12:00:00+0000'
    parent_job_id: str | None = None


def read_candidate(spool, name):
    # Worker chooses no external path, slash, traversal, or symbolic filename.
    if not isinstance(name, str) or not NAME.fullmatch(name):
        raise Rejected('Candidate must be a single fixed spool filename')
    # Anchor filename lookup to an opened real directory, preventing a rename
    # race from redirecting the spool between validation and opening the file.
    directory = os.open(spool, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
    finally:
        os.close(directory)
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_nlink != 1 or st.st_size > MAX_CANDIDATE:
            raise Rejected('Candidate must be a bounded regular file')
        with os.fdopen(fd, 'rb', closefd=False) as source:
            raw = source.read(MAX_CANDIDATE + 1)
        if len(raw) > MAX_CANDIDATE:
            raise Rejected('Candidate exceeds size limit')
        return raw
    finally:
        os.close(fd)


def parse_candidate(raw, job):
    try:
        manifest = json.loads(raw)
        if set(manifest) != {'version', 'base', 'head', 'commits', 'objects'}:
            raise Rejected('Unexpected candidate fields')
        if manifest['version'] != 1 or manifest['base'] != job.base_oid:
            raise Rejected('Candidate base does not match fixed job base')
        commits = manifest['commits']
        if not isinstance(commits, list) or not 1 <= len(commits) <= 32:
            raise Rejected('Expected a bounded nonempty linear commit increment')
        if len(set(commits)) != len(commits) or manifest['head'] != commits[-1]:
            raise Rejected('Candidate head does not match increment')
        objects = {}
        for oid, obj in manifest['objects'].items():
            if not OID.fullmatch(oid) or set(obj) != {'type', 'data'}:
                raise Rejected('Invalid object envelope')
            if obj['type'] not in ('commit', 'tree', 'blob'):
                raise Rejected('Unsupported object type')
            payload = base64.b64decode(obj['data'], validate=True)
            if object_oid(obj['type'], payload) != oid:
                raise Rejected('Object hash does not match content')
            objects[oid] = (obj['type'], payload)
        parent = job.base_oid
        parsed = []
        for oid in commits:
            kind, payload = objects[oid]
            if kind != 'commit':
                raise Rejected('Increment object is not a commit')
            headers, message = payload.split(b'\n\n', 1)
            lines = headers.decode('utf-8').splitlines()
            # A deliberately small protocol: no merges, existing signatures,
            # encoding headers, extra headers, or worker-selected signing data.
            if len(lines) != 4 or not lines[0].startswith('tree ') or lines[1] != f'parent {parent}':
                raise Rejected('Increment must have the fixed single parent')
            tree = lines[0][5:]
            if not OID.fullmatch(tree):
                raise Rejected('Invalid tree')
            for prefix, line in zip(('author ', 'committer '), lines[2:]):
                expected = re.escape(prefix + job.author) + r' [0-9]{1,12} [+-][0-9]{4}'
                if not re.fullmatch(expected, line):
                    raise Rejected('Candidate identity does not match fixed job author')
            author_date = lines[2][len('author ' + job.author) + 1:]
            parsed.append((oid, tree, message, author_date))
            parent = oid
        if tuple(row[1] for row in parsed) != job.reviewed_trees:
            raise Rejected('Candidate trees differ from reviewed trees')
        return objects, parsed
    except (AttributeError, KeyError, TypeError, UnicodeError, ValueError) as exc:
        if isinstance(exc, Rejected):
            raise
        raise Rejected('Malformed candidate') from exc


class Publisher:
    def __init__(self, root, spool, policy, signing_key):
        self.root, self.spool, self.policy = Path(root), Path(spool), policy
        self.key = Path(signing_key)
        self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
        (self.root / 'home' / 'empty-template').mkdir(parents=True, exist_ok=True)
        self.home = self.root / 'home'
        ensure_neutral_home(self.home)
        self.allowed_signers = self.root / 'allowed-signers'
        public_key = subprocess.run([SSH_KEYGEN, '-y', '-f', str(self.key)],
                                    env=clean_environment(self.home), cwd=self.home, check=True,
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout.decode().strip()
        self.allowed_signers.write_text(policy.author.rsplit(' <', 1)[1][:-1] + ' ' + public_key + '\n')
        self.jobs = {}
        self.calls = []
        # Local-only upload/receive processes get explicit config and clean env.
        # Production HTTPS/API transport needs its own fixed implementation.
        for name in ('upload', 'receive'):
            program = self.root / f'{name}-pack'
            command = [GIT, '-c', 'core.hooksPath=/dev/null',
                       '-c', 'core.fsmonitor=false', '-c', 'receive.denyNonFastForwards=true',
                       '-c', 'receive.fsckObjects=true', f'{name}-pack']
            program.write_text('#!/usr/bin/env python3\nimport os, sys\nos.chdir(' +
                               repr(str(self.home)) + ')\nos.execve(' +
                               repr(GIT) + ', ' + repr(command) + ' + sys.argv[1:], ' +
                               repr(clean_environment(self.home)) + ')\n')
            program.chmod(0o700)

    def admit(self, job):
        """Coordinator-only review admission; not exposed to workers."""
        if not NAME.fullmatch(job.job_id) or job.job_id in self.jobs:
            raise Rejected('Invalid or duplicate fixed job')
        if (job.repository, job.author) != (self.policy.repository, self.policy.author):
            raise Rejected('Job does not match repository policy')
        if job.parent_job_id:
            if job.parent_job_id not in self.jobs:
                raise Rejected('Unknown stack parent job')
            parent = self.jobs[job.parent_job_id]
            receipt_path = self.root / f'{parent.job_id}.receipt.json'
            if not receipt_path.exists():
                raise Rejected('Stack parent has not been published')
            receipt = json.loads(receipt_path.read_text())
            if receipt['status'] != 'published' or (job.base_branch, job.base_oid) != (
                    parent.head_branch, receipt['signed_head']):
                raise Rejected('Stack job must use canonical signed parent')
        elif job.base_branch != self.policy.base_branch:
            raise Rejected('Root job base does not match repository policy')
        for ref in (job.base_branch, job.head_branch):
            if ref.startswith(('-', 'codex/')) or ref == '' or ref == 'HEAD':
                raise Rejected('Invalid branch')
            git(self.home, None, 'check-ref-format', '--branch', ref)
        if job.head_branch in (job.base_branch, self.policy.base_branch) or not OID.fullmatch(job.base_oid):
            raise Rejected('Invalid fixed job base or head')
        if self._head(job.base_branch) != job.base_oid:
            raise Rejected('Fixed base does not match remote branch')
        raw = read_candidate(self.spool, job.candidate_name)
        if digest(raw) != job.candidate_sha256:
            raise Rejected('Review digest does not match candidate')
        parse_candidate(raw, job)
        self.jobs[job.job_id] = job

    def request_for(self, job_id):
        job = self.jobs[job_id]
        return {key: getattr(job, key) for key in (
            'job_id', 'repository', 'base_branch', 'head_branch', 'author',
            'candidate_name', 'candidate_sha256')}

    def _run(self, repo, *args, **kw):
        self.calls.append(args)
        return git(self.home, repo, *args, **kw)

    def _head(self, branch):
        out = self._run(None, 'ls-remote', '--upload-pack=' + self._transport('upload'),
                        '--heads', str(self.policy.remote),
                        f'refs/heads/{branch}').stdout.decode().split()
        return out[0] if out else None

    def _transport(self, name):
        # Git interprets upload/receive-pack values as shell command strings.
        return shlex.quote(str(self.root / (name + '-pack')))

    def publish(self, request):
        if not isinstance(request, dict) or request.get('job_id') not in self.jobs:
            raise Rejected('Unknown job')
        job = self.jobs[request['job_id']]
        if request != self.request_for(job.job_id):
            raise Rejected('Request does not match fixed publication job')
        raw = read_candidate(self.spool, job.candidate_name)
        if digest(raw) != job.candidate_sha256:
            raise Rejected('Candidate changed after review')
        objects, commits = parse_candidate(raw, job)
        receipt_file = self.root / f'{job.job_id}.receipt.json'
        receipt = json.loads(receipt_file.read_text()) if receipt_file.exists() else None
        # On retry verify exact remote SHA before returning the saved PR receipt.
        if receipt and receipt['status'] == 'published':
            if self._head(job.head_branch) != receipt['signed_head']:
                raise Rejected('Published branch moved after recorded publication')
            return receipt
        if self._head(job.base_branch) != job.base_oid:
            raise Rejected('Base branch moved after review')
        repo = self.root / f'{job.job_id}.git'
        if not repo.exists():
            self._run(None, 'init', '--bare', '--template=' + str(self.home / 'empty-template'), str(repo))
            self._run(repo, 'fetch', '--no-tags', '--upload-pack=' + self._transport('upload'),
                      '--', str(self.policy.remote), job.base_oid)
        self._run(repo, 'cat-file', '-e', job.base_oid + '^{commit}')
        for oid, (kind, payload) in objects.items():
            result = self._run(repo, 'hash-object', '-w', '-t', kind, '--stdin', data=payload)
            if result.stdout.decode().strip() != oid:
                raise Rejected('Git object import changed content')
        self._run(repo, 'fsck', '--strict', '--no-reflogs', '--no-dangling', '--no-progress')
        if receipt is None:
            name, email = job.author.rsplit(' <', 1)
            email = email[:-1]
            parent = job.base_oid
            signed = []
            for original, tree, message, author_date in commits:
                env = {'GIT_AUTHOR_NAME': name, 'GIT_AUTHOR_EMAIL': email,
                       'GIT_AUTHOR_DATE': author_date, 'GIT_COMMITTER_NAME': name,
                       'GIT_COMMITTER_EMAIL': email, 'GIT_COMMITTER_DATE': job.committed_at}
                output = self._run(repo, '-c', 'gpg.format=ssh',
                                   '-c', 'gpg.ssh.program=' + SSH_KEYGEN,
                                   '-c', 'user.signingKey=' + str(self.key),
                                   'commit-tree', '-S', tree, '-p', parent, data=message, extra=env)
                parent = output.stdout.decode().strip()
                signed.append({'candidate': original, 'signed': parent, 'tree': tree})
                actual_tree = self._run(repo, 'rev-parse', parent + '^{tree}').stdout.decode().strip()
                if actual_tree != tree:
                    raise Rejected('Signing did not preserve candidate tree')
                self._run(repo, '-c', 'gpg.format=ssh', '-c', 'gpg.ssh.program=' + SSH_KEYGEN,
                          '-c', 'gpg.ssh.allowedSignersFile=' + str(self.allowed_signers),
                          'verify-commit', parent)
            receipt = {'job_id': job.job_id, 'repository': job.repository,
                       'base_branch': job.base_branch, 'base_oid': job.base_oid,
                       'head_branch': job.head_branch, 'candidate_sha256': job.candidate_sha256,
                       'signed_head': parent, 'signed_commits': signed, 'status': 'signed',
                       'pr': {'id': digest(canonical([job.repository, job.head_branch]))[:16],
                              'draft': True, 'transport': 'deterministic-mock'}}
            # Persist canonical signed SHA before push; a crash after push retries
            # the same object and mock PR identity instead of signing again.
            self._save(receipt_file, receipt)
        current = self._head(job.head_branch)
        if current != receipt['signed_head']:
            # Existing branch must be an ancestor. No force capability exists.
            if current:
                self._run(repo, 'fetch', '--no-tags', '--upload-pack=' + self._transport('upload'),
                          '--', str(self.policy.remote), current)
                if self._run(repo, 'merge-base', '--is-ancestor', current, receipt['signed_head'],
                             check=False).returncode:
                    raise Rejected('Existing head would require a non-fast-forward publication')
            # Conservative spike policy: drift during signing also stops a new
            # push. This remains a check/push race, not a remote transaction.
            if self._head(job.base_branch) != job.base_oid:
                raise Rejected('Base branch moved before push')
            self._run(repo, 'push', '--receive-pack=' + self._transport('receive'),
                      '--', str(self.policy.remote), receipt['signed_head'] + ':refs/heads/' + job.head_branch)
        if self._head(job.head_branch) != receipt['signed_head']:
            raise Rejected('Remote publication SHA does not match signed result')
        receipt['status'] = 'published'
        self._save(receipt_file, receipt)
        return receipt

    @staticmethod
    def _save(path, receipt):
        temporary = path.with_suffix('.next')
        temporary.write_bytes(canonical(receipt))
        os.replace(temporary, path)


def candidate_from_worker(home, worker_git, base_oid):
    """Worker-side export only; publisher never calls this on a worker repository."""
    head = text_git(home, worker_git, 'rev-parse', 'HEAD')
    commits = text_git(home, worker_git, 'rev-list', '--reverse', base_oid + '..' + head).splitlines()
    object_ids = text_git(home, worker_git, 'rev-list', '--objects', '--no-object-names', head,
                          '^' + base_oid).splitlines()
    objects = {}
    for oid in object_ids:
        kind = text_git(home, worker_git, 'cat-file', '-t', oid)
        payload = git(home, worker_git, 'cat-file', kind, oid).stdout
        objects[oid] = {'type': kind, 'data': base64.b64encode(payload).decode()}
    trees = tuple(text_git(home, worker_git, 'rev-parse', oid + '^{tree}') for oid in commits)
    return canonical({'version': 1, 'base': base_oid, 'head': head,
                      'commits': commits, 'objects': objects}), trees
