"""Synthetic local acceptance probes; no host credentials or network calls."""
import base64
from dataclasses import replace
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

from publisher_spike import (
    GIT, SSH_KEYGEN, Job, Policy, Publisher, Rejected, candidate_from_worker,
    canonical, clean_environment, digest, git, text_git,
)

AUTHOR = 'Synthetic Developer <developer@example.invalid>'


class PublicationSpikeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='sdlc-publisher-probe-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.home = self.root / 'fixture-home'
        (self.home / 'empty-template').mkdir(parents=True)
        self.spool = self.root / 'spool'
        self.spool.mkdir()
        self.remote = self.root / 'approved.git'
        self.seed = self.root / 'seed.git'
        self.worker = self.root / 'worker.git'
        for repo in (self.remote, self.seed, self.worker):
            git(self.home, None, 'init', '--bare', '--template=' + str(self.home / 'empty-template'), str(repo))
        self.key = self.root / 'disposable-signing-key'
        subprocess.run([SSH_KEYGEN, '-q', '-t', 'ed25519', '-N', '', '-C', 'disposable-spike',
                        '-f', str(self.key)], check=True, env=clean_environment(self.home))
        self.allowed = self.root / 'allowed-signers'
        self.allowed.write_text('developer@example.invalid ' + self.key.with_suffix('.pub').read_text())
        self.policy = Policy('example-org/synthetic-repository', self.remote, 'main', AUTHOR)
        self.publisher = Publisher(self.root / 'helper', self.spool, self.policy, self.key)
        self.base = self.commit(self.seed, {}, 'Synthetic baseline\n', None)
        git(self.home, self.seed, 'push', '--', str(self.remote), self.base + ':refs/heads/main')
        git(self.home, self.worker, 'fetch', '--', str(self.remote), self.base)
        git(self.home, self.worker, 'update-ref', 'HEAD', self.base)

    def commit(self, repo, changes, message, parent):
        files = {}
        if parent:
            for line in text_git(self.home, repo, 'ls-tree', parent).splitlines():
                metadata, filename = line.split('\t', 1)
                mode, kind, oid = metadata.split()
                files[filename] = (mode, kind, oid)
        for name, content in changes.items():
            mode, payload = content if isinstance(content, tuple) else ('100644', content)
            oid = text_git(self.home, repo, 'hash-object', '-w', '--stdin', data=payload)
            files[name] = (mode, 'blob', oid)
        tree_bytes = ''.join(f'{mode} {kind} {oid}\t{name}\n'
                             for name, (mode, kind, oid) in sorted(files.items())).encode()
        tree = text_git(self.home, repo, 'mktree', data=tree_bytes)
        env = {'GIT_AUTHOR_NAME': 'Synthetic Developer', 'GIT_AUTHOR_EMAIL': 'developer@example.invalid',
               'GIT_AUTHOR_DATE': '2026-09-30T12:00:00+0000',
               'GIT_COMMITTER_NAME': 'Synthetic Developer', 'GIT_COMMITTER_EMAIL': 'developer@example.invalid',
               'GIT_COMMITTER_DATE': '2026-09-30T12:00:00+0000'}
        arguments = ('commit-tree', tree, '-p', parent) if parent else ('commit-tree', tree)
        oid = text_git(self.home, repo, *arguments, data=message.encode(), extra=env)
        git(self.home, repo, 'update-ref', 'HEAD', oid)
        return oid

    def make_job(self, job_id='synthetic-001', base=None, branch='synthetic/ticket-001',
                 base_branch='main', parent_job_id=None, changes=None, increments=1):
        base = base or self.base
        git(self.home, self.worker, 'update-ref', 'HEAD', base)
        for index in range(increments):
            head = text_git(self.home, self.worker, 'rev-parse', 'HEAD')
            self.commit(self.worker, changes or {f'change-{index}.txt': b'Synthetic change\n'},
                        f'{job_id} increment {index + 1}\n', head)
        raw, trees = candidate_from_worker(self.home, self.worker, base)
        filename = job_id + '.json'
        (self.spool / filename).write_bytes(raw)
        job = Job(job_id, self.policy.repository, base_branch, base, branch, AUTHOR,
                  filename, digest(raw), trees, parent_job_id=parent_job_id)
        self.publisher.admit(job)
        return job

    def publish(self, job):
        return self.publisher.publish(self.publisher.request_for(job.job_id))

    def verify(self, job, receipt):
        repo = self.publisher.root / f'{job.job_id}.git'
        parent = job.base_oid
        for signed in receipt['signed_commits']:
            self.assertNotEqual(signed['candidate'], signed['signed'])
            self.assertEqual(text_git(self.home, repo, 'rev-parse', signed['signed'] + '^{tree}'), signed['tree'])
            self.assertEqual(text_git(self.home, repo, 'rev-parse', signed['signed'] + '^'), parent)
            git(self.home, repo, '-c', 'gpg.format=ssh', '-c', 'gpg.ssh.program=' + SSH_KEYGEN,
                '-c', 'gpg.ssh.allowedSignersFile=' + str(self.allowed), 'verify-commit', signed['signed'])
            parent = signed['signed']
        self.assertEqual(receipt['signed_head'], parent)
        self.assertEqual(receipt['pr']['transport'], 'deterministic-mock')
        self.assertTrue(receipt['pr']['draft'])

    def test_signed_increment_preserves_every_tree(self):
        job = self.make_job(increments=2)
        receipt = self.publish(job)
        self.verify(job, receipt)
        self.assertEqual(len(receipt['signed_commits']), 2)
        self.assertEqual(text_git(self.home, self.remote, 'rev-parse', 'refs/heads/' + job.head_branch),
                         receipt['signed_head'])

    def test_stack_uses_canonical_signed_parent_and_preserves_trees(self):
        first = self.make_job(increments=2)
        first_result = self.publish(first)
        canonical_parent = first_result['signed_head']
        git(self.home, self.worker, 'fetch', '--', str(self.remote), canonical_parent)
        second = self.make_job('synthetic-002', canonical_parent, 'synthetic/ticket-002',
                               first.head_branch, first.job_id,
                               changes={'second-ticket.txt': b'Next synthetic increment\n'}, increments=2)
        result = self.publish(second)
        self.verify(second, result)
        self.assertEqual(result['base_oid'], canonical_parent)
        self.assertNotEqual(canonical_parent, first_result['signed_commits'][-1]['candidate'])
        self.assertEqual(result['base_branch'], first.head_branch)

    def test_unsigned_worker_sha_cannot_be_used_as_stack_parent(self):
        first = self.make_job()
        self.publish(first)
        unsigned = text_git(self.home, self.worker, 'rev-parse', 'HEAD')
        raw, trees = candidate_from_worker(self.home, self.worker, self.base)
        wrong = Job('synthetic-002', self.policy.repository, first.head_branch, unsigned,
                    'synthetic/ticket-002', AUTHOR, first.candidate_name, digest(raw), trees,
                    parent_job_id=first.job_id)
        with self.assertRaisesRegex(Rejected, 'canonical signed parent'):
            self.publisher.admit(wrong)

    def test_job_fixed_request_fields_and_force_capability_are_rejected(self):
        job = self.make_job()
        original = self.publisher.request_for(job.job_id)
        for field, value in (
                ('repository', 'example-org/wrong-repository'), ('base_branch', 'wrong-base'),
                ('head_branch', 'synthetic/wrong-head'), ('author', 'Other <other@example.invalid>'),
                ('candidate_sha256', '0' * 64), ('force', True), ('command', 'echo forbidden'),
                ('sign_payload', 'arbitrary bytes')):
            with self.subTest(field=field), self.assertRaisesRegex(Rejected, 'fixed publication job'):
                self.publisher.publish({**original, field: value})
        self.assertFalse(any('commit-tree' in call for call in self.publisher.calls))
        self.assertFalse(any(call and call[0] == 'push' for call in self.publisher.calls))

    def test_changed_tree_after_review_rejected_before_signing(self):
        job = self.make_job()
        self.commit(self.worker, {'changed-after-review.txt': b'Unreviewed content\n'},
                    'Unreviewed change\n', text_git(self.home, self.worker, 'rev-parse', 'HEAD'))
        changed, _ = candidate_from_worker(self.home, self.worker, self.base)
        (self.spool / job.candidate_name).write_bytes(changed)
        with self.assertRaisesRegex(Rejected, 'changed after review'):
            self.publish(job)
        self.assertFalse(any('commit-tree' in call for call in self.publisher.calls))

    def test_reviewed_tree_pin_rejects_wrong_tree_even_with_matching_digest(self):
        job = self.make_job()
        with self.assertRaisesRegex(Rejected, 'reviewed trees'):
            self.publisher.admit(replace(job, job_id='synthetic-003', reviewed_trees=('0' * 40,)))

    def test_wrong_candidate_parent_and_wrong_author_rejected_at_review(self):
        for kind in ('base', 'author'):
            with self.subTest(kind=kind):
                job = self.make_job(job_id='synthetic-' + kind)
                raw = json.loads((self.spool / job.candidate_name).read_bytes())
                if kind == 'base':
                    raw['base'] = 'f' * 40
                else:
                    commit = raw['commits'][0]
                    obj = raw['objects'].pop(commit)
                    payload = base64.b64decode(obj['data']).replace(b'Synthetic Developer', b'Hostile Developer')
                    from publisher_spike import object_oid
                    changed_oid = object_oid('commit', payload)
                    obj['data'] = base64.b64encode(payload).decode()
                    raw['objects'][changed_oid] = obj
                    raw['commits'][0] = changed_oid
                    raw['head'] = changed_oid
                changed = canonical(raw)
                (self.spool / job.candidate_name).write_bytes(changed)
                with self.assertRaises(Rejected):
                    self.publisher.admit(replace(job, job_id='check-' + kind, candidate_sha256=digest(changed)))

    def test_symbolic_external_and_traversal_candidate_paths_rejected(self):
        job = self.make_job()
        from publisher_spike import read_candidate
        source = self.spool / job.candidate_name
        symlink = self.spool / 'symbolic.json'
        symlink.symlink_to(source)
        for filename in ('../outside.json', '/tmp/outside.json', 'directory/candidate.json', 'symbolic.json'):
            with self.subTest(filename=filename), self.assertRaises((Rejected, OSError)):
                read_candidate(self.spool, filename)
        source.unlink()
        source.symlink_to(symlink)
        with self.assertRaises(OSError):
            self.publish(job)

    def test_candidate_hash_and_object_hash_tampering_rejected(self):
        job = self.make_job()
        manifest = json.loads((self.spool / job.candidate_name).read_bytes())
        object_entry = next(iter(manifest['objects'].values()))
        object_entry['data'] = base64.b64encode(b'Tampered object\n').decode()
        changed = canonical(manifest)
        (self.spool / job.candidate_name).write_bytes(changed)
        with self.assertRaisesRegex(Rejected, 'Object hash'):
            self.publisher.admit(replace(job, job_id='synthetic-003', candidate_sha256=digest(changed)))

    def test_hardlinked_directory_fifo_and_symbolic_spool_inputs_rejected(self):
        job = self.make_job()
        from publisher_spike import read_candidate
        source = self.spool / job.candidate_name
        linked = self.spool / 'hardlink.json'
        os.link(source, linked)
        with self.assertRaisesRegex(Rejected, 'regular file'):
            read_candidate(self.spool, linked.name)
        linked.unlink()
        (self.spool / 'directory.json').mkdir()
        os.mkfifo(self.spool / 'fifo.json')
        for filename in ('directory.json', 'fifo.json'):
            with self.subTest(filename=filename), self.assertRaises(Rejected):
                read_candidate(self.spool, filename)
        alias = self.root / 'symbolic-spool'
        alias.symlink_to(self.spool)
        with self.assertRaises(OSError):
            read_candidate(alias, job.candidate_name)

    def test_helper_verifies_signature_before_pushing(self):
        job = self.make_job(increments=2)
        self.publish(job)
        positions = [i for i, call in enumerate(self.publisher.calls) if 'verify-commit' in call]
        push = next(i for i, call in enumerate(self.publisher.calls) if call[0] == 'push')
        self.assertEqual(len(positions), 2)
        self.assertTrue(all(position < push for position in positions))

    def test_worker_config_hooks_filters_and_project_code_are_not_executed(self):
        marker = self.root / 'executed-worker-code'
        attack = self.root / 'worker-command'
        attack.write_text('#!/bin/sh\ntouch ' + str(marker) + '\nexit 1\n')
        attack.chmod(0o700)
        job = self.make_job(changes={
            '.gitattributes': b'* filter=hostile diff=hostile\n',
            'project.sh': ('100755', b'#!/bin/sh\nexit 99\n'),
            'symbolic-data': ('120000', b'../../outside-candidate-tree\n'),
        })
        for key, value in (
                ('core.hooksPath', str(self.worker / 'evil-hooks')), ('core.fsmonitor', str(attack)),
                ('gpg.ssh.program', str(attack)), ('gpg.program', str(attack)),
                ('user.signingKey', 'untrusted-key'), ('filter.hostile.clean', str(attack)),
                ('filter.hostile.smudge', str(attack)), ('filter.hostile.required', 'true'),
                ('diff.external', str(attack)), ('diff.hostile.command', str(attack)),
                ('credential.helper', str(attack)), ('core.sshCommand', str(attack)),
                ('url.ext::hostile.insteadOf', str(self.remote))):
            text_git(self.home, self.worker, 'config', '--local', key, value)
        hooks = self.worker / 'evil-hooks'
        hooks.mkdir()
        for hook in ('pre-commit', 'post-commit', 'pre-push'):
            (hooks / hook).symlink_to(attack)
        result = self.publish(job)
        self.verify(job, result)
        self.assertFalse(marker.exists())
        self.assertFalse(any(str(self.worker) in argument for call in self.publisher.calls
                             for argument in call))
        helper_repo = self.publisher.root / f'{job.job_id}.git'
        self.assertFalse((helper_repo / 'project.sh').exists())
        self.assertEqual(text_git(self.home, helper_repo, 'show', result['signed_head'] + ':symbolic-data'),
                         '../../outside-candidate-tree')

    def test_ambient_git_config_template_and_ssh_agent_are_discarded(self):
        job = self.make_job()
        marker = self.root / 'ambient-attack-executed'
        attack = self.root / 'ambient-command'
        attack.write_text('#!/bin/sh\ntouch ' + str(marker) + '\nexit 1\n')
        attack.chmod(0o700)
        config = self.root / 'ambient-config'
        config.write_text('[core]\nfsmonitor = ' + str(attack) + '\n[credential]\nhelper = ' + str(attack) + '\n')
        template = self.root / 'hostile-template'
        (template / 'hooks').mkdir(parents=True)
        (template / 'hooks' / 'pre-commit').symlink_to(attack)
        with mock.patch.dict(os.environ, {
                'HOME': str(self.root / 'ambient-home'), 'GIT_CONFIG_GLOBAL': str(config),
                'GIT_CONFIG_SYSTEM': str(config), 'GIT_CONFIG_COUNT': '1',
                'GIT_CONFIG_KEY_0': 'gpg.ssh.program', 'GIT_CONFIG_VALUE_0': str(attack),
                'GIT_TEMPLATE_DIR': str(template), 'GIT_DIR': str(self.worker),
                'GIT_WORK_TREE': str(self.root), 'SSH_AUTH_SOCK': '/not-an-agent',
                'GH_TOKEN': 'fake-not-a-real-token', 'LD_PRELOAD': '/not-a-library'}):
            result = self.publish(job)
        self.verify(job, result)
        self.assertFalse(marker.exists())
        helper_repo = self.publisher.root / f'{job.job_id}.git'
        self.assertFalse((helper_repo / 'hooks' / 'pre-commit').exists())
        env = clean_environment(self.publisher.home)
        self.assertNotIn('SSH_AUTH_SOCK', env)
        self.assertNotIn('GH_TOKEN', env)
        self.assertNotIn('GIT_CONFIG_COUNT', env)

    def test_remote_receive_hooks_are_disabled_for_local_mock_transport(self):
        job = self.make_job()
        marker = self.root / 'remote-hook-executed'
        hooks = self.remote / 'hostile-hooks'
        hooks.mkdir()
        for hook in ('pre-receive', 'update', 'post-receive', 'post-update'):
            program = hooks / hook
            program.write_text('#!/bin/sh\ntouch ' + str(marker) + '\nexit 1\n')
            program.chmod(0o700)
        text_git(self.home, self.remote, 'config', '--local', 'core.hooksPath', str(hooks))
        result = self.publish(job)
        self.verify(job, result)
        self.assertFalse(marker.exists())

    def test_non_fast_forward_is_rejected_without_force_push(self):
        job = self.make_job()
        unrelated = self.commit(self.seed, {'other.txt': b'Independent remote change\n'}, 'Remote change\n', self.base)
        git(self.home, self.seed, 'push', '--', str(self.remote), unrelated + ':refs/heads/' + job.head_branch)
        with self.assertRaisesRegex(Rejected, 'non-fast-forward'):
            self.publish(job)
        self.assertEqual(text_git(self.home, self.remote, 'rev-parse', 'refs/heads/' + job.head_branch), unrelated)
        self.assertFalse(any(call and call[0] == 'push' for call in self.publisher.calls))

    def test_repeat_publication_returns_same_sha_and_pr_without_resigning_or_pushing(self):
        job = self.make_job()
        first = self.publish(job)
        calls = len(self.publisher.calls)
        second = self.publish(job)
        self.assertEqual(first, second)
        repeated = self.publisher.calls[calls:]
        self.assertFalse(any('commit-tree' in call or call[0] == 'push' for call in repeated))

    def test_crash_after_push_recovers_same_signed_sha_and_mock_pr(self):
        job = self.make_job()
        original_save = self.publisher._save

        def crash_on_published(path, receipt):
            if receipt['status'] == 'published':
                raise RuntimeError('Synthetic crash after local push')
            original_save(path, receipt)

        with mock.patch.object(self.publisher, '_save', side_effect=crash_on_published):
            with self.assertRaisesRegex(RuntimeError, 'crash'):
                self.publish(job)
        recorded = json.loads((self.publisher.root / f'{job.job_id}.receipt.json').read_text())
        self.assertEqual(recorded['status'], 'signed')
        calls = len(self.publisher.calls)
        recovered = self.publish(job)
        self.assertEqual(recorded['signed_head'], recovered['signed_head'])
        self.assertEqual(recorded['pr'], recovered['pr'])
        self.assertFalse(any('commit-tree' in call or call[0] == 'push'
                             for call in self.publisher.calls[calls:]))

    def test_base_move_before_publication_is_rejected(self):
        job = self.make_job()
        moved = self.commit(self.seed, {'new-base.txt': b'New base\n'}, 'Base moved\n', self.base)
        git(self.home, self.seed, 'push', '--', str(self.remote), moved + ':refs/heads/main')
        with self.assertRaisesRegex(Rejected, 'Base branch moved'):
            self.publish(job)
        self.assertFalse(any('commit-tree' in call for call in self.publisher.calls))

    def test_warm_signed_retry_ignores_hostile_caller_config_and_rejects_real_base_move(self):
        job = self.make_job()
        original_run = self.publisher._run

        def stop_before_push(repo, *args, **kw):
            if args[0] == 'push':
                raise RuntimeError('Synthetic pause after signing before push')
            return original_run(repo, *args, **kw)

        with mock.patch.object(self.publisher, '_run', side_effect=stop_before_push):
            with self.assertRaisesRegex(RuntimeError, 'pause after signing'):
                self.publish(job)
        receipt_path = self.publisher.root / f'{job.job_id}.receipt.json'
        self.assertEqual(json.loads(receipt_path.read_text())['status'], 'signed')

        shadow = self.root / 'shadow.git'
        caller = self.root / 'hostile-caller'
        git(self.home, None, 'init', '--bare', '--template=' + str(self.home / 'empty-template'), str(shadow))
        git(self.home, self.seed, 'push', '--', str(shadow), self.base + ':refs/heads/main')
        git(self.home, None, 'init', '--template=' + str(self.home / 'empty-template'), str(caller))
        text_git(self.home, caller / '.git', 'config', '--local',
                 'url.' + str(shadow) + '.insteadOf', str(self.remote))
        moved = self.commit(self.seed, {'new-base.txt': b'New actual approved base\n'}, 'Actual base moved\n', self.base)
        git(self.home, self.seed, 'push', '--', str(self.remote), moved + ':refs/heads/main')
        # Confirm the hostile caller's config really redirects an unpinned cwd.
        leaked = subprocess.run([GIT, 'ls-remote', '--heads', str(self.remote), 'refs/heads/main'],
                                cwd=caller, env=clean_environment(self.home), check=True,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.assertEqual(leaked.stdout.decode().split()[0], self.base)
        calls = len(self.publisher.calls)
        previous_cwd = Path.cwd()
        try:
            os.chdir(caller)
            with self.assertRaisesRegex(Rejected, 'Base branch moved after review'):
                self.publish(job)
        finally:
            os.chdir(previous_cwd)
        self.assertFalse(any(call[0] == 'push' for call in self.publisher.calls[calls:]))
        actual_head = git(self.home, self.remote, 'show-ref', '--verify', '--quiet',
                          'refs/heads/' + job.head_branch, check=False)
        self.assertEqual(actual_head.returncode, 1)
        self.assertEqual(text_git(self.home, self.remote, 'rev-parse', 'refs/heads/main'), moved)

    def test_transport_executable_paths_with_spaces_and_shell_metacharacters_are_quoted(self):
        self.publisher = Publisher(self.root / "helper with spaces;$(touch shell-marker)`'&[x]",
                                   self.spool, self.policy, self.key)
        job = self.make_job()
        result = self.publish(job)
        self.verify(job, result)
        self.assertFalse((self.publisher.home / 'shell-marker').exists())
        self.assertFalse((self.root / 'shell-marker').exists())
        self.assertEqual(text_git(self.home, self.remote, 'rev-parse', 'refs/heads/' + job.head_branch),
                         result['signed_head'])

    def test_neutral_home_does_not_discover_ancestor_git_configuration(self):
        job = self.make_job()
        original_run = self.publisher._run

        def stop_before_push(repo, *args, **kw):
            if args[0] == 'push':
                raise RuntimeError('Synthetic pause after signing before push')
            return original_run(repo, *args, **kw)

        with mock.patch.object(self.publisher, '_run', side_effect=stop_before_push):
            with self.assertRaisesRegex(RuntimeError, 'pause after signing'):
                self.publish(job)
        receipt_path = self.publisher.root / f'{job.job_id}.receipt.json'
        self.assertEqual(json.loads(receipt_path.read_text())['status'], 'signed')
        shadow = self.root / 'ancestor-shadow.git'
        git(self.home, None, 'init', '--bare', '--template=' + str(self.home / 'empty-template'), str(shadow))
        git(self.home, self.seed, 'push', '--', str(shadow), self.base + ':refs/heads/main')
        git(self.home, None, 'init', '--template=' + str(self.home / 'empty-template'), str(self.root))
        text_git(self.home, self.root / '.git', 'config', '--local',
                 'url.' + str(shadow) + '.insteadOf', str(self.remote))
        moved = self.commit(self.seed, {'new-base.txt': b'New approved base\n'}, 'Approved base moved\n', self.base)
        git(self.home, self.seed, 'push', '--', str(self.remote), moved + ':refs/heads/main')
        vulnerable_env = clean_environment(self.publisher.home)
        vulnerable_env.pop('GIT_CEILING_DIRECTORIES')
        ancestor_lookup = subprocess.run([GIT, 'ls-remote', '--heads', str(self.remote), 'refs/heads/main'],
                                         cwd=self.publisher.home, env=vulnerable_env, check=True,
                                         stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.assertEqual(ancestor_lookup.stdout.decode().split()[0], self.base)
        calls = len(self.publisher.calls)
        with self.assertRaisesRegex(Rejected, 'Base branch moved after review'):
            self.publish(job)
        self.assertFalse(any(call[0] == 'push' for call in self.publisher.calls[calls:]))
        actual_head = git(self.home, self.remote, 'show-ref', '--verify', '--quiet',
                          'refs/heads/' + job.head_branch, check=False)
        self.assertEqual(actual_head.returncode, 1)

    def test_neutral_home_rejects_direct_git_repository_entries(self):
        for kind in ('file', 'directory', 'symlink', 'bare'):
            with self.subTest(kind=kind):
                helper = self.root / ('invalid-helper-' + kind)
                home = helper / 'home'
                home.mkdir(parents=True)
                marker = home / '.git'
                if kind == 'file':
                    marker.write_text('gitdir: ' + str(self.worker) + '\n')
                elif kind == 'directory':
                    marker.mkdir()
                elif kind == 'symlink':
                    marker.symlink_to(self.worker)
                else:
                    git(self.home, None, 'init', '--bare', '--template=' + str(self.home / 'empty-template'), str(home))
                with self.assertRaisesRegex(Rejected, 'Neutral helper home'):
                    Publisher(helper, self.spool, self.policy, self.key)
        job = self.make_job()
        (self.publisher.home / '.git').write_text('gitdir: ' + str(self.worker) + '\n')
        with self.assertRaisesRegex(Rejected, 'Neutral helper home'):
            self.publish(job)
        self.assertFalse(any('commit-tree' in call for call in self.publisher.calls))

    def test_stacked_job_cannot_publish_to_repository_root_base_branch(self):
        first = self.make_job()
        result = self.publish(first)
        git(self.home, self.worker, 'fetch', '--', str(self.remote), result['signed_head'])
        with self.assertRaisesRegex(Rejected, 'Invalid fixed job base or head'):
            self.make_job('synthetic-002', result['signed_head'], 'main',
                          first.head_branch, first.job_id)
        self.assertEqual(text_git(self.home, self.remote, 'rev-parse', 'refs/heads/main'), self.base)

    def test_base_move_during_signing_is_rejected_before_new_push(self):
        job = self.make_job(increments=2)
        original_run = self.publisher._run
        moved = None

        def move_during_signing(repo, *args, **kw):
            nonlocal moved
            if 'commit-tree' in args and moved is None:
                moved = self.commit(self.seed, {'during-signing.txt': b'New actual approved base\n'},
                                    'Base moved during signing\n', self.base)
                git(self.home, self.seed, 'push', '--', str(self.remote), moved + ':refs/heads/main')
            return original_run(repo, *args, **kw)

        with mock.patch.object(self.publisher, '_run', side_effect=move_during_signing):
            with self.assertRaisesRegex(Rejected, 'Base branch moved before push'):
                self.publish(job)
        self.assertIsNotNone(moved)
        self.assertTrue(any('commit-tree' in call for call in self.publisher.calls))
        self.assertFalse(any(call[0] == 'push' for call in self.publisher.calls))
        receipt = json.loads((self.publisher.root / f'{job.job_id}.receipt.json').read_text())
        self.assertEqual(receipt['status'], 'signed')
        actual_head = git(self.home, self.remote, 'show-ref', '--verify', '--quiet',
                          'refs/heads/' + job.head_branch, check=False)
        self.assertEqual(actual_head.returncode, 1)


if __name__ == '__main__':
    unittest.main(verbosity=2)
