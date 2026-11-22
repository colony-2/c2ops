# /// script
# requires-python = ">=3.11"
# dependencies = ["google-auth[requests]==2.43.0"]
# ///
"""Fresh native production-package credential checks. Public output is fixed categories only."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import urllib.parse

from opentofu_google import CASES, SafeError, expiry, http_json, now, profile

ROOT = Path(__file__).resolve().parents[1]
SCOPES = ['https://www.googleapis.com/auth/cloud-platform', 'https://www.googleapis.com/auth/userinfo.email']


def command(args, *, directory=None, input_data=None, timeout=900):
    try:
        r = subprocess.run(args, cwd=directory, input=None if input_data is None else json.dumps(input_data).encode(),
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
        if r.returncode or len(r.stdout) > 2**20:
            raise SafeError('private_command_failed')
        return r.stdout
    except (OSError, subprocess.TimeoutExpired):
        raise SafeError('private_command_failed') from None


def resolve(name):
    try:
        value = json.loads(command(['c2j', 'secret', 'get', name, '--format=raw']))
        if not isinstance(value, dict):
            raise ValueError()
        return value
    except (ValueError, TypeError):
        raise SafeError('invalid_named_credential') from None


def inspector_token(p, case):
    value = resolve(p['token_secret'] if case == 'token' else p['key_secret'])
    if case == 'token':
        if set(value) != {'access_token', 'expires_at'}:
            raise SafeError('invalid_named_credential')
        expiry(value['expires_at'], p['timeout_seconds'])
        token = value['access_token']
    else:
        if (value.get('type') != 'service_account' or value.get('client_email') != p['source_email']
                or value.get('token_uri') != 'https://oauth2.googleapis.com/token'):
            raise SafeError('invalid_named_credential')
        # Explicit service-account credentials; no ADC, gcloud, proxy or operator fallback.
        try:
            from google.oauth2 import service_account
            from google.auth import impersonated_credentials
            from google.auth.transport.requests import Request
            import requests
            session = requests.Session(); session.trust_env = False
            class FixedRequest(Request):
                def __call__(self, url, *args, **kwargs):
                    u = urllib.parse.urlparse(url)
                    if u.scheme != 'https' or u.hostname not in ('oauth2.googleapis.com', 'iamcredentials.googleapis.com'):
                        raise SafeError('unexpected_google_destination')
                    kwargs['allow_redirects'] = False
                    return super().__call__(url, *args, **kwargs)
            credentials = service_account.Credentials.from_service_account_info(value, scopes=SCOPES)
            if case == 'impersonated_key':
                credentials = impersonated_credentials.Credentials(source_credentials=credentials, target_principal=p['target_email'], target_scopes=SCOPES, lifetime=3600)
            credentials.refresh(FixedRequest(session=session))
            token = credentials.token
        except Exception:
            raise SafeError('inspector_token_failed') from None
    if not isinstance(token, str) or not token or any(c.isspace() for c in token):
        raise SafeError('invalid_named_credential')
    return token


def identity(token, expected):
    status, data = http_json('https://oauth2.googleapis.com/tokeninfo?access_token=' + urllib.parse.quote(token, safe=''), token)
    try:
        value = json.loads(data)
        if status != 200 or value.get('email') != expected or value.get('email_verified') not in (True, 'true'):
            raise ValueError()
    except (ValueError, TypeError, AttributeError):
        raise SafeError('authoritative_identity_mismatch') from None


def storage(token, bucket, name, *, media=False, method=None):
    url = 'https://storage.googleapis.com/storage/v1/b/' + urllib.parse.quote(bucket, safe='') + '/o/' + urllib.parse.quote(name, safe='')
    if media:
        url += '?alt=media'
    return http_json(url, token, method=method)


def denial(p, token):
    status, _ = storage(token, p['negative_bucket'], p['sentinel'], media=True)
    if status != 403:
        raise SafeError('explicit_denial_not_observed')


def git(repo, *args):
    return command(['git', '-c', 'maintenance.auto=false', '-c', 'gc.auto=0', *args], directory=repo).decode().strip()


def commit(repo):
    git(repo, 'init', '--quiet')
    git(repo, 'add', '.')
    git(repo, '-c', 'user.name=OpenTofu test', '-c', 'user.email=test@example.invalid', 'commit', '-qm', 'public credential fixture')
    return git(repo, 'rev-parse', 'HEAD')


def manifests(base, package):
    manifest = json.loads((Path(package) / 'share/c2j/op.json').read_text())
    manifest['command'] = [package + '/bin/opentofu-up']
    manifest.pop('dependencies', None)
    (base / 'op').mkdir()
    (base / 'op/op.yaml').write_text(json.dumps(manifest))
    (base / 'retain').mkdir()
    object_schema = {'type': 'object', 'x-c2j-object-type': 'c2ops.opentofu.recovery/v1'}
    retain = {'name': 'retain', 'command': [sys.executable, str(ROOT / 'opentofu-up/examples/retain_result.py')],
              'input_schema': {'type': 'object', 'properties': {'result': {'type': 'object', 'properties': {'recovery': object_schema}}, 'expected': {'type': 'object'}, 'recovery': object_schema,
                 'artifact_outbox_path': {'type': 'string', 'default': '{{ context.environment.op.outbox }}'}}},
              'output_schema': {'type': 'object', 'properties': {'accepted': {'type': 'boolean'}, 'result': {'type': 'object'}, 'recovery': object_schema}}}
    (base / 'retain/op.yaml').write_text(json.dumps(retain))
    commit(base)


def native(driver, manifests_dir, worktree, revision, request, private, recovery):
    private.mkdir(mode=0o700)
    selector = 'git+file://' + str(manifests_dir) + '//'
    recipe = {'id': 'google-credentials', 'git_profile': None,
              'perms': {'jobdb': {'secrets': {'resolve': [request['google_auth']['secret_name']]}}},
              'sequence': [
                  {'id': 'deploy', 'op': selector + 'op@HEAD', 'inputs': request},
                  {'id': 'retain', 'op': selector + 'retain@HEAD', 'inputs': {
                      'result': '${{ sequence.deploy.outputs }}',
                      'expected': {k: request[k] for k in ('mode', 'revision', 'root', 'backend')}}}],
              'outputs': {'accepted': '${{ sequence.retain.outputs.accepted }}', 'result': '${{ sequence.retain.outputs.result }}'}}
    recipe_path = private / 'recipe.json'; recipe_path.write_text(json.dumps(recipe))
    result_path = private / 'result.json'
    config = {'Recipe': str(recipe_path), 'Repo': str(worktree), 'Revision': revision, 'Root': str(private), 'Live': True,
              'SecretNames': [request['google_auth']['secret_name']], 'ResultPath': str(result_path), 'RecoveryPath': str(recovery),
              'TotalSeconds': int(request['timeout'][:-1])}
    command([str(driver)], input_data=config, timeout=config['TotalSeconds'] + 180)
    try:
        result = json.loads(result_path.read_text())
        if result['accepted'] is not True or result['result']['ok'] is not True:
            raise ValueError()
        return result['result']['outcome']
    except (OSError, KeyError, ValueError, TypeError):
        raise SafeError('native_deployment_failed') from None


def qualify(p, out, package, driver):
    results = []
    with tempfile.TemporaryDirectory(prefix='opentofu-google-') as temp:
        base = Path(temp); os.chmod(base, 0o700)
        manifests_dir = base / 'manifests'; manifests_dir.mkdir()
        manifests(manifests_dir, package)
        for case in CASES:
            token = None
            owns_namespace = False
            prefix = p['prefixes'][case]
            names = [prefix + '/marker', prefix + '/state/default.tfstate', prefix + '/state/default.tflock']
            try:
                token = inspector_token(p, case)
                identity(token, p['target_email'] if case == 'impersonated_key' else p['source_email'])
                denial(p, token)
                # Refuse existing namespaces: cleanup must never delete unrelated objects.
                for name in names:
                    status, _ = storage(token, p['bucket'], name)
                    if status != 404:
                        raise SafeError('namespace_not_empty')
                owns_namespace = True
                worktree = base / case; worktree.mkdir()
                config = 'terraform {\n backend "gcs" {}\n required_providers { google = { source = "registry.opentofu.org/hashicorp/google", version = "7.10.0" } }\n}\n'
                config += 'provider "google" { project = ' + json.dumps(p['project']) + ' }\n'
                config += 'resource "google_storage_bucket_object" "marker" {\n bucket = ' + json.dumps(p['bucket']) + '\n name = ' + json.dumps(names[0]) + '\n content = "opentofu credential marker"\n}\n'
                (worktree / 'main.tf').write_text(config)
                shutil.copyfile(ROOT / 'opentofu-up/testdata/.terraform.lock.hcl', worktree / '.terraform.lock.hcl')
                revision = commit(worktree)
                auth = {'mode': 'oauth_access_token' if case == 'token' else 'service_account_json',
                        'secret_name': p['token_secret'] if case == 'token' else p['key_secret']}
                if case != 'token':
                    auth['expected_service_account'] = p['source_email']
                if case == 'impersonated_key':
                    auth['impersonate_service_account'] = p['target_email']
                request = {'worktree_path': str(worktree), 'revision': revision, 'root': '.', 'mode': 'up',
                           'backend': {'bucket': p['bucket'], 'prefix': prefix + '/state'}, 'google_auth': auth,
                           'timeout': str(p['timeout_seconds']) + 's', 'lock_timeout': '30s', 'recovery': {'age_recipients': p['age_recipients']}}
                generation = None
                for phase in ('apply', 'repeat'):
                    outcome = native(driver, manifests_dir, worktree, revision, request, base / (case + '-' + phase), out / (case + '-' + phase + '.age'))
                    if outcome != ('applied' if phase == 'apply' else 'no_changes'):
                        raise SafeError('unexpected_native_outcome')
                    status, content = storage(token, p['bucket'], names[0], media=True)
                    if status != 200 or content != b'opentofu credential marker':
                        raise SafeError('marker_not_observed')
                    status, meta = storage(token, p['bucket'], names[0])
                    if status != 200:
                        raise SafeError('marker_not_observed')
                    current = json.loads(meta)['generation']
                    if phase == 'repeat' and current != generation:
                        raise SafeError('marker_generation_changed')
                    generation = current
                    status, state = storage(token, p['bucket'], names[1], media=True)
                    if status != 200 or not any(r.get('type') == 'google_storage_bucket_object' for r in json.loads(state).get('resources', [])):
                        raise SafeError('state_not_observed')
                results.append({'case': case, 'status': 'passed', 'mode': auth['mode'], 'principal_verified': True, 'native_invocations': 2, 'operation_resolutions': 2, 'inspector_resolutions': 1})
            except Exception as e:
                results.append({'case': case, 'status': 'failed', 'category': str(e) if isinstance(e, SafeError) else 'credential_check_failed'})
            finally:
                # No cleanup until this invocation established the namespace was empty.
                if token and owns_namespace:
                    for name in names:
                        try:
                            status, _ = storage(token, p['bucket'], name, method='DELETE')
                        except Exception:
                            status = 0
                        if status not in (204, 404):
                            results[-1]['status'] = 'failed'; results[-1]['cleanup'] = 'incomplete'
    return results


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--profile', required=True, type=Path)
    p.add_argument('--out', required=True, type=Path)
    args = p.parse_args()
    report = {'component': 'opentofu_credentials', 'status': 'failed', 'candidate': os.environ.get('VERIFY_CANDIDATE'),
              'base': os.environ.get('VERIFY_BASE'), 'cases': []}
    try:
        public = profile(json.loads(args.profile.read_text()))
        report['profile_sha256'] = hashlib.sha256(json.dumps(public, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
        args.out.mkdir(parents=True, exist_ok=True)
        package = command(['nix', '--extra-experimental-features', 'nix-command flakes', 'build', '--no-link', '--no-write-lock-file',
                           '--max-jobs', '0', '--builders', '', '--print-out-paths', 'path:' + str(ROOT) + '#opentofu-up']).decode().strip()
        if not package.startswith('/nix/store/') or '\n' in package:
            raise SafeError('invalid_package_identity')
        report['package'] = package
        with tempfile.TemporaryDirectory(prefix='opentofu-live-driver-') as temp:
            driver = Path(temp) / 'runtime-fixture'
            command(['go', 'build', '-mod=readonly', '-o', str(driver), '.'], directory=ROOT / 'opentofu-runtime-fixture')
            report['cases'] = qualify(public, args.out, package, driver)
        if len(report['cases']) == 3 and all(r['status'] == 'passed' for r in report['cases']):
            report['status'] = 'passed'
    except Exception as e:
        report['category'] = str(e) if isinstance(e, SafeError) else 'credential_check_failed'
    # The caller captures only this safe receipt. Private subprocess output is discarded.
    print(json.dumps(report, sort_keys=True))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
