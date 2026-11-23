"""Check clean/dirty/path release metadata by evaluating actual derivations."""
import contextlib
import io
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
NIX = ['nix', '--extra-experimental-features', 'nix-command flakes']


def run(*args, cwd=None):
    try:
        return subprocess.check_output(args, cwd=cwd, text=True, stderr=subprocess.PIPE).strip()
    except subprocess.CalledProcessError as error:
        if error.stderr:
            print(error.stderr, file=sys.stderr, end="")
        raise


def derivation(path):
    data = json.loads(run(*NIX, 'derivation', 'show', path))
    return next(iter(data.get('derivations', data).values()))


class GHAReleaseTests(unittest.TestCase):
    def test_command_failure_reports_stderr(self):
        with contextlib.redirect_stderr(io.StringIO()) as stderr:
            with self.assertRaises(subprocess.CalledProcessError) as failure:
                run(sys.executable, '-c', 'import sys; sys.stderr.write("fixture error\\n"); sys.exit(7)')
        self.assertEqual(failure.exception.returncode, 7)
        self.assertEqual(stderr.getvalue(), 'fixture error\n')

    def test_optional_string_schemas_have_no_mutable_defaults(self):
        # Regeneration compares source schemas with the generated schemas whose
        # exact string types reject JSON null; no presence distinction is added.
        run('uv', 'run', 'scripts/manifests.py', '--check', cwd=ROOT)
        run('uv', 'run', '--with', 'jsonschema==4.26.0', '--with', 'pyyaml==6.0.3',
            'python', '-c', """
import json
from pathlib import Path
import yaml
from jsonschema import Draft202012Validator
for name in ('gha', 'gha/workflow'):
    for extension in ('json', 'yaml'):
        manifest = yaml.safe_load((Path(name) / ('op.' + extension)).read_text())
        for field in ('helper_op', 'executor_op'):
            validator = Draft202012Validator(manifest['input_schema']['properties'][field])
            assert not validator.is_valid(None), (name, extension, field)
            assert validator.is_valid(''), (name, extension, field)
""", cwd=ROOT)
        for name in ('gha', 'gha/workflow'):
            manifest = json.loads((ROOT / name / 'op.json').read_text())
            schema = manifest['input_schema']
            for field in ('helper_op', 'executor_op'):
                self.assertEqual(schema['properties'][field], {'type': 'string'})
                self.assertNotIn(field, schema.get('required', []))

    @unittest.skipUnless(shutil.which('nix'), 'release metadata evaluation requires nix on PATH')
    def test_release_metadata_in_real_nix_derivations(self):
        with tempfile.TemporaryDirectory(prefix='gha-release-eval-') as directory:
            # Exercise the symlinked temporary-directory layout used on macOS
            # (/var -> /private/var) on every platform. Git flake references must
            # use the physical checkout path, including for dirty evaluations.
            physical = Path(directory) / 'physical'
            physical.mkdir()
            linked = Path(directory) / 'linked'
            linked.symlink_to(physical.resolve(), target_is_directory=True)
            root = (linked / 'source').resolve()
            # Dependency caches can contain dangling links into the original
            # .git directory and are not inputs to the release derivations.
            shutil.copytree(ROOT, root, ignore=shutil.ignore_patterns(
                '.git', '__pycache__', '.pytest_cache', '.pnpm-store', 'node_modules'))
            def git(*args):
                return run('git', '-C', str(root), *args)
            git('init', '-b', 'main')
            git('add', '.')
            def commit():
                git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.test',
                    '-c', 'commit.gpgsign=false', 'commit', '--allow-empty', '-m', 'release')
                return git('rev-parse', 'HEAD')
            first_rev = commit()
            system = run(*NIX, 'eval', '--impure', '--raw', '--expr', 'builtins.currentSystem')
            def packages(url, direct=False):
                if direct:
                    expr = f'''let f = builtins.getFlake {json.dumps(url)};
                      p = import (f.outPath + "/nix/packages.nix") {{ pkgs = f.inputs.nixpkgs.legacyPackages.{system}; }};
                      in builtins.mapAttrs (_: v: v.drvPath) {{ inherit (p) gha gha-workflow gha-job rule_gate; }}'''
                    paths = json.loads(run(*NIX, 'eval', '--impure', '--json', '--expr', expr))
                else:
                    paths = json.loads(run(*NIX, 'eval', '--json',
                                           url + '#packages.' + system, '--apply',
                                           'ps: builtins.mapAttrs (_: v: v.drvPath) '
                                           '{ inherit (ps) gha gha-workflow gha-job rule_gate; }'))
                result = {}
                for name, path in paths.items():
                    top = derivation(path)
                    inputs = top.get('inputDrvs', top.get('inputs', {}).get('drvs', {}))
                    binary = next(p for p in inputs if p.endswith(f'-c2ops-{name}-binary-0.1.0.drv'))
                    binary_path = binary if binary.startswith('/') else '/nix/store/' + binary
                    result[name] = (path, binary_path, derivation(binary_path)['env']['ldflags'])
                return result
            url = 'git+' + root.as_uri()
            first = packages(url)
            second_rev = commit()
            # The build/check steps resolve an ordinary checkout path as Git.
            second = packages(str(root))
            selectors = json.loads(run('uv', 'run', '--with', 'pyyaml==6.0.3',
                                       'python', '-c', '''
import json, sys, yaml
workflow = yaml.safe_load(open(sys.argv[1]))
print(json.dumps({step['name']: step['env']['C2OPS_TEST_FLAKE']
                  for step in workflow['jobs']['test']['steps']
                  if 'C2OPS_TEST_FLAKE' in step.get('env', {})}))
''', str(ROOT / '.github/workflows/test.yml')))
            self.assertTrue(selectors, 'CI must test the checkout packages')
            for step, selector in selectors.items():
                selected = selector.replace('${{ github.workspace }}', str(root))
                selected = selected.replace('${{ github.sha }}', second_rev)
                with self.subTest(step=step):
                    self.assertEqual(second, packages(selected),
                                     'CI tests must reuse the exact prebuilt derivations')
            for name in ('gha', 'gha-workflow'):
                self.assertIn('releaseRepository=github:colony-2/c2ops', first[name][2])
                self.assertIn('releaseRevision=' + first_rev, first[name][2])
                self.assertIn('releaseRevision=' + second_rev, second[name][2])
                self.assertNotEqual(first[name][:2], second[name][:2])
            for name in ('gha-job', 'rule_gate'):
                self.assertEqual(first[name], second[name])
                self.assertNotIn('releaseRepository', first[name][2])
                self.assertNotIn('releaseRevision', first[name][2])
            # A tracked dirty file removes self.rev; a path input and a direct
            # import also have no revision, even when the checkout is clean.
            (root / 'README.md').write_text((root / 'README.md').read_text() + '\nfixture\n')
            dirty = packages(url)
            git('restore', 'README.md')
            path = packages('path:' + str(root))
            direct = packages(url, direct=True)
            for evaluation in (dirty, path, direct):
                for name in ('gha', 'gha-workflow'):
                    self.assertIn('releaseRevision=', evaluation[name][2])
                    self.assertNotIn(second_rev, evaluation[name][2])
                for name in ('gha-job', 'rule_gate'):
                    self.assertEqual(first[name], evaluation[name])


if __name__ == '__main__':
    unittest.main()
