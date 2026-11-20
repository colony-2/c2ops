"""Real npm fixture installation; separately gated live N08 acceptance."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from npm_publish_fixture import MODULE, TOKEN, fixture, op


class NpmIntegrationTests(unittest.TestCase):
    def test_C02_C03_process_stdout_from_read_only_cwd(self):
        with fixture() as f:
            driver = ('import json,os,sys\n'
                'from npm_publish_fixture import op,routing_ports\n'
                'with routing_ports(json.loads(os.environ["FIXTURE_PORTS"])):\n'
                ' sys.exit(op.main())\n')
            env = {**os.environ, 'PYTHONPATH': str(Path(__file__).parent),
                'FIXTURE_PORTS': json.dumps(f.ports()), 'C2OPS_NPM_TEST_MODULE': str(MODULE)}
            result = subprocess.run([sys.executable, '-c', driver], input=json.dumps(f.request).encode(),
                env=env, cwd='/', capture_output=True, timeout=30)
            self.assertEqual(result.returncode, 0, result.stderr.decode())
            self.assertEqual(result.stderr, b'')
            self.assertNotIn(TOKEN.encode(), result.stdout)
            out = json.loads(result.stdout)['output']
            self.assertEqual(out['registry']['outcome'], 'published')
            self.assertEqual(f.publications, [(f.outbox / out['files'][0]['name']).read_bytes()])

    def test_fixture_packed_wrapper_downloads_checksums_executes_and_publishes(self):
        with fixture() as f:
            # Package execution does not depend on a writable or source cwd.
            previous = Path.cwd()
            try:
                os.chdir('/')
                result = f.execute()
            finally:
                os.chdir(previous)
            out = result['output']
            self.assertEqual(out['install_check']['status'], 'passed')
            self.assertEqual(out['registry']['outcome'], 'published')
            self.assertEqual(f.publications, [(f.outbox / out['files'][0]['name']).read_bytes()])
            downloads = [url for url in f.asset_requests if '/releases/download/' in url]
            self.assertEqual(len(downloads), 7, 'five public preflight downloads plus installer archive/checksums')

    def phase_process(self, f):
        driver = ('import json,os,sys\n'
            'from pathlib import Path\n'
            'from npm_publish_fixture import op,routing_ports,assert_external_paths\n'
            'real=op.run\n'
            'def run(argv,cwd,env=None,timeout=120):\n'
            ' assert_external_paths(argv,cwd,env,Path(os.environ["FIXTURE_WORKSPACE"]))\n'
            ' if argv[:2]==["npm","pack"]: raise AssertionError("resume packed")\n'
            ' return real(argv,cwd,env,timeout)\n'
            'def stage(*args): raise AssertionError("resume staged metadata")\n'
            'op.run=run;op.prepare=stage\n'
            'with routing_ports(json.loads(os.environ["FIXTURE_PORTS"])):\n'
            ' sys.exit(op.main())\n')
        env = {**os.environ, 'PYTHONPATH': str(Path(__file__).parent),
            'FIXTURE_PORTS': json.dumps(f.ports()), 'C2OPS_NPM_TEST_MODULE': str(MODULE),
            'FIXTURE_WORKSPACE': str(f.workspace)}
        return subprocess.run([sys.executable, '-c', driver], input=json.dumps(f.request).encode(),
            env=env, cwd='/', capture_output=True, timeout=40)

    def test_fresh_workspace_publish_reconciles_lost_acceptance_from_prepare(self):
        from npm_publish_fixture import prepare_artifact, transfer_artifacts, snapshot
        for observation_lost in (False, True):
            with self.subTest(observation_lost=observation_lost), fixture() as f:
                prepared = prepare_artifact(f)
                saved = transfer_artifacts(f, prepared)
                f.request.update(phase='publish', registry_token_file=str(f.token))
                f.ambiguous = True
                f.lose_observation = observation_lost
                before = snapshot(f.workspace)
                result = self.phase_process(f)
                self.assertEqual(snapshot(f.workspace), before)
                self.assertEqual(len(f.publications), 1)
                self.assertEqual(f.publications[0], saved[prepared['output']['files'][0]['name']])
                if observation_lost:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(result.stdout, b'')
                    f.registry_unavailable = False
                    transfer_artifacts(f, prepared)
                    before = snapshot(f.workspace)
                    result = self.phase_process(f)
                    self.assertEqual(snapshot(f.workspace), before)
                    self.assertEqual(len(f.publications), 1)
                self.assertEqual(result.returncode, 0, result.stderr.decode())
                out = json.loads(result.stdout)['output']
                self.assertEqual(out['phase'], 'publish')
                self.assertEqual(out['registry']['outcome'], 'reconciled')
                self.assertEqual(out['registry']['observed_integrity'], prepared['output']['registry']['integrity'])
                self.assertEqual((f.outbox / out['files'][0]['name']).read_bytes(), saved[out['files'][0]['name']])
                put = f.events.index('PUT')
                self.assertEqual(f.events[put+1], 'GET')

    def test_cancelled_publish_retains_prepared_artifact_and_checkout(self):
        import signal
        import time
        from npm_publish_fixture import prepare_artifact, snapshot
        with fixture() as f:
            prepared = prepare_artifact(f); f.resume(prepared)
            f.request.update(phase='publish', registry_token_file=str(f.token))
            marker = f.root / 'child.pid'
            driver = ('import json,os,sys\n'
                'from pathlib import Path\n'
                'from npm_publish_fixture import op,routing_ports\n'
                'real=op.run\n'
                'def run(argv,cwd,env=None,timeout=120):\n'
                ' if argv[:2]==["npm","publish"]:\n'
                '  return real([sys.executable,"-c",'
                '"import os,time;from pathlib import Path;Path("+repr(os.environ["PIDFILE"])+").write_text(str(os.getpid()));time.sleep(60)"],cwd,env,timeout)\n'
                ' return real(argv,cwd,env,timeout)\n'
                'op.run=run\n'
                'with routing_ports(json.loads(os.environ["FIXTURE_PORTS"])):\n'
                ' sys.exit(op.main())\n')
            env = {**os.environ, 'PYTHONPATH': str(Path(__file__).parent), 'PIDFILE': str(marker),
                'FIXTURE_PORTS': json.dumps(f.ports()), 'C2OPS_NPM_TEST_MODULE': str(MODULE)}
            before = snapshot(f.workspace)
            process = subprocess.Popen([sys.executable, '-c', driver], stdin=subprocess.PIPE,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, cwd='/')
            try:
                process.stdin.write(json.dumps(f.request).encode()); process.stdin.close(); process.stdin = None
                deadline = time.monotonic() + 15
                while not marker.exists() and time.monotonic() < deadline and process.poll() is None:
                    time.sleep(0.02)
                self.assertTrue(marker.exists())
                child = int(marker.read_text())
                process.terminate()
                stdout, stderr = process.communicate(timeout=5)
                self.assertNotEqual(process.returncode, 0)
                self.assertEqual(stdout, b'')
                self.assertIn(b'cancelled', stderr)
                status = Path('/proc') / str(child) / 'stat'
                self.assertTrue(not status.exists() or status.read_text().split()[2] == 'Z')
                self.assertEqual(snapshot(f.workspace), before)
                self.assertEqual(f.publications, [])
                self.assertEqual(f.events, ['GET'])
                self.assertEqual(op.digest(f.outbox / prepared['output']['files'][0]['name']),
                                 prepared['output']['files'][0]['sha256'])
            finally:
                if process.poll() is None:
                    process.kill(); process.communicate()

    @unittest.skipUnless(os.environ.get('C2OPS_NPM_LIVE_REQUEST'),
        'N08 unverified: needs confirmed GitHub op and authorized disposable public assets, registry and token')
    def test_N08_authorized_disposable_GitHub_and_registry(self):
        # The coordinator supplies an explicitly authorized request only after
        # its dependency/publication gates. Never synthesize production inputs.
        request = Path(os.environ['C2OPS_NPM_LIVE_REQUEST']).read_bytes()
        with tempfile.TemporaryDirectory() as cwd:
            os.chmod(cwd, 0o555)
            result = subprocess.run([sys.executable, str(MODULE)],
                input=request, cwd=cwd, capture_output=True, timeout=1250)
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        out = json.loads(result.stdout)['output']
        self.assertEqual(out['install_check']['status'], 'passed')
        self.assertIn(out['registry']['outcome'], ('published', 'reconciled'))


if __name__ == '__main__':
    unittest.main()
