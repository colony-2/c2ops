#!/usr/bin/env python3
"""Check a native candidate, or retain and publish its merged package closures."""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
from urllib.request import Request, urlopen

import publication
from verify import native_system

COLONY2_PUBLIC_KEY = 'colony2.cachix.org-1:KFMHSiaf14yDf8qopSAgsFj9gs783OlFuE7Hs2AU4Hc='


def checkout(revision):
    if not re.fullmatch('[a-f0-9]{40}', revision):
        raise ValueError('expected a full Git commit')
    head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip()
    if head != revision:
        raise ValueError(f'worker checkout {head} differs from requested {revision}')
    return 'git+' + Path.cwd().resolve().as_uri() + '?ref=HEAD&shallow=1&rev=' + revision


def build(revision, system, roots, *, publishing=False):
    flake = checkout(revision)
    if native_system() != system:
        raise ValueError(f'{system} requires a native worker, got {native_system()}')
    subprocess.run(['python3', 'scripts/prefetch-act.py'], check=True)
    subprocess.run(['nix', 'flake', 'check', '-L', '--max-jobs', '2', '--no-write-lock-file', flake], check=True)
    manifest = roots / (system + '.json')
    data = publication.record(manifest, revision, system, flake)
    packages = data['packages']
    # Keep every output and dependency alive through verification and publication.
    build_args = ['--max-jobs', '2', '--out-link', str(roots / 'root'), '--no-write-lock-file',
                  *[flake + '#' + name for name in sorted(packages)]]
    subprocess.run(['nix', 'build', *build_args], check=True)
    outputs = [package['store_path'] for package in packages.values()]
    if not publishing:
        if system.endswith('-linux'):
            subprocess.run(['python3', 'scripts/check-codex-worker.py', '--native'],
                           env=dict(os.environ, C2OPS_TEST_FLAKE=flake), check=True)
        verify_native_outputs(outputs)
        return
    try:
        verify_native_outputs(outputs)
    except subprocess.CalledProcessError as error:
        # Nix uses exit code 1 for a content mismatch. Earlier checks could
        # leave bytecode in installed packages on writable worker stores.
        if error.returncode != 1:
            raise
        subprocess.run(['nix', 'build', '--repair', *build_args], check=True)
        verify_native_outputs(outputs)
    return data


def verify_native_outputs(outputs, *, store=None):
    # Local outputs have not been signed yet. Hash every file in their runtime
    # closures after the last probe, while the build roots are still alive.
    selected = ['--store', str(store)] if store is not None else []
    subprocess.run(['nix', '--extra-experimental-features', 'nix-command', *selected,
                    'store', 'verify', '--recursive', '--no-trust', '--', *outputs], check=True)


def missing_uploads(roots, token):
    paths = subprocess.check_output(['nix-store', '--query', '--requisites', *roots], text=True).splitlines()
    by_hash = {Path(path).name.split('-', 1)[0]: path for path in paths}
    # Use the same missing-path query as cachix push. Refuse an upload if the
    # destination ever stops recognising cache.nixos.org as an upstream.
    request = Request('https://cachix.org/api/v1/cache/colony2/narinfo',
                      data=json.dumps(list(by_hash)).encode(),
                      headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json',
                               'User-Agent': 'c2ops-publish/1.0'})
    with urlopen(request, timeout=90) as response:
        missing = [by_hash[key] for key in json.load(response)]
    if missing:
        upstream = json.loads(subprocess.check_output([
            'nix', 'path-info', '--store', 'https://cache.nixos.org', '--json',
            '--option', 'narinfo-cache-negative-ttl', '0', '--', *missing]))
        if set(upstream) != set(missing) or any(info is not None for info in upstream.values()):
            raise ValueError('Cachix requested paths that could not be confirmed absent from cache.nixos.org')
    print(f'Cache requested {len(missing)} of {len(paths)} runtime paths', flush=True)
    return missing


def publish(revision, system, outbox):
    # Keep credentials out of Nix evaluation, builders and package checks.
    # The active cell Git profile supplies private source access.
    checkout(revision)
    subprocess.run(['git', 'fetch', '--no-tags', 'https://github.com/colony-2/c2ops.git',
                    'refs/heads/main'], check=True)
    subprocess.run(['git', 'merge-base', '--is-ancestor', revision, 'FETCH_HEAD'], check=True)
    with tempfile.TemporaryDirectory() as tmp:
        roots = Path(tmp)
        data = build(revision, system, roots, publishing=True)
        # Push op roots. Cachix skips paths already in colony2 or its upstreams.
        # The CLI helpers are runtime references of the corresponding ops.
        paths = [p['store_path'] for name, p in data['packages'].items()
                 if name not in ('aider-cli', 'codex-cli')]
        token = subprocess.check_output(['c2j', 'secret', 'get', 'cachix_colony2_publish_key',
                                         '--format', 'raw'], text=True).strip()
        if not token:
            raise ValueError('empty Cachix publish credential')
        env = dict(os.environ, CACHIX_AUTH_TOKEN=token)
        missing = missing_uploads(paths, token)
        if missing:
            subprocess.run(['cachix', 'push', 'colony2', *missing], env=env, check=True)
        store = Path(tmp) / 'verified'
        publication.verify(data, store, 'colony2')
        publication.pin(data, store, 'colony2', env=env)
        outbox.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(roots / (system + '.json'), outbox / (system + '.json'))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('check', 'publish', 'check-manifests'))
    parser.add_argument('revision')
    parser.add_argument('system', nargs='?', choices=publication.SYSTEMS)
    args = parser.parse_args()
    os.environ['NIX_CONFIG'] = (os.environ.get('NIX_CONFIG', '') +
                                '\nextra-experimental-features = nix-command flakes')
    if args.action == 'check-manifests':
        flake = checkout(args.revision)
        for system in publication.SYSTEMS:
            manifest = Path(os.environ['INBOX']) / (system + '.json')
            data = publication.read_manifest(manifest, args.revision, system)
            if {k: v['store_path'] for k, v in data['packages'].items()} != publication.evaluate(system, flake):
                raise ValueError(f'{system} child manifest differs from the merged flake')
    elif not args.system:
        parser.error('a native system is required')
    elif args.action == 'check':
        with tempfile.TemporaryDirectory() as tmp:
            build(args.revision, args.system, Path(tmp))
    else:
        os.environ['NIX_CONFIG'] += '\nextra-trusted-public-keys = ' + COLONY2_PUBLIC_KEY
        publish(args.revision, args.system, Path(os.environ['OUTBOX']))
