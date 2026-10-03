import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import unittest
from unittest.mock import patch

from worker_push_fixtures import PushFixture
from push_spike import GIT, MODULE, PYTHON, Rejected, canonical, clean_env, decode, encode, git, worker_git


class WorkerPushTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='synthetic-worker-push-')
        self.fixture = PushFixture(self.temporary.name)
        self.head = self.fixture.commit('Signed worker increment', 'signed worker change\n')
        self.token = self.fixture.token()

    def tearDown(self):
        self.temporary.cleanup()

    def assert_rejected(self, result):
        self.assertNotEqual(result.returncode, 0, (result.stdout + result.stderr).decode())

    def test_actual_worker_signed_commit_and_push_preserve_oid(self):
        result = self.fixture.push(self.token)
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.assertEqual(self.fixture.remote_head(), self.head)
        self.assertEqual(self.fixture.remote_head(branch='refs/heads/main'), self.fixture.baseline)
        verified = git(self.fixture.provider.home, self.fixture.provider.root / 'repos' / 'target.git',
                       '-c', 'gpg.format=ssh', '-c', 'gpg.ssh.program=/usr/bin/ssh-keygen',
                       '-c', 'gpg.ssh.allowedSignersFile=' +
                       self.fixture.provider._load()['jobs']['job-a']['policy'].replace('.json', '.signers'),
                       'verify-commit', self.head, check=False)
        self.assertEqual(verified.returncode, 0, verified.stderr.decode())
        self.assertNotEqual(os.getuid(), 0)

    def test_same_repo_two_signed_jobs_form_stack(self):
        self.assertEqual(self.fixture.push(self.token).returncode, 0)
        branch_b = 'refs/heads/job/synthetic-b'
        cap_b = self.fixture.provider.register('job-b', 'target.git', branch_b, self.fixture.public_key)
        head_b = self.fixture.commit('Second signed stack increment', 'second stack change\n')
        token_b = self.fixture.token('job-b', cap_b)
        self.assertEqual(self.fixture.push(token_b, branch=branch_b).returncode, 0)
        self.assertEqual(self.fixture.remote_head(), self.head)
        self.assertEqual(self.fixture.remote_head(branch=branch_b), head_b)
        parent = worker_git(self.fixture.worker, 'rev-parse', 'HEAD^').stdout.decode().strip()
        self.assertEqual(parent, self.head)
        self.assert_rejected(self.fixture.push(token_b, branch=self.fixture.branch))

    def test_other_repository_rejected_by_provider_even_if_client_url_is_changed(self):
        self.assert_rejected(self.fixture.push(self.token, repo='other.git',
                                               branch='refs/heads/job/other'))
        self.assertIsNone(self.fixture.remote_head('other.git', 'refs/heads/job/other'))

    def test_missing_invalid_and_forged_tokens_rejected(self):
        version, body, signature = self.token.split('.')
        payload = json.loads(decode(body))
        for token in ['', 'fake-unissued-token', self.token + 'x']:
            self.assert_rejected(self.fixture.push(token))
        payload['repo'] = 'other.git'
        forged = version + '.' + encode(canonical(payload)) + '.' + signature
        self.assert_rejected(self.fixture.push(forged, repo='other.git', branch='refs/heads/job/other'))
        del payload['repo']
        missing_scope = version + '.' + encode(canonical(payload)) + '.' + signature
        self.assert_rejected(self.fixture.push(missing_scope))
        self.assertIsNone(self.fixture.remote_head())

    def test_revoked_token_rejected(self):
        self.fixture.provider.revoke(self.token)
        self.assert_rejected(self.fixture.push(self.token))
        self.assertIsNone(self.fixture.remote_head())

    def test_expired_token_rejected_without_sleep(self):
        # The controller clock fixture issues an authentic token in the past,
        # preserving the registered positive TTL and finite job lifetime.
        with patch('push_spike.time.time', return_value=time.time() - 120):
            expired = self.fixture.provider.renew({'job': 'job-a', 'capability': self.fixture.capability})
        self.assert_rejected(self.fixture.push(expired))
        self.assertIsNone(self.fixture.remote_head())

    def test_unattended_renewal_keeps_one_repo_scope_and_overlap(self):
        renewed = self.fixture.token()
        payload = json.loads(decode(renewed.split('.')[1]))
        old_payload = json.loads(decode(self.token.split('.')[1]))
        self.assertEqual(payload['repo'], 'target.git')
        self.assertEqual(payload['scope'], ['git:read', 'git:push'])
        self.assertNotEqual(payload['jti'], old_payload['jti'])
        self.assertEqual(self.fixture.push(self.token).returncode, 0)
        head = self.fixture.commit('Increment after renewal', 'renewed token change\n')
        self.assertEqual(self.fixture.push(renewed).returncode, 0)
        self.assertEqual(self.fixture.remote_head(), head)
        self.fixture.provider.revoke(self.token)
        self.assert_rejected(self.fixture.push(self.token, repo='other.git'))
        self.assertEqual(self.fixture.push(renewed).returncode, 0)

    def test_renewal_cannot_choose_repo_scope_lifetime_or_another_job(self):
        for extra in [{'repo': 'other.git'}, {'repository_ids': []}, {'scope': []}, {'ttl': 300}]:
            self.assert_rejected(self.fixture.renew(**extra))
        self.assert_rejected(self.fixture.renew(job='job-other'))
        self.assert_rejected(self.fixture.renew(capability='fake-capability'))
        request = b'{"job":"job-a"}'
        result = subprocess.run([PYTHON, MODULE, 'renew', str(self.fixture.provider.root)],
                                input=request, env=clean_env(self.fixture.worker),
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.assert_rejected(result)

    def test_job_expiry_ends_existing_tokens_and_future_renewal(self):
        self.fixture.provider.finish('job-a')
        self.assert_rejected(self.fixture.renew())
        self.assert_rejected(self.fixture.push(self.token))
        self.assertIsNone(self.fixture.remote_head())

    def test_main_tags_and_other_branch_rejected_by_receive_policy(self):
        for branch in ['refs/heads/main', 'refs/tags/synthetic', 'refs/heads/job/unregistered']:
            self.assert_rejected(self.fixture.push(self.token, branch=branch))
        self.assertEqual(self.fixture.remote_head(branch='refs/heads/main'), self.fixture.baseline)
        self.assertIsNone(self.fixture.remote_head(branch='refs/tags/synthetic'))

    def test_excess_refs_are_atomic_rejection(self):
        result = worker_git(self.fixture.worker, 'push', 'fixture::target.git',
                            'HEAD:' + self.fixture.branch, 'HEAD:refs/tags/synthetic',
                            token=self.token, helper_directory=self.fixture.helpers, check=False)
        self.assert_rejected(result)
        self.assertIsNone(self.fixture.remote_head())
        self.assertIsNone(self.fixture.remote_head(branch='refs/tags/synthetic'))

    def test_force_and_delete_rejected(self):
        self.assertEqual(self.fixture.push(self.token).returncode, 0)
        worker_git(self.fixture.worker, 'checkout', '--detach', self.fixture.baseline)
        self.fixture.commit('Signed divergent commit', 'divergent signed change\n')
        self.assert_rejected(self.fixture.push(self.token, force=True))
        self.assert_rejected(self.fixture.push(self.token, source=''))
        self.assertEqual(self.fixture.remote_head(), self.head)

    def test_unsigned_incoming_commit_rejected_in_quarantine(self):
        self.fixture.commit('Unsigned worker increment', 'unsigned change\n', signed=False)
        self.assert_rejected(self.fixture.push(self.token))
        self.assertIsNone(self.fixture.remote_head())

    def test_unsigned_existing_tip_is_not_a_signing_policy_bypass(self):
        self.assert_rejected(self.fixture.push(self.token, source=self.fixture.baseline))
        self.assertIsNone(self.fixture.remote_head())

    def test_token_protocol_does_not_return_master_or_issuer_authority(self):
        master = self.fixture.provider._load()['master']
        payload = json.loads(decode(self.token.split('.')[1]))
        self.assertNotIn(master, self.token)
        self.assertNotIn(master.encode(), decode(self.token.split('.')[1]))
        self.assertNotIn('master', payload)
        request = subprocess.run([PYTHON, MODULE, 'remote-helper', str(self.fixture.provider.root),
                                  'origin', 'target.git'], input=b'get-master\n',
                                 env={**clean_env(self.fixture.worker), 'FAKE_REPOSITORY_TOKEN': self.token},
                                 stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.assert_rejected(request)
        self.assertNotIn(master.encode(), request.stdout + request.stderr)
        self.assertIsNone(self.fixture.remote_head())

    def test_same_uid_fixture_does_not_isolate_provider_storage(self):
        # Positive control for the stated limit: file modes cannot separate two
        # processes with the same UID. This reads only a disposable fake key.
        result = subprocess.run([PYTHON, '-c',
                                 'import json,sys; print(json.load(open(sys.argv[1]))["master"])',
                                 str(self.fixture.provider.state)], env=clean_env(self.fixture.worker),
                                cwd=self.fixture.worker, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout.decode().strip(), self.fixture.provider._load()['master'])

    def test_repository_path_injection_rejected_by_provider(self):
        for repo in ['../target.git', '/target.git', 'target.git/../other.git', 'target.git;echo bad']:
            self.assert_rejected(self.fixture.push(self.token, repo=repo))
        self.assertIsNone(self.fixture.remote_head())

    def test_receiver_drops_worker_git_env_hooks_and_quarantine_injection(self):
        # Worker commands remain full Git commands; only server-side authority
        # receives a rebuilt environment. This probe bypasses worker_git cleanup.
        env = clean_env(self.fixture.worker)
        env.update({'PATH': str(self.fixture.helpers) + ':/usr/bin:/bin',
                    'FAKE_REPOSITORY_TOKEN': self.token,
                    'FIXTURE_RECEIVE_POLICY': '/does/not/exist',
                    'GIT_OBJECT_DIRECTORY': '/does/not/exist',
                    'GIT_ALTERNATE_OBJECT_DIRECTORIES': '/does/not/exist'})
        # Inject server variables at the helper boundary; a worker-local bad
        # object directory would stop Git before it can ask the server anything.
        result = subprocess.run([PYTHON, MODULE, 'remote-helper', str(self.fixture.provider.root),
                                  'origin', 'target.git'], input=b'capabilities\nconnect git-upload-pack\n',
                                 env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.assertNotIn(b'/does/not/exist', result.stdout + result.stderr)
        self.assertIn(self.fixture.baseline.encode(), result.stdout)


if __name__ == '__main__':
    unittest.main(verbosity=2)
