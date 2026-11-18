#!/usr/bin/env python3
"""Check release defaults through installed submitting wrappers and a recording broker."""
import argparse
import base64
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile
import threading

import yaml


def check(binary, revision):
    records = []

    class Broker(BaseHTTPRequestHandler):
        def do_POST(self):
            request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            for recipe in request['embedded_recipes']:
                records.append(yaml.safe_load(base64.b64decode(recipe['yaml'])))
            self.send_response(200)
            self.end_headers()
            self.wfile.write(json.dumps({'job_id': request['start']['job_id']}).encode())

        def log_message(self, *_):
            pass

    with tempfile.TemporaryDirectory(prefix='gha-release-') as directory:
        root = Path(directory)
        workflow = root / 'workflow.yml'
        workflow.write_text('on: workflow_dispatch\njobs:\n  build:\n    runs-on: local\n    steps: [{run: "echo hello"}]\n')
        def git(*args):
            return subprocess.check_output(['git', '-C', directory, *args], text=True).strip()
        git('init', '-b', 'main')
        git('add', '.')
        git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.test',
            '-c', 'commit.gpgsign=false', 'commit', '-m', 'base')
        base = git('rev-parse', 'HEAD')
        (root / 'candidate.txt').write_text('candidate')
        git('add', '.')
        git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.test',
            '-c', 'commit.gpgsign=false', 'commit', '-m', 'candidate')
        server = ThreadingHTTPServer(('127.0.0.1', 0), Broker)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            env = dict(os.environ, C2J_CHILD_JOB_ENDPOINT=f'http://127.0.0.1:{server.server_port}',
                       C2J_CHILD_JOB_TOKEN='fixture', C2J_CHILD_JOB_SESSION_ID='session',
                       C2J_CURRENT_TENANT_ID='tenant', C2J_CURRENT_JOB_ID='parent',
                       C2J_CURRENT_INVOCATION_HASH='invocation')
            inputs = dict(backend='c2j', workflow='workflow.yml', worktree_path=directory,
                          repository='example.test/fixture', base_revision=base,
                          runner_profiles=[{'labels': ['local'], 'execution_needs': {
                              'platform': platform.system().lower() + '/' +
                                  {'aarch64': 'arm64', 'x86_64': 'amd64'}.get(platform.machine(), platform.machine())}}])
            inputs['git_context'] = dict(worktree_path=directory, base_repo='example.test/fixture',
                                         resolved_base_hash=base)
            payload = inputs
            result = subprocess.run([binary], input=json.dumps(payload), env=env,
                                    text=True, capture_output=True, timeout=30)
            if not revision:
                assert result.returncode != 0 and not records, (result, records)
                assert 'helper_op and executor_op' in result.stdout + result.stderr, result
                return
            assert result.returncode == 0, (result.stdout, result.stderr)
            prefix = f'nix:github:colony-2/c2ops/{revision}'
            helper, executor = prefix + '#gha-workflow', prefix + '#gha-job'
            assert len(records) == 2, records
            for recipe in records:
                assert recipe['sequence'][0]['op'] == helper, recipe
                for node in recipe['sequence']:
                    if node['id'] == 'expand':
                        assert node['op'] == helper, node
                        carried = json.loads(base64.b64decode(node['inputs']['payload']))['input']
                        assert carried['helper_op'] == helper, carried
                        assert carried['executor_op'] == executor, carried
            print(json.dumps({'binary': binary, 'revision': revision, 'helper': helper,
                              'executor': executor, 'recipes': len(records)}))
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--revision', default='')
    parser.add_argument('binary')
    args = parser.parse_args()
    check(args.binary, args.revision)


if __name__ == '__main__':
    main()
