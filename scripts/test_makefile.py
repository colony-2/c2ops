"""Exercise root Makefile dispatch with the multiline op list used by CI."""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class MakefileTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix='c2ops-make-')
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        shutil.copy2(ROOT / 'Makefile', self.root / 'Makefile')
        for name in ('alpha', 'beta', 'gha'):
            module = self.root / name
            module.mkdir()
            (module / 'op.yaml').touch()
            (module / 'Makefile').write_text(
                '.PHONY: build test install-test-deps\n'
                'build test install-test-deps:\n'
                f'\t@echo "{name}:$@" >> ../calls\n'
            )

    def run_command(self, *args):
        env = os.environ.copy()
        # The outer make test may supply OP_DIRS; fixtures use their own modules.
        for name in ('MAKEFLAGS', 'MFLAGS', 'MAKEOVERRIDES', 'MAKELEVEL'):
            env.pop(name, None)
        result = subprocess.run(args, cwd=self.root, env=env,
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result.stdout

    def test_ci_multiline_selection(self):
        for target in ('build', 'test', 'install-test-deps'):
            with self.subTest(target=target):
                # Skip test-tools: this fixture tests dispatch, not this suite itself.
                self.run_command(
                    'bash', '-c',
                    f'make -o test-tools {target} OP_DIRS="$(make ops | sed \'/^gha$/d\')"',
                )
                calls = self.root / 'calls'
                self.assertEqual(calls.read_text().splitlines(),
                                 [f'alpha:{target}', f'beta:{target}'])
                calls.unlink()

    def test_ops_accepts_multiline_override(self):
        output = self.run_command('make', '--no-print-directory', 'ops',
                                  'OP_DIRS=beta\nalpha')
        self.assertEqual(output.splitlines(), ['beta', 'alpha'])

    def test_empty_selection(self):
        self.run_command('make', '-o', 'test-tools', 'test', 'OP_DIRS=')
        self.assertFalse((self.root / 'calls').exists())
