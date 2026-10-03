"""Worker creates broker-signed Git objects and pushes those same objects."""
from pathlib import Path
import tempfile
import unittest

from test_worker_signer import WorkerFixture
from push_spike import Provider, git, worker_push


def exercise_worker_stack():
    with tempfile.TemporaryDirectory(prefix='synthetic-worker-stack-') as directory:
        root = Path(directory)
        with WorkerFixture(root / 'signing') as worker:
            provider = Provider(root / 'provider')
            helpers = root / 'helpers'
            provider.write_worker_helper(helpers)
            first_branch = 'refs/heads/job/ticket-a'
            first_capability = provider.register('ticket-a', 'target.git', first_branch,
                                                 worker.public_key)
            provider.register('other-job', 'other.git', 'refs/heads/job/other', worker.public_key)
            target = provider.root / 'repos' / 'target.git'
            # Trusted fixture seeds fresh main before delegating worker access.
            git(provider.home, worker.worker / '.git', 'push', str(target),
                worker.base + ':refs/heads/main')
            parent = worker.base
            increments = []
            for index in range(2):
                message = f'Synthetic first ticket increment {index + 1}'
                tree = worker.stage(f'Synthetic first ticket {index}\n'.encode(), f'first-{index}.txt')
                worker.approve(message, tree, parent)
                worker.commit(message)
                head = worker.head
                worker.verify(head)
                increments.append({'head': head, 'parent': parent, 'tree': tree})
                parent = head
            first_head = parent
            first_token = provider.renew({'job': 'ticket-a', 'capability': first_capability})
            result = worker_push(worker.worker, 'target.git', first_branch, first_token, helpers)
            if result.returncode:
                raise AssertionError(result.stderr.decode())
            first_remote = git(provider.home, target, 'rev-parse', first_branch).stdout.decode().strip()
            if first_remote != first_head:
                raise AssertionError('First pushed object ID changed')

            worker.git('switch', '-c', 'synthetic/ticket-002', first_head)
            second_branch = 'refs/heads/job/ticket-b'
            second_capability = provider.register('ticket-b', 'target.git', second_branch,
                                                  worker.public_key)
            message = 'Synthetic second ticket increment'
            tree = worker.stage(b'Synthetic dependent ticket\n', 'second.txt')
            worker.approve(message, tree, first_head)
            worker.commit(message)
            second_head = worker.head
            worker.verify(second_head)
            second_token = provider.renew({'job': 'ticket-b', 'capability': second_capability})
            result = worker_push(worker.worker, 'target.git', second_branch, second_token, helpers)
            if result.returncode:
                raise AssertionError(result.stderr.decode())
            second_remote = git(provider.home, target, 'rev-parse', second_branch).stdout.decode().strip()
            second_parent = git(provider.home, target, 'rev-parse', second_remote + '^').stdout.decode().strip()
            if second_remote != second_head or second_parent != first_head:
                raise AssertionError('Second pushed object or its signed parent changed')
            unchanged_first = git(provider.home, target, 'rev-parse', first_branch).stdout.decode().strip()
            unchanged_main = git(provider.home, target, 'rev-parse', 'refs/heads/main').stdout.decode().strip()
            if unchanged_first != first_head or unchanged_main != worker.base:
                raise AssertionError('Stack publication altered an earlier branch')
            # Changing the client URL still cannot extend provider-issued scope.
            rejected = worker_push(worker.worker, 'other.git', 'refs/heads/job/other',
                                   second_token, helpers)
            if not rejected.returncode:
                raise AssertionError('Provider accepted second repository')
            for oid in [entry['head'] for entry in increments] + [second_head]:
                git(provider.home, target, '-c', 'gpg.format=ssh',
                    '-c', 'gpg.ssh.program=/usr/bin/ssh-keygen',
                    '-c', 'gpg.ssh.allowedSignersFile=' + str(worker.allowed), 'verify-commit', oid)
            increments.append({'head': second_head, 'parent': first_head, 'tree': tree})
            return {'schema': 1, 'signed_in_worker': 3, 'tickets': 2,
                    'same_oid_after_push': True, 'second_repo_rejected': True,
                    'main_unchanged': True, 'increments': increments}


class DirectWorkerStackTests(unittest.TestCase):
    def test_broker_signed_worker_objects_push_unchanged_as_two_ticket_stack(self):
        proof = exercise_worker_stack()
        self.assertEqual(proof['signed_in_worker'], 3)
        self.assertEqual(proof['tickets'], 2)
        self.assertEqual(proof['increments'][2]['parent'], proof['increments'][1]['head'])


if __name__ == '__main__':
    unittest.main(verbosity=2)
