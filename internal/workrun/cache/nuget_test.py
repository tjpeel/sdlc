import base64
import hashlib
import json
import pathlib
import tempfile
import subprocess
import unittest.mock
import unittest
import zipfile

import nuget


class PackageCacheTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name).resolve()
        self.host = self.root / 'host'
        self.host.mkdir()
        self.stage = self.root / 'stage'
        self.stage.mkdir()
        self.seed = self.root / 'seed'
        self.index = self.root / 'index.json'

    def package(self, name='example', extra=None, version='1.0.0'):
        root = self.host / name / version
        root.mkdir(parents=True)
        archive = root / (name + '.' + version + '.nupkg')
        entries = {name + '.nuspec': ('<package><metadata><id>' + name + '</id><version>' + version + '</version></metadata></package>').encode(), 'lib/net8.0/example.dll': b'disposable-package-data'}
        entries.update(extra or {})
        with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED) as z:
            for path, data in entries.items():
                z.writestr(path, data)
                if '..' not in pathlib.PurePosixPath(path).parts:
                    destination = root / path
                    destination.parent.mkdir(parents=True, exist_ok=True)
                    destination.write_bytes(data)
        digest = base64.b64encode(hashlib.sha512(archive.read_bytes()).digest()).decode()
        pathlib.Path(str(archive) + '.sha512').write_text(digest)
        (root / '.nupkg.metadata').write_text(json.dumps({'version': 2, 'contentHash': digest, 'source': 'https://example.invalid/private-source?fake-secret'}))
        return root

    def export(self):
        nuget.export(self.host, self.stage, self.index)
        self.index.write_bytes((self.stage / 'index.json').read_bytes())

    def project(self, files):
        project = self.root / 'project'
        project.mkdir()
        for name, data in files.items():
            path = project / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(data)
        env = {'PATH': __import__('os').environ['PATH'], 'HOME': str(self.root), 'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null'}
        for args in [['init', '--initial-branch=main', '--template='], ['add', '.'], ['-c', 'user.name=Example User', '-c', 'user.email=example@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-m', 'Create disposable dependency fixture']]:
            subprocess.run(['git', '-C', str(project), *args], env=env, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        return project

    def test_lock_selects_exact_direct_and_transitive_versions(self):
        nuspec = b'<package><metadata><id>example</id><version>1.0.0</version><dependencies><dependency id="unrelated" version="1.0.0"/></dependencies></metadata></package>'
        self.package(extra={'example.nuspec': nuspec})
        self.package(version='2.0.0')
        self.package('transitive')
        self.package('unrelated')
        lock = {'version': 1, 'dependencies': {'net10.0': {'Example': {'type': 'Direct', 'resolved': '1.0.0'}, 'Transitive': {'type': 'Transitive', 'resolved': '1.0.0'}, 'LocalProject': {'type': 'Project'}}}}
        project = self.project({'App.csproj': '<Project><ItemGroup><PackageReference Include="Example" Version="1.*"/></ItemGroup></Project>', 'packages.lock.json': json.dumps(lock)})
        # Changes outside the committed lock must not alter selection.
        (project / 'packages.lock.json').write_text('{}')
        nuget.export(self.host, self.stage, self.index, project, 'locked', max_packages=512)
        self.assertTrue((self.stage / 'example/1.0.0').is_dir())
        self.assertTrue((self.stage / 'transitive/1.0.0').is_dir())
        self.assertFalse((self.stage / 'example/2.0.0').exists())
        self.assertFalse((self.stage / 'unrelated').exists())

    def test_configured_budget_imports_more_than_legacy_limit(self):
        for i in range(70):
            self.package('package%03d' % i)
        nuget.export(self.host, self.stage, self.index, max_packages=80)
        self.assertEqual(sum(p.is_dir() for p in self.stage.iterdir()), 70)
        limited = self.root / 'limited'; limited.mkdir()
        nuget.export(self.host, limited, self.index, max_packages=3)
        self.assertEqual(sum(p.is_dir() for p in limited.iterdir()), 3)

    def test_mixed_projects_keep_unlocked_versions_and_dependencies(self):
        self.package()
        nuspec = b'<package><metadata><id>example</id><version>2.0.0</version><dependencies><dependency id="child" version="1.0.0"/></dependencies></metadata></package>'
        self.package(version='2.0.0', extra={'example.nuspec': nuspec})
        self.package('child')
        lock = {'version': 1, 'dependencies': {'net10.0': {'Example': {'type': 'Direct', 'resolved': '1.0.0'}}}}
        project = self.project({
            'A/A.csproj': '<Project><ItemGroup><PackageReference Include="Example" Version="1.*"/></ItemGroup></Project>',
            'A/packages.A.lock.json': json.dumps(lock),
            'B/B.csproj': '<Project><ItemGroup><PackageReference Include="Example"/></ItemGroup></Project>',
            'Directory.Packages.props': '<Project><ItemGroup><PackageVersion Include="Example" Version="2.0.0"/></ItemGroup></Project>',
        })
        nuget.export(self.host, self.stage, self.index, project, 'mixed')
        self.assertTrue((self.stage / 'example/1.0.0').is_dir())
        self.assertTrue((self.stage / 'example/2.0.0').is_dir())
        self.assertTrue((self.stage / 'child/1.0.0').is_dir())

    def test_lock_removal_keeps_dependencies_of_unchanged_indexed_packages(self):
        nuspec = b'<package><metadata><id>example</id><version>1.0.0</version><dependencies><dependency id="child" version="1.0.0"/></dependencies></metadata></package>'
        self.package(extra={'example.nuspec': nuspec})
        self.package('child')
        lock = {'version': 1, 'dependencies': {'net10.0': {'Example': {'type': 'Direct', 'resolved': '1.0.0'}}}}
        project = self.project({'App.csproj': '<Project><ItemGroup><PackageReference Include="Example" Version="1.0.0"/></ItemGroup></Project>', 'packages.lock.json': json.dumps(lock)})
        nuget.export(self.host, self.stage, self.index, project, 'same')
        self.assertFalse((self.stage / 'child').exists())
        self.index.write_bytes((self.stage / 'index.json').read_bytes())
        subprocess.run(['git', '-C', str(project), 'rm', 'packages.lock.json'], check=True, stdout=subprocess.DEVNULL)
        subprocess.run(['git', '-C', str(project), '-c', 'user.name=Example User', '-c', 'user.email=example@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-m', 'Remove disposable lock'], check=True, stdout=subprocess.DEVNULL)
        second = self.root / 'second'; second.mkdir()
        nuget.export(self.host, second, self.index, project, 'same')
        self.assertTrue((second / 'child/1.0.0').is_dir())

    def test_mixed_locks_keep_floating_unlocked_fallback(self):
        self.package()
        self.package(version='2.0.0')
        lock = {'version': 1, 'dependencies': {'net10.0': {'Example': {'type': 'Direct', 'resolved': '1.0.0'}}}}
        project = self.project({
            'A/A.csproj': '<Project><ItemGroup><PackageReference Include="Example" Version="1.*"/></ItemGroup></Project>',
            'A/packages.lock.json': json.dumps(lock),
            'B/B.csproj': '<Project><ItemGroup><PackageReference Include="Example" Version="2.*"/></ItemGroup></Project>',
        })
        nuget.export(self.host, self.stage, self.index, project, 'mixed-floating')
        self.assertTrue((self.stage / 'example/1.0.0').is_dir())
        self.assertTrue((self.stage / 'example/2.0.0').is_dir())

    def test_byte_budget_includes_expanded_payloads_and_defers_for_retry(self):
        package = self.package(extra={'lib/net8.0/large.dll': b'x' * 100000})
        self.assertLess((package / 'example.1.0.0.nupkg').stat().st_size, 4096)
        nuget.export(self.host, self.stage, self.index, max_bytes=4096)
        self.assertFalse((self.stage / 'example').exists())
        retry = self.root / 'retry'; retry.mkdir()
        nuget.export(self.host, retry, self.index, max_bytes=200000)
        self.assertEqual((retry / 'example/1.0.0/lib/net8.0/large.dll').read_bytes(), b'x' * 100000)

    def test_invalid_budget_cannot_import_packages(self):
        self.package()
        for options in [{'max_packages': -1}, {'max_packages': 4097}, {'max_bytes': -1}, {'max_bytes': 1024*1024*1024+1}]:
            with self.assertRaises(ValueError):
                nuget.export(self.host, self.stage, self.index, **options)
        self.assertEqual(list(self.stage.iterdir()), [])

    def test_import_reconstructs_only_package_data_and_reuses_unchanged_host_index(self):
        self.package()
        self.export()
        nuget.promote(self.stage, self.seed)
        package = self.seed / 'example' / '1.0.0'
        self.assertEqual((package / 'lib/net8.0/example.dll').read_bytes(), b'disposable-package-data')
        self.assertNotIn('private-source', (package / '.nupkg.metadata').read_text())
        nuget.valid(package)
        second = self.root / 'second'
        second.mkdir()
        nuget.export(self.host, second, self.index)
        self.assertEqual([p.name for p in second.iterdir()], ['index.json'])

    def test_signed_archive_hash_and_nuget_content_hash_remain_distinct(self):
        package = self.package(extra={'.signature.p7s': b'disposable-signature-fixture'})
        marker = package / '.nupkg.metadata'
        metadata = json.loads(marker.read_text())
        content_hash = base64.b64encode(hashlib.sha512(b'original unsigned package bytes').digest()).decode()
        metadata['contentHash'] = content_hash
        marker.write_text(json.dumps(metadata))
        self.export()
        nuget.promote(self.stage, self.seed)
        imported = self.seed / 'example' / '1.0.0'
        self.assertEqual(json.loads((imported / '.nupkg.metadata').read_text())['contentHash'], content_hash)
        self.assertEqual((imported / 'example.1.0.0.nupkg').read_bytes(), (package / 'example.1.0.0.nupkg').read_bytes())
        self.assertEqual((imported / '.signature.p7s').read_bytes(), b'disposable-signature-fixture')
        nuget.valid(imported)

    def test_distinct_content_hash_requires_signed_archive(self):
        package = self.package()
        marker = package / '.nupkg.metadata'
        metadata = json.loads(marker.read_text())
        metadata['contentHash'] = base64.b64encode(hashlib.sha512(b'wrong unsigned package hash').digest()).decode()
        marker.write_text(json.dumps(metadata))
        self.export()
        self.assertFalse((self.stage / 'example').exists())

    def test_tampered_payload_is_not_imported(self):
        package = self.package()
        (package / 'lib/net8.0/example.dll').write_bytes(b'tampered')
        self.export()
        self.assertFalse((self.stage / 'example').exists())

    def test_incomplete_extraction_is_not_imported(self):
        package = self.package()
        (package / '.nupkg.metadata').unlink()
        self.export()
        self.assertFalse((self.stage / 'example').exists())

    def test_archive_traversal_is_not_imported(self):
        self.package(extra={'../../escape': b'must-not-escape'})
        self.export()
        self.assertFalse((self.stage / 'example').exists())
        self.assertFalse((self.root / 'escape').exists())

    def test_symlink_payload_and_package_root_are_not_imported(self):
        package = self.package()
        payload = package / 'lib/net8.0/example.dll'
        payload.unlink()
        payload.symlink_to(self.root / 'private')
        self.export()
        self.assertFalse((self.stage / 'example').exists())
        link = self.host / 'linked'
        link.symlink_to(self.host / 'example', target_is_directory=True)
        self.export()
        self.assertFalse((self.stage / 'linked').exists())

    def test_hash_and_package_identity_are_checked(self):
        package = self.package()
        pathlib.Path(str(package / 'example.1.0.0.nupkg') + '.sha512').write_text('wrong-hash')
        self.export()
        self.assertFalse((self.stage / 'example').exists())
        package.rename(package.parent / '2.0.0')
        self.export()
        self.assertFalse((self.stage / 'example').exists())

    def test_committed_project_pins_select_host_packages_and_reset_epoch_reimports(self):
        self.package()
        self.package('unrelated')
        project = self.root / 'project'
        project.mkdir()
        (project / 'App.csproj').write_text('<Project><ItemGroup><PackageReference Include="Example" Version="1.0.0" /></ItemGroup></Project>')
        env = {'PATH': __import__('os').environ['PATH'], 'HOME': str(self.root), 'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null'}
        for args in [['init', '--initial-branch=main', '--template='], ['add', '.'], ['-c', 'user.name=Example User', '-c', 'user.email=example@example.invalid', 'commit', '-m', 'Create disposable dependency fixture']]:
            subprocess.run(['git', '-C', str(project), *args], env=env, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        # Uncommitted declarations must not change the selected dependency set.
        (project / 'App.csproj').write_text('<Project><ItemGroup><PackageReference Include="Unrelated" Version="1.0.0" /></ItemGroup></Project>')
        nuget.export(self.host, self.stage, self.index, project, 'epoch-one')
        self.assertTrue((self.stage / 'example').is_dir())
        self.assertFalse((self.stage / 'unrelated').exists())
        self.index.write_bytes((self.stage / 'index.json').read_bytes())
        warm = self.root / 'warm'
        warm.mkdir()
        nuget.export(self.host, warm, self.index, project, 'epoch-one')
        self.assertFalse((warm / 'example').exists())
        reset = self.root / 'reset'
        reset.mkdir()
        nuget.export(self.host, reset, self.index, project, 'epoch-two')
        self.assertTrue((reset / 'example').is_dir())

    def test_archive_is_parsed_from_the_same_checked_bytes(self):
        package = self.package()
        original = nuget.read_regular
        def replace_after_read(path):
            data = original(path)
            if str(path).endswith('.nupkg'):
                path.write_bytes(b'concurrent replacement is not a zip')
            return data
        with unittest.mock.patch.object(nuget, 'read_regular', side_effect=replace_after_read):
            data, digest, entries, content_hash = nuget.valid(package)
        self.assertTrue(data.startswith(b'PK'))
        self.assertIn(('lib/net8.0/example.dll', b'disposable-package-data'), entries)

    def test_expanded_opc_metadata_counts_toward_archive_limit(self):
        package = self.package(extra={'_rels/.rels': b'x' * 4096})
        self.assertLess((package / 'example.1.0.0.nupkg').stat().st_size, 1024)
        with unittest.mock.patch.object(nuget, 'LIMIT', 1024):
            with self.assertRaises(ValueError):
                nuget.valid(package)

    def test_unchanged_seed_clone_skips_archive_reads_but_changed_copy_conflicts(self):
        package = self.package()
        self.export()
        nuget.promote(self.stage, self.seed)
        archive = package / 'example.1.0.0.nupkg'
        initial = self.root / 'initial.json'
        initial.write_text(json.dumps({'example/1.0.0': nuget.fingerprint(archive)}))
        original = nuget.read_regular
        def reject_archive_read(path):
            if path.suffix == '.nupkg':
                raise AssertionError('unchanged warm archive was reread')
            return original(path)
        with unittest.mock.patch.object(nuget, 'read_regular', side_effect=reject_archive_read):
            nuget.promote(self.host, self.seed, initial)
        old = (self.seed / 'example/1.0.0/example.1.0.0.nupkg').read_bytes()
        archive.write_bytes(b'changed private session archive')
        nuget.promote(self.host, self.seed, initial)
        self.assertEqual((self.seed / 'example/1.0.0/example.1.0.0.nupkg').read_bytes(), old)

    def test_conflicting_session_payload_never_overwrites_seed(self):
        self.package()
        self.export()
        nuget.promote(self.stage, self.seed)
        original = (self.seed / 'example/1.0.0/example.1.0.0.nupkg').read_bytes()
        changed = self.root / 'changed'
        changed.mkdir()
        self.host = changed
        self.package(extra={'new.txt': b'conflicting payload'})
        nuget.promote(self.host, self.seed)
        self.assertEqual((self.seed / 'example/1.0.0/example.1.0.0.nupkg').read_bytes(), original)


if __name__ == '__main__':
    unittest.main()
