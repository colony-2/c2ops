"""Exercise root Makefile dispatch with the multiline op list used by CI."""

import os
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
TARGETS = ('build', 'test', 'install-test-deps')


def make_env():
    env = os.environ.copy()
    for name in ('MAKEFLAGS', 'MFLAGS', 'MAKEOVERRIDES', 'MAKELEVEL', 'OP_DIRS'):
        env.pop(name, None)
    return env


def check_entrypoints(root):
    """Probe the real discovery rule, independently of CI's OP_DIRS filter."""
    operations = sorted(path.parent for path in root.glob('*/op.yaml'))
    if not operations:
        raise AssertionError('no operations discovered')
    for operation in operations:
        if not (operation / 'Makefile').is_file():
            raise AssertionError(f'{operation.name}: missing Makefile')
        for target in TARGETS:
            result = subprocess.run(['make', '--no-print-directory', '-n', target],
                                    cwd=operation, env=make_env(), capture_output=True,
                                    text=True, timeout=30)
            if result.returncode:
                raise AssertionError(f'{operation.name}: {target}: {result.stdout}{result.stderr}')


class OperationEntrypointTests(unittest.TestCase):
    def copy_inventory(self, root):
        for manifest in ROOT.glob('*/op.yaml'):
            operation = root / manifest.parent.name
            operation.mkdir()
            shutil.copy2(manifest, operation / 'op.yaml')
            makefile = manifest.parent / 'Makefile'
            if makefile.exists():
                shutil.copy2(makefile, operation / 'Makefile')

    def test_real_inventory(self):
        with patch.dict(os.environ, {'OP_DIRS': 'llm'}):
            check_entrypoints(ROOT)

    def test_dry_runs_create_no_outputs(self):
        # Includes recursive Make recipes. Dry-run is not a general sandbox;
        # compare all scratch files to catch actual build/install side effects.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.copy_inventory(root)
            before = {str(p.relative_to(root)): p.read_bytes() if p.is_file() else None
                      for p in root.rglob('*')}
            check_entrypoints(root)
            self.assertEqual(before, {str(p.relative_to(root)): p.read_bytes() if p.is_file() else None
                                     for p in root.rglob('*')})

    def test_missing_makefile_and_target(self):
        for target in (None, *TARGETS):
            with self.subTest(target=target), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                self.copy_inventory(root)
                makefile = root / 'github_release/Makefile'
                if target is None:
                    makefile.unlink()
                else:
                    makefile.write_text('\n'.join(f'{name}:\n\t@true' for name in TARGETS if name != target))
                with self.assertRaisesRegex(AssertionError, 'github_release: ' + (target or 'missing Makefile')):
                    check_entrypoints(root)

    def test_github_release_recipes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shutil.copy2(ROOT / 'github_release/Makefile', root / 'Makefile')
            go = root / 'go'
            go.write_text('#!' + sys.executable + '\nimport json,sys\n'
                          + 'with open("calls", "a") as f: f.write(json.dumps(sys.argv[1:])+"\\n")\n')
            go.chmod(0o755)
            env = make_env()
            env['PATH'] = str(root) + os.pathsep + env['PATH']
            for target in TARGETS:
                result = subprocess.run(['make', target], cwd=root, env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual([json.loads(line) for line in (root / 'calls').read_text().splitlines()], [
                ['build', '-mod=readonly', '-o', '../bin/github-release', '.'],
                ['test', '-mod=readonly', '-count=1', './...']])


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
        env = make_env()
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

    def test_local_build_and_tests_select_same_flake(self):
        bin_dir = self.root / 'bin'
        bin_dir.mkdir()
        nix = bin_dir / 'nix'
        nix.write_text('#!/bin/sh\nprintf "%s\\n" "${4:-.}" >> references\n')
        nix.chmod(0o755)
        with (self.root / 'Makefile').open('a') as makefile:
            makefile.write('\ntest-tools:\n\t@true\n'
                           '\ntest:\n\t@echo "$$C2OPS_TEST_FLAKE" >> references\n')
        with patch.dict(os.environ, {'PATH': str(bin_dir) + os.pathsep + os.environ['PATH']}):
            self.run_command('make', '-o', 'test-tools', 'test-local')
        references = (self.root / 'references').read_text().splitlines()
        self.assertEqual(len(references), 2)
        self.assertEqual(references[0], references[1])
