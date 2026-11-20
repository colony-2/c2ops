"""Retained artifact regressions use real installation and independent services."""
import copy
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from npm_publish_fixture import TOKEN, fixture, git, op, prepare_artifact, transfer_artifacts


class NpmPhaseTests(unittest.TestCase):
    def test_temporary_buffers_are_external_and_bad_temp_never_allocates(self):
        with fixture() as f:
            real = tempfile.TemporaryFile
            buffers = []
            def temporary_file(*args, **kwargs):
                directory = Path(kwargs['dir']).resolve()
                self.assertFalse(any(directory.is_relative_to(root) for root in (f.workspace, f.inbox, f.outbox)))
                buffers.append(directory)
                return real(*args, **kwargs)
            with patch.object(tempfile, 'TemporaryFile', temporary_file):
                prepare_artifact(f)
            self.assertTrue(buffers)
            alias = f.root / 'bad-temp'
            alias.symlink_to(f.workspace)
            with patch.dict(os.environ, {'TMPDIR': str(alias)}), \
                 patch.object(tempfile, 'tempdir', None), \
                 patch.object(tempfile, 'TemporaryFile', side_effect=AssertionError('allocated before rejection')), \
                 patch.object(tempfile, 'TemporaryDirectory', side_effect=AssertionError('allocated before rejection')), \
                 self.assertRaisesRegex(op.Failure, 'scratch must be outside bound roots'):
                f.execute()
            self.assertEqual(f.events, [])

    def no_pack(self, argv, *args, **kwargs):
        self.assertNotEqual(argv[:2], ['npm', 'pack'], 'retained bytes must never be packed')
        return self.real_run(argv, *args, **kwargs)

    def test_credential_free_prepare_and_resume_install_original_bytes(self):
        with fixture() as f:
            # A prepare call cannot even read a token path.
            f.token.unlink()
            runtime_path = os.environ['PATH']
            with patch.dict(os.environ, {'PATH': runtime_path}, clear=True):
                result = prepare_artifact(f)
            out = result['output']
            retained = f.outbox / out['files'][0]['name']
            data = retained.read_bytes()
            self.assertEqual(out['phase'], 'prepare')
            self.assertEqual(out['install_check']['status'], 'passed')
            self.assertEqual(out['registry'], {'url': f.request['registry'],
                'integrity': op.integrity(retained), 'outcome': 'not_attempted'})
            self.assertEqual(f.events, [])
            self.assertEqual(sum(c['argv'][:2] == ['npm', 'pack'] for c in f.commands), 1)
            f.resume(result)
            self.real_run = op.run
            with patch.object(op, 'run', self.no_pack), \
                 patch.object(op, 'prepare', side_effect=AssertionError('resume staged metadata')):
                repeated = f.execute()
            self.assertEqual(repeated, result)
            self.assertEqual(f.events, [])
            self.assertEqual(retained.read_bytes(), data)
            self.assertEqual(sum(c['argv'][:2] == ['npm', 'install'] for c in f.commands), 2)

    def test_six_artifacts_reject_missing_truncated_corrupt_or_changed_identity(self):
        with fixture() as f:
            result = prepare_artifact(f)
            transfer_artifacts(f, result)
            good = copy.deepcopy(f.request)
            self.real_run = op.run
            for phase in ('prepare', 'publish'):
                for index in range(6):
                    for damage in ('missing', 'truncated', 'corrupt', 'identity'):
                        with self.subTest(phase=phase, index=index, damage=damage):
                            f.request = copy.deepcopy(good)
                            f.request['phase'] = phase
                            if phase == 'publish':
                                f.request['registry_token_file'] = str(f.token)
                            desc = (f.request['files'] + [f.request['retained_tarball']])[index]
                            path = f.inbox / desc['name']
                            data = path.read_bytes()
                            try:
                                if damage == 'missing':
                                    path.unlink()
                                elif damage == 'truncated':
                                    path.write_bytes(data[:len(data)//2])
                                elif damage == 'corrupt':
                                    path.write_bytes(bytes([data[0] ^ 1]) + data[1:])
                                else:
                                    desc['release_id'] = 'different-release'
                                # Matching registry state cannot rescue invalid local bytes.
                                f.registry_bytes = (f.inbox / good['retained_tarball']['name']).read_bytes() if index != 5 or damage == 'identity' else b'matching remote state'
                                with patch.object(op, 'run', self.no_pack), self.assertRaises(op.Failure):
                                    f.execute()
                                self.assertEqual(f.events, [])
                            finally:
                                path.write_bytes(data)

    def test_retained_descriptor_and_committed_content_fail_without_repair(self):
        with fixture() as f:
            result = prepare_artifact(f); f.resume(result)
            base = copy.deepcopy(f.request)
            path = f.inbox / base['retained_tarball']['name']
            original = path.read_bytes()
            for damage in ('name', 'size', 'sha256', 'package.json', 'scripts/postinstall.js', 'README.md'):
                with self.subTest(damage=damage):
                    f.request = copy.deepcopy(base)
                    if damage == 'name':
                        f.request['retained_tarball']['name'] = 'npm/other.tgz'
                    elif damage in ('size', 'sha256'):
                        f.request['retained_tarball'][damage] = 1 if damage == 'size' else '0' * 64
                    else:
                        entries = op.archive_files(path)
                        entries[damage] += b' '
                        with tarfile.open(path, 'w:gz') as archive:
                            for name, data in entries.items():
                                member = tarfile.TarInfo('package/' + name); member.size = len(data)
                                archive.addfile(member, io.BytesIO(data))
                        f.request['retained_tarball'].update(size=path.stat().st_size, sha256=op.digest(path))
                    try:
                        with patch.object(op, 'prepare', side_effect=AssertionError('resume staged')), \
                             self.assertRaises(op.Failure):
                            f.execute()
                        self.assertEqual(f.events, [])
                    finally:
                        path.write_bytes(original)

    def test_resumed_prepare_rechecks_public_assets_source_and_documents(self):
        for damage in ('source', 'wrapper', 'document', 'asset', 'private', 'tag'):
            with self.subTest(damage=damage), fixture() as f:
                result = prepare_artifact(f); f.resume(result)
                if damage == 'source':
                    f.request['intent']['source_commit'] = 'a' * 40
                elif damage == 'wrapper':
                    (f.workspace / 'npm/c2j/bin/cli.js').write_text('changed')
                elif damage == 'document':
                    (f.workspace / 'README.md').write_text('changed')
                elif damage == 'asset':
                    name = f.request['files'][0]['name']; f.asset_bytes[name] = b'bad'
                elif damage == 'private':
                    f.private = True
                else:
                    f.tag_commit = 'a' * 40
                with self.assertRaises(op.Failure):
                    f.execute()
                self.assertEqual(f.events, [])

    def test_output_conflict_and_result_bounds_block_registry(self):
        for damage in ('outbox', 'oversized', 'invalid'):
            with self.subTest(damage=damage), fixture() as f:
                result = prepare_artifact(f); f.resume(result)
                f.request.update(phase='publish', registry_token_file=str(f.token))
                if damage == 'outbox':
                    retained = f.outbox / result['output']['files'][0]['name']
                    retained.chmod(0o600); retained.write_bytes(b'conflict')
                    with self.assertRaisesRegex(op.Failure, 'conflicts'):
                        f.execute()
                    self.assertEqual(retained.read_bytes(), b'conflict')
                else:
                    real = op.install_check
                    def install(*args):
                        checked = real(*args)
                        if damage == 'oversized':
                            # Valid schema but aggregate size exceeds the envelope bound.
                            f.request['intent']['release_id'] = 'x' * op.LIMIT
                        else:
                            checked['status'] = 'failed'
                        return checked
                    with patch.object(op, 'install_check', install), self.assertRaises(op.Failure):
                        f.execute()
                self.assertEqual(f.events, [])

    def test_publish_uses_native_token_authorization(self):
        with fixture() as f:
            result = prepare_artifact(f); f.resume(result)
            f.request.update(phase='publish', registry_token_file=str(f.token))
            for damage in ('permissions', 'missing', 'inside'):
                with self.subTest(damage=damage):
                    if damage == 'permissions':
                        f.token.chmod(0o644)
                    elif damage == 'missing':
                        f.token.unlink()
                    else:
                        f.request['registry_token_file'] = str(f.workspace / 'untracked.txt')
                    with patch.dict(os.environ, {'ACTIONS_ID_TOKEN_REQUEST_TOKEN': TOKEN}), self.assertRaises(op.Failure):
                        f.execute()
                    self.assertEqual(f.events, [])

    def test_registry_absence_conflict_retry_cancellation_and_dist_tags(self):
        for variant in ('absent', 'conflict', 'fallback', 'download_conflict',
                        'unknown', 'malformed', 'retry', 'cancel', 'absent_after_success', 'dist_tag'):
            with self.subTest(variant=variant), fixture() as f:
                result = prepare_artifact(f); f.resume(result)
                f.request.update(phase='publish', registry_token_file=str(f.token))
                data = (f.outbox / result['output']['files'][0]['name']).read_bytes()
                if variant in ('fallback', 'download_conflict', 'dist_tag'):
                    f.registry_bytes = data
                if variant == 'conflict':
                    f.registry_bytes = b'conflicting'
                if variant in ('fallback', 'download_conflict'):
                    f.no_integrity = True
                if variant == 'download_conflict':
                    f.registry_bytes = b'corrupt'
                if variant == 'unknown':
                    f.registry_unavailable = True
                if variant == 'malformed':
                    f.registry_responses = [None]
                if variant == 'dist_tag':
                    f.request['dist_tag'] = 'other'
                    f.registry_responses = [{'name': f.request['package_name'], 'version': '1.2.3',
                        'dist': {'integrity': result['output']['registry']['integrity']},
                        'dist-tags': {'other': '9.9.9'}}]
                calls = []
                real = op.run
                def run(argv, *args, **kwargs):
                    self.assertNotEqual(argv[:2], ['npm', 'pack'])
                    if argv[:2] == ['npm', 'publish']:
                        calls.append(list(f.events))
                        if variant == 'cancel':
                            raise op.Cancelled('fixture cancel')
                        if variant == 'retry':
                            raise op.Failure('fixture timeout')
                        if variant == 'absent_after_success':
                            return b'{}'
                    return real(argv, *args, **kwargs)
                with patch.object(op, 'run', run):
                    if variant in ('conflict', 'download_conflict', 'unknown', 'malformed', 'retry', 'cancel', 'absent_after_success'):
                        with self.assertRaises(op.Failure):
                            f.execute()
                    else:
                        out = f.execute()['output']
                        self.assertEqual(out['registry']['integrity'], out['registry']['observed_integrity'])
                        self.assertEqual(out['phase'], 'publish')
                self.assertEqual(len(calls), 2 if variant == 'retry' else 1 if variant in ('absent', 'cancel', 'absent_after_success') else 0)
                if variant == 'retry':
                    self.assertEqual(len(f.events), 4)
                    self.assertEqual(calls[1][-2:], ['GET', 'GET'])
                if variant == 'dist_tag':
                    self.assertEqual(f.publications, [])
                    self.assertNotIn('dist_tag', out['registry'])
    def test_root_overlap_and_install_byte_mismatch_block_registry(self):
        for variant in ('overlap', 'inside', 'alias', 'installed_bytes'):
            with self.subTest(variant=variant), fixture() as f:
                f.request['phase'] = 'prepare'
                f.request.pop('registry_token_file')
                if variant == 'overlap':
                    f.request['outbox'] = str(f.inbox)
                elif variant == 'inside':
                    child = f.workspace / 'nested-outbox'; child.mkdir()
                    f.request['outbox'] = str(child)
                elif variant == 'alias':
                    alias = f.root / 'workspace-alias'; alias.symlink_to(f.workspace)
                    f.request['workspace'] = str(alias)
                real = op.run
                def run(argv, cwd, env=None, timeout=120):
                    result = real(argv, cwd, env, timeout)
                    if variant == 'installed_bytes' and argv[:2] == ['npm', 'install']:
                        (cwd / 'node_modules/@colony2/c2j/vendor/c2j').write_bytes(b'wrong executable')
                    return result
                with patch.object(op, 'run', run), self.assertRaises(op.Failure):
                    f.execute()
                self.assertEqual(f.events, [])
                if variant == 'installed_bytes':
                    self.assertTrue((f.outbox / 'npm/c2j-1.2.3.tgz').is_file())
