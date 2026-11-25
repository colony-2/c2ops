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
import time
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('native_verify', Path(__file__).with_name('verify.py'))
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


class VerificationTests(unittest.TestCase):
    def setUp(self):
        # These receipts belong to each test, not an enclosing managed hook.
        environment = patch.dict(os.environ)
        environment.start()
        self.addCleanup(environment.stop)
        for name in tuple(os.environ):
            if name.startswith('VERIFY_'):
                del os.environ[name]

    @unittest.skipUnless(sys.platform == 'linux', 'inspects Linux process state')
    def test_completed_command_stops_leftover_descendants(self):
        for returncode in (0, 7):
            with self.subTest(returncode=returncode), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                marker = root / 'child.pid'
                child = 'import os,time;from pathlib import Path;Path("child.pid").write_text(str(os.getpid()));time.sleep(30)'
                parent = ('import subprocess,sys,time;from pathlib import Path;'
                          'subprocess.Popen([sys.executable,"-c",sys.argv[1]]);'
                          'deadline=time.monotonic()+5\n'
                          'while (not Path("child.pid").exists() or not Path("child.pid").read_text()) and time.monotonic()<deadline: time.sleep(.01)\n'
                          'assert Path("child.pid").read_text()\n'
                          'raise SystemExit(int(sys.argv[2]))')
                with (root / 'checks.log').open('wb') as log:
                    checks = verify.Checks(root, dict(os.environ), log, timeout=10)
                    self.assertEqual(checks.run([sys.executable, '-c', parent, child, str(returncode)]), returncode == 0)
                pid = int(marker.read_text())
                try:
                    deadline = time.monotonic() + 2
                    while True:
                        try:
                            state = Path(f'/proc/{pid}/stat').read_text().split()[2]
                        except FileNotFoundError:
                            break
                        if state == 'Z':
                            break
                        self.assertLess(time.monotonic(), deadline, 'descendant still running after command completion')
                        time.sleep(.01)
                finally:
                    try:
                        os.kill(pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                self.assertEqual(checks.commands[0]['returncode'], returncode)

    def test_kimi_prepares_helper_before_cli_deadline(self):
        for build_fails in (False, True):
            with self.subTest(build_fails=build_fails), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / 'codex').mkdir()
                (root / 'kimi').mkdir()
                tools = root / 'tools'
                tools.mkdir()
                nix = tools / 'nix'
                nix.write_text('#!' + sys.executable + '\nimport os,sys\n'
                               'assert sys.argv[-1] == os.environ["C2OPS_TEST_FLAKE"] + "#kimi"\n')
                nix.chmod(0o755)
                go = tools / 'go'
                go.write_text('#!' + sys.executable + '\nimport os,sys\n'
                              'assert os.environ["GOWORK"] == "off"\n'
                              'assert "PNPM_CONFIG_ALLOW_BUILDS" in os.environ\n'
                              'assert sys.argv[1:3] == ["build", "-mod=readonly"]\n'
                              'raise SystemExit(' + ('9' if build_fails else '0') + ')\n')
                go.chmod(0o755)
                uv = tools / 'uv'
                uv.write_text('#!' + sys.executable + '\nraise SystemExit(0)\n')
                uv.chmod(0o755)
                with (root / 'checks.log').open('wb') as log:
                    checks = verify.Checks(root, {**os.environ, 'GOWORK':'/ambient/go.work',
                        'C2OPS_TEST_FLAKE':'path:' + str(root), 'PNPM_CONFIG_ALLOW_BUILDS':'{"legacy":false}',
                        'PATH':str(tools)+os.pathsep+os.environ['PATH']}, log)
                    verify.execute('kimi', checks, race=False)
                self.assertEqual(len(checks.commands), 2 if build_fails else 3)
                self.assertEqual(checks.commands[0]['returncode'], 0)
                build = checks.commands[1]
                self.assertEqual(build['directory'], 'codex')
                self.assertEqual(build['argv'][-1], '../scripts/with-tools.go')
                self.assertEqual(build['returncode'], 9 if build_fails else 0)
                if not build_fails:
                    self.assertEqual(checks.commands[2]['directory'], 'kimi')
                    self.assertIn('test_main.py', checks.commands[2]['argv'])

    def test_codex_failure_stops_before_unprepared_candidate_use(self):
        root = Path(verify.__file__).resolve().parents[1]
        hook = (root / ".c2/recipes/hooks/build/verify/native.yaml").read_text()
        task = hook.split("- id: codex\n", 1)[1].split("\n- id:", 1)[0]
        self.assertIn('GOWORK: "off"', task)
        for failed_stage in ('reader', 'prefetch', 'package', 'suite'):
            with self.subTest(failed_stage=failed_stage), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / 'codex').mkdir()
                (root / 'nix').mkdir()
                catalog = {'codex': {'kind': 'go'}, 'gha': {'kind': 'go'},
                           'gha-workflow': {'kind': 'go'}, 'aider': {'kind': 'python'}}
                (root / 'nix/ops.json').write_text(json.dumps(catalog))
                tools = root / 'tools'
                tools.mkdir()
                # Real failing subprocesses exercise Checks' failure recording.
                for name, stage in (('go', 'reader'), ('nix', 'package'), ('python3', 'python')):
                    program = tools / name
                    program.write_text(
                        '#!' + sys.executable + '\nimport os, sys\n'
                        'assert os.environ["GOWORK"] == "off"\n'
                        'assert "C2J_CURRENT_JOB_ID" not in os.environ\n'
                        'assert "C2J_JOBDB" not in os.environ\n'
                        f'stage = {stage!r}\n'
                        'if stage == "python":\n'
                        '    stage = "prefetch" if sys.argv[1].endswith("prefetch-act.py") else "suite"\n'
                        f'raise SystemExit(9 if stage == {failed_stage!r} else 0)\n')
                    program.chmod(0o755)
                env = {**os.environ, 'PATH':str(tools)+os.pathsep+os.environ['PATH'],
                       'C2OPS_TEST_FLAKE':'path:' + str(root), 'GOWORK':'/ambient/go.work',
                       'C2J_CURRENT_JOB_ID':'parent', 'C2J_JOBDB':'parent-db'}
                with (root / 'codex.log').open('wb') as log:
                    checks = verify.Checks(root, env, log)
                    verify.execute('codex', checks, race=False)
                expected_count = {'reader':1, 'prefetch':2, 'package':3, 'suite':4}[failed_stage]
                self.assertEqual(len(checks.commands), expected_count)
                self.assertEqual(checks.commands[-1]['returncode'], 9)
                self.assertIn('./internal/extensioncmd', checks.commands[0]['argv'])
                self.assertIn('./test-fixtures', checks.commands[0]['argv'])
                selection = checks.commands[0]['argv'][checks.commands[0]['argv'].index('-run') + 1]
                exclusion = checks.commands[0]['argv'][checks.commands[0]['argv'].index('-skip') + 1]
                for name in ('TestArtifactTextExactComposition', 'TestArtifactPrefixSchemaRetries',
                             'TestArtifactFileInputManifests', 'TestDecodeArtifactFileInputs'):
                    self.assertRegex(name, selection)
                    self.assertNotRegex(name, exclusion)
                self.assertRegex('TestArtifactTextNativeSessionAndRepairs', exclusion,
                                 'native session tests need the CLI prepared for the full suite')
                self.assertNotRegex('TestArtifactInputRecipeReferences', selection,
                                    'recipe fixtures must wait until the candidate is built')
                if expected_count >= 3:
                    prefix = env['C2OPS_TEST_FLAKE'] + '#'
                    self.assertEqual([arg for arg in checks.commands[2]['argv'] if arg.startswith(prefix)],
                                     [prefix + op for op in ('codex', 'gha', 'gha-workflow', 'codex-cli')])
                if expected_count == 4:
                    self.assertEqual(checks.commands[3]['directory'], 'codex')
                    self.assertEqual(checks.commands[3]['argv'][-1], './...')
                    self.assertNotIn('-run', checks.commands[3]['argv'])
                    self.assertNotIn('-skip', checks.commands[3]['argv'])

    def test_npm_hook_has_const_task_and_exports_evidence(self):
        root = Path(verify.__file__).resolve().parents[1]
        hook = (root / '.c2/recipes/hooks/build/verify/native.yaml').read_text()
        task = hook.split('- id: npm_publish\n', 1)[1].split('\n- id:', 1)[0]
        for text in ('const: true', 'op: command_execution', 'check npm.publish'):
            self.assertIn(text, task)
        for text in ("sequence.npm_publish.artifacts['npm.publish.log']",
                     "sequence.npm_publish.artifacts['npm.publish.json']",
                     'VERIFY_STATUS_NPM_PUBLISH'):
            self.assertIn(text, hook)
        for sibling in ('github_release', 'go_build', 'gha', 'gha_runtime'):
            self.assertIn('- id: ' + sibling + '\n', hook)
        self.assertIn('github-release', verify.TASKS)
        self.assertIn('go-build', verify.TASKS)

    def test_verification_does_not_inherit_live_npm_request(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / 'candidate'
            root.mkdir()
            def execute(name, checks, race):
                self.assertEqual(name, 'tooling')
                self.assertNotIn('C2OPS_NPM_LIVE_REQUEST', checks.env)
                checks.commands.append({'argv': ['fixture-only'], 'returncode': 0})
            def run(checks, argv, *args, **kwargs):
                checks.commands.append({'argv': argv, 'returncode': 0})
                return True
            with patch.dict(os.environ, {'C2OPS_NPM_LIVE_REQUEST': '/must/not/publish.json'}), \
                 patch.object(verify, 'supported_platform', return_value=True), \
                 patch.object(verify, 'execute', execute), patch.object(verify.Checks, 'run', run), \
                 contextlib.redirect_stdout(io.StringIO()) as stdout:
                verify.check('tooling', root, Path(directory) / 'out')
            self.assertEqual(stdout.getvalue(), 'passed')

    def test_npm_task_retains_results_and_failure_blocks_aggregate(self):
        # Exercise the real source-suite subprocess and inject the Nix build result.
        for failure in (None, 'source', 'nix'):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                root = Path(directory) / 'candidate'
                for name in ('scripts', 'npm_publish'):
                    (root / name).mkdir(parents=True)
                (root / 'npm_publish/main.py').write_text('# candidate operation\n')
                (root / 'scripts/test_npm_publish.py').write_text(
                    'import os,unittest\nfrom pathlib import Path\n'
                    'class Fixtures(unittest.TestCase):\n'
                    ' def test_fixture(self):\n'
                    '  self.assertNotIn("C2OPS_NPM_LIVE_REQUEST", os.environ)\n'
                    '  self.assertEqual(Path(os.environ["C2OPS_NPM_TEST_MODULE"]), '
                    'Path(__file__).resolve().parents[1] / "npm_publish/main.py")\n'
                    '  self.assertEqual(Path(os.environ["C2OPS_NPM_SOURCE"]), '
                    'Path(__file__).resolve().parents[1] / "npm_publish")\n'
                    '  print("local fixture suite reached")\n'
                    '  self.assertFalse(' + repr(failure == 'source') + ')\n')
                out = Path(directory) / 'evidence'
                out.mkdir()
                self.fixtures(out)
                temp_alias = Path(directory) / 'temp-alias'
                temp_alias.symlink_to(directory, target_is_directory=True)
                original = verify.Checks.run
                def run(checks, argv, directory='.', input_text=None):
                    if 'unittest' in argv:
                        return original(checks, argv, directory, input_text)
                    failed = failure == 'nix' and f'path:.#checks.{verify.native_system()}.npm-fixtures' in argv
                    checks.commands.append({'argv': argv, 'returncode': int(failed)})
                    return not failed
                with patch.dict(os.environ, {'C2OPS_NPM_LIVE_REQUEST': '/must/not/publish.json',
                        'C2OPS_NPM_TEST_MODULE': '/must/not/test-another-operation.py',
                        'C2OPS_NPM_SOURCE': '/must/not/check-another-source'}), \
                     patch.object(tempfile, 'tempdir', str(temp_alias)), \
                     patch.object(verify, 'supported_platform', return_value=True), \
                     patch.object(verify.Checks, 'run', run), \
                     contextlib.redirect_stdout(io.StringIO()) as stdout:
                    verify.check('npm.publish', root, out)
                expected = 'failed' if failure else 'passed'
                self.assertEqual(stdout.getvalue(), expected)
                receipt = json.loads((out / 'npm.publish.json').read_text())
                self.assertEqual(receipt['task'], 'npm.publish')
                self.assertEqual(receipt['status'], expected)
                suites = [c for c in receipt['commands'] if 'unittest' in c['argv']]
                self.assertEqual(len(suites), 1)
                self.assertEqual(suites[0]['returncode'], int(failure == 'source'))
                builds = [c for c in receipt['commands'] if f'path:.#checks.{verify.native_system()}.npm-fixtures' in c['argv']]
                expected_builds = [] if failure == 'source' else [int(failure == 'nix')]
                self.assertEqual([c['returncode'] for c in builds], expected_builds)
                log = (out / 'npm.publish.log').read_bytes()
                self.assertIn(b'local fixture suite reached', log)
                self.assertEqual(receipt['log_sha256'], hashlib.sha256(log).hexdigest())
                statuses = {'VERIFY_STATUS_' + name.upper().replace('-', '_').replace('.', '_'): ('pending' if name == verify.LIVE_TASK else 'passed')
                            for name in verify.ALL_TASKS}
                statuses['VERIFY_STATUS_NPM_PUBLISH'] = stdout.getvalue()
                with patch.dict(os.environ, statuses), contextlib.redirect_stdout(io.StringIO()) as aggregate_stdout:
                    verify.aggregate(out, out / 'aggregate')
                self.assertEqual(aggregate_stdout.getvalue(), expected)
                aggregate = json.loads((out / 'aggregate/verification.json').read_text())
                self.assertIn({'task': 'npm.publish', 'status': expected}, aggregate['tasks'])
                if not failure:
                    for damage in ('changed log', 'missing receipt'):
                        if damage == 'changed log':
                            (out / 'npm.publish.log').write_bytes(log + b'tampered')
                        else:
                            (out / 'npm.publish.log').write_bytes(log)
                            (out / 'npm.publish.json').unlink()
                        with patch.dict(os.environ, statuses), contextlib.redirect_stdout(io.StringIO()) as aggregate_stdout:
                            verify.aggregate(out, out / 'aggregate')
                        self.assertEqual(aggregate_stdout.getvalue(), 'failed', damage)


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
                statuses = {name: ('pending' if name == verify.LIVE_TASK else 'passed') for name in verify.ALL_TASKS}
                statuses[task] = result.stdout
                with contextlib.redirect_stdout(io.StringIO()) as stdout:
                    verify.aggregate(root, root / 'aggregate', statuses)
                self.assertEqual(stdout.getvalue(), 'failed')

    def test_gha_tasks_select_pinned_compiler_and_fail_closed(self):
        for task in ('gha', 'gha-runtime'):
            for available in (True, False):
                with self.subTest(task=task, available=available), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory) / 'candidate'
                    (root / 'gha/integration').mkdir(parents=True)
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
                    self.assertIn('.#gha.buildCompiler', builds[0]['argv'])
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
        hook = (source / '.c2/recipes/hooks/build/verify/native.yaml').read_text()
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
        for name in verify.ALL_TASKS:
            log = (name + ' evidence\n').encode()
            (root / (name + '.log')).write_bytes(log)
            (root / (name + '.json')).write_text(json.dumps({
                'task': name, 'status': 'pending' if name == verify.LIVE_TASK else 'passed', 'commands': [] if name == verify.LIVE_TASK else [{'argv': ['fixture'], 'returncode': 0}],
                'log_sha256': hashlib.sha256(log).hexdigest()}))

    def aggregate(self, root):
        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            verify.aggregate(root, root / 'out', {name: ('pending' if name == verify.LIVE_TASK else 'passed') for name in verify.ALL_TASKS})
        return stdout.getvalue()

    def test_candidate_and_base_mismatch_reject_ordinary_receipts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.fixtures(root)
            for name in verify.ALL_TASKS:
                path = root / (name + '.json')
                row = json.loads(path.read_text())
                row.update(candidate='fixture-candidate', base='fixture-base')
                path.write_text(json.dumps(row))
            with patch.dict(os.environ, {'VERIFY_CANDIDATE': 'fixture-candidate',
                                        'VERIFY_BASE': 'fixture-base'}):
                self.assertEqual(self.aggregate(root), 'passed')
                path = root / 'manifests.json'
                original = json.loads(path.read_text())
                for field in ('candidate', 'base'):
                    with self.subTest(field=field):
                        path.write_text(json.dumps({**original, field: 'other'}))
                        self.assertEqual(self.aggregate(root), 'failed')
                        report = json.loads((root / 'out/verification.json').read_text())
                        self.assertEqual(report['errors'], ['manifests: candidate identity mismatch'])

    def test_baseline_failure_and_evidence_gate(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.fixtures(root)
            self.assertEqual(self.aggregate(root), 'passed')
            with contextlib.redirect_stdout(io.StringIO()) as bad_stdout:
                verify.aggregate(root, root / 'out', {name: 'malformed' for name in verify.ALL_TASKS})
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
                for name in ('pydantic', 'scripts'):
                    (root / name).mkdir(parents=True)
                for name in ('pydantic/Makefile', 'scripts/pydantic_test.py', 'flake.lock'):
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
                statuses = {name:('pending' if name == verify.LIVE_TASK else 'passed') for name in verify.ALL_TASKS}; statuses['pydantic'] = expected
                with contextlib.redirect_stdout(io.StringIO()) as aggregate_stdout:
                    verify.aggregate(out, out / 'aggregate', statuses)
                self.assertEqual(aggregate_stdout.getvalue(), expected)

    def test_go_build_suites_are_mandatory_and_aggregated(self):
        source = Path(verify.__file__).resolve().parents[1]
        hook = (source / '.c2/recipes/hooks/build/verify/native.yaml').read_text()
        for binding in ('check go-build', 'VERIFY_STATUS_GO_BUILD', "sequence.go_build.artifacts['go-build.log']", "sequence.go_build.artifacts['go-build.json']"):
            self.assertIn(binding, hook)
        for failing in ('none', 'unit', 'real'):
            with self.subTest(failing=failing), tempfile.TemporaryDirectory() as directory:
                root = Path(directory) / 'candidate'
                (root / 'go_build').mkdir(parents=True)
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
                statuses = {name: ('pending' if name == verify.LIVE_TASK else 'passed') for name in verify.ALL_TASKS}
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
from unittest.mock import patch
signal.signal(signal.SIGTERM,v.stop)
with patch.object(os,'killpg',wraps=os.killpg) as killpg, open(sys.argv[2]+'.log','wb') as log:
 c=v.Checks(pathlib.Path('.'),dict(os.environ),log)
 try:
  c.run([sys.executable,'-c','import os,pathlib,time;pathlib.Path('+repr(sys.argv[2])+').write_text(str(os.getpid()));time.sleep(60)'])
 finally:
  killpg.assert_called_once()
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
                # The verifier reaps its direct child before returning the signal status.
                with self.assertRaises(ProcessLookupError):
                    os.kill(child, 0)
            finally:
                if proc.poll() is None:
                    proc.kill()
                    proc.wait()

    def test_opentofu_qualifier_and_preparation_failures_gate_aggregate(self):
        # Regression-only preparation fixtures. The dispatch/failure process and
        # aggregate run for real; native acceptance is a separate hook run.
        self.assertIn('opentofu', verify.TASKS)
        for failing in ('none', 'helper', 'version', 'package', 'qualifier'):
            with self.subTest(failing=failing), tempfile.TemporaryDirectory() as directory:
                root = Path(directory) / 'candidate'
                (root / 'scripts').mkdir(parents=True)
                (root / 'scripts/check-opentofu.py').write_text(
                    'import os,sys\nprint("real qualifier dispatch", os.environ["GOBIN"])\n'
                    + 'raise SystemExit(' + ('9' if failing == 'qualifier' else '0') + ')\n')
                out = Path(directory) / 'evidence'; out.mkdir()
                self.fixtures(out)
                original = verify.Checks.run
                def run(checks, argv, directory='.', input_text=None):
                    if argv == ['python3', 'scripts/check-opentofu.py']:
                        return original(checks, argv, directory, input_text)
                    code = 9 if ((failing == 'helper' and argv[:2] == ['go', 'install'])
                                 or (failing == 'version' and argv[-1] == 'version')
                                 or (failing == 'package' and 'path:.#opentofu-up' in argv)) else 0
                    checks.commands.append({'argv':argv, 'returncode':code})
                    return code == 0
                with patch.object(verify, 'supported_platform', return_value=True), \
                     patch.object(verify.Checks, 'run', run), \
                     contextlib.redirect_stdout(io.StringIO()) as stdout:
                    verify.check('opentofu', root, out)
                expected = 'passed' if failing == 'none' else 'failed'
                self.assertEqual(stdout.getvalue(), expected)
                report = json.loads((out / 'opentofu.json').read_text())
                installs = [c for c in report['commands'] if c['argv'][:2] == ['go','install']]
                self.assertEqual(len(installs), 1)
                self.assertEqual(installs[0]['argv'][2], 'github.com/colony-2/c2j/cmd/c2j@' + verify.OPENTOFU_C2J)
                versions = [c for c in report['commands'] if c['argv'][0].endswith('/c2j')]
                self.assertEqual(len(versions), int(failing != 'helper'))
                if versions:
                    self.assertEqual(versions[0]['argv'][1:], ['version'])
                suites = [c for c in report['commands'] if c['argv'] == ['python3','scripts/check-opentofu.py']]
                self.assertEqual(len(suites), int(failing not in ('helper','version','package')))
                if suites:
                    self.assertEqual(suites[0]['returncode'], 9 if failing == 'qualifier' else 0)
                    self.assertIn('real qualifier dispatch', (out / 'opentofu.log').read_text())
                statuses = {name:('pending' if name == verify.LIVE_TASK else 'passed') for name in verify.ALL_TASKS}; statuses['opentofu'] = expected
                with contextlib.redirect_stdout(io.StringIO()) as aggregate_stdout:
                    verify.aggregate(out, out / 'aggregate', statuses)
                self.assertEqual(aggregate_stdout.getvalue(), expected)
                # The new required receipt cannot disappear while the caller
                # still reports success from all its other tasks.
                (out / 'opentofu.json').unlink()
                self.assertEqual(self.aggregate(out), 'failed')


class LiveVerificationTests(unittest.TestCase):
    setUp = VerificationTests.setUp
    fixtures = VerificationTests.fixtures
    def check_live(self, root, value, driver_stdout=None, returncode=0):
        env = {'VERIFY_OPENTOFU_PROFILE': value, 'VERIFY_CANDIDATE': 'candidate', 'VERIFY_BASE': 'base'}
        with patch.dict(os.environ, env), patch.object(verify.platform, 'platform', return_value='fixture'), patch.object(verify.subprocess, 'Popen') as popen, contextlib.redirect_stdout(io.StringIO()) as stdout:
            process = popen.return_value.__enter__.return_value
            process.communicate.return_value = (driver_stdout or b'', b'never-export-private-credential')
            process.returncode = returncode
            process.pid = 123456789
            verify.check_credentials(root, root)
        return stdout.getvalue(), json.loads((root / 'opentofu_credentials.json').read_text()), popen

    def test_v02_unconfigured_is_pending_without_processes(self):
        for value in ('', 'null'):
            with tempfile.TemporaryDirectory() as tmp:
                status, report, popen = self.check_live(Path(tmp), value)
                self.assertEqual(status, 'pending'); popen.assert_not_called()
                self.assertEqual(report['commands'], [])
                self.assertIsNone(report['profile_sha256'])

    def valid_live(self):
        from test_check_opentofu_google import public_profile
        value = json.dumps(public_profile())
        _, digest = verify.profile_binding(value)
        data = dict(component=verify.LIVE_TASK, status='passed', candidate='candidate', base='base', profile_sha256=digest,
                    package='/nix/store/' + 'a' * 32 + '-c2ops-opentofu-up-0.1.0', cases=[
                        dict(case=c, status='passed', mode='oauth_access_token' if c == 'token' else 'service_account_json',
                             principal_verified=True, native_invocations=2, operation_resolutions=2, inspector_resolutions=1)
                        for c in ('token', 'direct_key', 'impersonated_key')])
        return value, data

    def test_v03_configured_errors_fail_and_do_not_publish_private_output(self):
        value, data = self.valid_live()
        for payload, code in [(b'invalid never-export-private-credential', 0), (json.dumps(data).encode(), 9),
                              (json.dumps({**data, 'status': 'pending'}).encode(), 0),
                              (json.dumps({**data, 'status': 'failed', 'category': 'never-export-private-credential'}).encode(), 0)]:
            with tempfile.TemporaryDirectory() as tmp:
                status, report, _ = self.check_live(Path(tmp), value, payload, code)
                self.assertEqual(status, 'failed')
                self.assertNotIn('never-export-private-credential', json.dumps(report))
                self.assertNotIn('never-export-private-credential', (Path(tmp) / 'opentofu_credentials.log').read_text())
        with tempfile.TemporaryDirectory() as tmp:
            status, _, popen = self.check_live(Path(tmp), '{}')
            self.assertEqual(status, 'failed'); popen.assert_not_called()

    def test_v04_v05_live_binding_and_aggregate_tampering(self):
        import copy
        value, data = self.valid_live()
        mutations = [('candidate', 'other'), ('base', 'other'), ('profile_sha256', 'wrong'), ('package', '/mutable'),
                     ('component', 'opentofu'), ('status', 'skipped'), ('cases', [])]
        for field, replacement in [(None, None), *mutations, ('log', 'tampered'), ('principal', False), ('pending', True), ('missing', True)]:
            with self.subTest(field=field), tempfile.TemporaryDirectory() as tmp:
                root=Path(tmp); self.fixtures(root)
                for name in verify.TASKS:
                    path=root/(name+'.json'); row=json.loads(path.read_text()); row.update(candidate='candidate',base='base');path.write_text(json.dumps(row))
                status, receipt, _ = self.check_live(root, value, json.dumps(data).encode())
                self.assertEqual(status, 'passed')
                if field in dict(mutations): receipt[field]=replacement
                if field == 'principal': receipt['cases'][0]['principal_verified']=False
                if field == 'pending': receipt.update(status='pending',commands=[],profile_sha256=None,package=None,cases=[])
                path=root/'opentofu_credentials.json';path.write_text(json.dumps(receipt))
                if field == 'log': (root/'opentofu_credentials.log').write_text('tampered')
                if field == 'missing': path.unlink()
                statuses={name:'passed' for name in verify.ALL_TASKS}
                if field in ('status','pending'): statuses[verify.LIVE_TASK]=receipt['status']
                with patch.dict(os.environ, {'VERIFY_OPENTOFU_PROFILE':value,'VERIFY_CANDIDATE':'candidate','VERIFY_BASE':'base'}), contextlib.redirect_stdout(io.StringIO()) as out:
                    verify.aggregate(root,root/'out',statuses)
                self.assertEqual(out.getvalue(), 'passed' if field is None else 'failed')



if __name__ == '__main__':
    unittest.main()
