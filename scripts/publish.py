#!/usr/bin/env -S uv run --script
# /// script
# dependencies = ["PyYAML==6.0.3"]
# ///
"""Submit a live c2ops publication using the evidence from a passed verify job."""

import argparse
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import tempfile
import time

import yaml

from publication import SYSTEMS

ROOT = Path(__file__).resolve().parents[1]


def c2j(jobdb, *args):
    return subprocess.check_output(['c2j', *args, '--jobdb', jobdb], cwd=ROOT)


def check_verification(jobdb, job, revision):
    result = json.loads(c2j(jobdb, 'job', 'show', job, '--json'))['job']
    if result['cell'] != 'c2ops' or result.get('completion_status') != 'success':
        raise ValueError('expected a successful c2ops verification job')
    status = json.loads(c2j(jobdb, 'job', 'result', job, '--field', '/status', '--output', '-'))
    if status != 'passed':
        raise ValueError(f'verification returned {status!r}')
    refs = json.loads(c2j(jobdb, 'job', 'result', job, '--field', '/artifact_refs', '--output', '-'))
    bases = set()
    for system in SYSTEMS:
        key = refs[f'{system}/evidence.tar.gz']['stored']['key']
        blob = c2j(jobdb, 'job', 'artifact', key['jobId'], '--ref', json.dumps(key), '--output', '-')
        with tarfile.open(fileobj=io.BytesIO(blob), mode='r:gz') as evidence:
            receipt = json.load(evidence.extractfile('manifests.json'))
        if receipt['status'] != 'passed' or receipt['candidate'] != revision:
            raise ValueError(f'{system} did not verify {revision}')
        machine = receipt['machine'].lower()
        arch = 'aarch64' if machine == 'arm64' else machine
        platform = receipt['platform'].split('-', 1)[0].lower()
        os_name = 'darwin' if platform == 'macos' else platform
        if system != f'{arch}-{os_name}':
            raise ValueError(f'{system} verification evidence came from {machine} on {platform}')
        bases.add(receipt['base'])
    if len(bases) != 1:
        raise ValueError('native verification reports disagree on the base commit')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('revision', help='full Git SHA to publish')
    parser.add_argument('--verify-job', required=True, help='passed three-base verify job ID')
    parser.add_argument('--jobdb', default=os.environ.get('C2J_JOBDB'), required=not os.environ.get('C2J_JOBDB'))
    args = parser.parse_args()
    if not re.fullmatch('[0-9a-f]{40}', args.revision):
        parser.error('expected a full Git SHA')
    check_verification(args.jobdb, args.verify_job, args.revision)
    recipes = ROOT / '.c2/recipes/hooks/build/publish'
    worker = native_recipe(recipes)
    children = {}
    for key, system in [('mac', 'aarch64-darwin'), ('amd', 'x86_64-linux'), ('arm', 'aarch64-linux')]:
        title = f'Publish c2ops {args.revision[:12]} on {system}'
        job = submit(args.jobdb, worker, dict(revision=args.revision, system=system), title)
        children[key] = job['job_id']
        print(system, children[key], flush=True)
    title = f'Promote verified c2ops {args.revision[:12]}'
    finish = yaml.safe_load((recipes / 'finish.yaml').read_text())
    result = submit(args.jobdb, finish, dict(revision=args.revision, **children), title)
    print(json.dumps(result), flush=True)
    wait_for_promotion(args.jobdb, result['job_id'])


def native_recipe(recipes):
    worker = yaml.safe_load((recipes / 'build.yaml').read_text())
    # Embed the publisher from this checkout as trusted recipe code. Package
    # evaluation and builds still use the remote, exact-SHA worker checkout.
    operation = worker['sequence'][0]['inputs']
    helper = (ROOT / 'scripts/hook_packages.py').read_text()
    operation['run'] = ('PYTHONPATH="$PWD/scripts" python3 - publish "$REVISION" "$SYSTEM" '
                        "<<'C2OPS_PUBLISH_PY'\n" + helper + '\nC2OPS_PUBLISH_PY')
    return worker


def submit(jobdb, recipe, inputs, title):
    # Standalone jobs have no merged hook workspace to inherit. Pin the source
    # even if main advances between submission and the next available worker.
    pinned = dict(recipe, workspace={'cell': 'c2ops', 'ref': inputs['revision']})
    with tempfile.TemporaryDirectory() as directory:
        path = Path(directory) / 'publish.yaml'
        path.write_text(yaml.safe_dump(pinned))
        return json.loads(c2j(jobdb, 'submit', '--cell', 'c2ops', '--cell-repo', 'github.com/colony-2/c2ops',
                              '--advanced-recipe-file', str(path), '--inputs-json', json.dumps(inputs),
                              '--title', title, '--json', title))


def wait_for_promotion(jobdb, job_id):
    def read(*args):
        for attempt in range(4):
            try:
                return json.loads(c2j(jobdb, 'job', *args))
            except subprocess.CalledProcessError:
                if attempt == 3:
                    raise SystemExit(f'JobDB could not report publication {job_id}. '
                                     f'Check it with c2j job show {job_id} --jobdb {jobdb}') from None
                print(f'Retrying JobDB read for publication {job_id}', file=sys.stderr, flush=True)
                time.sleep(5 * 2**attempt)

    while True:
        job = read('show', job_id, '--json')['job']
        if job['status'] in ('COMPLETED', 'CANCELLED'):
            break
        time.sleep(20)
    if job.get('completion_status') != 'success':
        raise SystemExit(f'Publication job {job_id} ended with {job.get("completion_status") or job["status"]}')
    status = read('result', job_id, '--field', '/status', '--output', '-')
    if status != 'passed':
        raise SystemExit(f'Publication job {job_id} returned {status!r}')
    print(read('result', job_id, '--field', '/summary', '--output', '-'))


if __name__ == '__main__':
    main()
