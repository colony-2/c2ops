"""Start all three native verification jobs from the incoming candidate snapshot."""
import hashlib
import json
import os
import subprocess

PLATFORMS = {
    'aarch64-darwin': 'macos*',
    'x86_64-linux': 'ubuntu*amd64',
    'aarch64-linux': 'ubuntu*arm64',
}


def main():
    inputs = json.loads(os.environ['VERIFY_INPUTS'])
    common = {name: inputs[name] for name in ('candidate', 'base', 'target_directory')}
    if inputs.get('settings') is not None:
        common['settings'] = inputs['settings']

    jobs = {}
    for system, worker_base in PLATFORMS.items():
        child = dict(common, system=system, worker_base=worker_base)
        identity = json.dumps([os.environ['VERIFY_PARENT'], child], sort_keys=True)
        job_id = 'verify-' + hashlib.sha256(identity.encode()).hexdigest()[:32]
        title = f'Verify c2ops {common["candidate"][:12]} on {system}'
        try:
            result = subprocess.check_output([
                'c2j', 'submit', '--from-workspace', '--job-id', job_id,
                '--advanced-recipe-file', '.c2/recipes/hooks/build/verify/platform.yaml',
                '--inputs-json', json.dumps(child), '--title', title, '--json', title,
            ], text=True)
        except subprocess.CalledProcessError:
            # A retry can find a child already accepted by JobDB before interruption.
            children = json.loads(subprocess.check_output(
                ['c2j', 'list', 'children', '--all', '--json'], text=True))
            if not any(job['job_id'] == job_id for job in children['jobs']):
                raise
        else:
            if json.loads(result)['job_id'] != job_id:
                raise ValueError(f'JobDB returned an unexpected job ID for {system}')
        jobs[system] = job_id
    print(json.dumps(jobs))


if __name__ == '__main__':
    main()
