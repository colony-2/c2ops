#!/usr/bin/env python3
"""Operator-run disposable Google test project setup; credentials are never printed."""
import argparse
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import sys
import tempfile
from urllib.parse import urlparse

from opentofu_google import (BUCKET, NAME, PROJECT, Runner, SafeError, expiry, http_json, now,
                            private_dir, private_read, private_write, profile, recipient)

OBJECT_PERMISSIONS = ['storage.objects.create', 'storage.objects.get', 'storage.objects.list', 'storage.objects.delete']


class Setup:
    def __init__(self, args, runner=None):
        self.a, self.r = args, runner or Runner()
        if (not PROJECT.fullmatch(args.project)
                or not all(NAME.fullmatch(n) for n in (args.token_secret, args.key_secret))
                or args.token_secret == args.key_secret
                or 'gitvend_base_token' in (args.token_secret, args.key_secret)):
            raise SafeError('invalid_names')
        u = urlparse(args.admin_url)
        if (args.admin_provider != 'development_admin_provider' or not args.confirm_development_admin
                or u.scheme not in ('https', 'http') or not u.netloc or u.username or u.password or u.query or u.fragment):
            raise SafeError('unsupported_or_unconfirmed_jobdb_administration')
        h = urlparse(args.hook_jobdb)
        if not args.hook_cell or not h.scheme or h.username or h.password or h.query or h.fragment or any(c.isspace() for c in args.hook_jobdb):
            raise SafeError('public_hook_route_required')
        if args.folder and args.organization or not 60 <= args.timeout_seconds <= 1800 or not args.tenant:
            raise SafeError('invalid_setup_arguments')
        if not re.fullmatch(r'[0-9a-f]{40}', args.hook_candidate):
            raise SafeError('full_hook_candidate_required')
        if not args.age_recipient or not all(map(recipient, args.age_recipient)):
            raise SafeError('invalid_recipients')
        self.dir = private_dir(args.private_dir)
        journal = self.dir / 'ownership.json'
        existing = list(self.dir.iterdir())
        if existing and not journal.exists():
            raise SafeError('unowned_private_files')
        self.j = private_read(journal) if journal.exists() else {'version': 1, 'project': args.project, 'nonce': secrets.token_hex(8), 'resources': [], 'completed': False}
        if self.j.get('version') != 1 or self.j.get('project') != args.project:
            raise SafeError('ownership_mismatch')
        if any(p.is_symlink() or p.name not in ('ownership.json', 'key.json', 'token.json', 'profile.json', 'role.json', 'mint-role.json') for p in existing):
            raise SafeError('unowned_private_files')
        for p in existing:
            private_read(p)
        self.save()

    def save(self):
        private_write(self.dir / 'ownership.json', self.j)

    def preflight(self):
        self.r.run(['gcloud', 'version', '--format=json'])
        for sub in ('set', 'remove'):
            b = self.r.run(['jobdb', 'secrets', sub, '--help'])
            for flag in (b'--tenant', b'--url', *([b'--value-file'] if sub == 'set' else [])):
                if flag not in b:
                    raise SafeError('unsupported_jobdb_cli')
        b = self.r.run(['c2j', 'hooks', 'perms', 'add', '--help'])
        if any(flag not in b for flag in (b'--secret', b'--candidate-commit', b'--cell', b'--jobdb')):
            raise SafeError('unsupported_hook_administration')
        accounts = self.r.cloud('auth', 'list', '--filter=status:ACTIVE')
        if len(accounts) != 1 or not accounts[0].get('account'):
            raise SafeError('operator_identity_required')
        self.operator = accounts[0]['account']

    def create(self, identity, args, cleanup):
        if any(x['id'] == identity for x in self.j['resources']):
            return
        self.r.cloud(*args)
        self.j['resources'].append({'id': identity, 'cleanup': cleanup})
        self.save()  # Journal each confirmed effect before attempting another.

    def refresh(self):
        if not any(x['id'] == 'key' for x in self.j['resources']):
            raise SafeError('setup_credentials_missing')
        operator = self.r.run(['gcloud', 'auth', 'print-access-token']).decode().strip()
        if not operator or any(c.isspace() for c in operator):
            raise SafeError('operator_token_invalid')
        status, b = http_json('https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/' + self.source + ':generateAccessToken', operator,
                             {'scope': ['https://www.googleapis.com/auth/cloud-platform', 'https://www.googleapis.com/auth/userinfo.email'], 'lifetime': '3600s'})
        if status != 200:
            raise SafeError('operator_token_mint_denied')
        try:
            minted = json.loads(b)
            expiry(minted['expireTime'], self.a.timeout_seconds)
            token = minted['accessToken']
            if not isinstance(token, str) or not token or any(c.isspace() for c in token):
                raise ValueError()
        except (ValueError, KeyError):
            raise SafeError('invalid_token_issuance') from None
        private_write(self.dir / 'token.json', {'access_token': token, 'expires_at': minted['expireTime']})
        self.j['token_expiry'] = minted['expireTime']
        self.save()

    @property
    def source(self):
        return 'tofu-source@' + self.a.project + '.iam.gserviceaccount.com'

    @property
    def target(self):
        return 'tofu-target@' + self.a.project + '.iam.gserviceaccount.com'

    def validate_denial(self, negative):
        # Conservative: unknown group/domain/condition applicability cannot certify denial.
        ancestors = self.r.cloud('projects', 'get-ancestors', self.a.project)
        policies = [self.r.cloud('projects', 'get-iam-policy', self.a.project),
                    self.r.cloud('storage', 'buckets', 'get-iam-policy', 'gs://' + negative)]
        for a in ancestors:
            if a['type'] in ('folder', 'organization'):
                plural = 'folders' if a['type'] == 'folder' else 'organizations'
                policies.append(self.r.cloud('resource-manager', plural, 'get-iam-policy', str(a['id'])) if plural == 'folders'
                                else self.r.cloud('organizations', 'get-iam-policy', str(a['id'])))
        identities = {'serviceAccount:' + self.source, 'serviceAccount:' + self.target, 'allUsers', 'allAuthenticatedUsers'}
        for policy in policies:
            for binding in policy.get('bindings', []):
                members = binding.get('members', [])
                if (binding.get('condition') or any(m in identities or m.startswith(('group:', 'domain:', 'principalSet:')) for m in members)):
                    raise SafeError('denial_policy_unresolved')
        if not self.a.confirm_denial_policy:
            raise SafeError('operator_denial_confirmation_required')
        self.r.cloud('storage', 'objects', 'describe', 'gs://' + negative + '/denial-sentinel')

    def setup(self):
        p = self.a.project
        recorded = any(x['id'] == 'project' for x in self.j['resources'])
        existing = self.r.cloud('projects', 'describe', p, missing=True)
        if existing is not None and not recorded:
            raise SafeError('project_not_owned')
        if recorded and (existing is None or existing.get('labels', {}).get('c2ops-tofu-test') != self.j['nonce']):
            raise SafeError('project_ownership_lost')
        create = ['projects', 'create', p, '--labels=c2ops-tofu-test=' + self.j['nonce']]
        if self.a.folder:
            create += ['--folder=' + self.a.folder]
        if self.a.organization:
            create += ['--organization=' + self.a.organization]
        self.create('project', create, ['projects', 'delete', p])
        if self.a.billing_account:
            self.r.cloud('billing', 'projects', 'link', p, '--billing-account=' + self.a.billing_account)
        self.r.cloud('services', 'enable', 'storage.googleapis.com', 'iam.googleapis.com', 'iamcredentials.googleapis.com', '--project=' + p)
        for name in ('tofu-source', 'tofu-target'):
            self.create(name, ['iam', 'service-accounts', 'create', name, '--project=' + p],
                        ['iam', 'service-accounts', 'delete', name + '@' + p + '.iam.gserviceaccount.com', '--project=' + p])
        positive, negative = p + '-tofu-positive', p + '-tofu-negative'
        if not all(BUCKET.fullmatch(b) for b in (positive, negative)):
            raise SafeError('invalid_bucket_names')
        for bucket in (positive, negative):
            self.create(bucket, ['storage', 'buckets', 'create', 'gs://' + bucket, '--project=' + p,
                                 '--location=' + self.a.location, '--uniform-bucket-level-access', '--public-access-prevention'],
                        ['storage', 'rm', '--recursive', 'gs://' + bucket])
        for name, permissions in [('tofuObjects', OBJECT_PERMISSIONS), ('tofuMint', ['iam.serviceAccounts.getAccessToken'])]:
            self.create(name, ['iam', 'roles', 'create', name, '--project=' + p, '--title=' + name,
                               '--permissions=' + ','.join(permissions), '--stage=GA'],
                        ['iam', 'roles', 'delete', name, '--project=' + p])
        for email in (self.source, self.target):
            args = ['storage', 'buckets', 'add-iam-policy-binding', 'gs://' + positive, '--member=serviceAccount:' + email, '--role=projects/' + p + '/roles/tofuObjects']
            self.create('storage:' + email, args, [x.replace('add-iam-policy-binding', 'remove-iam-policy-binding') for x in args])
        args = ['iam', 'service-accounts', 'add-iam-policy-binding', self.target, '--project=' + p,
                '--member=serviceAccount:' + self.source, '--role=projects/' + p + '/roles/tofuMint']
        self.create('mint-binding', args, [x.replace('add-iam-policy-binding', 'remove-iam-policy-binding') for x in args])
        if not any(x['id'] == 'sentinel' for x in self.j['resources']):
            with tempfile.NamedTemporaryFile(dir=self.dir, prefix='.tofu-', delete=False) as f:
                f.write(b'opentofu denial sentinel')
                temp = f.name
            try:
                self.create('sentinel', ['storage', 'cp', temp, 'gs://' + negative + '/denial-sentinel'], ['storage', 'rm', 'gs://' + negative + '/denial-sentinel'])
            finally:
                os.unlink(temp)
        self.validate_denial(negative)
        if not any(x['id'] == 'key' for x in self.j['resources']):
            fd, temp = tempfile.mkstemp(dir=self.dir, prefix='.tofu-'); os.close(fd)
            try:
                self.r.cloud('iam', 'service-accounts', 'keys', 'create', temp, '--iam-account=' + self.source, '--project=' + p)
                key = private_read(temp)
                if key.get('type') != 'service_account' or key.get('client_email') != self.source or not key.get('private_key_id') or not key.get('private_key'):
                    raise SafeError('invalid_issued_key')
                # Record the key before any subsequent file or token operation.
                self.j['resources'].append({'id': 'key', 'cleanup': ['iam', 'service-accounts', 'keys', 'delete', key['private_key_id'], '--iam-account=' + self.source, '--project=' + p]})
                self.save()
                private_write(self.dir / 'key.json', key)
            finally:
                os.unlink(temp)
        key = private_read(self.dir / 'key.json')
        if key.get('client_email') != self.source or not key.get('private_key'):
            raise SafeError('issued_key_unavailable_use_cleanup')
        self.refresh()
        public = profile(dict(version=1, project=p, bucket=positive, negative_bucket=negative, sentinel='denial-sentinel',
                              source_email=self.source, target_email=self.target, token_secret=self.a.token_secret, key_secret=self.a.key_secret,
                              timeout_seconds=self.a.timeout_seconds, age_recipients=self.a.age_recipient,
                              prefixes={k: 'c2ops-' + self.j['nonce'] + '/' + k for k in ('token', 'direct_key', 'impersonated_key')}, negative_confirmed=True))
        private_write(self.dir / 'profile.json', public)
        self.j['completed'] = True
        self.save()

    def instructions(self, refresh=False):
        pairs = [(self.a.token_secret, 'token.json')]
        if not refresh:
            pairs.append((self.a.key_secret, 'key.json'))
        for name, file in pairs:
            print(shlex.join(['jobdb', 'secrets', 'set', name, '--tenant', self.a.tenant, '--url', self.a.admin_url, '--value-file', str(self.dir / file)]))
        print('Removal commands (run during cleanup):')
        for name in (self.a.token_secret, self.a.key_secret):
            print(shlex.join(['jobdb', 'secrets', 'remove', name, '--tenant', self.a.tenant, '--url', self.a.admin_url]))
        print('Leaf .c2/recipes/hooks/build/verify/native.yaml node opentofu_credentials permission patch:')
        print(json.dumps({'perms': {'jobdb': {'secrets': {'resolve': ['gitvend_base_token', self.a.token_secret, self.a.key_secret]}}}}, indent=2))
        print('Preserve gitvend_base_token for private repository preparation; cleanup removes only the Google grants.')
        print('Administrative hook grants (review the candidate before trusting its code):')
        hook = '.c2/recipes/hooks/build/verify.yaml'
        trust = ['--candidate-commit', self.a.hook_candidate]
        print(shlex.join(['c2j', 'hooks', 'perms', 'add', hook, '--cell', self.a.hook_cell, '--jobdb', self.a.hook_jobdb, '--secret', self.a.token_secret, '--secret', self.a.key_secret, *trust]))
        print(shlex.join(['c2j', 'hooks', 'perms', 'remove', hook, '--cell', self.a.hook_cell, '--jobdb', self.a.hook_jobdb, '--secret', self.a.token_secret, '--secret', self.a.key_secret]))
        print('Supply settings.variables.opentofu_google with the PUBLIC contents of ' + str(self.dir / 'profile.json'))
        print('The enclosing build-hook caller and every perms.limit ceiling must authorize these same literal names; the leaf cannot expand authority.')
        print('Refresh the token immediately before fresh verification and re-register its file. Keep age decryption identity with the operator; only public recipients enter the hook.')

    def cleanup(self):
        failures = []
        for item in reversed(self.j['resources'][:]):
            try:
                self.r.cloud(*item['cleanup'], missing=True)
                self.j['resources'].remove(item)
                self.save()
            except SafeError:
                failures.append(item['id'])
        print('Bearer tokens may remain valid until their recorded expiry; deleting a key does not revoke already-issued tokens.')
        print('Recorded token expiry: ' + self.j.get('token_expiry', 'no token issued'))
        for name in (self.a.token_secret, self.a.key_secret):
            print(shlex.join(['jobdb', 'secrets', 'remove', name, '--tenant', self.a.tenant, '--url', self.a.admin_url]))
        print(shlex.join(['c2j', 'hooks', 'perms', 'remove', '.c2/recipes/hooks/build/verify.yaml', '--cell', self.a.hook_cell, '--jobdb', self.a.hook_jobdb, '--secret', self.a.token_secret, '--secret', self.a.key_secret]))
        if failures:
            print('Cleanup incomplete for owned resources: ' + ', '.join(failures))
            raise SafeError('cleanup_incomplete')
        if self.a.remove_private_files:
            for file in self.dir.iterdir():
                if file.is_symlink():
                    raise SafeError('unsafe_private_file')
                file.unlink()
            self.dir.rmdir()


def parser():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('action', choices=('setup', 'token-refresh', 'cleanup'))
    for name in ('project', 'private-dir', 'tenant', 'admin-url', 'admin-provider', 'hook-jobdb', 'hook-cell', 'hook-candidate'):
        p.add_argument('--' + name, required=True)
    p.add_argument('--folder'); p.add_argument('--organization'); p.add_argument('--billing-account')
    p.add_argument('--location', default='US')
    p.add_argument('--token-secret', default='opentofu_test_token'); p.add_argument('--key-secret', default='opentofu_test_key')
    p.add_argument('--timeout-seconds', type=int, default=600)
    p.add_argument('--age-recipient', action='append', required=True)
    p.add_argument('--confirm-development-admin', action='store_true')
    p.add_argument('--confirm-denial-policy', action='store_true')
    p.add_argument('--remove-private-files', action='store_true')
    return p


def main():
    try:
        s = Setup(parser().parse_args())
        s.preflight()
        if s.a.action == 'cleanup':
            s.cleanup()
        elif s.a.action == 'token-refresh':
            s.refresh(); s.instructions(refresh=True)
        else:
            s.setup(); s.instructions()
        return 0
    except Exception as e:
        print('OpenTofu test setup stopped: ' + (str(e) if isinstance(e, SafeError) else 'local_setup_failed') + '. Owned-resource records remain in the private directory; use cleanup with the same arguments.', file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
