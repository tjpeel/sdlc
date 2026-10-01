"""Offline checks for automated runtime dependency updates."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('runtime_pin_updater', ROOT / 'scripts/update_runtime_pins.py')
updater = importlib.util.module_from_spec(spec)
spec.loader.exec_module(updater)

PINS = {
    'CODEX_VERSION': '0.159.2',
    'GH_VERSION': '2.81.0',
    'SKILLS_REVISION': 'a' * 40,
    'AGENTS_REVISION': 'b' * 40,
}
CANDIDATES = {
    'CODEX_VERSION': '0.160.0',
    'GH_VERSION': '2.82.0',
    'SKILLS_REVISION': 'c' * 40,
    'AGENTS_REVISION': 'd' * 40,
}


def dockerfile(pins=None, newline='\n'):
    pins = pins or PINS
    lines = ['FROM node:24-bookworm', '# Runtime pins']
    lines.extend(f'ARG {name}={pins[name]}' for name in updater.PIN_NAMES)
    lines.extend(['', 'RUN echo "leave this alone"', ''])
    return newline.join(lines)


class DependencyUpdateChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / 'Dockerfile'
        self.original = dockerfile().encode('utf-8')
        self.path.write_bytes(self.original)

    def update(self, candidates=None):
        return updater.update_pins(self.path, lambda: candidates if candidates is not None else CANDIDATES)

    def test_all_four_pins_update_and_other_contents_and_mode_survive(self):
        os.chmod(self.path, 0o640)
        changes = self.update()
        self.assertEqual(self.path.read_text(), dockerfile(CANDIDATES))
        self.assertEqual(changes, [(name, PINS[name], CANDIDATES[name]) for name in updater.PIN_NAMES])
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o640)
        self.assertEqual(sorted(path.name for path in self.path.parent.iterdir()), ['Dockerfile'])

    def test_crlf_and_assignment_whitespace_are_preserved(self):
        original = dockerfile(newline='\r\n').replace('ARG GH_VERSION=', '\tARG GH_VERSION=').replace(
            'GH_VERSION=2.81.0\r', 'GH_VERSION=2.81.0 \t\r')
        self.path.write_bytes(original.encode('utf-8'))
        self.update()
        expected = original
        for name in updater.PIN_NAMES:
            expected = expected.replace(PINS[name], CANDIDATES[name])
        self.assertEqual(self.path.read_bytes(), expected.encode('utf-8'))

    def test_unchanged_candidates_do_not_replace_file(self):
        with mock.patch.object(updater.os, 'replace') as replace:
            self.assertEqual(self.update(PINS), [])
        replace.assert_not_called()
        self.assertEqual(self.path.read_bytes(), self.original)

    def test_numeric_comparison_prevents_downgrades(self):
        pins = dict(PINS, CODEX_VERSION='0.9.0', GH_VERSION='10.0.0')
        self.path.write_text(dockerfile(pins))
        candidates = dict(pins, CODEX_VERSION='0.10.0', GH_VERSION='2.99.0')
        self.assertEqual(self.update(candidates), [('CODEX_VERSION', '0.9.0', '0.10.0')])
        self.assertEqual(self.path.read_text(), dockerfile(dict(pins, CODEX_VERSION='0.10.0')))

    def test_malformed_current_pins_fail_before_fetching(self):
        invalid = ['v1.2.3', '1.2', '1.2.3-beta.1', '01.2.3', '1.2.3\nARG CODEX_VERSION=1.2.4']
        for value in invalid:
            with self.subTest(value=value):
                self.path.write_text(dockerfile(dict(PINS, CODEX_VERSION=value)))
                fetcher = mock.Mock(return_value=CANDIDATES)
                with self.assertRaises(updater.UpdateError):
                    updater.update_pins(self.path, fetcher)
                fetcher.assert_not_called()

    def test_missing_and_duplicate_definitions_are_rejected(self):
        for name in updater.PIN_NAMES:
            for contents in (dockerfile().replace(f'ARG {name}={PINS[name]}\n', ''),
                             dockerfile() + f'ARG {name}={PINS[name]}\n',
                             dockerfile() + f'arg {name}={PINS[name]}\n'):
                with self.subTest(name=name, contents=contents), self.assertRaises(updater.UpdateError):
                    updater.read_pins(contents)

    def test_commented_arg_is_not_a_definition(self):
        contents = dockerfile() + '# ARG CODEX_VERSION=999.0.0\n'
        self.assertEqual(updater.read_pins(contents)['CODEX_VERSION'][0], PINS['CODEX_VERSION'])

    def test_invalid_current_sha_is_rejected(self):
        with self.assertRaises(updater.UpdateError):
            updater.read_pins(dockerfile(dict(PINS, SKILLS_REVISION='A' * 40)))

    def test_malformed_candidates_leave_file_unchanged(self):
        for name in updater.PIN_NAMES:
            values = (None, '', 3, '1.2.3-beta', 'a' * 39, 'A' * 40, 'a' * 40 + '\n')
            for value in values:
                with self.subTest(name=name, value=value), self.assertRaises(updater.UpdateError):
                    self.update(dict(CANDIDATES, **{name: value}))
                self.assertEqual(self.path.read_bytes(), self.original)

    def test_incomplete_or_extra_candidate_set_is_rejected(self):
        for candidates in ({}, list(CANDIDATES), dict(CANDIDATES, OTHER_VERSION='1.0.0')):
            with self.subTest(candidates=candidates), self.assertRaises(updater.UpdateError):
                self.update(candidates)
            self.assertEqual(self.path.read_bytes(), self.original)

    def test_fetch_failure_leaves_file_unchanged(self):
        fetcher = mock.Mock(side_effect=updater.UpdateError('Unavailable upstream metadata.'))
        with self.assertRaises(updater.UpdateError):
            updater.update_pins(self.path, fetcher)
        self.assertEqual(self.path.read_bytes(), self.original)

    def test_file_changed_during_fetch_is_not_overwritten(self):
        altered = self.original + b'# A concurrent edit\n'

        def fetcher():
            self.path.write_bytes(altered)
            return CANDIDATES

        with self.assertRaisesRegex(updater.UpdateError, 'changed during the update'):
            updater.update_pins(self.path, fetcher)
        self.assertEqual(self.path.read_bytes(), altered)
        self.assertEqual(sorted(path.name for path in self.path.parent.iterdir()), ['Dockerfile'])

    def test_symlinks_are_rejected(self):
        link = self.path.parent / 'linked-Dockerfile'
        link.symlink_to(self.path)
        with self.assertRaises(updater.UpdateError):
            updater.load_dockerfile(link)

    def test_check_is_read_only_and_does_not_fetch(self):
        output = io.StringIO()
        with (mock.patch.object(updater, 'fetch_candidates') as fetcher,
              contextlib.redirect_stdout(output)):
            self.assertEqual(updater.main(['--check', '--dockerfile', str(self.path)]), 0)
        fetcher.assert_not_called()
        self.assertEqual(output.getvalue(), 'Runtime pins are valid.\n')
        self.assertEqual(self.path.read_bytes(), self.original)

    def test_write_reports_changes_and_default_reports_no_updates(self):
        output = io.StringIO()
        with (mock.patch.object(updater, 'fetch_candidates', return_value=CANDIDATES),
              contextlib.redirect_stdout(output)):
            self.assertEqual(updater.main(['--write', '--dockerfile', str(self.path)]), 0)
        self.assertEqual(output.getvalue().splitlines(),
                         [f'{name}: {PINS[name]} -> {CANDIDATES[name]}' for name in updater.PIN_NAMES])
        output = io.StringIO()
        with (mock.patch.object(updater, 'fetch_candidates', return_value=CANDIDATES),
              contextlib.redirect_stdout(output)):
            self.assertEqual(updater.main(['--dockerfile', str(self.path)]), 0)
        self.assertEqual(output.getvalue(), 'No runtime pin updates.\n')

    def test_cli_failure_has_nonzero_status(self):
        output = io.StringIO()
        with (mock.patch.object(updater, 'fetch_candidates', side_effect=updater.UpdateError('Fetch failed.')),
              contextlib.redirect_stderr(output)):
            self.assertEqual(updater.main(['--dockerfile', str(self.path)]), 1)
        self.assertIn('Fetch failed.', output.getvalue())
        self.assertEqual(self.path.read_bytes(), self.original)


class UpstreamMetadataChecks(unittest.TestCase):
    def github_responses(self):
        return [
            {'tag_name': 'v2.82.0', 'draft': False, 'prerelease': False},
            {'sha': CANDIDATES['SKILLS_REVISION']},
            {'sha': CANDIDATES['AGENTS_REVISION']},
        ]

    def test_upstream_values_and_github_endpoints(self):
        npm = {'name': '@openai/codex', 'version': CANDIDATES['CODEX_VERSION']}
        with (mock.patch.object(updater, 'npm_json', return_value=npm),
              mock.patch.object(updater, 'github_json', side_effect=self.github_responses()) as github):
            self.assertEqual(updater.fetch_candidates(), CANDIDATES)
        self.assertEqual(github.call_args_list, [mock.call('repos/cli/cli/releases/latest'),
                                               mock.call('repos/tjpeel/skills/commits/main'),
                                               mock.call('repos/tjpeel/agents/commits/main')])

    def test_invalid_npm_package_or_version_is_rejected(self):
        for payload in ({}, {'name': 'other', 'version': '1.2.3'},
                        {'name': '@openai/codex', 'version': '1.2.3-beta'}):
            with (self.subTest(payload=payload), mock.patch.object(updater, 'npm_json', return_value=payload),
                  self.assertRaises(updater.UpdateError)):
                updater.fetch_candidates()

    def test_invalid_release_or_commit_is_rejected(self):
        invalid_releases = [None, {}, {'tag_name': 'v2.82.0', 'draft': False, 'prerelease': True},
                            {'tag_name': '2.82.0', 'draft': False, 'prerelease': False}]
        invalid_commits = [{}, {'sha': 'A' * 40}, {'sha': 'z' * 40}]
        for index, payload in ([(0, payload) for payload in invalid_releases]
                               + [(2, payload) for payload in invalid_commits]):
            responses = self.github_responses()
            responses[index] = payload
            # JSON fetch helpers reject null responses before extraction.
            if payload is None:
                with self.assertRaises(updater.UpdateError):
                    updater.parse_json('null', 'example source')
                continue
            with (self.subTest(payload=payload),
                  mock.patch.object(updater, 'npm_json', return_value={'name': '@openai/codex', 'version': '0.160.0'}),
                  mock.patch.object(updater, 'github_json', side_effect=responses),
                  self.assertRaises(updater.UpdateError)):
                updater.fetch_candidates()

    def test_github_command_failure_does_not_expose_stderr(self):
        error = subprocess.CalledProcessError(1, ['gh'], stderr='synthetic-private-token')
        with mock.patch.object(updater.subprocess, 'run', side_effect=error):
            with self.assertRaises(updater.UpdateError) as raised:
                updater.github_json('repos/example/repo/commits/main')
        self.assertNotIn('synthetic-private-token', str(raised.exception))

    def test_github_uses_structured_command_and_json(self):
        response = subprocess.CompletedProcess([], 0, stdout='{"sha": "example"}')
        with mock.patch.object(updater.subprocess, 'run', return_value=response) as run:
            self.assertEqual(updater.github_json('repos/example/repo/commits/main'), {'sha': 'example'})
        self.assertEqual(run.call_args.args[0],
                         ['gh', 'api', '--method', 'GET', 'repos/example/repo/commits/main'])
        self.assertTrue(run.call_args.kwargs['capture_output'])

    def test_npm_fetch_uses_bounded_response_and_fixed_url(self):
        response = mock.MagicMock()
        response.__enter__.return_value.read.return_value = json.dumps(
            {'name': '@openai/codex', 'version': '0.160.0'}).encode('utf-8')
        with mock.patch.object(updater.urllib.request, 'urlopen', return_value=response) as urlopen:
            self.assertEqual(updater.npm_json()['version'], '0.160.0')
        self.assertEqual(urlopen.call_args.args[0].full_url, updater.NPM_URL)
        response.__enter__.return_value.read.assert_called_once_with(updater.MAX_RESPONSE_BYTES + 1)

    def test_malformed_json_and_non_object_json_are_rejected(self):
        for raw in ('{', '[]', 'null', b'\xff'):
            with self.subTest(raw=raw), self.assertRaises(updater.UpdateError):
                updater.parse_json(raw, 'example source')


if __name__ == '__main__':
    unittest.main()
