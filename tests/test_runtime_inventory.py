"""Offline fixtures for immutable runtime inventory and Debian candidate checks."""
import importlib.util
import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location(
    'runtime_dependencies', Path(__file__).resolve().parents[1] / 'runtime/dependencies.py')
DEPENDENCIES = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DEPENDENCIES)


class RuntimeInventoryTests(unittest.TestCase):
    def test_capture_records_actual_versions_full_npm_tree_and_public_pins(self):
        npm_tree = {'dependencies': {
            '@openai/codex': {'version': '0.1.2', 'dependencies': {
                'example-transitive': {'version': '1.2.3'},
                'example-optional-platform': {'missing': True, 'optional': True},
                '@openai/codex-linux-arm64': {'name': '@openai/codex', 'version': '0.1.2-linux-arm64', 'overridden': False},
                'example-optional-empty-platform': {},
                'example-optional-overridden-platform': {'overridden': False}}},
            '@anthropic-ai/claude-code': {'version': '2.1.0', 'dependencies': {
                'example-transitive': {'version': '1.2.3'}}},
            'npm': {'version': '11.0.0'}}}
        outputs = {
            ('dpkg', '--print-architecture'): 'arm64\n',
            ('node', '--version'): 'v24.1.2\n',
            ('npm', '--version'): '11.0.0\n',
            ('npm', 'ls', '--global', '--all', '--long', '--json'): json.dumps(npm_tree),
            ('yarn', '--version'): '1.22.22\n',
            ('gh', '--version'): 'gh version 2.0.1 (example)\n',
            ('docker', '--version'): 'Docker version 29.1.0, build example\n',
            ('docker', 'compose', 'version'): 'Docker Compose version v2.1.0\n',
            ('docker', 'buildx', 'version'): 'github.com/docker/buildx v0.1.0 example\n',
            ('dotnet', '--list-sdks'): '10.0.100 [/opt/dotnet/sdk]\n',
            ('dotnet', '--list-runtimes'): 'Microsoft.NETCore.App 10.0.1 [/opt/dotnet/shared]\n'
                                        'Microsoft.AspNetCore.App 10.0.1 [/opt/dotnet/shared]\n',
        }

        def fake_run(args, **_kwargs):
            if args[0] == 'dpkg-query':
                return 'ii \texample-base\t1:1.0-1\tarm64\n' \
                       'hi \texample-direct:arm64\t2.0-1\tarm64\n' \
                       'rc \texample-removed\t1.0-1\tarm64\n'
            return outputs[tuple(args)]

        dockerfile = 'FROM docker:29.1.0-cli AS docker-tools\nFROM node:24-bookworm@sha256:' + 'a' * 64
        with patch.object(DEPENDENCIES, 'run', side_effect=fake_run), \
                patch.object(DEPENDENCIES.shutil, 'which', return_value='/example/yarn'), \
                patch.object(Path, 'read_text', return_value='ID=debian\nVERSION_CODENAME=bookworm\n'):
            inventory = DEPENDENCIES.capture('b' * 40, 'c' * 40, dockerfile)
        self.assertEqual(inventory['platform'], 'linux/arm64')
        self.assertEqual(inventory['distribution'], 'debian:bookworm')
        entries = {(item['kind'], item['source'], item['version']) for item in inventory['dependencies']}
        self.assertIn(('npm', 'example-transitive', '1.2.3'), entries)
        self.assertIn(('npm', 'yarn', '1.22.22'), entries)
        self.assertIn(('npm', '@openai/codex', '0.1.2'), entries)
        alias = next(item for item in inventory['dependencies'] if item['name'] == '@openai/codex-linux-arm64')
        self.assertEqual((alias['source'], alias['track'], alias['version']), ('@openai/codex', 'linux-arm64', '0.1.2-linux-arm64'))
        self.assertIn(('github-release', 'docker/buildx', '0.1.0'), entries)
        self.assertIn(('git', 'tjpeel/skills', 'b' * 40), entries)
        self.assertIn(('image', 'library/node', 'sha256:' + 'a' * 64), entries)
        self.assertEqual(len(entries), len(inventory['dependencies']))
        self.assertFalse(any('optional-' in item['source'] for item in inventory['dependencies']))
        self.assertEqual([item['name'] for item in inventory['packages']], ['example-base', 'example-direct'])

    def test_npm_inventory_rejects_missing_nonoptional_dependencies_and_tool_roots(self):
        roots = {'@openai/codex': {'version': '0.1.2'},
                 '@anthropic-ai/claude-code': {'version': '2.1.0'}}
        for metadata in ({'missing': True, 'required': '*'},
                         {'problems': ['missing: example-required@*, required by example@1.0.0']},
                         {'overridden': False, 'required': '*'}):
            with self.subTest(metadata=metadata):
                tree = {'dependencies': {**roots, 'example-required': metadata}}
                with self.assertRaises(ValueError):
                    DEPENDENCIES.npm_inventory(tree, '11.0.0')
        tree = {'dependencies': {**roots, 'example-required': {}},
                'problems': ['missing: example-required@*, required by example@1.0.0']}
        with self.assertRaises(ValueError):
            DEPENDENCIES.npm_inventory(tree, '11.0.0')
        for missing in ('@openai/codex', '@anthropic-ai/claude-code'):
            tree = {'dependencies': {**roots, missing: {}}}
            with self.subTest(required=missing), self.assertRaises(ValueError):
                DEPENDENCIES.npm_inventory(tree, '11.0.0')

    def test_npm_alias_sources_are_derived_without_endpoint_guessing(self):
        roots = {'@openai/codex': {'name': '@openai/codex', 'version': '0.159.3'},
                 '@anthropic-ai/claude-code': {'name': '@anthropic-ai/claude-code', 'version': '2.1.0'}}
        for tag in sorted(DEPENDENCIES.CODEX_PLATFORM_TAGS):
            alias = '@openai/codex-' + tag
            tree = {'dependencies': {**roots, alias: {
                'name': '@openai/codex', 'version': '0.159.3-' + tag, 'overridden': False}}}
            with self.subTest(tag=tag):
                self.assertIn((alias, '@openai/codex', tag, '0.159.3-' + tag),
                              DEPENDENCIES.npm_inventory(tree, '11.0.0'))
        for name, metadata in (
                ('@openai/codex-linux-arm64', {'version': '0.159.3-linux-arm64'}),
                ('@openai/codex-linux-arm64', {'name': '@openai/codex', 'version': '0.159.3-linux-x64'}),
                ('@openai/codex-unknown', {'name': '@openai/codex', 'version': '0.159.3-unknown'}),
                ('example-alias', {'name': 'https://example.invalid/package', 'version': '1.0.0'})):
            with self.subTest(alias=name), self.assertRaises(ValueError):
                DEPENDENCIES.npm_inventory({'dependencies': {**roots, name: metadata}}, '11.0.0')
        tree = {'dependencies': {**roots,
                'example-alias': {'name': 'example-published', 'version': '1.0.0'},
                'example-published': {'name': 'example-published', 'version': '1.0.0'}}}
        entries = DEPENDENCIES.npm_inventory(tree, '11.0.0')
        self.assertIn(('example-published', 'example-published', '', '1.0.0'), entries)
        self.assertEqual(sum(source == 'example-published' for _, source, _, _ in entries), 1)

    def test_image_and_revision_validation(self):
        with self.assertRaises(ValueError):
            DEPENDENCIES.image_reference('FROM node:24-bookworm\nFROM docker:29-cli')
        with self.assertRaises(ValueError):
            DEPENDENCIES.capture('main', 'b' * 40, 'not used')
        self.assertEqual(DEPENDENCIES.image_reference(
            'FROM docker:29-cli\nFROM node:24-bookworm@sha256:' + 'a' * 64),
            ('24-bookworm', 'sha256:' + 'a' * 64))

    def test_debian_candidates_include_unknown_and_use_native_epoch_comparison(self):
        inventory = {'version': 1, 'distribution': 'debian:bookworm', 'packages': [
            {'name': 'example-package', 'version': '1:1.0-1', 'architecture': 'arm64'},
            {'name': 'example-missing', 'version': '2.0-1', 'architecture': 'all'}]}
        policy = '''example-package:arm64:
  Installed: 1:1.0-1
  Candidate: 1:1.0-2
  Version table:
     1:1.0-2 500
        500 https://deb.debian.org/debian bookworm/main arm64 Packages
 *** 1:1.0-1 100
        100 /var/lib/dpkg/status
example-missing:
  Installed: 2.0-1
  Candidate: 2.0-1
  Version table:
 *** 2.0-1 100
        100 /var/lib/dpkg/status
'''
        calls = []

        def fake_run(args, **kwargs):
            calls.append((args, kwargs))
            return policy if args[0] == 'apt-cache' else ''

        with tempfile.TemporaryDirectory() as temporary, \
                patch.object(DEPENDENCIES, 'run', side_effect=fake_run), \
                patch.object(DEPENDENCIES.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0)) as compare:
            directory = Path(temporary)
            updates = DEPENDENCIES.package_updates(inventory, directory)
            bootstrap = (directory / 'apt.conf').read_text()
            sources = (directory / 'sources.list').read_text()
        self.assertTrue(updates[0]['update'])
        self.assertEqual(updates[0]['candidate'], '1:1.0-2')
        self.assertEqual(updates[1]['candidate'], '')
        self.assertFalse(updates[1]['update'])
        self.assertEqual(compare.call_args.args[0], ['dpkg', '--compare-versions', '1:1.0-2', 'gt', '1:1.0-1'])
        self.assertIn('Dir::Etc::parts "-";', bootstrap)
        self.assertIn('Dir::Etc::main "-";', bootstrap)
        self.assertIn('https://deb.debian.org/debian-security bookworm-security main', sources)
        self.assertEqual([call[0][0] for call in calls], ['apt-get', 'apt-cache'])
        for args, kwargs in calls:
            self.assertIn('APT::Sandbox::User=root', args)
            self.assertIn('Dir::State::status=/var/lib/dpkg/status', args)
            self.assertIn('Dir::Etc::sourceparts=-', args)
            self.assertEqual(kwargs['apt_config'].name, 'apt.conf')
            self.assertNotIn('install', args)
        self.assertIn('example-missing', calls[1][0])
        self.assertNotIn('example-missing:all', calls[1][0])

    def test_package_candidate_refresh_errors_are_not_replaced_with_current(self):
        inventory = {'version': 1, 'distribution': 'debian:bookworm', 'packages': [
            {'name': 'example-package', 'version': '1.0-1', 'architecture': 'arm64'}]}
        with tempfile.TemporaryDirectory() as temporary, \
                patch.object(DEPENDENCIES, 'run', side_effect=ValueError('disposable private error')):
            with self.assertRaises(ValueError):
                DEPENDENCIES.package_updates(inventory, Path(temporary))

    def test_subprocess_environment_does_not_forward_proxies_or_diagnostics(self):
        failed = subprocess.CompletedProcess([], 1, stdout='disposable private output', stderr='private stderr')
        with patch.dict(DEPENDENCIES.os.environ, {'HTTPS_PROXY': 'https://example.invalid', 'EXAMPLE_PRIVATE': 'fake'}), \
                patch.object(DEPENDENCIES.subprocess, 'run', return_value=failed) as command:
            with self.assertRaisesRegex(ValueError, '^runtime dependency command failed$'):
                DEPENDENCIES.run(['apt-get', 'update'], apt_config=Path('/tmp/example-apt.conf'))
        env = command.call_args.kwargs['env']
        self.assertNotIn('HTTPS_PROXY', env)
        self.assertNotIn('EXAMPLE_PRIVATE', env)
        self.assertEqual(env['APT_CONFIG'], '/tmp/example-apt.conf')
        self.assertTrue(command.call_args.kwargs['capture_output'])


if __name__ == '__main__':
    unittest.main()
