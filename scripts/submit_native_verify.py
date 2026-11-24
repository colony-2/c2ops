"""Start native verification jobs from the incoming candidate snapshot."""
import hashlib
import json
import os
import subprocess

PLATFORMS = {
    'aarch64-darwin': 'macos*',
    'x86_64-linux': 'ubuntu*amd64',
    'aarch64-linux': 'ubuntu*arm64',
}


def submit(recipe, inputs, title):
    identity = json.dumps([os.environ['VERIFY_PARENT'], recipe, inputs], sort_keys=True)
    job_id = 'verify-' + hashlib.sha256(identity.encode()).hexdigest()[:32]
    try:
        result = subprocess.check_output([
            'c2j', 'submit', '--from-workspace', '--job-id', job_id,
            '--advanced-recipe-file', f'.c2/recipes/hooks/build/verify/{recipe}.yaml',
            '--inputs-json', json.dumps(inputs), '--title', title, '--json', title,
        ], text=True)
    except subprocess.CalledProcessError:
        # A retry can find a child already accepted by JobDB before interruption.
        children = json.loads(subprocess.check_output(
            ['c2j', 'list', 'children', '--all', '--json'], text=True))
        if not any(job['job_id'] == job_id for job in children['jobs']):
            raise
    else:
        if json.loads(result)['job_id'] != job_id:
            raise ValueError(f'JobDB returned an unexpected job ID for {title}')
    return job_id


def main():
    inputs = json.loads(os.environ['VERIFY_INPUTS'])
    common = {name: inputs[name] for name in ('candidate', 'base', 'target_directory')}
    if inputs.get('settings') is not None:
        common['settings'] = inputs['settings']

    jobs = {}
    for system, worker_base in PLATFORMS.items():
        child = dict(common, system=system, worker_base=worker_base)
        title = f'Verify c2ops {common["candidate"][:12]} on {system}'
        if system == 'aarch64-darwin':
            # The full release build and Go test caches exceed one macOS worker's disk.
            package = {key: child[key] for key in ('candidate', 'system', 'worker_base')}
            child['package_job'] = submit('packages', package, title + ' (packages)')
        jobs[system] = submit('platform', child, title)
    print(json.dumps(jobs))


if __name__ == '__main__':
    main()
