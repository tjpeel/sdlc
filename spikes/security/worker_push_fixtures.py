"""Disposable local setup shared by the tests and combined signing/push proof."""
import json
from pathlib import Path
import subprocess

from push_spike import GIT, MODULE, PYTHON, Provider, clean_env, git, worker_git, worker_push


class PushFixture:
    def __init__(self, root, public_key=None):
        self.root = Path(root).resolve()
        self.root.mkdir(parents=True, exist_ok=True)
        self.worker = self.root / 'worker'
        self.worker.mkdir()
        self.provider = Provider(self.root / 'provider')
        self.helpers = self.root / 'helpers'
        self.provider.write_worker_helper(self.helpers)
        self.key = self.worker / 'disposable-signing-key'
        if public_key is None:
            subprocess.run(['/usr/bin/ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(self.key)],
                           env=clean_env(self.worker), cwd=self.worker, check=True)
            public_key = self.key.with_suffix('.pub').read_text()
        self.public_key = public_key
        self.branch = 'refs/heads/job/synthetic-a'
        self.capability = self.provider.register('job-a', 'target.git', self.branch, public_key)
        self.other_capability = self.provider.register('job-other', 'other.git',
                                                       'refs/heads/job/other', public_key)
        worker_git(self.worker, 'init')
        for name, value in [('user.name', 'Synthetic Worker'), ('user.email', 'worker@example.invalid'),
                            ('gpg.format', 'ssh'), ('gpg.ssh.program', '/usr/bin/ssh-keygen'),
                            ('user.signingkey', str(self.key))]:
            worker_git(self.worker, 'config', name, value)
        self.baseline = self.commit('Fixture baseline', 'baseline\n', signed=False)
        # Trusted controller seeds main before the worker receives a credential.
        git(self.provider.home, self.worker / '.git', 'push',
            str(self.provider.root / 'repos' / 'target.git'), 'HEAD:refs/heads/main')

    def commit(self, message, content, signed=True):
        (self.worker / 'fixture.txt').write_text(content)
        worker_git(self.worker, 'add', 'fixture.txt')
        worker_git(self.worker, 'commit', '-S' if signed else '--no-gpg-sign', '-m', message)
        return worker_git(self.worker, 'rev-parse', 'HEAD').stdout.decode().strip()

    def renew(self, job='job-a', capability=None, **additional):
        """Worker renewal subprocess knows a job capability, never an issuer key."""
        request = {'job': job, 'capability': capability if capability is not None else self.capability}
        request.update(additional)
        result = subprocess.run([PYTHON, MODULE, 'renew', str(self.provider.root)],
                                input=json.dumps(request).encode(), env=clean_env(self.worker),
                                cwd=self.worker, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        return result

    def token(self, job='job-a', capability=None):
        result = self.renew(job, capability)
        if result.returncode:
            raise RuntimeError('Synthetic renewal failed')
        return result.stdout.decode().strip()

    def push(self, token, repo='target.git', branch=None, source='HEAD', force=False):
        return worker_push(self.worker, repo, branch or self.branch, token,
                           self.helpers, source=source, force=force)

    def remote_head(self, repo='target.git', branch=None):
        result = git(self.provider.home, self.provider.root / 'repos' / repo,
                     'rev-parse', '--verify', branch or self.branch, check=False)
        return result.stdout.decode().strip() if result.returncode == 0 else None
