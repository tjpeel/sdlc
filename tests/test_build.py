"""Build overrides must not replace the tracked dependency defaults."""
import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('build_launcher', ROOT / 'scripts/sdlc.py')
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)


class BuildOverrides(unittest.TestCase):
    def test_omitted_overrides_use_dockerfile_defaults(self):
        self.assertEqual(launcher.build_arguments({}), [])

    def test_explicit_catalogue_overrides_are_forwarded(self):
        self.assertEqual(launcher.build_arguments({
            'SDLC_SKILLS_REVISION': 'a' * 40, 'SDLC_AGENTS_REVISION': 'b' * 40,
        }), ['--build-arg', 'SKILLS_REVISION=' + 'a' * 40,
             '--build-arg', 'AGENTS_REVISION=' + 'b' * 40])

    def test_invalid_overrides_fail_before_build(self):
        for value in ('', 'main', 'a' * 39, 'A' * 40, 'g' * 40, 'a' * 40 + '\n'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                launcher.build_arguments({'SDLC_SKILLS_REVISION': value})


if __name__ == '__main__':
    unittest.main()
