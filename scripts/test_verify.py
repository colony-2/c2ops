"""Failure and evidence regressions for the native verification hook."""
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
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
    def test_gha_cli_without_explicit_race_fails_task_and_aggregate(self):
        for task in ('gha', 'gha-runtime'):
            with self.subTest(task=task), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                self.fixtures(root)
                result = subprocess.run([sys.executable, verify.__file__, 'check', task],
                    env={**os.environ, 'VERIFY_ROOT': str(root), 'VERIFY_OUTBOX': str(root)},
                    capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode, 0, 'ordinary failure is exported as hook evidence')
                self.assertEqual(result.stdout, 'failed')
                receipt = json.loads((root / (task + '.json')).read_text())
                self.assertIn('requires --race', receipt['error'])
                self.assertFalse(receipt['race'])
                self.assertEqual(receipt['commands'], [], 'omitted race mode must fail before running tools')
                statuses = {name: 'passed' for name in verify.TASKS}
                statuses[task] = result.stdout
                with contextlib.redirect_stdout(io.StringIO()) as stdout:
                    verify.aggregate(root, root / 'aggregate', statuses)
                self.assertEqual(stdout.getvalue(), 'failed')

    def test_gha_tasks_select_pinned_compiler_and_fail_closed(self):
        for task in ('gha', 'gha-runtime'):
            for available in (True, False):
                with self.subTest(task=task, available=available), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory) / 'candidate'
                    (root / 'nix').mkdir(parents=True)
                    (root / 'gha/integration').mkdir(parents=True)
                    (root / 'nix/pnpm-build-policy.json').write_text('{}')
                    tools = Path(directory) / 'tools'; tools.mkdir()
                    compiler = Path(directory) / 'compiler'
                    (compiler / 'bin').mkdir(parents=True)
                    old_marker = Path(directory) / 'old-go-ran'
                    old_go = tools / 'go'
                    old_go.write_text('#!' + sys.executable + '\nfrom pathlib import Path\nPath(' + repr(str(old_marker)) + ').touch()\n')
                    old_go.chmod(0o755)
                    pinned_go = compiler / 'bin/go'
                    pinned_go.write_text('#!' + sys.executable + '\nimport os,sys\nprint("pinned GHA compiler")\n'
                        + 'if "test" in sys.argv:\n'
                        + ' assert "-race" in sys.argv\n'
                        + (' assert os.environ.get("C2OPS_TEST_GHA_RACE") == "1"\n' if task == 'gha-runtime' else '')
                        + 'raise SystemExit(9 if "test" in sys.argv else 0)\n')
                    pinned_go.chmod(0o755)
                    nix = tools / 'nix'
                    nix.write_text('#!' + sys.executable + '\nimport json,sys\n'
                        + 'if "eval" in sys.argv: print(json.dumps(' + repr(str(compiler)) + '))\n'
                        + 'raise SystemExit(' + ('0' if available else '1') + ' if "build" in sys.argv else 0)\n')
                    nix.chmod(0o755)
                    out = Path(directory) / 'out'
                    with patch.dict(os.environ, {'PATH':str(tools)+os.pathsep+os.environ['PATH']}), \
                         patch.object(verify, 'supported_platform', return_value=True), \
                         contextlib.redirect_stdout(io.StringIO()) as stdout:
                        verify.check(task, root, out, race=True)
                    self.assertEqual(stdout.getvalue(), 'failed')
                    self.assertFalse(old_marker.exists(), 'old worker Go must never run for GHA')
                    report = json.loads((out / (task + '.json')).read_text())
                    self.assertTrue(report['race'])
                    builds = [c for c in report['commands'] if 'build' in c['argv']]
                    self.assertEqual(len(builds), 1)
                    self.assertIn('--max-jobs', builds[0]['argv'])
                    self.assertIn('0', builds[0]['argv'])
                    suites = [c for c in report['commands'] if c['argv'][:2] == ['go', 'test']]
                    if available:
                        self.assertEqual(report['tools']['go'], str(pinned_go))
                        self.assertEqual(len(suites), len(verify.GHA_RUNTIME_PARTITIONS) if task == 'gha-runtime' else 1)
                        for suite in suites:
                            self.assertIn('-race', suite['argv'])
                            self.assertEqual(suite['returncode'], 9, 'suite failure must remain a failure')
                        if task == 'gha-runtime':
                            for suite, (flag, pattern) in zip(suites, verify.GHA_RUNTIME_PARTITIONS):
                                self.assertEqual(suite['argv'][suite['argv'].index(flag) + 1], pattern)
                    else:
                        self.assertEqual(suites, [], 'missing pinned compiler must block the suite')
                        self.assertIn('pinned GHA compiler', report['error'])

    def test_each_gha_runtime_partition_failure_remains_visible(self):
        for failed_pattern in (p for _, p in verify.GHA_RUNTIME_PARTITIONS):
            with self.subTest(failed_pattern=failed_pattern), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / 'gha/integration').mkdir(parents=True)
                go = root / 'go'
                go.write_text('#!' + sys.executable + '\nimport sys\n'
                              + 'raise SystemExit(9 if ' + repr(failed_pattern) + ' in sys.argv else 0)\n')
                go.chmod(0o755)
                with (root / 'runtime.log').open('wb') as log, patch.dict(os.environ, {'PATH':str(root)+os.pathsep+os.environ['PATH']}):
                    checks = verify.Checks(root, dict(os.environ), log)
                    verify.execute('gha-runtime', checks, race=True)
                self.assertEqual(len(checks.commands), len(verify.GHA_RUNTIME_PARTITIONS))
                self.assertEqual(sorted(c['returncode'] for c in checks.commands),
                                 [0] * (len(verify.GHA_RUNTIME_PARTITIONS) - 1) + [9])
                self.assertFalse(all(c['returncode'] == 0 for c in checks.commands))
                self.assertEqual(checks.env['C2OPS_TEST_GHA_RACE'], '1')
                self.assertTrue(all('-race' in c['argv'] and '-v' in c['argv'] and '-timeout=8m' in c['argv'] for c in checks.commands))

    def test_runtime_partitions_include_every_test_once(self):
        source = Path(verify.__file__).resolve().parents[1] / 'gha/integration'
        names = [name for path in source.glob('*_test.go')
                 for name in re.findall(r'^func (Test\w+)\(t \*testing\.T\)', path.read_text(), re.MULTILINE)]
        self.assertGreater(len(names), 30)
        # Include a future name to prove the complement cannot drop new tests.
        for name in [*names, 'TestFutureRuntimeContract']:
            matches = [bool(re.search(pattern, name)) == (flag == '-run')
                       for flag, pattern in verify.GHA_RUNTIME_PARTITIONS]
            self.assertEqual(sum(matches), 1, (name, matches))

    def test_gha_runtime_partitions_cover_every_test_once(self):
        integration = Path(__file__).resolve().parent.parent / 'gha/integration'
        names = [name for source in integration.glob('*_test.go')
                 for name in re.findall(r'^func (Test\w+)\(t \*testing.T\)', source.read_text(), re.M)]
        self.assertTrue(names)
        checks = verify.Checks(integration, {}, io.BytesIO())
        with patch.object(checks, 'go') as run:
            verify.execute('gha-runtime', checks, race=True)
        filters = []
        for call in run.call_args_list:
            directory, argv = call.args
            self.assertEqual(directory, 'gha/integration')
            flag = '-skip' if '-skip' in argv else '-run'
            filters.append((flag, re.compile(argv[argv.index(flag) + 1])))
        for name in [*names, 'TestFutureRuntimeFixture', 'TestSubmittedWorkflowFutureFixture']:
            matches = sum((pattern.search(name) is not None) == (flag == '-run')
                          for flag, pattern in filters)
            self.assertEqual(matches, 1, name + ' must run exactly once')
        for flag, pattern in filters:
            self.assertTrue(any((pattern.search(name) is not None) == (flag == '-run')
                                for name in names), 'partition must select real tests')

    def test_runtime_partitions_cover_each_actual_test_once(self):
        with tempfile.TemporaryDirectory() as directory, (Path(directory) / 'checks.log').open('wb') as log:
            checks = verify.Checks(Path(directory), {}, log)
            with patch.object(checks, 'go') as go:
                verify.execute('gha-runtime', checks, race=True)
            selections = []
            for call in go.call_args_list:
                argv = call.args[1]
                flag = '-skip' if '-skip' in argv else '-run'
                selections.append((flag, re.compile(argv[argv.index(flag) + 1])))
            names = []
            for path in Path(verify.__file__).resolve().parents[1].glob('gha/integration/*_test.go'):
                names.extend(re.findall(r'^func (Test\w+)\(t \*testing.T\)', path.read_text(), re.M))
            self.assertGreater(len(names), 30)
            for name in names:
                with self.subTest(name=name):
                    matches = [bool(pattern.search(name)) != (flag == '-skip') for flag, pattern in selections]
                    self.assertEqual(sum(matches), 1, 'every runtime test must run exactly once')

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

    def test_github_release_dispatch_and_hook_evidence(self):
        # A failed real suite must remain failed in the shared runner.
        source = Path(verify.__file__).resolve().parents[1]
        hook = (source / '.c2/recipes/hooks/build/verify.yaml').read_text()
        self.assertIn('github-release', verify.TASKS)
        for text in ('check github-release', "sequence.github_release.artifacts['github-release.json']",
                     'VERIFY_STATUS_GITHUB_RELEASE: ${{ sequence.github_release.outputs.stdout }}',
                     "''github-release.log'': sequence.github_release.artifacts[''github-release.log'']"):
            self.assertIn(text, hook)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'github_release').mkdir()
            go = root / 'go'
            go.write_text('#!' + sys.executable + '\nimport sys\n'
                          + 'assert "-race" in sys.argv\nraise SystemExit(9)\n')
            go.chmod(0o755)
            with (root / 'release.log').open('wb') as log:
                checks = verify.Checks(root, {**os.environ, 'PATH':str(root)+os.pathsep+os.environ['PATH']}, log)
                verify.execute('github-release', checks, race=False)
            self.assertEqual(len(checks.commands), 1)
            self.assertEqual(checks.commands[0]['directory'], 'github_release')
            self.assertEqual(checks.commands[0]['returncode'], 9)

    def test_installed_github_success_check_failure_propagates(self):
        # Run a real failing subprocess through the installed-check dispatch.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'nix').mkdir()
            (root / 'github_release').mkdir()
            (root / 'nix/ops.json').write_text('{"github-release":{"path":"github_release"}}')
            (root / 'flake.lock').write_text('{"nodes":{"nixpkgs":{"locked":{"rev":"fixture"}}}}')
            go = root / 'go'
            go.write_text('#!' + sys.executable + '\nimport os,sys\n'
                          + 'assert os.environ["C2OPS_GITHUB_INSTALLED_BINARY"] == "/fixture/bin/github-release"\n'
                          + 'assert "^TestC03InstalledPackageFromImmutableCWD$" in sys.argv\n'
                          + 'raise SystemExit(9)\n')
            go.chmod(0o755)
            def metadata(checks, selector, expression):
                if selector == '.#github-release':
                    return {'path':'/fixture', 'manifest':{}, 'compiled':None}
                return None
            original = verify.Checks.run
            def run(checks, argv, directory='.', input_text=None):
                if argv[0] == 'env':
                    return original(checks, argv, directory, input_text)
                checks.commands.append({'argv':argv, 'returncode':0})
                return True
            with (root / 'release.log').open('wb') as log, \
                 patch.object(verify.Checks, 'metadata', metadata), \
                 patch.object(verify.Checks, 'run', run):
                checks = verify.Checks(root, {**os.environ, 'PATH':str(root)+os.pathsep+os.environ['PATH']}, log)
                verify.execute('nix-process', checks, race=False)
            installed = [c for c in checks.commands if c['argv'][0] == 'env']
            self.assertEqual(len(installed), 1)
            self.assertEqual(installed[0]['returncode'], 9)
            self.assertEqual(installed[0]['directory'], 'github_release')
            self.assertFalse(all(c['returncode'] == 0 for c in checks.commands))

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
            # Generated caches may retain links into a removed Git checkout.
            # They must not prevent verification from reaching the source tests.
            for cache in ('.pnpm-store', 'node_modules'):
                (root / cache).mkdir()
                (root / cache / 'stale').symlink_to(root / '.git/missing')
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

    def test_go_build_suites_are_mandatory_and_aggregated(self):
        source = Path(verify.__file__).resolve().parents[1]
        hook = (source / '.c2/recipes/hooks/build/verify.yaml').read_text()
        for binding in ('check go-build', 'VERIFY_STATUS_GO_BUILD', "sequence.go_build.artifacts['go-build.log']", "sequence.go_build.artifacts['go-build.json']"):
            self.assertIn(binding, hook)
        for failing in ('none', 'unit', 'real'):
            with self.subTest(failing=failing), tempfile.TemporaryDirectory() as directory:
                root = Path(directory) / 'candidate'
                (root / 'nix').mkdir(parents=True)
                (root / 'go_build').mkdir()
                (root / 'nix/pnpm-build-policy.json').write_text('{}')
                out = Path(directory) / 'evidence'
                out.mkdir()
                self.fixtures(out)
                original = verify.Checks.run
                def run(checks, argv, directory='.', input_text=None):
                    if argv[:2] == ['go', 'test']:
                        suite = 'real' if '^TestReal' in argv else 'unit'
                        self.assertEqual(directory, 'go_build')
                        if suite == 'real':
                            self.assertIn('-v', argv)
                            self.assertEqual(checks.env['C2OPS_TEST_GO_BUILD_REAL'], '1')
                            self.assertEqual(checks.env['C2OPS_GO_BUILD_LAUNCHER'], '/fixture/bin/go-build')
                        return original(checks, [sys.executable, '-c', 'raise SystemExit(' + ('9' if failing == suite else '0') + ')'], directory)
                    checks.commands.append({'argv': argv, 'returncode': 0})
                    return True
                def metadata(checks, selector, expression):
                    self.assertEqual(selector, '.#go-build')
                    return {'path': '/fixture', 'manifest': {}, 'compiled': '/fixture/compiled'}
                with patch.object(verify, 'supported_platform', return_value=True), patch.object(verify.Checks, 'run', run), patch.object(verify.Checks, 'metadata', metadata), contextlib.redirect_stdout(io.StringIO()) as stdout:
                    verify.check('go-build', root, out)
                expected = 'passed' if failing == 'none' else 'failed'
                self.assertEqual(stdout.getvalue(), expected)
                statuses = {name: 'passed' for name in verify.TASKS}
                statuses['go-build'] = expected
                with contextlib.redirect_stdout(io.StringIO()) as result:
                    verify.aggregate(out, out / 'aggregate', statuses)
                self.assertEqual(result.getvalue(), expected)

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
