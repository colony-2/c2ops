"""N01–N07 and npm-owned C contracts, with independent service boundaries."""
import contextlib
import copy
import io
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from npm_publish_fixture import MODULE, TOKEN, fixture, git, op


class NpmPublicationTests(unittest.TestCase):
    def test_N01_committed_source_metadata_and_conventions(self):
        with fixture() as f:
            before = {str(p.relative_to(f.workspace)): p.read_bytes() for p in f.workspace.rglob('*') if p.is_file()}
            (f.workspace / 'ignored.txt').write_text('ambient ignored data')
            (f.workspace / 'npm/c2j/untracked.js').write_text('ambient injected code')
            commands = []
            real = op.run
            def recording(argv, *args, **kwargs):
                commands.append([str(x) for x in argv])
                return real(argv, *args, **kwargs)
            with patch.object(op, 'run', recording):
                result = f.execute()
            desc = result['output']['files'][0]
            entries = op.archive_files(f.outbox / desc['name'])
            pkg = json.loads(entries['package.json'])
            original = json.loads(before['npm/c2j/package.json'])
            original['version'] = '1.2.3'
            self.assertEqual(pkg, original)
            self.assertEqual(entries['bin/cli.js'], before['npm/c2j/bin/cli.js'])
            self.assertEqual(entries['scripts/postinstall.js'], before['npm/c2j/scripts/postinstall.js'])
            self.assertEqual(entries['README.md'], before['README.md'])
            self.assertNotIn('untracked.js', entries)
            self.assertEqual([c[1] for c in commands if c[0] == 'npm' and c[1] == 'pack'], ['pack'])
            for name, data in before.items():
                self.assertEqual((f.workspace / name).read_bytes(), data, name)
            self.assertTrue((f.workspace / 'ignored.txt').exists())
            op.validate(result, op.SCHEMA['$defs']['result'])

    def test_N02_reject_metadata_and_public_release_mismatches(self):
        # Each negative has an independent fixture and asserts no registry use.
        for variant in ('version', 'repository', 'draft_input', 'incomplete_input', 'draft_remote',
                        'private_remote', 'incomplete_remote', 'tag_commit', 'release_commit', 'asset_hash'):
            with self.subTest(variant=variant), fixture() as f:
                if variant in ('version', 'repository'):
                    p = f.workspace / 'npm/c2j/package.json'
                    pkg = json.loads(p.read_text())
                    pkg[variant] = '2.0.0' if variant == 'version' else 'https://github.com/other/repo'
                    p.write_text(json.dumps(pkg)); git(f.workspace, 'add', '.')
                    git(f.workspace, '-c', 'user.name=Fixture', '-c', 'user.email=f@example.test', 'commit', '-qm', 'bad metadata')
                    commit = git(f.workspace, 'rev-parse', 'HEAD')
                    f.request['intent']['source_commit'] = commit
                    f.request['release']['commit'] = commit
                    for d in f.request['files'] + f.request['release']['assets']:
                        d['source_commit'] = commit
                elif variant == 'draft_input': f.request['release']['draft'] = True
                elif variant == 'incomplete_input': f.request['release']['assets'].pop()
                elif variant == 'draft_remote': f.draft = True
                elif variant == 'private_remote': f.private = True
                elif variant == 'incomplete_remote': f.incomplete = True
                elif variant == 'tag_commit': f.tag_commit = 'a' * 40
                elif variant == 'release_commit': f.request['release']['commit'] = 'a' * 40
                elif variant == 'asset_hash':
                    name = f.request['files'][0]['name']; f.asset_bytes[name] = b'bad'
                with self.assertRaises(op.Failure): f.execute()
                self.assertEqual(f.events, [])

    def test_N03_install_and_checksum_failure_retain_tarball_without_publication(self):
        for failure in ('install', 'checksum', 'executable'):
            with self.subTest(failure=failure), fixture() as f:
                if failure == 'checksum': f.corrupt_install = True
                if failure == 'executable': f.request['check']['stdout'] = 'wrong 1.2.3\n'
                real = op.run
                def run(argv, *args, **kwargs):
                    if failure == 'install' and argv[:2] == ['npm', 'install']:
                        raise op.Failure('fixture install failure')
                    return real(argv, *args, **kwargs)
                with patch.object(op, 'run', run), self.assertRaises(op.Failure): f.execute()
                self.assertTrue((f.outbox / 'npm/c2j-1.2.3.tgz').is_file())
                self.assertEqual(f.publications, [])

    def test_N04_exact_bytes_and_resume_never_repacks(self):
        with fixture() as f:
            result = f.execute()
            tarball = f.outbox / result['output']['files'][0]['name']
            tested = tarball.read_bytes()
            self.assertEqual(f.publications, [tested])
            self.assertNotIn(TOKEN.encode(), tested)
            f.resume(result)
            real = op.run
            def run(argv, *args, **kwargs):
                self.assertFalse(argv[:2] == ['npm', 'pack'], 'resume must never pack')
                return real(argv, *args, **kwargs)
            with patch.object(op, 'run', run): repeated = f.execute()
            self.assertEqual(repeated['output']['registry']['outcome'], 'reconciled')
            self.assertEqual(tarball.read_bytes(), tested)
            self.assertEqual(f.publications, [tested])

    def test_N04_resume_rejects_tampered_content_even_with_recomputed_descriptor(self):
        with fixture() as f:
            result = f.execute(); f.resume(result)
            p = f.inbox / f.request['retained_tarball']['name']
            entries = op.archive_files(p)
            entries['scripts/postinstall.js'] = b'throw new Error("tampered")'
            with tarfile.open(p, 'w:gz') as archive:
                for name, data in entries.items():
                    entry = tarfile.TarInfo('package/' + name); entry.size = len(data)
                    archive.addfile(entry, io.BytesIO(data))
            f.request['retained_tarball'].update(size=p.stat().st_size, sha256=op.digest(p))
            with self.assertRaisesRegex(op.Failure, 'differs from committed'): f.execute()
            self.assertEqual(len(f.publications), 1)

    def test_N05_explicit_native_auth_no_oidc(self):
        with fixture() as f:
            f.token.unlink()
            with patch.dict(os.environ, {'ACTIONS_ID_TOKEN_REQUEST_TOKEN': TOKEN}), \
                 self.assertRaisesRegex(op.Failure, 'explicit private registry token'):
                f.execute()
            self.assertEqual(f.events, [])

    def test_N06_existing_integrity_reconciles_or_requires_new_version(self):
        for variant in ('matching', 'conflicting', 'fallback', 'unavailable'):
            with self.subTest(variant=variant), fixture() as f:
                result = f.execute(); f.resume(result)
                if variant == 'conflicting': f.registry_bytes = b'different'
                if variant == 'fallback': f.no_integrity = True
                if variant == 'unavailable': f.registry_unavailable = True
                if variant in ('conflicting', 'unavailable'):
                    with self.assertRaises(op.Failure) as error: f.execute()
                    self.assertNotIn(TOKEN, str(error.exception))
                    if variant == 'conflicting': self.assertIn('new version required', str(error.exception))
                else:
                    repeated = f.execute()
                    self.assertEqual(repeated['output']['registry']['outcome'], 'reconciled')
                self.assertEqual(len(f.publications), 1)

    def test_N07_lost_publish_response_reads_and_reconciles(self):
        with fixture() as f:
            f.ambiguous = True
            result = f.execute()
            self.assertEqual(result['output']['registry']['outcome'], 'reconciled')
            self.assertEqual(len(f.publications), 1)
            i = f.events.index('PUT')
            self.assertEqual(f.events[i+1], 'GET')

    def test_N07_timeout_requires_reads_before_only_retry(self):
        with fixture() as f:
            real = op.run; calls = []
            def run(argv, *args, **kwargs):
                if argv[:2] == ['npm', 'publish']:
                    calls.append(list(f.events))
                    if len(calls) == 1:
                        raise op.Failure('subprocess timeout; remote publication may need reconciliation')
                return real(argv, *args, **kwargs)
            with patch.object(op, 'run', run): result = f.execute()
            self.assertEqual(result['output']['registry']['outcome'], 'published')
            self.assertEqual(len(calls), 2)
            self.assertGreaterEqual(len(calls[1]) - len(calls[0]), 2)
            self.assertEqual(calls[1][-2:], ['GET', 'GET'])
            self.assertEqual(len(f.publications), 1)

    def test_N07_unknown_state_and_cancellation_never_retry(self):
        for cancel in (False, True):
            with self.subTest(cancel=cancel), fixture() as f:
                real = op.run; calls = []
                def run(argv, *args, **kwargs):
                    if argv[:2] == ['npm', 'publish']:
                        calls.append(1); f.registry_unavailable = True
                        raise op.Cancelled('cancelled') if cancel else op.Failure('timeout')
                    return real(argv, *args, **kwargs)
                with patch.object(op, 'run', run), self.assertRaises(op.Failure): f.execute()
                self.assertEqual(len(calls), 1)

    def test_N06_N07_null_response_never_authorizes_publication_or_retry(self):
        for after_timeout in (False, True):
            with self.subTest(after_timeout=after_timeout), fixture() as f:
                real = op.run; calls = []
                if not after_timeout:
                    f.registry_responses = [None]
                def run(argv, *args, **kwargs):
                    if argv[:2] == ['npm', 'publish']:
                        calls.append(1)
                        if after_timeout and len(calls) == 1:
                            f.registry_responses = [None]
                            raise op.Failure('timeout')
                    return real(argv, *args, **kwargs)
                with patch.object(op, 'run', run), self.assertRaisesRegex(op.Failure, 'registry metadata'):
                    f.execute()
                self.assertEqual(len(calls), int(after_timeout))
                self.assertEqual(f.publications, [])
                self.assertEqual(f.events, ['GET'] * (1 + int(after_timeout)))

    def test_N06_malformed_registry_metadata_fails_closed(self):
        with fixture() as f:
            tarball = f.root / 'candidate.tgz'
            tarball.write_bytes(b'tested bytes')
            valid = {'name': f.request['package_name'], 'version': '1.2.3',
                     'dist': {'integrity': op.integrity(tarball)}}
            invalid = [None, [], 'metadata', True, 1, {},
                       {**valid, 'name': 'other'}, {**valid, 'version': 'other'},
                       {**valid, 'dist': None}, {**valid, 'dist': []},
                       {**valid, 'dist': {}}, {**valid, 'dist': {'integrity': None}},
                       {**valid, 'dist': {'integrity': []}},
                       {**valid, 'dist': {'tarball': 1}}]
            with f.routing():
                network = op.Network()
                # Healthy controls distinguish HTTP 404 from a valid object.
                self.assertFalse(op.observe_registry(f.request, tarball, network, f.root, TOKEN))
                f.registry_responses = [valid]
                self.assertTrue(op.observe_registry(f.request, tarball, network, f.root, TOKEN))
                for response in invalid:
                    with self.subTest(response=response):
                        f.registry_responses = [response]
                        with self.assertRaises(op.Failure):
                            op.observe_registry(f.request, tarball, network, f.root, TOKEN)
            self.assertEqual(f.publications, [])

    def test_C03_symlinked_tmpdir_uses_canonical_scratch(self):
        with tempfile.TemporaryDirectory() as directory:
            physical = Path(directory) / 'physical-temp'
            physical.mkdir()
            alias = Path(directory) / 'temp-alias'
            alias.symlink_to(physical, target_is_directory=True)
            with patch.dict(os.environ, {'TMPDIR': str(alias)}), patch.object(tempfile, 'tempdir', None), fixture() as f:
                source = (f.workspace / 'npm/c2j/package.json').read_bytes()
                result = f.execute()
                self.assertEqual(result['output']['registry']['outcome'], 'published')
                self.assertEqual(f.publications, [(f.outbox / result['output']['files'][0]['name']).read_bytes()])
                self.assertEqual((f.workspace / 'npm/c2j/package.json').read_bytes(), source)
            self.assertEqual(list(physical.iterdir()), [])

    def test_C03_symlinked_tmpdir_inside_bound_root_is_rejected(self):
        with fixture() as f:
            alias = f.root / 'temp-alias'
            alias.symlink_to(f.workspace, target_is_directory=True)
            with patch.dict(os.environ, {'TMPDIR': str(alias)}), patch.object(tempfile, 'tempdir', None):
                with self.assertRaisesRegex(op.Failure, 'scratch must be outside bound roots'):
                    f.execute()
            self.assertEqual(f.events, [])
            self.assertEqual(f.asset_requests, [])

    def test_C01_C04_C06_contract_negatives_before_network(self):
        with fixture() as f:
            original = copy.deepcopy(f.request)
            for variant in ('schema', 'missing', 'duplicate', 'identity', 'traversal', 'symlink', 'truncated', 'hash', 'resume_missing'):
                with self.subTest(variant=variant):
                    f.request = copy.deepcopy(original)
                    path = f.inbox / f.request['files'][0]['name']; data = path.read_bytes()
                    if variant == 'schema': f.request['intent']['schema_version'] = 2
                    elif variant == 'missing': del f.request['intent']['release_id']
                    elif variant == 'duplicate': f.request['files'][1] = f.request['files'][0]
                    elif variant == 'identity': f.request['files'][0]['source_commit'] = 'b' * 40
                    elif variant == 'traversal': f.request['files'][0]['name'] = '../escape'
                    elif variant == 'symlink':
                        path.unlink(); path.symlink_to(f.token)
                    elif variant == 'truncated': path.write_bytes(data[:2])
                    elif variant == 'hash':
                        path.write_bytes(bytes([data[0] ^ 1]) + data[1:])
                    elif variant == 'resume_missing': f.request['resume'] = True
                    with self.assertRaises(op.Failure): f.execute()
                    if path.is_symlink(): path.unlink()
                    path.write_bytes(data)
                    self.assertEqual(f.events, []); self.assertEqual(f.asset_requests, [])

    def test_source_projection_complete_modes_and_no_ambient_inputs(self):
        with fixture() as f:
            source = f.root / 'projection'
            (f.workspace / 'ignored.txt').write_text('untrusted')
            (f.workspace / 'untracked.txt').write_text('untrusted')
            tree = op.project(f.workspace, f.request['intent']['source_commit'], source)
            self.assertEqual(tree, git(f.workspace, 'rev-parse', 'HEAD^{tree}'))
            self.assertEqual((source / 'README.md').read_bytes(), (f.workspace / 'README.md').read_bytes())
            self.assertTrue(os.access(source / 'npm/c2j/bin/cli.js', os.X_OK))
            self.assertFalse((source / 'ignored.txt').exists())
            self.assertFalse((source / 'untracked.txt').exists())

    def test_source_mismatch_tracked_changes_escaping_links_and_local_overrides(self):
        for variant in ('head', 'tracked', 'symlink', 'workspaces', 'local_dependency'):
            with self.subTest(variant=variant), fixture() as f:
                commit = f.request['intent']['source_commit']
                if variant == 'head': commit = 'b' * 40
                elif variant == 'tracked': (f.workspace / 'README.md').write_text('edited')
                else:
                    if variant == 'symlink': (f.workspace / 'escape').symlink_to(f.root)
                    else:
                        p = f.workspace / 'npm/c2j/package.json'; pkg = json.loads(p.read_text())
                        if variant == 'workspaces': pkg['workspaces'] = ['../../elsewhere']
                        else: pkg['dependencies'] = {'local': 'file:../../elsewhere'}
                        p.write_text(json.dumps(pkg))
                    git(f.workspace, 'add', '.')
                    git(f.workspace, '-c', 'user.name=Fixture', '-c', 'user.email=f@example.test', 'commit', '-qm', 'bad source')
                    commit = git(f.workspace, 'rev-parse', 'HEAD')
                with self.assertRaises(op.Failure):
                    op.project(f.workspace, commit, f.root / 'projection')
                    op.prepare(f.request, f.root / 'projection', f.root / 'prepared')

    def test_C02_C07_C09_process_failure_json_bounds_and_secret_suppression(self):
        for data in (b'{}', b'invalid', b'x' * (op.LIMIT + 1), json.dumps({'secret': TOKEN}).encode()):
            result = subprocess.run([sys.executable, str(MODULE)],
                input=data, capture_output=True, cwd='/', timeout=5)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(result.stdout, b'')
            self.assertTrue(result.stderr)
            self.assertNotIn(TOKEN.encode(), result.stderr)
            self.assertNotIn(b'Traceback', result.stderr)
        with self.assertRaisesRegex(op.Failure, '128 KiB'):
            op.json_bytes({'blob': 'x' * op.LIMIT})
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaises(op.Failure) as error:
                op.run([sys.executable, '-c', 'import sys;print(sys.argv[1],file=sys.stderr);sys.exit(1)', TOKEN], Path(tmp))
            self.assertNotIn(TOKEN, str(error.exception))

    def test_C09_install_environment_excludes_caller_credentials_and_node_options(self):
        with tempfile.TemporaryDirectory() as tmp, patch.dict(os.environ, {
                'NPM_TOKEN': TOKEN, 'GITHUB_TOKEN': TOKEN, 'NODE_OPTIONS': '--require=evil',
                'NPM_CONFIG_USERCONFIG': '/secret/path', 'GIT_CONFIG_COUNT': '1'}):
            env = op.clean_env(Path(tmp), 'https://registry.fixture.test/')
            self.assertNotIn(TOKEN, json.dumps(env))
            self.assertNotIn('NODE_OPTIONS', env)
            self.assertNotIn('GIT_CONFIG_COUNT', env)
            self.assertNotEqual(env['NPM_CONFIG_USERCONFIG'], '/secret/path')
            self.assertEqual(env['NPM_CONFIG_FETCH_RETRIES'], '0')

    def test_subprocess_timeout_kills_descendant_group(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); marker = root / 'should-not-exist'
            child = 'import time;from pathlib import Path;time.sleep(2);Path(' + repr(str(marker)) + ').touch()'
            parent = 'import subprocess,sys,time;subprocess.Popen([sys.executable,"-c",sys.argv[1]]);time.sleep(20)'
            with self.assertRaisesRegex(op.Failure, 'timeout'):
                op.run([sys.executable, '-c', parent, child], root, timeout=0.1)
            # A separate process wait avoids test time depending on shell polling.
            subprocess.run([sys.executable, '-c', 'import time;time.sleep(2.1)'], check=True)
            self.assertFalse(marker.exists())


if __name__ == '__main__':
    unittest.main()
