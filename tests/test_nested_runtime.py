"""Offline invariants for the optional private Docker daemon and catalogue mounts."""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('nested_entrypoint', ROOT / 'runtime/entrypoint.py')
entrypoint = importlib.util.module_from_spec(spec)
spec.loader.exec_module(entrypoint)


class ClaudeCatalogueChecks(unittest.TestCase):
    def test_persistent_state_keeps_image_catalogues_visible(self):
        with tempfile.TemporaryDirectory() as folder:
            folder = Path(folder)
            state, source = folder / 'state', folder / 'image'
            state.mkdir()
            for name in ('skills', 'agents'):
                (source / name).mkdir(parents=True)
                (source / name / 'example').write_text('version one')
            entrypoint.claude_catalogues(state, source)
            entrypoint.claude_catalogues(state, source)
            for name in ('skills', 'agents'):
                self.assertEqual((state / name / 'example').read_text(), 'version one')
                (source / name / 'example').write_text('version two')
                self.assertEqual((state / name / 'example').read_text(), 'version two')

    def test_bootstrap_refuses_conflicting_catalogue_state(self):
        with tempfile.TemporaryDirectory() as folder:
            state = Path(folder)
            (state / 'skills').mkdir()
            with self.assertRaisesRegex(ValueError, 'conflicts'):
                entrypoint.claude_catalogues(state)
            self.assertTrue((state / 'skills').is_dir())


@unittest.skipUnless(shutil.which('docker'), 'Docker CLI is needed to resolve Compose offline')
class NestedComposeChecks(unittest.TestCase):
    def config(self, job_id):
        env = {key: value for key, value in os.environ.items()
               if not key.startswith(('SDLC_', 'COMPOSE_'))}
        env.update(SDLC_JOB_ID=job_id, SDLC_REPOSITORY='example-org/example-repo',
                   SDLC_GITHUB_LOGIN='example-user', SDLC_GIT_NAME='Example User',
                   SDLC_GIT_EMAIL='example@example.invalid')
        command = ['docker', 'compose', '--project-name', 'sdlc-offline',
                   '-f', str(ROOT / 'runtime/compose.yaml'),
                   '-f', str(ROOT / 'runtime/compose.job.yaml'),
                   '-f', str(ROOT / 'runtime/compose.docker-tests.yaml'),
                   'config', '--format', 'json']
        result = subprocess.run(command, env=env, capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def test_worker_localhost_and_bind_paths_share_the_daemon_context(self):
        config = self.config('first-job')
        worker, engine = (config['services'][name] for name in ('worker', 'docker-engine'))
        self.assertEqual(worker['network_mode'], 'service:docker-engine')
        worker_workspace = next(item for item in worker['volumes'] if item['target'] == '/workspace')
        engine_workspace = next(item for item in engine['volumes'] if item['target'] == '/workspace')
        self.assertEqual(worker_workspace, engine_workspace)
        self.assertTrue(engine['privileged'])
        self.assertFalse(worker.get('privileged', False))
        self.assertFalse(engine.get('ports'))
        self.assertFalse(engine.get('secrets'))
        self.assertEqual({item['type'] for item in engine['volumes']}, {'volume'})
        self.assertEqual({item['source'] for item in engine['volumes']},
                         {'workspace', 'docker-data', 'docker-socket'})
        self.assertIn('--host=unix:///run/job-docker/docker.sock', engine['command'][0])
        self.assertNotIn('tcp://', engine['command'][0])
        self.assertEqual(worker['environment']['DOCKER_HOST'], 'unix:///run/job-docker/docker.sock')

    def test_job_workspaces_and_daemons_change_while_provider_login_volumes_persist(self):
        first, second = (self.config(name) for name in ('first-job', 'second-job'))
        for name in ('workspace', 'docker-data', 'docker-socket'):
            self.assertNotEqual(first['volumes'][name]['name'], second['volumes'][name]['name'])
        for name in ('codex-state', 'claude-state', 't3-state'):
            self.assertEqual(first['volumes'][name]['name'], second['volumes'][name]['name'])


if __name__ == '__main__':
    unittest.main()
