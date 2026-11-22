"""Operator command, private storage and failure tests; never call Google."""
import contextlib
import datetime as dt
import importlib.util
import io
import json
import os
from pathlib import Path
import shlex
import stat
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from opentofu_google import SafeError, expiry, now, private_read, private_write, Runner

spec = importlib.util.spec_from_file_location('setup_google', Path(__file__).with_name('setup-opentofu-google-test.py'))
setup = importlib.util.module_from_spec(spec); spec.loader.exec_module(setup)
AGE = 'age1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq5cu47z'
PRIVATE = 'never-export-private-credential'


class Cloud:
    def __init__(self):
        self.calls = []; self.created = False; self.nonce = None; self.fail = None; self.policies = {'bindings': []}
    def run(self, args, **kwargs):
        self.calls.append(args)
        if args[:3] == ['gcloud', 'auth', 'print-access-token']:
            return PRIVATE.encode()
        return b'--tenant --url --value-file --secret --candidate-commit --cell --jobdb'
    def cloud(self, *args, **kwargs):
        self.calls.append(list(args))
        if self.fail and self.fail in args:
            raise SafeError('administrative_command_failed')
        if args[:2] == ('auth', 'list'):
            return [{'account': 'operator@example.invalid'}]
        if args[:2] == ('projects', 'describe'):
            return {'labels': {'c2ops-tofu-test': self.nonce}} if self.created else None
        if args[:2] == ('projects', 'create'):
            self.created = True; self.nonce = next(x.split('=', 2)[2] for x in args if x.startswith('--labels='))
        if 'get-iam-policy' in args:
            return self.policies
        if 'get-ancestors' in args:
            return []
        if args[:4] == ('iam', 'service-accounts', 'keys', 'create'):
            Path(args[4]).write_text(json.dumps({'type': 'service_account', 'client_email': 'tofu-source@tofu-test-project.iam.gserviceaccount.com', 'private_key_id': 'created-key-id', 'private_key': PRIVATE}))
        return {}


def arguments(directory, *extra):
    return setup.parser().parse_args(['setup', '--project', 'tofu-test-project', '--private-dir', str(directory), '--tenant', 'tenant ; literal',
                                     '--admin-url', 'https://admin.invalid/tenant', '--admin-provider', 'development_admin_provider',
                                     '--confirm-development-admin', '--confirm-denial-policy', '--age-recipient', AGE, '--hook-jobdb', 'sqlite:///private/jobdb/c2', '--hook-cell', 'github.com/colony-2/c2ops', '--hook-candidate', 'a' * 40, *extra])


def mint():
    return 200, json.dumps({'accessToken': PRIVATE, 'expireTime': (now() + dt.timedelta(hours=1)).isoformat()}).encode()


class SetupTests(unittest.TestCase):
    def test_s01_invalid_inputs_before_cloud_mutation(self):
        for extra in [('--project', 'invalid!'), ('--admin-provider', 'external'), ('--folder', 'f', '--organization', 'o'), ('--key-secret', 'opentofu_test_token'), ('--age-recipient', 'not-age'),
                      ('--token-secret', 'gitvend_base_token'), ('--key-secret', 'gitvend_base_token')]:
            with tempfile.TemporaryDirectory() as tmp:
                cloud = Cloud()
                with self.assertRaises(SafeError):
                    setup.Setup(arguments(Path(tmp) / 'private', *extra), cloud)
                self.assertEqual(cloud.calls, [])

    def test_s02_s03_dedicated_resources_and_minimal_roles(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(setup, 'http_json', return_value=mint()):
            cloud = Cloud(); s = setup.Setup(arguments(Path(tmp) / 'private', '--folder', '123', '--billing-account', 'bill', '--location', 'EU'), cloud)
            s.preflight(); s.setup()
            create = next(c for c in cloud.calls if c[:2] == ['projects', 'create'])
            self.assertIn('--folder=123', create)
            buckets = [c for c in cloud.calls if c[:3] == ['storage', 'buckets', 'create']]
            self.assertEqual(len(buckets), 2)
            for c in buckets:
                self.assertIn('--location=EU', c); self.assertIn('--uniform-bucket-level-access', c); self.assertIn('--public-access-prevention', c)
            roles = [c for c in cloud.calls if c[:3] == ['iam', 'roles', 'create']]
            self.assertEqual({x for c in roles for a in c if a.startswith('--permissions=') for x in a.split('=', 1)[1].split(',')}, set(setup.OBJECT_PERMISSIONS + ['iam.serviceAccounts.getAccessToken']))
            bindings = [c for c in cloud.calls if 'add-iam-policy-binding' in c]
            self.assertEqual(len(bindings), 3)
            self.assertFalse(any('roles/owner' in a or 'roles/editor' in a or 'roles/iam.serviceAccountTokenCreator' in a for c in cloud.calls for a in c))
            self.assertEqual(next(c for c in bindings if c[:2] == ['iam', 'service-accounts'])[3], s.target)
            self.assertEqual(private_read(s.dir / 'profile.json')['source_email'], s.source)

    def test_s02_unrelated_project_and_rerun_ownership(self):
        with tempfile.TemporaryDirectory() as tmp:
            cloud = Cloud(); cloud.created = True
            s = setup.Setup(arguments(Path(tmp) / 'private'), cloud)
            with self.assertRaisesRegex(SafeError, 'project_not_owned'):
                s.setup()
            self.assertFalse(any(c[:2] == ['projects', 'create'] for c in cloud.calls))

    def test_s04_policy_or_sentinel_cannot_be_assumed(self):
        for binding in [{'members': ['allUsers']}, {'members': ['group:unknown@example.invalid']}, {'condition': {'expression': 'unknown'}}]:
            with tempfile.TemporaryDirectory() as tmp:
                cloud = Cloud(); cloud.policies = {'bindings': [binding]}
                s = setup.Setup(arguments(Path(tmp) / 'private'), cloud)
                with self.assertRaisesRegex(SafeError, 'denial_policy_unresolved'):
                    s.validate_denial('negative')
        with tempfile.TemporaryDirectory() as tmp:
            cloud = Cloud(); cloud.fail = 'describe'
            s = setup.Setup(arguments(Path(tmp) / 'private'), cloud)
            with self.assertRaises(SafeError):
                s.validate_denial('negative')

    def test_s05_issuance_refusal_leaves_cleanup_records_without_token(self):
        with tempfile.TemporaryDirectory() as tmp:
            cloud = Cloud(); cloud.fail = 'keys'
            s = setup.Setup(arguments(Path(tmp) / 'private'), cloud)
            with self.assertRaises(SafeError):
                s.setup()
            self.assertTrue(s.j['resources'])
            self.assertFalse((s.dir / 'token.json').exists())
            self.assertFalse(s.j['completed'])

    def test_s06_private_modes_links_and_unowned_files(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'private'
            s = setup.Setup(arguments(path), Cloud())
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o700)
            self.assertEqual(stat.S_IMODE((path / 'ownership.json').stat().st_mode), 0o600)
            link = path / 'token.json'; link.symlink_to(Path(tmp) / 'elsewhere')
            with self.assertRaises((SafeError, OSError)):
                private_write(link, {'access_token': PRIVATE})
            with self.assertRaises(SafeError):
                setup.Setup(arguments(path), Cloud())
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'private'; path.mkdir(mode=0o700); (path / 'unrelated').write_text('owned by someone else')
            with self.assertRaises(SafeError):
                setup.Setup(arguments(path), Cloud())

    def test_s07_truthful_expiry_boundary(self):
        fixed = now()
        with patch('opentofu_google.now', return_value=fixed):
            self.assertEqual(expiry((fixed + dt.timedelta(seconds=1020)).isoformat(), 600), fixed + dt.timedelta(seconds=1020))
            for value in ['not-time', (fixed + dt.timedelta(seconds=1019)).isoformat(), fixed.replace(tzinfo=None).isoformat()]:
                with self.assertRaises(SafeError):
                    expiry(value, 600)

    def test_s08_s09_s10_s14_instructions_are_literal_and_never_register(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(setup, 'http_json', return_value=mint()):
            cloud = Cloud(); s = setup.Setup(arguments(Path(tmp) / "private ; $(false)", '--token-secret', 'custom_token', '--key-secret', 'custom_key'), cloud)
            s.preflight(); s.setup()
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                s.instructions()
            text = out.getvalue()
            commands = [shlex.split(l) for l in text.splitlines() if l.startswith('jobdb ')]
            self.assertEqual(commands[0], ['jobdb', 'secrets', 'set', 'custom_token', '--tenant', s.a.tenant, '--url', s.a.admin_url, '--value-file', str(s.dir / 'token.json')])
            self.assertEqual(len(commands), 4)
            self.assertNotIn(PRIVATE, text)
            self.assertNotIn(PRIVATE, json.dumps(cloud.calls))
            self.assertIn('every perms.limit ceiling', text)
            self.assertIn('settings.variables.opentofu_google', text)
            hook_commands = [shlex.split(l) for l in text.splitlines() if l.startswith('c2j hooks perms ')]
            self.assertEqual(len(hook_commands), 2)
            self.assertIn('--candidate-commit', hook_commands[0])
            self.assertIn('a' * 40, hook_commands[0])
            self.assertIn('custom_token', hook_commands[0])
            self.assertIn('custom_key', hook_commands[0])
            permission_patch = json.JSONDecoder().raw_decode(text[text.index('{'):])[0]
            self.assertEqual(permission_patch['perms']['jobdb']['secrets']['resolve'],
                             ['gitvend_base_token', 'custom_token', 'custom_key'])
            self.assertNotIn('gitvend_base_token', hook_commands[1])
            self.assertFalse(any(c[:3] == ['jobdb', 'secrets', 'set'] and '--help' not in c for c in cloud.calls))

    def test_s11_s13_partial_cleanup_attempts_all_owned_actions_and_reports_expiry(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(setup, 'http_json', return_value=mint()):
            cloud = Cloud(); s = setup.Setup(arguments(Path(tmp) / 'private'), cloud); s.setup()
            before = list(s.j['resources']); cloud.fail = 'keys'; cloud.calls.clear()
            with contextlib.redirect_stdout(io.StringIO()) as out, self.assertRaisesRegex(SafeError, 'cleanup_incomplete'):
                s.cleanup()
            self.assertEqual(len(cloud.calls), len(before))
            self.assertEqual([x['id'] for x in s.j['resources']], ['key'])
            self.assertIn('does not revoke', out.getvalue())
            self.assertIn(s.j['token_expiry'], out.getvalue())

    def test_s12_s15_refresh_and_resume_do_not_duplicate_resources(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(setup, 'http_json', return_value=mint()):
            cloud = Cloud(); args = arguments(Path(tmp) / 'private'); s = setup.Setup(args, cloud); s.setup()
            key = (s.dir / 'key.json').read_bytes(); resources = list(s.j['resources'])
            cloud.calls.clear(); s.refresh()
            self.assertEqual((s.dir / 'key.json').read_bytes(), key)
            self.assertEqual(s.j['resources'], resources)
            self.assertFalse(any('create' in c for c in cloud.calls))
            cloud.calls.clear(); s = setup.Setup(args, cloud); s.setup()
            self.assertFalse(any('create' in c for c in cloud.calls))

    def test_s09_failure_output_is_not_propagated(self):
        response = subprocess.CompletedProcess(['gcloud'], 1, PRIVATE.encode(), PRIVATE.encode())
        with patch('opentofu_google.subprocess.run', return_value=response), self.assertRaises(SafeError) as error:
            Runner().run(['gcloud', 'version'])
        self.assertNotIn(PRIVATE, str(error.exception))
