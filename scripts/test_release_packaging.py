"""X07: release.pack keeps catalog, schemas and native verification in sync."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('release_verify', ROOT / 'scripts/verify.py')
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


class ReleasePackagingTests(unittest.TestCase):
    def setUp(self):
        # Synthetic aggregate receipts must not inherit the caller's identity.
        environment = patch.dict(os.environ)
        environment.start()
        self.addCleanup(environment.stop)
        for name in tuple(os.environ):
            if name.startswith('VERIFY_'):
                del os.environ[name]

    def test_catalog_and_generated_manifest(self):
        catalog = json.loads((ROOT / 'nix/ops.json').read_text())
        self.assertEqual(catalog['release-pack'], dict(path='release_pack', kind='go', runtime=['gitMinimal']))
        self.assertEqual(catalog['skill-run']['path'], 'codex/run_skill')
        for old in ('llm', 'llm2', 'gha', 'gha-many', 'gha-job', 'gha-workflow', 'rule_gate'):
            self.assertEqual(catalog[old]['kind'], 'go')
        source = json.loads((ROOT / 'release_pack/op.yaml').read_text())  # JSON is YAML
        generated = json.loads((ROOT / 'release_pack/op.json').read_text())
        source['command'] = ['bin/release-pack']
        del source['dependencies']
        self.assertEqual(source, generated)
        self.assertEqual(generated['name'], 'release.pack')
        self.assertIsNone(json.loads((ROOT / 'nix/go-vendor-hashes.json').read_text())['release_pack'])

    def test_installed_release_pack_name_mapping(self):
        import subprocess
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'bin').mkdir()
            (root / 'share/c2j').mkdir(parents=True)
            manifest = dict(name='release.pack', command=['bin/release-pack'])
            path = root / 'share/c2j/op.json'
            path.write_text(json.dumps(manifest))
            command = root / 'bin/release-pack'
            command.write_text('#!/bin/sh\necho "missing required input" >&2\nexit 1\n')
            command.chmod(0o755)
            result = subprocess.run([sys.executable, str(ROOT / 'scripts/check-package.py'),
                str(root), str(path), 'nix:github:colony-2/c2ops/published#release-pack'],
                capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_verify_task_and_failure_are_aggregated(self):
        self.assertIn('release-pack', verify.TASKS)
        import re
        order = re.findall(r'^- id: ([a-z0-9_]+)$', (ROOT / '.c2/recipes/hooks/build/verify/native.yaml').read_text(), re.M)
        self.assertEqual(['npm.publish' if name == 'npm_publish' else name if name == verify.LIVE_TASK else name.replace('_','-')
                          for name in order[:-1]], list(verify.ALL_TASKS))
        # Actual command failure must remain visible even when other tasks pass.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'release_pack').mkdir()
            with (root / 'test.log').open('wb') as log:
                checks = verify.Checks(root, {}, log)
                with patch.object(checks, 'go', return_value=False) as run:
                    verify.execute('release-pack', checks, race=False)
                    run.assert_called_once_with('release_pack', ['-race', './...'])
            self.assertIn('release_pack', (ROOT / '.c2/recipes/hooks/build/verify/native.yaml').read_text())
            # The aggregate checks every configured task, its stdout, evidence
            # and command status. A real failing command drives this fixture.
            import hashlib
            statuses = {name: 'pending' if name == verify.LIVE_TASK else 'passed' for name in verify.ALL_TASKS}
            for name in verify.ALL_TASKS:
                data = (name + ' log\n').encode()
                status = 'passed'
                commands = [{'argv':['fixture'], 'returncode':0}]
                if name == verify.LIVE_TASK:
                    status = 'pending'; commands = []
                if name == 'release-pack':
                    with (root / 'command.log').open('wb') as log:
                        failing = verify.Checks(root, {}, log)
                        self.assertFalse(failing.run([sys.executable, '-c', 'raise SystemExit(7)']))
                    commands = failing.commands
                    status = statuses[name] = 'failed'
                (root / (name + '.log')).write_bytes(data)
                (root / (name + '.json')).write_text(json.dumps(dict(task=name, status=status,
                    commands=commands, log_sha256=hashlib.sha256(data).hexdigest())))
            with contextlib.redirect_stdout(io.StringIO()) as result:
                verify.aggregate(root, root / 'out', statuses)
            self.assertEqual(result.getvalue(), 'failed')
            report = json.loads((root / 'out/verification.json').read_text())
            self.assertIn(dict(task='release-pack', status='failed'), report['tasks'])
            self.assertEqual(report['errors'], [])

    def test_hook_exports_task_artifacts_and_status(self):
        hook = (ROOT / '.c2/recipes/hooks/build/verify/native.yaml').read_text()
        self.assertIn('check release-pack', hook)
        self.assertIn('release.pack archive test (release_pack / release-pack): ${{ sequence.release_pack.outputs.stdout }}.', hook)
        self.assertIn('Verification: ${{ sequence.report.outputs.stdout }}.', hook)
        self.assertIn('VERIFY_STATUS_RELEASE_PACK: ${{ sequence.release_pack.outputs.stdout }}', hook)
        for suffix in ('log','json'):
            self.assertIn(f"release-pack.{suffix}: ${{{{ sequence.release_pack.artifacts['release-pack.{suffix}'] }}}}", hook)
            self.assertIn(f"''release-pack.{suffix}'': sequence.release_pack.artifacts[''release-pack.{suffix}'']", hook)


if __name__ == '__main__':
    unittest.main()
