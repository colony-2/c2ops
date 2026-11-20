#!/usr/bin/env python3
"""Validate candidate release.pack bytes/modes across unrelated fixture roots.

No upstream package, service, signing credential or real worker storage is used.
This is the package's C03 and X06 fixture check, not full K06 acceptance.
"""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import zlib


def check(executable, *, empty_path=False, manifest=None):
    schemas = json.loads(Path(manifest).read_text()) if manifest else None
    if schemas:
        from jsonschema import Draft202012Validator
    executable = str(Path(executable).resolve())
    with tempfile.TemporaryDirectory(prefix='release-pack-contract-') as temp:
        base = Path(temp).resolve()
        source, producer, inbox, outbox, cwd = [base / p for p in ('source', 'producer', 'hydrated', 'outbox', 'readonly')]
        for p in (source, producer, inbox, outbox, cwd):
            p.mkdir()
        env = dict(os.environ, GIT_AUTHOR_NAME='Fixture', GIT_AUTHOR_EMAIL='fixture@example.test',
                   GIT_COMMITTER_NAME='Fixture', GIT_COMMITTER_EMAIL='fixture@example.test')
        def git(*args):
            return subprocess.check_output(['git', '-C', str(source), *args], env=env, stderr=subprocess.PIPE).decode().strip()
        git('init', '-q')
        (source / 'README.md').write_bytes(b'committed readme\n')
        (source / 'LICENSE').write_bytes(b'license\n')
        (source / 'docs').mkdir()
        (source / 'docs/guide.txt').write_bytes(b'committed docs\n')
        (source / '.gitattributes').write_text('docs/guide.txt export-ignore\n')
        (source / '.gitignore').write_text('docs/ignored.txt\n')
        git('add', '.')
        git('commit', '-qm', 'fixture')
        (source / 'docs/ignored.txt').write_bytes(b'ignored injection')
        (source / 'docs/untracked.txt').write_bytes(b'untracked injection')
        intent = dict(schema_version=1, release_id='fixture-1.2.3', repository='github.com/example/cli',
                      source_commit=git('rev-parse', 'HEAD'), target_branch='main', version='1.2.3',
                      tag='v1.2.3', build_date='2026-11-18T12:00:00Z', binary_name='cli')
        identity = {k: intent[k] for k in ('schema_version', 'release_id', 'repository', 'source_commit', 'version', 'tag')}
        files, expected = [], {}
        for goos in ('linux', 'darwin'):
            for goarch in ('amd64', 'arm64'):
                # Representative byte size, not a claim about production retention.
                data = (f'final {goos}/{goarch}\0'.encode() + bytes(range(256))) * 4096
                name = f'final/{goos}/{goarch}/cli'
                p = producer / name
                p.parent.mkdir(parents=True)
                p.write_bytes(data)
                p.chmod(0o755)
                q = inbox / name
                q.parent.mkdir(parents=True)
                shutil.copyfile(p, q)
                q.chmod(0o600)
                d = dict(identity, name=name, size=len(data), sha256=hashlib.sha256(data).hexdigest(),
                         kind='binary', goos=goos, goarch=goarch)
                if goos == 'darwin':
                    d.update(unsigned_sha256='a'*64, signing={'tool': 'fixture', 'status': 'notarized'})
                files.append(d)
                expected[(goos, goarch)] = data
        shutil.rmtree(producer)
        request = dict(intent=intent, files=files, docs=['README.md', 'LICENSE', 'docs'],
                       worktree_path=str(source), artifact_inbox_path=str(inbox), artifact_outbox_path=str(outbox))
        if schemas:
            Draft202012Validator(schemas['input_schema']).validate(request)
        cwd.chmod(0o555)
        run_env = dict(os.environ, PATH='' if empty_path else os.environ.get('PATH', ''), FIXTURE_SECRET='DO_NOT_LEAK_SENTINEL')
        def run(value):
            return subprocess.run([executable], input=json.dumps(value), text=True, capture_output=True,
                                  cwd=cwd, env=run_env, timeout=60)
        try:
            result = run(request)
            assert result.returncode == 0, result.stderr
            assert not result.stderr, result.stderr
            envelope = json.loads(result.stdout)
            assert set(envelope) == {'output'} and len(result.stdout.encode()) <= 128*1024
            output = envelope['output']
            if schemas:
                Draft202012Validator(schemas['output_schema']).validate(output)
            assert output['intent'] == intent
            descriptors = output['files']
            assert len(descriptors) == 5
            archives = descriptors[:-1]
            names = [d['name'] for d in archives]
            assert names == sorted(names)
            assert descriptors[-1]['name'] == 'checksums.txt'
            checksums = []
            for d in descriptors:
                content = (outbox / d['name']).read_bytes()
                assert len(content) == d['size'] and hashlib.sha256(content).hexdigest() == d['sha256']
                assert all(d[k] == v for k, v in identity.items())
                if d['kind'] != 'archive':
                    continue
                os_name = {'linux':'Linux', 'darwin':'Darwin'}[d['goos']]
                arch_name = {'amd64':'x86_64', 'arm64':'arm64'}[d['goarch']]
                assert d['name'] == f'cli_1.2.3_{os_name}_{arch_name}.tar.gz'
                checksums.append(f"{hashlib.sha256(content).hexdigest()}  {d['name']}\n")
                with tarfile.open(fileobj=io.BytesIO(content), mode='r:gz') as archive:
                    members = archive.getmembers()
                    assert members[0].name == 'cli'
                    assert {m.name for m in members} == {'cli','README.md','LICENSE','docs','docs/guide.txt'}
                    for member in members:
                        assert member.uid == member.gid == member.mtime == 0
                        assert not member.uname and not member.gname
                        assert member.isfile() or member.isdir()
                    destination = base / ('extract-' + d['goos'] + '-' + d['goarch'])
                    archive.extractall(destination, filter='data')
                    binary = destination / 'cli'
                    assert binary.read_bytes() == expected[(d['goos'],d['goarch'])]
                    assert binary.stat().st_mode & 0o777 == 0o755
                    assert (destination / 'docs/guide.txt').read_bytes() == b'committed docs\n'
            assert (outbox / 'checksums.txt').read_text() == ''.join(checksums)
            # Retry with shuffled descriptors is byte-identical and reconciles.
            request['files'].reverse()
            again = run(request)
            assert again.returncode == 0 and json.loads(again.stdout) == envelope, again.stderr
            assert not list(cwd.iterdir())
            # Tracked edits cannot be hidden by either index flag. Validation
            # must leave the caller's index byte-for-byte unchanged.
            retained = {p.name: p.read_bytes() for p in outbox.iterdir()}
            for flag in ('assume-unchanged', 'skip-worktree'):
                git('update-index', '--' + flag, 'README.md')
                index = source / '.git/index'
                before = index.read_bytes()
                (source / 'README.md').write_bytes(b'hidden tracked modification\n')
                failed = run(request)
                assert failed.returncode != 0 and not failed.stdout and failed.stderr
                assert index.read_bytes() == before
                assert {p.name: p.read_bytes() for p in outbox.iterdir()} == retained
                (source / 'README.md').write_bytes(b'committed readme\n')
                git('update-index', '--no-' + flag, 'README.md')
            # Corrupt a child tree under its original storage key, pointing it
            # at a valid but uncommitted blob. Root/leaf checks alone miss this.
            tree = git('rev-parse', 'HEAD:docs')
            raw = subprocess.check_output(['git', '-C', str(source), 'cat-file', 'tree', tree], env=env)
            original_blob = bytes.fromhex(git('rev-parse', 'HEAD:docs/guide.txt'))
            injected = base / 'injected-document'
            injected.write_bytes(b'uncommitted injected documentation\n')
            new_blob = bytes.fromhex(git('hash-object', '-w', str(injected)))
            corrupt = raw.replace(original_blob, new_blob, 1)
            assert corrupt != raw
            obj = source / '.git/objects' / tree[:2] / tree[2:]
            original_object, original_mode = obj.read_bytes(), obj.stat().st_mode & 0o777
            obj.chmod(0o600)
            obj.write_bytes(zlib.compress(b'tree ' + str(len(corrupt)).encode() + b'\0' + corrupt))
            failed = run(request)
            assert failed.returncode != 0 and not failed.stdout and failed.stderr
            assert {p.name: p.read_bytes() for p in outbox.iterdir()} == retained
            obj.write_bytes(original_object)
            obj.chmod(original_mode)
            # Restoring healthy source must restore successful reconciliation.
            restored = run(request)
            assert restored.returncode == 0 and json.loads(restored.stdout) == envelope, restored.stderr
            for failure in ('missing', 'truncated', 'tampered'):
                p = inbox / files[0]['name']
                original = p.read_bytes()
                if failure == 'missing':
                    p.unlink()
                elif failure == 'truncated':
                    p.write_bytes(original[:-1])
                else:
                    p.write_bytes(bytes([original[0]^1])+original[1:])
                failed = run(request)
                assert failed.returncode != 0 and not failed.stdout and failed.stderr
                assert 'DO_NOT_LEAK_SENTINEL' not in failed.stderr
                p.write_bytes(original)
            print('release.pack: four archives, extracted bytes/modes, final checksums, source integrity, explicit roots and hydration failure controls passed')
        finally:
            cwd.chmod(0o755)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('executable')
    parser.add_argument('--empty-path', action='store_true', help='Installed wrapper must provide its own Git runtime')
    parser.add_argument('--manifest', type=Path, help='Validate successful request/result schemas (requires jsonschema)')
    args = parser.parse_args()
    check(args.executable, empty_path=args.empty_path, manifest=args.manifest)


if __name__ == '__main__':
    main()
