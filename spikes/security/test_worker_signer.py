"""Reusable disposable worker fixture and actual Git acceptance probes."""
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock

from worker_signer import (
    IDENTITY, MAX_FRAME, SSH_KEYGEN, Rejected, Signer, SigningJob, client_request,
    receive_frame, send_frame, service_environment, sha256, shim_main,
)

GIT = str(Path(shutil.which('git')).resolve())
MODULE_ROOT = Path(__file__).resolve().parent


class WorkerFixture:
    """Context fixture for combining this actual signed head with push probes."""
    def __init__(self, root):
        self.root = Path(root)
        self.worker = self.root / 'worker'
        self.worker.mkdir(parents=True)
        self.home = self.root / 'worker-home'
        self.home.mkdir()
        self.service_root = self.root / 'service'
        self.secret_root = self.service_root / 'secrets'
        self.secret_root.mkdir(parents=True)
        self.key = self.secret_root / 'disposable-key'
        subprocess.run([SSH_KEYGEN, '-q', '-t', 'ed25519', '-N', '', '-C', 'synthetic-signing-spike',
                        '-f', str(self.key)], check=True, env=service_environment(self.home), cwd=self.home)
        self.public_key = self.key.with_suffix('.pub').read_text().strip()
        self.signer = Signer(self.service_root, self.key)
        self.job = SigningJob('synthetic-001', 'example-org/synthetic-repository',
                              secrets.token_hex(32), IDENTITY, time.time() + 300)
        self.signer.register(self.job)
        self.client, self.server = socket.socketpair()
        self.client.settimeout(5)
        self.thread = threading.Thread(target=self.signer.serve, args=(self.server,), daemon=True)
        self.thread.start()
        self.environment = service_environment(self.home)
        self.environment.update({
            'GIT_AUTHOR_NAME': 'Synthetic Developer', 'GIT_AUTHOR_EMAIL': 'developer@example.invalid',
            'GIT_COMMITTER_NAME': 'Synthetic Developer', 'GIT_COMMITTER_EMAIL': 'developer@example.invalid',
            'GIT_AUTHOR_DATE': '1700000000 +0000', 'GIT_COMMITTER_DATE': '1700000000 +0000',
            'GIT_TERMINAL_PROMPT': '0', 'GIT_TEMPLATE_DIR': str(self.root / 'empty-template'),
        })
        (self.root / 'empty-template').mkdir()
        self.git('init', '-b', 'main', '--template=' + str(self.root / 'empty-template'))
        for key, value in (('user.name', 'Synthetic Developer'), ('user.email', 'developer@example.invalid'),
                           ('core.hooksPath', '/dev/null')):
            self.git('config', '--local', key, value)
        (self.worker / 'baseline.txt').write_text('Synthetic baseline\n')
        self.git('add', '--', 'baseline.txt')
        self.git('commit', '--no-gpg-sign', '-m', 'Synthetic baseline')
        self.base = self.head
        self.git('switch', '-c', 'synthetic/ticket-001')
        self.temporary_root = self.worker / '.git' / 'sign-tmp'
        self.temporary_root.mkdir()
        self.client_data = {'job_id': self.job.job_id, 'repository': self.job.repository,
                            'capability': self.job.capability, 'public_key': self.public_key,
                            'temporary_root': str(self.temporary_root)}
        client_file = self.worker / '.git' / 'signer-client.json'
        client_file.write_text(json.dumps(self.client_data))
        self.allowed = self.worker / '.git' / 'allowed-signers'
        self.allowed.write_text('developer@example.invalid ' + self.public_key + '\n')
        self.shim = self.root / 'trusted-shim'
        self.shim.write_text('#!' + sys.executable + '\nimport json, os, sys\nfrom pathlib import Path\n'
                             'sys.path.insert(0, ' + repr(str(MODULE_ROOT)) + ')\n'
                             'from worker_signer import shim_main\n'
                             'try:\n    shim_main(sys.argv[1:], json.loads(Path(os.environ["SDLC_SIGN_CLIENT"]).read_text()))\n'
                             'except Exception as error:\n    print(str(error), file=sys.stderr)\n    sys.exit(1)\n')
        self.shim.chmod(0o700)
        self.environment.update({'TMPDIR': str(self.temporary_root), 'SDLC_SIGN_CLIENT': str(client_file),
                                 'SDLC_SIGN_FD': str(self.client.fileno())})
        for key, value in (('gpg.format', 'ssh'), ('gpg.ssh.program', str(self.shim)),
                           ('user.signingKey', 'key::' + self.public_key)):
            self.git('config', '--local', key, value)

    def git(self, *arguments, check=True, extra=None):
        env = dict(self.environment)
        if extra:
            env.update(extra)
        return subprocess.run([GIT, *arguments], cwd=self.worker, env=env,
                              pass_fds=(self.client.fileno(),), stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE, check=check, timeout=10)

    @property
    def head(self):
        return self.git('rev-parse', 'HEAD').stdout.decode().strip()

    def stage(self, content=b'Synthetic increment\n', filename='change.txt'):
        (self.worker / filename).write_bytes(content)
        self.git('add', '--', filename)
        return self.git('write-tree').stdout.decode().strip()

    def approve(self, message='Synthetic increment', tree=None, parent=None):
        tree = tree or self.git('write-tree').stdout.decode().strip()
        parent = parent or self.head
        return self.signer.authorize(self.job.job_id, tree, parent, message.encode() + b'\n')

    def commit(self, message='Synthetic increment', check=True, extra=None):
        return self.git('commit', '-S', '-m', message, check=check, extra=extra)

    def verify(self, oid=None):
        oid = oid or self.head
        self.git('-c', 'gpg.ssh.program=' + SSH_KEYGEN,
                 '-c', 'gpg.ssh.allowedSignersFile=' + str(self.allowed), 'verify-commit', oid)
        signed = self.git('cat-file', 'commit', oid).stdout
        self.asserted_oid = hashlib.sha1(f'commit {len(signed)}\0'.encode() + signed).hexdigest()
        if self.asserted_oid != oid:
            raise AssertionError('Worker head differs from actual signed Git commit object')
        return signed

    def request(self, raw_payload, **changes):
        request = {'version': 1, 'job_id': self.job.job_id, 'repository': self.job.repository,
                   'capability': self.job.capability, 'payload': base64.b64encode(raw_payload).decode()}
        request.update(changes)
        send_frame(self.client, json.dumps(request).encode())
        return json.loads(receive_frame(self.client))

    def close(self):
        self.client.close()
        try:
            self.server.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        self.thread.join(timeout=2)
        self.server.close()

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.close()


class WorkerSigningTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='sdlc-worker-signing-')
        self.addCleanup(self.temp.cleanup)
        self.fixture = WorkerFixture(Path(self.temp.name))
        self.addCleanup(self.fixture.close)

    def test_actual_git_commit_sign_retains_signed_object_and_no_worker_private_key(self):
        f = self.fixture
        tree = f.stage()
        f.approve(tree=tree)
        f.commit()
        signed = f.verify()
        self.assertEqual(f.git('rev-parse', 'HEAD^{tree}').stdout.decode().strip(), tree)
        self.assertIn(b'gpgsig -----BEGIN SSH SIGNATURE-----', signed)
        private_bytes = f.key.read_bytes()  # Disposable fixture key only.
        for path in f.worker.rglob('*'):
            if path.is_file():
                self.assertNotIn(private_bytes, path.read_bytes())
                self.assertNotIn(str(f.key).encode(), path.read_bytes())
        self.assertNotIn(str(f.key), json.dumps(f.environment))
        self.assertEqual(len(f.signer.signing_environments), 1)

    def test_incremental_stack_uses_worker_created_signed_parent_without_rebuild(self):
        f = self.fixture
        parent = f.base
        for index in range(3):
            tree = f.stage(f'Synthetic increment {index}\n'.encode(), f'change-{index}.txt')
            message = f'Synthetic ticket increment {index + 1}'
            f.approve(message, tree, parent)
            f.commit(message)
            head = f.head
            f.verify(head)
            self.assertEqual(f.git('rev-parse', 'HEAD^').stdout.decode().strip(), parent)
            self.assertEqual(f.git('rev-parse', 'HEAD^{tree}').stdout.decode().strip(), tree)
            parent = head
        self.assertEqual(len(f.signer.signing_environments), 3)

    def test_new_increment_requires_new_authorisation_then_same_capability_succeeds(self):
        f = self.fixture
        f.stage()
        f.approve()
        f.commit()
        first = f.head
        f.stage(b'Next synthetic change\n', 'next.txt')
        failed = f.commit('Next synthetic increment', check=False)
        self.assertNotEqual(failed.returncode, 0)
        self.assertEqual(f.head, first)
        f.approve('Next synthetic increment')
        f.commit('Next synthetic increment')
        f.verify()
        self.assertEqual(len(f.signer.signing_environments), 2)

    def test_strict_job_capability_and_repository_scope(self):
        f = self.fixture
        payload = f.approve(tree=f.stage())
        for changes in ({'capability': 'wrong' * 16}, {'job_id': 'synthetic-other'},
                        {'repository': 'example-org/wrong-repository'}, {'command': 'arbitrary'}):
            with self.subTest(changes=changes):
                self.assertFalse(f.request(payload, **changes)['ok'])
        self.assertEqual(f.signer.signing_environments, [])

    def test_tree_parent_message_identity_and_timestamp_binding(self):
        f = self.fixture
        payload = f.approve(tree=f.stage())
        cases = (payload.replace(b'tree ', b'tree ' + b'0', 1),
                 payload.replace(f.base.encode(), b'0' * 40),
                 payload.replace(b'Synthetic increment\n', b'Unreviewed increment\n'),
                 payload.replace(b'Synthetic Developer', b'Other Developer'),
                 payload.replace(b'1700000000', b'1700000001'))
        for changed in cases:
            self.assertFalse(f.request(changed)['ok'])
        self.assertEqual(f.signer.signing_environments, [])

    def test_actual_git_wrong_identity_is_rejected(self):
        f = self.fixture
        f.approve(tree=f.stage())
        failed = f.commit(check=False, extra={'GIT_AUTHOR_NAME': 'Unapproved Author'})
        self.assertNotEqual(failed.returncode, 0)
        self.assertEqual(f.head, f.base)
        self.assertEqual(f.signer.signing_environments, [])

    def test_malformed_non_git_extra_headers_merge_and_oversized_requests(self):
        f = self.fixture
        payload = f.approve(tree=f.stage())
        for malformed in (b'not json', b'[]', b'{}'):
            send_frame(f.client, malformed)
            self.assertFalse(json.loads(receive_frame(f.client))['ok'])
        for changed in (b'not a Git commit', payload.replace(b'\n\n', b'\ngpgsig injected\n\n'),
                        payload.replace(b'\nauthor ', b'\nparent ' + b'0' * 40 + b'\nauthor '),
                        b'x' * 17000):
            self.assertFalse(f.request(changed)['ok'])
        self.assertFalse(f.request(payload, payload='not-base64')['ok'])
        self.assertEqual(f.signer.signing_environments, [])

    def test_expiry_and_revocation_also_reject_previously_signed_replay(self):
        f = self.fixture
        payload = f.approve(tree=f.stage())
        self.assertTrue(f.request(payload)['ok'])
        f.job.expires_at = time.time() - 1
        self.assertFalse(f.request(payload)['ok'])
        f.job.expires_at = time.time() + 300
        f.signer.revoke(f.job.job_id)
        self.assertFalse(f.request(payload)['ok'])
        failed = f.commit(check=False)
        self.assertNotEqual(failed.returncode, 0)
        self.assertEqual(f.head, f.base)
        self.assertEqual(len(f.signer.signing_environments), 1)

    def test_replay_is_same_signature_and_responses_contain_no_key_or_path(self):
        f = self.fixture
        payload = f.approve(tree=f.stage())
        first, second = f.request(payload), f.request(payload)
        self.assertEqual(first, second)
        self.assertEqual(set(first), {'version', 'ok', 'signature'})
        encoded = json.dumps(first).encode()
        self.assertNotIn(f.key.read_bytes(), encoded)
        self.assertNotIn(str(f.key).encode(), encoded)
        self.assertEqual(len(f.signer.signing_environments), 1)

    def test_already_emitted_public_signature_survives_capability_expiry_and_revocation(self):
        f = self.fixture
        f.approve(tree=f.stage())
        f.commit()
        head = f.head
        f.job.expires_at = time.time() - 1
        f.signer.revoke(f.job.job_id)
        f.verify(head)
        self.assertEqual(f.head, head)
        self.assertEqual(len(f.signer.signing_environments), 1)

    def test_hostile_global_environment_and_worker_cwd_do_not_reach_signer(self):
        f = self.fixture
        payload = f.approve(tree=f.stage())
        marker = f.root / 'ambient-attack'
        script = f.root / 'ambient-command'
        script.write_text('#!/bin/sh\ntouch ' + str(marker) + '\nexit 1\n')
        script.chmod(0o700)
        config = f.root / 'ambient-config'
        config.write_text('[core]\nfsmonitor = ' + str(script) + '\n[gpg "ssh"]\nprogram = ' + str(script) + '\n')
        with mock.patch.dict(os.environ, {'HOME': str(f.worker), 'SSH_AUTH_SOCK': '/untrusted-agent',
                                         'GIT_CONFIG_GLOBAL': str(config), 'GIT_CONFIG_COUNT': '1',
                                         'GIT_CONFIG_KEY_0': 'gpg.ssh.program',
                                         'GIT_CONFIG_VALUE_0': str(script), 'LD_PRELOAD': '/not-a-library'}):
            self.assertTrue(f.request(payload)['ok'])
        self.assertFalse(marker.exists())
        env = f.signer.signing_environments[-1]
        self.assertEqual(env['HOME'], str(f.signer.home))
        self.assertNotIn('SSH_AUTH_SOCK', env)
        self.assertNotIn('GIT_CONFIG_COUNT', env)
        self.assertNotIn('LD_PRELOAD', env)

    def test_worker_hook_tree_mutation_cannot_expand_signing_approval(self):
        f = self.fixture
        f.approve(tree=f.stage())
        hooks = f.worker / '.git' / 'hostile-hooks'
        hooks.mkdir()
        hook = hooks / 'pre-commit'
        hook.write_text('#!/bin/sh\nprintf "Unapproved hook change\\n" > hook-added.txt\ngit add -- hook-added.txt\n')
        hook.chmod(0o700)
        f.git('config', '--local', 'core.hooksPath', str(hooks))
        result = f.commit(check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue((f.worker / 'hook-added.txt').exists())
        self.assertEqual(f.head, f.base)
        self.assertEqual(f.signer.signing_environments, [])

    def test_shim_rejects_wrong_mode_key_selector_and_external_symbolic_paths(self):
        f = self.fixture
        payload = f.approve(tree=f.stage())
        selector = f.temporary_root / 'selector'
        source = f.temporary_root / 'payload'
        selector.write_text(f.public_key + '\n')
        source.write_bytes(payload)
        normal = ['-Y', 'sign', '-n', 'git', '-f', str(selector), str(source)]
        for args in (['-Y', 'verify', '-n', 'git', '-f', str(selector), str(source)],
                     ['-Y', 'sign', '-n', 'file', '-f', str(selector), str(source)],
                     normal[:-1] + [str(f.root / 'outside')]):
            with self.assertRaises(Rejected):
                shim_main(args, f.client_data)
        source.unlink()
        source.symlink_to(f.key)
        with self.assertRaises(OSError):
            shim_main(normal, f.client_data)
        source.unlink()
        source.write_bytes(payload)
        selector.write_text('ssh-ed25519 unapproved-public-key\n')
        with self.assertRaises(Rejected):
            shim_main(normal, f.client_data)
        self.assertEqual(f.signer.signing_environments, [])

    def test_preplanted_signature_output_link_is_not_followed(self):
        f = self.fixture
        payload = f.approve(tree=f.stage())
        selector, source = f.temporary_root / 'selector', f.temporary_root / 'payload'
        selector.write_text(f.public_key + '\n')
        source.write_bytes(payload)
        outside = f.root / 'outside-output'
        outside.write_bytes(b'Unchanged synthetic marker\n')
        source.with_suffix('.sig').symlink_to(outside)
        with mock.patch.dict(os.environ, {'SDLC_SIGN_FD': str(f.client.fileno())}):
            with self.assertRaises(OSError):
                shim_main(['-Y', 'sign', '-n', 'git', '-f', str(selector), str(source)], f.client_data)
        self.assertEqual(outside.read_bytes(), b'Unchanged synthetic marker\n')

    def test_oversized_and_incomplete_protocol_frames_close_without_signing(self):
        f = self.fixture
        for frame in (struct.pack('!I', MAX_FRAME + 1), struct.pack('!I', 100) + b'{'):
            with self.subTest(frame_length=len(frame)):
                client, server = socket.socketpair()
                client.settimeout(4)
                thread = threading.Thread(target=f.signer.serve, args=(server,), daemon=True)
                thread.start()
                client.sendall(frame)
                thread.join(timeout=3)
                self.assertFalse(thread.is_alive())
                client.close()
                server.close()
        self.assertEqual(f.signer.signing_environments, [])

    def test_same_uid_worker_can_read_and_write_external_disposable_key(self):
        f = self.fixture
        # This intentionally proves the absence of OS isolation. It exports no
        # key bytes and writes the first byte back unchanged to the same file.
        program = ('from pathlib import Path; import sys; '
                   'source=Path(sys.argv[1]); stream=source.open("r+b"); '
                   'first=stream.read(1); stream.seek(0); stream.write(first); stream.close(); '
                   'print(bool(first))')
        result = subprocess.run([sys.executable, '-c', program, str(f.key)], cwd=f.worker,
                                env=f.environment, check=True, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, timeout=5)
        self.assertEqual(result.stdout.strip(), b'True')

    def test_slow_protocol_frame_has_total_deadline(self):
        client, server = socket.socketpair()
        result = []

        def receive():
            try:
                receive_frame(server, active_timeout=0.15)
                result.append('accepted')
            except TimeoutError:
                result.append('timeout')

        thread = threading.Thread(target=receive, daemon=True)
        thread.start()
        start = time.monotonic()
        client.sendall(struct.pack('!I', 5))
        for _ in range(4):
            time.sleep(0.06)
            client.sendall(b'x')
        thread.join(timeout=1)
        self.assertFalse(thread.is_alive())
        self.assertEqual(result, ['timeout'])
        self.assertLess(time.monotonic() - start, 0.8)
        client.close()
        server.close()

    def test_signer_failure_is_sanitised_and_next_approved_request_still_works(self):
        f = self.fixture
        payload = f.approve(tree=f.stage())
        with mock.patch.object(f.signer, '_sign', side_effect=subprocess.CalledProcessError(
                1, ['fixed-signer', 'synthetic-private-key-path'])):
            result = f.request(payload)
            self.assertEqual(result, {'version': 1, 'ok': False, 'error': 'Signing request rejected'})
        self.assertTrue(f.request(payload)['ok'])


if __name__ == '__main__':
    unittest.main(verbosity=2)
