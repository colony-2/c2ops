#!/usr/bin/env python3
"""Check a native candidate, or retain and publish its merged package closures."""
import argparse
import gzip
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

import publication
from verify import native_system


def checkout(revision):
    if not re.fullmatch('[a-f0-9]{40}', revision):
        raise ValueError('expected a full Git commit')
    head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip()
    if head != revision:
        raise ValueError(f'worker checkout {head} differs from requested {revision}')
    return 'git+' + Path.cwd().resolve().as_uri() + '?rev=' + revision


def build(revision, system, roots, outbox=None):
    flake = checkout(revision)
    if native_system() != system:
        raise ValueError(f'{system} requires a native worker, got {native_system()}')
    subprocess.run(['python3', 'scripts/prefetch-act.py'], check=True)
    subprocess.run(['nix', 'flake', 'check', '-L', '--max-jobs', '2', '--no-write-lock-file', flake], check=True)
    manifest = roots / (system + '.json')
    data = publication.record(manifest, revision, system, flake)
    packages = data['packages']
    # GC roots protect every output and dependency through streaming export.
    subprocess.run(['nix', 'build', '--max-jobs', '2', '--out-link', str(roots / 'root'), '--no-write-lock-file',
                    *[flake + '#' + name for name in sorted(packages)]], check=True)
    if outbox is None:
        return
    outbox.mkdir(parents=True, exist_ok=True)
    outputs = [package['store_path'] for package in packages.values()]
    paths = subprocess.check_output(['nix-store', '--query', '--requisites', *outputs],
                                    text=True).splitlines()
    export_closure(paths, outbox / (system + '.nar-export.gz'))
    shutil.copyfile(manifest, outbox / manifest.name)
    return data


def export_closure(paths, archive):
    # Expose the final name only after writing the complete stream.
    with tempfile.NamedTemporaryFile(dir=archive.parent, delete=False) as staged:
        try:
            with gzip.GzipFile(fileobj=staged, mode='wb', compresslevel=1, mtime=0) as compressed:
                with subprocess.Popen(['nix-store', '--export', *paths], stdout=subprocess.PIPE) as process:
                    shutil.copyfileobj(process.stdout, compressed, length=1024*1024)
                    process.stdout.close()
                    if process.wait() != 0:
                        raise RuntimeError('Nix closure export failed')
            staged.flush()
            os.replace(staged.name, archive)
        finally:
            Path(staged.name).unlink(missing_ok=True)


def publish(revision, system, outbox):
    # Keep credentials out of Nix evaluation, builders and package checks.
    # The active cell Git profile supplies private source access.
    checkout(revision)
    subprocess.run(['git', 'fetch', '--no-tags', 'https://github.com/colony-2/c2ops.git',
                    'refs/heads/main'], check=True)
    subprocess.run(['git', 'merge-base', '--is-ancestor', revision, 'FETCH_HEAD'], check=True)
    with tempfile.TemporaryDirectory() as tmp:
        data = build(revision, system, Path(tmp), outbox)
        paths = [p['store_path'] for p in data['packages'].values()]
        token = subprocess.check_output(['c2j', 'secret', 'get', 'cachix_colony2_publish_key',
                                         '--format', 'raw'], text=True).strip()
        if not token:
            raise ValueError('empty Cachix publish credential')
        env = dict(os.environ, CACHIX_AUTH_TOKEN=token)
        subprocess.run(['cachix', 'push', 'colony2', *paths], env=env, check=True)
        store = Path(tmp) / 'verified'
        publication.verify(data, store, 'colony2')
        publication.pin(data, store, 'colony2', env=env)


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
        publish(args.revision, args.system, Path(os.environ['OUTBOX']))
