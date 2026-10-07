"""Offline Homebrew coordination checks with real committed Git archives."""
import argparse
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import time
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('homebrew_installer', ROOT / 'scripts/install_homebrew.py')
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)

# Homebrew is the external boundary. Git archives, private receipts, backups,
# CLI replacement and runtime command execution use the real filesystem.
FAKE_BREW = '''#!/usr/bin/env python3
import hashlib, json, os, pathlib, re, sys, tarfile
from urllib.parse import unquote, urlparse
root = pathlib.Path(os.environ['FAKE_BREW_ROOT'])
args = sys.argv[1:]
with (root/'calls').open('a') as output:
    output.write(json.dumps(args)+'\\n')
tap = root/'Library/Taps/local/homebrew-sdlc'
if args == ['--repository'] or args == ['--prefix']:
    print(root)
elif args == ['tap-new', '--no-git', 'local/sdlc']:
    (tap/'Formula').mkdir(parents=True)
elif args[:2] == ['list', '--versions']:
    if (root/'candidate').exists(): print('sdlc installed')
    else: sys.exit(1)
elif args[:1] in (['install'], ['upgrade']):
    if os.environ.get('FAKE_BREW_FAIL') == 'build': sys.exit(1)
    text = (tap/'Formula/sdlc.rb').read_text()
    version = re.search(r'  version "([^"]+)"', text)[1]
    revision = re.search(r'buildinfo.Revision=([a-f0-9]+)', text)[1]
    counter = re.search(r'  revision (\\d+)', text)[1]
    if os.environ.get('FAKE_BREW_FAIL') == 'identity': revision = 'b'*40
    keg = root/'Cellar/sdlc'/(version+'_'+counter)
    (keg/'bin').mkdir(parents=True)
    (keg/'libexec/source').mkdir(parents=True)
    archive = pathlib.Path(unquote(urlparse(json.loads(re.search(r'  url (.+)', text)[1])).path))
    with tarfile.open(archive) as contents:
        contents.extractall(keg/'libexec/source', filter='data')
    candidate = keg/'bin/sdlc'
    candidate.write_text("#!/usr/bin/env python3\\nimport os,sys\\nfrom pathlib import Path\\n"
        "if sys.argv[1:] == ['--version']: print('sdlc " + version + " (" + revision + "; test/test)')\\n"
        "else:\\n Path(os.environ['SDLC_STATE_DIR'],'runtime-call').write_text(os.environ.get('SDLC_UPDATE_PROJECT_ROOT','')+'\\\\n'+' '.join(sys.argv[1:]))\\n"
        " if os.environ.get('FAKE_BREW_FAIL') == 'guard': print('sdlc: runtime replacement would prevent resuming feature EX-123, run abcdef123; complete this saved work first, or use sdlc update --sdlc-only', file=sys.stderr); sys.exit(1)\\n"
        " if os.environ.get('FAKE_BREW_FAIL') == 'runtime': sys.exit(1)\\n")
    candidate.chmod(0o700)
    (keg/'libexec/sdlc-homebrew.json').write_text(json.dumps({'schema_version':1,
        'formula':'local/sdlc/sdlc','version':version,'revision':revision,
        'sha256':hashlib.sha256(candidate.read_bytes()).hexdigest()}))
    (keg/'INSTALL_RECEIPT.json').write_text('{"source":{"tap":"local/sdlc"}}')
    (root/'candidate').write_text(str(candidate))
    if args[:1] == ['upgrade']:
        (root/'bin/sdlc').unlink()
        (root/'bin/sdlc').symlink_to(candidate)
elif args == ['--prefix', 'local/sdlc/sdlc']:
    print(pathlib.Path((root/'candidate').read_text()).parent.parent)
elif args == ['link', 'local/sdlc/sdlc']:
    (root/'bin/sdlc').symlink_to((root/'candidate').read_text())
    if os.environ.get('FAKE_BREW_FAIL') == 'link': sys.exit(1)
else:
    sys.exit(2)
'''


class HomebrewInstallChecks(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.source = self.root / 'source'
        self.source.mkdir()
        self.state = self.root / 'state'
        self.state.mkdir(mode=0o700)
        self.brew = self.root / 'brew'
        (self.brew / 'bin').mkdir(parents=True)
        self.native = self.brew / 'bin/sdlc'
        self.native.write_bytes(b'previous managed command')
        self.native.chmod(0o700)
        receipt = {'schema_version': 1, 'executable': str(self.native), 'sha256': installer.digest(self.native)}
        (self.state / 'installation.json').write_text(json.dumps(receipt))
        (self.state / 'installation.json').chmod(0o600)
        (self.state / 'runtime.json').write_text('{"private_runtime":"unchanged"}')
        (self.brew / 'bin/brew').write_text(FAKE_BREW)
        (self.brew / 'bin/brew').chmod(0o700)
        for name, content in {
            'go.mod': 'module github.com/tjpeel/sdlc\n\ngo 1.25.0\n',
            'cmd/sdlc/main.go': 'package main\nfunc main() {}\n',
            'internal/buildinfo/version.go': 'package buildinfo\nvar Version = "0.1.0-beta.5"\n',
            'scripts/check_sensitive.py': 'print("Disposable public source checked")\n',
            '.gitignore': '.state/\n',
        }.items():
            path = self.source / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
        self.git('init', '-q')
        self.git('config', 'user.name', 'Example Developer')
        self.git('config', 'user.email', 'developer@example.com')
        self.git('add', '.')
        self.git('-c', 'commit.gpgsign=false', 'commit', '-qm', 'Prepare public snapshot')
        (self.source / '.state').mkdir()
        (self.source / '.state/private.txt').write_text('disposable private input')
        patch = mock.patch.dict(os.environ, {
            'PATH': str(self.brew / 'bin') + os.pathsep + os.environ['PATH'],
            'FAKE_BREW_ROOT': str(self.brew), 'FAKE_BREW_FAIL': '',
        })
        patch.start()
        self.addCleanup(patch.stop)

    def git(self, *args):
        return subprocess.run(['git', '-C', str(self.source), *args], check=True,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout

    def install(self, cli_only=True, dry_run=False):
        args = argparse.Namespace(source=self.source, state_dir=self.state, cli_only=cli_only, agent_tools=False, update_dockerfile=False,
                                  dependencies=False, dry_run=dry_run)
        with contextlib.redirect_stdout(io.StringIO()):
            installer.install(args)

    def test_migration_packages_only_committed_source_and_keeps_runtime(self):
        self.install()
        self.assertTrue(self.native.is_symlink())
        self.assertEqual((self.state / 'runtime.json').read_text(), '{"private_runtime":"unchanged"}')
        self.assertFalse((self.state / 'runtime-call').exists())
        backups = list((self.state / 'homebrew/backups').iterdir())
        self.assertEqual([path.read_bytes() for path in backups], [b'previous managed command'])
        archive = next((self.state / 'homebrew/archives').iterdir())
        with tarfile.open(archive) as contents:
            names = contents.getnames()
        self.assertIn('sdlc/go.mod', names)
        self.assertFalse(any('.state' in name or '/.git/' in name for name in names))
        receipt = installer.private_json(self.state / 'homebrew.json')
        self.assertEqual(receipt['source'], str(self.source))
        self.assertEqual(receipt['formula'], 'local/sdlc/sdlc')

    def assert_failed_migration(self, reason):
        with mock.patch.dict(os.environ, {'FAKE_BREW_FAIL': reason}):
            with self.assertRaises((installer.InstallError, subprocess.CalledProcessError)):
                self.install()
        self.assertFalse(self.native.is_symlink())
        self.assertEqual(self.native.read_bytes(), b'previous managed command')
        self.assertEqual((self.state / 'runtime.json').read_text(), '{"private_runtime":"unchanged"}')

    def test_failed_build_keeps_native_executable(self):
        self.assert_failed_migration('build')

    def test_different_candidate_identity_keeps_native_executable(self):
        self.assert_failed_migration('identity')

    def test_failed_link_restores_native_executable_and_retry_reuses_keg(self):
        self.assert_failed_migration('link')
        calls = [json.loads(line) for line in (self.brew / 'calls').read_text().splitlines()]
        self.assertEqual(calls[-1], ['link', 'local/sdlc/sdlc'])
        self.install()
        self.assertTrue(self.native.is_symlink())
        calls = [json.loads(line) for line in (self.brew / 'calls').read_text().splitlines()]
        self.assertEqual(sum(call[0] == 'install' for call in calls), 1)

    def test_retry_cannot_link_a_foreign_unlinked_keg(self):
        self.assert_failed_migration('link')
        candidate = Path((self.brew / 'candidate').read_text())
        (candidate.parent.parent / 'INSTALL_RECEIPT.json').write_text('{"source":{"tap":"example/other"}}')
        with self.assertRaisesRegex(installer.InstallError, 'does not belong'):
            self.install()
        self.assertFalse(self.native.is_symlink())
        self.assertEqual(self.native.read_bytes(), b'previous managed command')

    def test_amended_commit_advances_formula_and_installed_snapshot(self):
        self.install()
        original = self.native.resolve()
        original_counter = int(original.parent.parent.name.split('_')[1])
        self.git('-c', 'commit.gpgsign=false', 'commit', '--amend', '-qm', 'Amend snapshot identity')
        revision = self.git('rev-parse', 'HEAD').decode().strip()
        self.install()
        self.assertNotEqual(original, self.native.resolve())
        self.assertEqual(int(self.native.resolve().parent.parent.name.split('_')[1]), original_counter + 1)
        result = subprocess.run([str(self.native), '--version'], capture_output=True, text=True, check=True)
        self.assertIn(revision, result.stdout)

    def test_unverified_native_executable_is_left_in_place(self):
        self.native.write_bytes(b'another executable')
        with self.assertRaises(installer.InstallError):
            self.install()
        self.assertEqual(self.native.read_bytes(), b'another executable')
        self.assertFalse((self.brew / 'Library').exists())

    def test_dry_run_and_dirty_source_never_call_homebrew_or_write(self):
        self.install(dry_run=True)
        self.assertFalse((self.brew / 'calls').exists())
        self.assertFalse((self.state / 'homebrew.json').exists())
        (self.source / 'go.mod').write_text('changed source')
        with self.assertRaises(installer.InstallError):
            self.install()
        self.assertFalse((self.brew / 'calls').exists())

    def test_runtime_failure_reports_partial_result_and_forwards_work_checkout(self):
        work = self.root / 'work-project'
        work.mkdir()
        subprocess.run(['git', '-C', str(work), 'init', '-q'], check=True)
        previous_cwd = Path.cwd()
        self.addCleanup(os.chdir, previous_cwd)
        os.chdir(work)
        with mock.patch.dict(os.environ, {'FAKE_BREW_FAIL': 'runtime', 'SDLC_UPDATE_PROJECT_ROOT': ''}):
            with self.assertRaisesRegex(installer.InstallError, 'Runtime command exited with status 1 without diagnostics'):
                self.install(cli_only=False)
        self.assertTrue(self.native.is_symlink())
        caller, args = (self.state / 'runtime-call').read_text().split('\n', 1)
        self.assertEqual(caller, str(work))
        self.assertIn('runtime build --source', args)
        self.assertIn('--source-pins', args)


    def test_guard_failure_has_one_metadata_header_and_actionable_partial_result(self):
        stdout, stderr = io.StringIO(), io.StringIO()
        args = ['--source', str(self.source), '--state-dir', str(self.state)]
        with mock.patch.dict(os.environ, {'FAKE_BREW_FAIL': 'guard', 'SDLC_INSTALL_RESULT_PROTOCOL': '1'}), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            status = installer.main(args)
        self.assertEqual(status, 3)
        self.assertEqual(stdout.getvalue().count('Homebrew: local/sdlc/sdlc'), 1)
        self.assertEqual(stdout.getvalue().count('Source: '), 1)
        final = stderr.getvalue().split('Update incomplete', 1)[1]
        self.assertIn('CLI: installed through Homebrew', final)
        self.assertIn('Runtime: kept to protect saved work', final)
        self.assertIn('Problem: runtime replacement would prevent resuming feature EX-123, run abcdef123', final)
        self.assertIn('Next: sdlc update --sdlc-only', final)
        self.assertNotIn('Homebrew installation failed', stderr.getvalue())
        self.assertNotIn('\x1b', stdout.getvalue() + stderr.getvalue())
        self.assertEqual((self.state / 'runtime.json').read_text(), '{"private_runtime":"unchanged"}')
        self.assertTrue(self.native.is_symlink())

    def test_dockerfile_flag_targets_selected_checkout_and_preview_is_offline(self):
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            status = installer.main(['--source', str(self.source), '--state-dir', str(self.state), '--agent-tools', '--update-dockerfile', '--dry-run'])
        self.assertEqual(status, 0)
        self.assertIn('Dockerfile: update only the four agent-tool pins', output.getvalue())
        self.assertFalse((self.brew / 'calls').exists())
        with contextlib.redirect_stdout(io.StringIO()):
            status = installer.main(['--source', str(self.source), '--state-dir', str(self.state), '--agent-tools', '--update-dockerfile'])
        self.assertEqual(status, 0)
        invocation = (self.state / 'runtime-call').read_text()
        self.assertIn('runtime update --source ' + str(self.source.resolve()), invocation)
        self.assertTrue(invocation.endswith('--agent-tools --update-dockerfile'))

    def test_dockerfile_flag_requires_agent_tools(self):
        for modes in (['--update-dockerfile'], ['--sdlc-only', '--update-dockerfile'], ['--dependencies', '--update-dockerfile']):
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
                installer.main(modes)
            self.assertEqual(error.exception.code, 2)

    def test_agent_tools_forwards_selected_mode(self):
        with contextlib.redirect_stdout(io.StringIO()):
            status = installer.main(['--source', str(self.source), '--state-dir', str(self.state), '--agent-tools'])
        self.assertEqual(status, 0)
        invocation = (self.state / 'runtime-call').read_text()
        self.assertIn('runtime update --source ', invocation)
        self.assertTrue(invocation.endswith('--agent-tools'))

    def test_update_modes_are_exclusive_and_legacy_alias_works(self):
        for modes in (['--agent-tools', '--dependencies'], ['--agent-tools', '--sdlc-only'], ['--agent-tools', '--cli-only']):
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
                installer.main(modes)
            self.assertEqual(error.exception.code, 2)
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(installer.main(['--source', str(self.source), '--state-dir', str(self.state), '--cli-only']), 0)
        self.assertFalse((self.state / 'runtime-call').exists())

    def test_cli_only_success_summary_keeps_runtime(self):
        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            status = installer.main(['--source', str(self.source), '--state-dir', str(self.state), '--sdlc-only'])
        self.assertEqual(status, 0)
        self.assertIn('Update complete\nCLI: installed through Homebrew\nRuntime: kept (--sdlc-only)', stdout.getvalue())
        self.assertFalse((self.state / 'runtime-call').exists())

    def test_failure_before_cli_install_does_not_claim_partial_success(self):
        stderr = io.StringIO()
        with mock.patch.dict(os.environ, {'FAKE_BREW_FAIL': 'build'}), contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(stderr):
            status = installer.main(['--source', str(self.source), '--state-dir', str(self.state)])
        self.assertEqual(status, 1)
        self.assertIn('CLI: installation did not complete', stderr.getvalue())
        self.assertIn('Runtime: not updated', stderr.getvalue())
        self.assertNotIn('CLI: installed through Homebrew', stderr.getvalue())

    def test_runtime_result_does_not_wait_for_inherited_descendant_pipe(self):
        # The orphan emits continuously until our reader closes; it cannot retain
        # the installer's pipe after the direct runtime process has exited.
        program = """import os,time
pid=os.fork()
if pid:
 time.sleep(0.02)
 os._exit(0)
try:
 deadline=time.monotonic()+2.5
 while time.monotonic()<deadline:
  os.write(2,b'descendant')
  time.sleep(0.001)
except BrokenPipeError:
 os._exit(0)
"""
        started = time.monotonic()
        with contextlib.redirect_stderr(io.StringIO()):
            installer.runtime_command([sys.executable, '-c', program], dict(os.environ))
        self.assertLess(time.monotonic() - started, 2)

    def test_runtime_diagnostics_keep_split_unicode_and_sanitize_cause(self):
        stderr = io.StringIO()
        program = "import os,sys,time; os.write(2,b'x'*20000+b'\\nsdlc: \\x1b[31m'); data='caf\u00e9'.encode(); os.write(2,data[:-1]); time.sleep(0.02); os.write(2,data[-1:]+b'\\x1b[0m\\n'); sys.exit(1)"
        with contextlib.redirect_stderr(stderr):
            with self.assertRaisesRegex(installer.InstallError, '^caf\u00e9$'):
                installer.runtime_command([sys.executable, '-c', program], dict(os.environ))
        self.assertIn('caf\u00e9', stderr.getvalue())
        self.assertNotIn('\ufffd', stderr.getvalue())
        self.assertEqual(installer.plain_text('\x1b]0;hidden\x07bad\u202e\x1b[31m cause'), 'bad cause')

    def test_color_only_for_supported_terminal(self):
        stream = mock.Mock()
        stream.isatty.return_value = True
        with mock.patch.dict(os.environ, {'TERM': 'xterm'}, clear=True):
            self.assertIn('\x1b[1;31m', installer.styled('Problem', '1;31', stream))
        for env in ({'TERM': 'xterm', 'NO_COLOR': ''}, {'TERM': 'dumb'}):
            with mock.patch.dict(os.environ, env, clear=True):
                self.assertEqual(installer.styled('Problem', '1;31', stream), 'Problem')


if __name__ == '__main__':
    unittest.main()
