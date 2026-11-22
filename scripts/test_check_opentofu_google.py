"""Local credential-driver validation and privacy tests; never contact Google."""
import contextlib
import copy
import datetime as dt
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from opentofu_google import CASES, SafeError, now, profile
from test_setup_opentofu_google import AGE, PRIVATE

spec = importlib.util.spec_from_file_location('check_google', Path(__file__).with_name('check-opentofu-google.py'))
driver = importlib.util.module_from_spec(spec); spec.loader.exec_module(driver)


def public_profile():
    return dict(version=1, project='tofu-test-project', bucket='positive-bucket', negative_bucket='negative-bucket',
                sentinel='denial-sentinel', source_email='source@tofu-test-project.iam.gserviceaccount.com',
                target_email='target@tofu-test-project.iam.gserviceaccount.com', token_secret='test_token', key_secret='test_key',
                timeout_seconds=600, age_recipients=[AGE], prefixes={k: 'test/' + k for k in CASES}, negative_confirmed=True)


class CredentialTests(unittest.TestCase):
    def test_g06_authoritative_identity_required(self):
        expected = public_profile()['source_email']
        for status, value in [(200, {}), (200, {'email': expected}), (200, {'email': 'other', 'email_verified': True}),
                              (401, {'email': expected, 'email_verified': True})]:
            with self.subTest(status=status, value=value), patch.object(driver, 'http_json', return_value=(status, json.dumps(value).encode())):
                with self.assertRaisesRegex(SafeError, 'authoritative_identity_mismatch'):
                    driver.identity(PRIVATE, expected)
        with patch.object(driver, 'http_json', return_value=(200, json.dumps({'email': expected, 'email_verified': 'true'}).encode())):
            driver.identity(PRIVATE, expected)

    def test_g05_only_explicit_denial_counts(self):
        for status in (200, 401, 404, 429, 500, 0):
            with self.subTest(status=status), patch.object(driver, 'storage', return_value=(status, b'')):
                with self.assertRaises(SafeError):
                    driver.denial(public_profile(), PRIVATE)
        with patch.object(driver, 'storage', return_value=(403, b'')):
            driver.denial(public_profile(), PRIVATE)

    def test_g07_profile_rejected_before_any_resolution_or_command(self):
        bad = []
        for change in ({'extra': 'unknown'}, {'age_recipients': []}, {'negative_confirmed': False}, {'version': True},
                       {'prefixes': dict(token='same', direct_key='same/nested', impersonated_key='other')}, {'source_email': 'not-email'}):
            p = public_profile(); p.update(change); bad.append(p)
        for p in bad:
            with tempfile.TemporaryDirectory() as tmp, patch.object(driver, 'command') as command, patch.object(driver, 'resolve') as resolve:
                path = Path(tmp) / 'profile.json'; path.write_text(json.dumps(p))
                with patch('sys.argv', ['check', '--profile', str(path), '--out', str(Path(tmp) / 'out')]), contextlib.redirect_stdout(io.StringIO()) as out:
                    self.assertEqual(driver.main(), 0)
                self.assertEqual(json.loads(out.getvalue())['status'], 'failed')
                command.assert_not_called(); resolve.assert_not_called()

    def test_g08_subprocess_output_never_in_error(self):
        for result in (subprocess.CompletedProcess([], 9, PRIVATE.encode(), PRIVATE.encode()),):
            with patch.object(driver.subprocess, 'run', return_value=result), self.assertRaises(SafeError) as e:
                driver.command(['private-helper'])
            self.assertNotIn(PRIVATE, str(e.exception))
        with patch.object(driver, 'command', return_value=json.dumps({'access_token': PRIVATE, 'expires_at': (now() + dt.timedelta(hours=1)).isoformat()}).encode()):
            self.assertEqual(driver.inspector_token(public_profile(), 'token'), PRIVATE)
        for v in ({'access_token': PRIVATE, 'expires_at': 'invalid'}, {'private_key': PRIVATE}):
            with patch.object(driver, 'resolve', return_value=v), self.assertRaises(SafeError) as e:
                driver.inspector_token(public_profile(), 'token')
            self.assertNotIn(PRIVATE, str(e.exception))

    def test_failed_native_result_keeps_ciphertext_before_assertion(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp); recovery = base / 'retained.age'
            request = dict(mode='up', revision='a' * 40, root='.', backend={}, google_auth={'secret_name': 'test_token'}, timeout='600s')
            def execute(argv, **kw):
                c = kw['input_data']
                recovery.write_bytes(b'age-encryption.org/v1\nprivate ciphertext')
                Path(c['ResultPath']).write_text(json.dumps({'accepted': False, 'result': {'ok': False}}))
                return b'fixed safe output'
            with patch.object(driver, 'command', side_effect=execute), self.assertRaisesRegex(SafeError, 'native_deployment_failed'):
                driver.native(base / 'runtime', base / 'manifests', base, request['revision'], request, base / 'private', recovery)
            self.assertTrue(recovery.read_bytes().startswith(b'age-encryption.org/v1'))
            recipe = (base / 'private/recipe.json').read_text()
            self.assertNotIn(PRIVATE, recipe)
            self.assertIn('test_token', recipe)

    def test_namespace_refusal_never_deletes_existing_objects(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(driver, 'manifests'), patch.object(driver, 'inspector_token', return_value=PRIVATE), \
             patch.object(driver, 'identity'), patch.object(driver, 'denial'), patch.object(driver, 'storage', return_value=(200, b'existing')) as storage:
            rows = driver.qualify(public_profile(), Path(tmp), '/package', '/driver')
            self.assertTrue(all(r['status'] == 'failed' for r in rows))
            self.assertFalse(any(c.kwargs.get('method') == 'DELETE' for c in storage.call_args_list))
            self.assertNotIn(PRIVATE, json.dumps(rows))

    def test_g08_identity_failure_stops_before_storage_and_redacts(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(driver, 'manifests'), patch.object(driver, 'inspector_token', return_value=PRIVATE), \
             patch.object(driver, 'identity', side_effect=ValueError(PRIVATE)), patch.object(driver, 'storage') as storage:
            rows = driver.qualify(public_profile(), Path(tmp), '/package', '/driver')
            self.assertEqual(len(rows), 3)
            self.assertTrue(all(r['status'] == 'failed' for r in rows))
            storage.assert_not_called()
            self.assertNotIn(PRIVATE, json.dumps(rows))


if __name__ == '__main__':
    unittest.main()
