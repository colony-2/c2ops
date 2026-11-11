"""Failure and evidence regressions for the native verification hook."""
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('native_verify', Path(__file__).with_name('verify.py'))
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


class VerificationTests(unittest.TestCase):
    def test_command_failures_timeout_and_independent_continuation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with (root / 'checks.log').open('wb') as log:
                checks = verify.Checks(root, dict(os.environ), log, timeout=5)
                self.assertFalse(checks.run(['/no/such/verification-tool']))
                self.assertFalse(checks.run([sys.executable, '-c', 'raise SystemExit(3)']))
                checks.timeout = 0.1
                self.assertFalse(checks.run([sys.executable, '-c', 'import time;time.sleep(20)']))
                checks.timeout = 5
                self.assertTrue(checks.run([sys.executable, '-c', 'print("independent check ran")']))
            self.assertEqual([c['returncode'] for c in checks.commands], [127, 3, 124, 0])
            self.assertIn('independent check ran', (root / 'checks.log').read_text())

    def fixtures(self, root):
        for name in verify.TASKS:
            log = (name + ' evidence\n').encode()
            (root / (name + '.log')).write_bytes(log)
            (root / (name + '.json')).write_text(json.dumps({
                'task': name, 'status': 'passed', 'commands': [{'argv': ['fixture'], 'returncode': 0}],
                'log_sha256': hashlib.sha256(log).hexdigest()}))

    def aggregate(self, root):
        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            verify.aggregate(root, root / 'out', {name: 'passed' for name in verify.TASKS})
        return stdout.getvalue()

    def test_baseline_failure_and_evidence_gate(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.fixtures(root)
            self.assertEqual(self.aggregate(root), 'passed')
            with contextlib.redirect_stdout(io.StringIO()) as bad_stdout:
                verify.aggregate(root, root / 'out', {name: 'malformed' for name in verify.TASKS})
            self.assertEqual(bad_stdout.getvalue(), 'failed')
            manifest = root / 'manifests.json'
            data = json.loads(manifest.read_text())
            data['commands'][0]['returncode'] = 1
            data['status'] = 'failed'
            manifest.write_text(json.dumps(data))
            self.assertEqual(self.aggregate(root), 'failed')
            self.fixtures(root)
            manifest.write_text('malformed')
            self.assertEqual(self.aggregate(root), 'failed')
            self.fixtures(root)
            manifest.unlink()
            self.assertEqual(self.aggregate(root), 'failed')
            self.fixtures(root)
            (root / 'manifests.log').write_text('tampered evidence')
            self.assertEqual(self.aggregate(root), 'failed')
            self.fixtures(root)
            data = json.loads(manifest.read_text())
            data['commands'][0].pop('returncode')
            manifest.write_text(json.dumps(data))
            self.assertEqual(self.aggregate(root), 'failed')

    def test_mandatory_failure_and_shell_like_paths_preserve_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / 'candidate $(touch injected)'
            root.mkdir()
            (root / 'Makefile').write_text('check-manifests:\n\t@echo forced mandatory failure >&2\n\t@printf mutation > linked-input\n\t@exit 9\n')
            (root / 'nix').mkdir()
            (root / 'nix/pnpm-build-policy.json').write_text('{}')
            sentinel = root / 'untracked.txt'
            sentinel.write_text('preserve me')
            external = Path(directory) / 'external.txt'
            external.write_text('external candidate input')
            (root / 'linked-input').symlink_to(external)
            out = Path(directory) / 'out; false'
            before = {str(p): p.read_bytes() for p in root.rglob('*') if p.is_file()}
            with contextlib.redirect_stdout(io.StringIO()) as stdout, patch.object(verify, 'supported_platform', return_value=True):
                verify.check('manifests', root, out)
            self.assertEqual(stdout.getvalue(), 'failed')
            self.assertEqual(before, {str(p): p.read_bytes() for p in root.rglob('*') if p.is_file()})
            self.assertFalse((Path(directory) / 'injected').exists())
            self.assertEqual(external.read_text(), 'external candidate input')
            data = json.loads((out / 'manifests.json').read_text())
            self.assertTrue(any(c.get('returncode') != 0 for c in data['commands']))
            self.assertIn('forced mandatory failure', (out / 'manifests.log').read_text())

    def test_pydantic_shared_suites_and_aggregate(self):
        # Execute the real Makefile and runner. Only package preparation and
        # tool probes are stubbed; suite subprocess failures are real exits.
        source = Path(verify.__file__).resolve().parents[1]
        for failing in ('none', 'go', 'python'):
            with self.subTest(failing=failing), tempfile.TemporaryDirectory() as directory:
                root = Path(directory) / 'candidate'
                for name in ('pydantic', 'scripts', 'nix'):
                    (root / name).mkdir(parents=True)
                for name in ('pydantic/Makefile', 'scripts/pydantic_test.py', 'flake.lock', 'nix/pnpm-build-policy.json'):
                    (root / name).write_bytes((source / name).read_bytes())
                tools = Path(directory) / 'tools'; tools.mkdir()
                for name in ('go', 'python'):
                    path = tools / name
                    path.write_text('#!/bin/sh\necho "injected suite '+name+'"\nexit '+('9' if name == failing else '0')+'\n')
                    path.chmod(0o755)
                (tools / 'bin').mkdir()
                (tools / 'bin/python').symlink_to(tools / 'python')
                out = Path(directory) / 'evidence'; out.mkdir()
                self.fixtures(out)
                original = verify.Checks.run
                def run(checks, argv, directory='.', input_text=None):
                    if argv[:3] == ['make', '-C', 'pydantic']:
                        return original(checks, argv, directory, input_text)
                    checks.commands.append({'argv': argv, 'returncode': 0})
                    return True
                def metadata(checks, selector, expression):
                    if 'pyyaml' in selector:
                        return {'path':str(tools), 'site':'.'}
                    return {'path':str(tools), 'python':str(tools), 'drv':'fixture'}
                with patch.object(verify, 'supported_platform', return_value=True), \
                     patch.object(verify.Checks, 'run', run), \
                     patch.object(verify.Checks, 'metadata', metadata), \
                     patch.dict(os.environ, {'PATH':str(tools)+os.pathsep+os.environ['PATH']}), \
                     contextlib.redirect_stdout(io.StringIO()) as stdout:
                    verify.check('pydantic', root, out)
                expected = 'passed' if failing == 'none' else 'failed'
                self.assertEqual(stdout.getvalue(), expected)
                report = json.loads((out / 'pydantic.json').read_text())
                shared = [c for c in report['commands'] if c['argv'] == ['make', '-C', 'pydantic', 'test']]
                self.assertEqual(len(shared), 1)
                self.assertEqual(shared[0]['returncode'] == 0, failing == 'none')
                log = (out / 'pydantic.log').read_text()
                self.assertIn('injected suite go', log)
                self.assertIn('injected suite python', log)
                self.assertIn('"go", "test"', log)
                self.assertIn('pydantic/test_binding.py', log)
                statuses = {name:'passed' for name in verify.TASKS}; statuses['pydantic'] = expected
                with contextlib.redirect_stdout(io.StringIO()) as aggregate_stdout:
                    verify.aggregate(out, out / 'aggregate', statuses)
                self.assertEqual(aggregate_stdout.getvalue(), expected)

    def test_cancellation_terminates_child_group(self):
        with tempfile.TemporaryDirectory() as directory:
            pidfile = Path(directory) / 'child.pid'
            driver = """import importlib.util,os,pathlib,sys
s=importlib.util.spec_from_file_location('v',sys.argv[1]);v=importlib.util.module_from_spec(s);s.loader.exec_module(v)
import signal
signal.signal(signal.SIGTERM,v.stop)
with open(sys.argv[2]+'.log','wb') as log:
 c=v.Checks(pathlib.Path('.'),dict(os.environ),log)
 c.run([sys.executable,'-c','import os,pathlib,time;pathlib.Path('+repr(sys.argv[2])+').write_text(str(os.getpid()));time.sleep(60)'])
"""
            proc = subprocess.Popen([sys.executable, '-c', driver, str(Path(verify.__file__)), str(pidfile)])
            import time
            deadline = time.monotonic() + 5
            while (not pidfile.exists() or not pidfile.read_text()) and time.monotonic() < deadline:
                time.sleep(0.01)
            try:
                self.assertTrue(pidfile.exists())
                child = int(pidfile.read_text())
                proc.terminate()
                self.assertEqual(proc.wait(timeout=5), 128 + signal.SIGTERM)
                # A killed child may briefly remain a zombie until init reaps it.
                stat = Path('/proc') / str(child) / 'stat'
                self.assertTrue(not stat.exists() or stat.read_text().split()[2] == 'Z')
            finally:
                if proc.poll() is None:
                    proc.kill()
                    proc.wait()


if __name__ == '__main__':
    unittest.main()
