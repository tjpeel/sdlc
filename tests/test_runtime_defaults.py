"""Offline checks for per-container Codex defaults."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('codex_entrypoint', ROOT / 'runtime/entrypoint.py')
entrypoint = importlib.util.module_from_spec(spec)
spec.loader.exec_module(entrypoint)


class CodexDefaultsChecks(unittest.TestCase):
    def test_model_defaults_are_replaced_and_cleared(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'etc/codex/config.toml'
            entrypoint.codex_defaults('example-model', path)
            self.assertEqual(path.read_text(), 'model = "example-model"\n')
            entrypoint.codex_defaults('other-model', path)
            self.assertEqual(path.read_text(), 'model = "other-model"\n')
            entrypoint.codex_defaults('', path)
            self.assertEqual(path.read_text(), '')

    def test_model_is_escaped_as_one_toml_string(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'config.toml'
            model = 'provider/quoted"módel🧪'
            entrypoint.codex_defaults(model, path)
            self.assertEqual(json.loads(path.read_text().removeprefix('model = ')), model)
            self.assertEqual(len(path.read_text().splitlines()), 1)

    def test_invalid_environment_model_is_rejected(self):
        with tempfile.TemporaryDirectory() as folder:
            for model in ('-option', 'two models', 'line\nbreak', 'control\x00', 'control\x7f',
                          'invisible\u200b'):
                with self.subTest(model=model), self.assertRaises(ValueError):
                    entrypoint.codex_defaults(model, Path(folder) / 'config.toml')


if __name__ == '__main__':
    unittest.main()
