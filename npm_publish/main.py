#!/usr/bin/env python3
"""Prepare, install-check and publish one immutable npm wrapper tarball.

Only the Python standard library is needed. All tool invocations use argv;
untrusted tool/service diagnostics and private paths never become diagnostics.
"""
import base64
import datetime
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import stat
import subprocess
import sys
import tarfile
import tempfile
import urllib.error
import urllib.parse
import urllib.request

LIMIT = 128 * 1024
IDENTITY = ('schema_version', 'release_id', 'repository', 'source_commit', 'version', 'tag')
TARGETS = {(o, a) for o in ('linux', 'darwin') for a in ('amd64', 'arm64')}
SCHEMA = json.loads(Path(__file__).with_name('contract.json').read_text())
ACTIVE = None
# JSON null is malformed metadata, never evidence of a missing version.
ABSENT = object()


class Failure(Exception):
    """Operation-owned, safe to display diagnostic."""


class Cancelled(Failure):
    """Cancellation must never authorize another mutation."""


def require(condition, message):
    if not condition:
        raise Failure(message)


def validate(value, schema=SCHEMA):
    """Validate the closed subset of JSON Schema used by our checked-in contract.

    The manifests also expose the full schema to the caller's validator. Keeping
    this validator here makes direct process invocation equally strict.
    """
    if '$ref' in schema:
        target = SCHEMA
        for key in schema['$ref'].split('/')[1:]:
            target = target[key]
        validate(value, target)
    if 'type' in schema:
        types = {'object': dict, 'array': list, 'string': str, 'integer': int,
                 'boolean': bool, 'null': type(None)}
        allowed = schema['type'] if isinstance(schema['type'], list) else [schema['type']]
        require(any(type(value) is types[t] for t in allowed), 'invalid input type')
    if 'const' in schema:
        require(type(value) is type(schema['const']) and value == schema['const'], 'invalid contract constant')
    if 'enum' in schema:
        require(value in schema['enum'], 'unsupported contract value')
    if isinstance(value, dict):
        for key in schema.get('required', []):
            require(key in value, 'missing required input field: ' + key)
        props = schema.get('properties', {})
        if schema.get('additionalProperties') is False:
            require(not (value.keys() - props.keys()), 'unknown input field')
        for key in value.keys() & props.keys():
            validate(value[key], props[key])
    if isinstance(value, (str, list)):
        lo, hi = ('minLength', 'maxLength') if isinstance(value, str) else ('minItems', 'maxItems')
        require(len(value) >= schema.get(lo, 0) and len(value) <= schema.get(hi, sys.maxsize), 'input length exceeds contract bounds')
    if isinstance(value, str) and 'pattern' in schema:
        require(re.search(schema['pattern'], value) is not None, 'invalid input syntax')
    if isinstance(value, str) and schema.get('format') == 'date-time':
        require(re.fullmatch(r'\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z', value), 'invalid UTC RFC3339 build date')
        try:
            datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))
        except ValueError:
            raise Failure('invalid UTC build date') from None
    if type(value) is int:
        require(value >= schema.get('minimum', -sys.maxsize) and value <= schema.get('maximum', sys.maxsize), 'input number exceeds contract bounds')
    if isinstance(value, list) and 'items' in schema:
        for item in value:
            validate(item, schema['items'])
    for child in schema.get('allOf', []):
        validate(value, child)
    for name, count in (('oneOf', 1), ('anyOf', None)):
        if name in schema:
            matches = 0
            for child in schema[name]:
                try:
                    validate(value, child)
                    matches += 1
                except Failure:
                    pass
            require(matches == count if count else matches > 0, 'invalid contract alternative')
    if 'not' in schema:
        try:
            validate(value, schema['not'])
        except Failure:
            pass
        else:
            raise Failure('forbidden contract field combination')
    if 'if' in schema:
        try:
            validate(value, schema['if'])
        except Failure:
            validate(value, schema.get('else', {}))
        else:
            validate(value, schema.get('then', {}))


def json_bytes(value):
    data = json.dumps(value, ensure_ascii=False, separators=(',', ':')).encode()
    require(len(data) <= LIMIT, 'JSON exceeds 128 KiB limit')
    return data


def rel(name):
    require(isinstance(name, str) and name and '\\' not in name and
            all(p not in ('', '.', '..') for p in name.split('/')) and
            not any(ord(c) < 32 or ord(c) == 127 for c in name), 'unsafe relative artifact path')
    return name


def path_under(root, name):
    p = root
    for part in rel(name).split('/'):
        p = p / part
        require(not p.is_symlink(), 'symlink in bound artifact path')
    require(p.resolve().is_relative_to(root), 'artifact escapes bound root')
    return p


def digest(path, algorithm='sha256'):
    h = hashlib.new(algorithm)
    with path.open('rb') as f:
        for block in iter(lambda: f.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def integrity(path):
    return 'sha512-' + base64.b64encode(bytes.fromhex(digest(path, 'sha512'))).decode()


def run(argv, cwd, env=None, timeout=120):
    global ACTIVE
    try:
        # Keep even large, secret-bearing tool output in private scratch, not
        # process pipes or outbox. Only bounded successful output is returned.
        temp_dir = env.get('TMPDIR') if env is not None else None
        with tempfile.TemporaryFile(dir=temp_dir) as stdout, tempfile.TemporaryFile(dir=temp_dir) as stderr:
            ACTIVE = subprocess.Popen([str(x) for x in argv], cwd=cwd, env=env,
                                      stdin=subprocess.DEVNULL, stdout=stdout,
                                      stderr=stderr, start_new_session=True)
            try:
                ACTIVE.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                os.killpg(ACTIVE.pid, signal.SIGKILL)
                ACTIVE.wait()
                raise Failure('subprocess timeout; remote publication may need reconciliation') from None
            require(ACTIVE.returncode == 0, 'subprocess failed; raw diagnostics suppressed to protect credentials')
            stdout.seek(0)
            data = stdout.read(LIMIT + 1)
            require(len(data) <= LIMIT, 'subprocess output exceeds 128 KiB')
            return data
    except OSError:
        raise Failure('required subprocess could not execute') from None
    finally:
        ACTIVE = None


def git(workspace, *args, temp_dir=None):
    # Preserve Git credential configuration/helper sockets. Ignore ambient
    # source-selection overrides and replacement refs, without changing config.
    env = {k: v for k, v in os.environ.items() if k not in (
        'GIT_DIR', 'GIT_WORK_TREE', 'GIT_INDEX_FILE', 'GIT_OBJECT_DIRECTORY',
        'GIT_ALTERNATE_OBJECT_DIRECTORIES', 'GIT_COMMON_DIR')}
    env['GIT_OPTIONAL_LOCKS'] = '0'
    if temp_dir is not None:
        env['TMPDIR'] = str(temp_dir)
    return run(['git', '--no-replace-objects', '-c', 'core.fsmonitor=false', '-C', workspace, *args],
               workspace, env)


def project(workspace, commit, dest):
    require(git(workspace, 'rev-parse', 'HEAD', temp_dir=dest.parent).decode().strip() == commit, 'source HEAD does not match intent')
    require(not git(workspace, 'status', '--porcelain', '--untracked-files=no', temp_dir=dest.parent), 'tracked source changes are not release inputs')
    tree = git(workspace, 'rev-parse', commit + '^{tree}', temp_dir=dest.parent).decode().strip()
    # ls-tree and blob output can exceed protocol output limits; read through
    # dedicated Git subprocesses rather than the bounded JSON/tool helper.
    def object_read(args):
        global ACTIVE
        env = dict(os.environ)
        for key in ('GIT_DIR', 'GIT_WORK_TREE', 'GIT_INDEX_FILE', 'GIT_OBJECT_DIRECTORY',
                    'GIT_ALTERNATE_OBJECT_DIRECTORIES', 'GIT_COMMON_DIR'):
            env.pop(key, None)
        env['TMPDIR'] = str(dest.parent)
        try:
            ACTIVE = subprocess.Popen(['git', '--no-replace-objects', '-C', str(workspace), *args],
                env=env, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, start_new_session=True)
            try:
                data, _ = ACTIVE.communicate(timeout=120)
            except subprocess.TimeoutExpired:
                os.killpg(ACTIVE.pid, signal.SIGKILL)
                ACTIVE.wait()
                raise Failure('Git object read timed out') from None
            require(ACTIVE.returncode == 0, 'cannot read recorded Git object')
            return data
        finally:
            ACTIVE = None
    entries = {}
    dest.mkdir()
    for entry in object_read(['ls-tree', '-rz', '--full-tree', commit]).split(b'\0'):
        if not entry:
            continue
        meta, raw_name = entry.split(b'\t', 1)
        mode, kind, oid = meta.decode().split()
        name = rel(os.fsdecode(raw_name))
        require(kind == 'blob' and mode in ('100644', '100755', '120000'), 'unsupported submodule or source mode')
        data = object_read(['cat-file', 'blob', oid])
        require(not data.startswith(b'version https://git-lfs.github.com/spec/v1'), 'unresolved Git LFS source')
        h = hashlib.new('sha1' if len(oid) == 40 else 'sha256', b'blob ' + str(len(data)).encode() + b'\0' + data)
        require(h.hexdigest() == oid, 'Git blob projection verification failed')
        p = path_under(dest, name)
        p.parent.mkdir(parents=True, exist_ok=True)
        if mode == '120000':
            target = os.fsdecode(data)
            require(not os.path.isabs(target) and (p.parent / target).resolve().is_relative_to(dest), 'source symlink escapes projection')
            p.symlink_to(target)
        else:
            p.write_bytes(data)
            p.chmod(int(mode[-3:], 8))
        entries[name] = (mode, oid)
    # Verify the whole path set and content after construction, independently of
    # ignore/export-ignore and checkout filters. No archive/checkout operation.
    require({str(p.relative_to(dest)) for p in dest.rglob('*') if not p.is_dir() or p.is_symlink()} == set(entries), 'source projection path verification failed')
    for name, (mode, oid) in entries.items():
        p = dest / name
        require(not p.is_symlink() or p.resolve().is_relative_to(dest), 'source symlink chain escapes projection')
        data = os.fsencode(os.readlink(p)) if mode == '120000' else p.read_bytes()
        h = hashlib.new('sha1' if len(oid) == 40 else 'sha256', b'blob ' + str(len(data)).encode() + b'\0' + data)
        require(h.hexdigest() == oid and (mode == '120000' or stat.S_IMODE(p.stat().st_mode) == int(mode[-3:], 8)), 'source projection bytes or mode verification failed')
    return tree


def expected_wrapper(request, source):
    intent = request['intent']
    wrapper = path_under(source, request['wrapper_path'])
    require(wrapper.is_dir(), 'committed wrapper directory is missing')
    # npm does not preserve symlinks as packaged source. Reject them in the
    # wrapper rather than silently replacing their semantics during a copy.
    require(not any(p.is_symlink() for p in wrapper.rglob('*')), 'wrapper symlinks are unsupported')
    require(not any(p.name in ('node_modules', 'vendor', '.npmrc') for p in wrapper.rglob('*')),
            'wrapper contains ambient install state or npm configuration')
    pkg = json.loads((wrapper / 'package.json').read_text())
    leaf = request['package_name'].split('/')[-1]
    require(pkg.get('name') == request['package_name'] and leaf == intent['binary_name'], 'wrapper package name mismatch')
    require(pkg.get('version') in ('0.0.0-development', intent['version']), 'wrapper version mismatch')
    repo = pkg.get('repository', {})
    repo = repo.get('url', '') if isinstance(repo, dict) else repo
    accepted = ('https://' + intent['repository'], 'https://' + intent['repository'] + '.git',
                'git+https://' + intent['repository'] + '.git', 'git@' + intent['repository'].replace('/', ':', 1) + '.git')
    require(repo in accepted, 'wrapper repository mismatch')
    require(pkg.get('bin') == {leaf: 'bin/cli.js'} and request['check']['bin'] == leaf,
            'unsupported wrapper bin convention')
    require(set(pkg.get('os', [])) == {'linux', 'darwin'} and set(pkg.get('cpu', [])) == {'x64', 'arm64'} and
            isinstance(pkg.get('engines', {}).get('node'), str), 'wrapper platform or engine conventions missing')
    require(pkg.get('scripts', {}).get('postinstall') == 'node scripts/postinstall.js', 'wrapper postinstall convention missing')
    require(not any(k in pkg for k in ('workspaces', 'publishConfig', 'bundledDependencies', 'bundleDependencies')),
            'wrapper workspaces or publication overrides are unsupported')
    for group in ('dependencies', 'devDependencies', 'optionalDependencies', 'peerDependencies'):
        require(not pkg.get(group), 'wrapper dependencies are unsupported; installation must use only committed installer')
    require((wrapper / 'bin/cli.js').is_file() and (wrapper / 'scripts/postinstall.js').is_file(), 'wrapper shim or installer missing')
    expected = {str(p.relative_to(wrapper)): p.read_bytes() for p in wrapper.rglob('*') if p.is_file()}
    seen = set()
    for doc in request.get('documents', []):
        name = rel(doc['destination'])
        require(name not in seen and name.split('/')[0] not in ('package.json', 'bin', 'scripts', 'vendor', 'node_modules', '.npmrc'), 'unsafe or duplicate documentation destination')
        seen.add(name)
        original = path_under(source, doc['source'])
        require(original.is_file(), 'committed documentation missing')
        expected[name] = original.read_bytes()
    pkg['version'] = intent['version']
    expected['package.json'] = (json.dumps(pkg, indent=2) + '\n').encode()
    return pkg, expected


def prepare(request, source, dest):
    pkg, expected = expected_wrapper(request, source)
    shutil.copytree(path_under(source, request['wrapper_path']), dest)
    for name, data in expected.items():
        target = path_under(dest, name)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
    return pkg


def https_url(url):
    parsed = urllib.parse.urlsplit(url)
    require(parsed.scheme == 'https' and parsed.hostname and not parsed.username and not parsed.password and
            not parsed.fragment and not any(ord(c) <= 32 or ord(c) == 127 for c in url), 'invalid HTTPS service URL')
    return parsed


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Network:
    def __init__(self):
        # Do not inherit proxy authorization, cookies or a caller's .netrc.
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def get(self, url, destination, limit, token=None, allow_absent=False):
        original = https_url(url)
        for _ in range(6):
            parsed = https_url(url)
            headers = {'User-Agent': 'c2ops-npm-publish/1', 'Accept': 'application/json'}
            if token:
                require((parsed.scheme, parsed.netloc) == (original.scheme, original.netloc), 'registry credentials cannot follow cross-origin redirects')
                headers['Authorization'] = 'Bearer ' + token
            try:
                response = self.opener.open(urllib.request.Request(url, headers=headers), timeout=30)
            except urllib.error.HTTPError as error:
                if error.code in (301, 302, 303, 307, 308):
                    url = urllib.parse.urljoin(url, error.headers.get('Location', ''))
                    error.close()
                    continue
                if allow_absent and error.code == 404:
                    error.close()
                    return False
                error.close()
                raise Failure('service request denied or failed; response suppressed') from None
            except (OSError, urllib.error.URLError):
                raise Failure('service request unavailable; remote state unknown') from None
            with response, destination.open('wb') as f:
                total = 0
                while True:
                    block = response.read(1024 * 1024)
                    if not block:
                        break
                    total += len(block)
                    require(total <= limit, 'service response exceeds expected size')
                    f.write(block)
            return True
        raise Failure('too many service redirects')

    def json(self, url, scratch, token=None, allow_absent=False):
        dest = scratch / 'response.json'
        if not self.get(url, dest, LIMIT, token, allow_absent):
            return ABSENT
        try:
            return json.loads(dest.read_bytes())
        except (ValueError, UnicodeError):
            raise Failure('invalid service JSON response') from None


def validate_files(request, inbox, dest):
    files = request['files']
    require(len({f['name'] for f in files}) == len(files), 'duplicate artifact names')
    require(len({(f.get('goos'), f.get('goarch')) for f in files if f['kind'] == 'archive'}) == 4, 'duplicate or missing archive target')
    require({(f.get('goos'), f.get('goarch')) for f in files if f['kind'] == 'archive'} == TARGETS, 'incomplete required targets')
    intent = request['intent']
    paths = []
    # Check the entire descriptor set and path boundary before reading bytes.
    for f in files:
        require(all(f[k] == intent[k] for k in IDENTITY), 'artifact identity mismatch')
        if f['kind'] == 'archive':
            name = '{}_{}_{}_{}.tar.gz'.format(intent['binary_name'], intent['version'],
                    {'linux': 'Linux', 'darwin': 'Darwin'}[f['goos']], {'amd64': 'x86_64', 'arm64': 'arm64'}[f['goarch']])
        else:
            name = 'checksums.txt'
        require(f['name'] == name, 'incompatible release artifact name')
        original = path_under(inbox, f['name'])
        require(original.is_file() and original.stat().st_size == f['size'], 'missing or truncated hydrated artifact')
        paths.append((f, original, name))
    for f, original, name in paths:
        target = path_under(dest, name)
        shutil.copyfile(original, target)
        require(digest(target) == f['sha256'], 'hydrated artifact hash mismatch')
    lines = (dest / 'checksums.txt').read_text().splitlines()
    expected = {f['name']: f['sha256'] for f in files if f['kind'] == 'archive'}
    actual = {}
    for line in lines:
        match = re.fullmatch(r'([0-9a-f]{64})  ([^\s]+)', line)
        require(match and match[2] not in actual, 'invalid or duplicate checksum entry')
        actual[match[2]] = match[1]
    require(actual == expected, 'checksum manifest does not match final archives')


def public_release(request, network, scratch, assets):
    intent, release = request['intent'], request['release']
    base = 'https://' + intent['repository']
    require(release['url'] == base + '/releases/tag/' + intent['tag'] and release['tag'] == intent['tag'] and
            release['commit'] == intent['source_commit'], 'GitHub release identity mismatch')
    expected = {f['name']: f for f in request['files']}
    require(len({a['name'] for a in release['assets']}) == 5, 'duplicate release assets')
    for a in release['assets']:
        require({k: v for k, v in a.items() if k != 'download_url'} == expected.get(a['name']) and
                a['download_url'] == base + '/releases/download/' + intent['tag'] + '/' + a['name'], 'GitHub asset descriptor mismatch')
    api = 'https://api.github.com/repos/' + intent['repository'].removeprefix('github.com/')
    release_path = api + '/releases/' + urllib.parse.quote(str(release['id']), safe='')
    repo = network.json(api, scratch)
    require(repo.get('private') is False and repo.get('full_name') == intent['repository'].removeprefix('github.com/'), 'GitHub repository is not publicly verified')
    remote = network.json(release_path, scratch)
    require(str(remote.get('id')) == str(release['id']) and remote.get('draft') is False and
            remote.get('tag_name') == intent['tag'] and remote.get('html_url') == release['url'], 'GitHub release is not publicly verified')
    ref = network.json(api + '/git/ref/tags/' + intent['tag'], scratch)['object']
    for _ in range(8):
        if ref.get('type') == 'commit':
            break
        require(ref.get('type') == 'tag' and re.fullmatch(r'[0-9a-f]{40}|[0-9a-f]{64}', ref.get('sha', '')), 'invalid public tag object')
        ref = network.json(api + '/git/tags/' + ref['sha'], scratch)['object']
    require(ref.get('type') == 'commit' and ref.get('sha') == intent['source_commit'], 'public tag does not resolve to release commit')
    remote_assets = network.json(release_path + '/assets?per_page=100', scratch)
    require(isinstance(remote_assets, list) and len(remote_assets) == 5 and len({a['name'] for a in remote_assets}) == 5, 'public asset set is incomplete')
    remote_assets = {a['name']: a for a in remote_assets}
    for a in release['assets']:
        observed = remote_assets.get(a['name'], {})
        require(observed.get('state') == 'uploaded' and observed.get('size') == a['size'] and
                observed.get('browser_download_url') == a['download_url'], 'public asset metadata mismatch')
        target = scratch / 'download'
        network.get(a['download_url'], target, a['size'])
        require(target.stat().st_size == a['size'] and digest(target) == a['sha256'], 'public GitHub asset hash mismatch')
    # All archive paths must be safe before the unchanged installer invokes tar.
    for f in request['files']:
        if f['kind'] == 'archive':
            entries = archive_files(assets / f['name'], prefix='')
            require(intent['binary_name'] in entries, 'release archive has no root executable')


def archive_files(path, prefix='package/'):
    entries = {}
    seen = set()
    total = 0
    with tarfile.open(path, 'r:gz') as archive:
        for member in archive:
            require(member.name.startswith(prefix), 'tar entry outside package root')
            name = member.name[len(prefix):]
            normalized = rel(name.rstrip('/') if member.isdir() else name)
            require(normalized not in seen, 'duplicate tar entry')
            seen.add(normalized)
            if member.isdir():
                continue
            require(member.isfile() and name not in entries, 'unsafe or duplicate tar entry')
            total += member.size
            require(total <= 1024 * 1024 * 1024 and len(entries) < 100000, 'archive exceeds safe unpacked bounds')
            entries[name] = archive.extractfile(member).read()
    return entries


def verify_tarball(path, expected, pkg):
    entries = archive_files(path)
    require('package.json' in entries and json.loads(entries['package.json']) == pkg, 'retained wrapper metadata mismatch')
    require({'bin/cli.js', 'scripts/postinstall.js'} <= entries.keys(), 'packed wrapper convention missing')
    for name, data in entries.items():
        require(name.split('/')[0] not in ('vendor', 'node_modules', '.npmrc'), 'tarball bundles installed state or credentials')
        require(name in expected and data == expected[name], 'packed wrapper differs from committed preparation')


def clean_env(scratch, registry):
    home = scratch / 'home'
    home.mkdir(exist_ok=True)
    tmp = scratch / 'tmp'
    tmp.mkdir(exist_ok=True)
    empty = scratch / 'empty.npmrc'
    empty.touch()
    global_config = scratch / 'global.npmrc'
    global_config.touch()
    return {'PATH': os.environ.get('PATH', ''), 'HOME': str(home), 'TMPDIR': str(tmp),
            'LANG': 'C.UTF-8', 'NPM_CONFIG_USERCONFIG': str(empty), 'NPM_CONFIG_GLOBALCONFIG': str(global_config),
            'NPM_CONFIG_CACHE': str(scratch / 'cache'), 'NPM_CONFIG_REGISTRY': registry,
            'NPM_CONFIG_AUDIT': 'false', 'NPM_CONFIG_FUND': 'false', 'NPM_CONFIG_UPDATE_NOTIFIER': 'false',
            'NPM_CONFIG_FETCH_RETRIES': '0',
            'SSL_CERT_FILE': os.environ.get('SSL_CERT_FILE', ''),
            'NODE_EXTRA_CA_CERTS': os.environ.get('SSL_CERT_FILE', '')}


def install_check(request, tarball, assets, scratch, env):
    goos = {'Linux': 'linux', 'Darwin': 'darwin'}.get(platform.system())
    goarch = {'x86_64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(platform.machine())
    require((goos, goarch) in TARGETS, 'unsupported installation check host')
    install = scratch / 'install'
    install.mkdir()
    (install / 'package.json').write_text('{"name":"isolated-install-check","version":"1.0.0","private":true}')
    run(['npm', 'install', '--offline', '--no-audit', '--no-fund', '--ignore-scripts=false',
         '--package-lock=false', '--', tarball], install, env, 300)
    package = install / 'node_modules' / request['package_name']
    binary = path_under(package, 'vendor/' + request['intent']['binary_name'])
    require(binary.is_file() and os.access(binary, os.X_OK), 'installer did not produce an executable')
    selected = next(f for f in request['files'] if (f.get('goos'), f.get('goarch')) == (goos, goarch))
    expected = archive_files(assets / selected['name'], prefix='')[request['intent']['binary_name']]
    require(binary.read_bytes() == expected, 'installed executable differs from verified GitHub binary')
    shim = install / 'node_modules/.bin' / request['check']['bin']
    require(shim.is_symlink() and shim.resolve() == package / 'bin/cli.js', 'installed bin shim mismatch')
    output = run([shim, *request['check']['args']], install, env, request['check'].get('timeout_seconds', 60))
    require(output == request['check']['stdout'].encode(), 'installed executable version check failed')
    return {'status': 'passed', 'goos': goos, 'goarch': goarch, 'check': request['check']}


def observe_registry(request, tarball, network, scratch, token):
    url = request['registry'] + urllib.parse.quote(request['package_name'], safe='@') + '/' + request['intent']['version']
    remote = network.json(url, scratch, token, allow_absent=True)
    if remote is ABSENT:
        return False
    require(isinstance(remote, dict), 'invalid registry metadata: expected package object')
    require(remote.get('name') == request['package_name'] and remote.get('version') == request['intent']['version'], 'registry package identity mismatch')
    dist = remote.get('dist')
    require(isinstance(dist, dict), 'invalid registry metadata: expected dist object')
    observed = dist.get('integrity', '')
    require(isinstance(observed, str), 'invalid registry metadata: expected integrity string')
    if 'tarball' in dist:
        require(isinstance(dist['tarball'], str), 'invalid registry metadata: expected tarball URL')
        https_url(dist['tarball'])
    strong = [s for s in observed.split() if s.startswith('sha512-')]
    if strong:
        require(integrity(tarball) in strong, 'registry bytes conflict; new version required')
    else:
        require('tarball' in dist, 'invalid registry metadata: missing tarball URL')
        url = dist['tarball']
        https_url(url)
        download = scratch / 'registry.tgz'
        # Send authorization only to the configured registry origin/path.
        network.get(url, download, tarball.stat().st_size, token if url.startswith(request['registry']) else None)
        require(integrity(download) == integrity(tarball), 'registry bytes conflict; new version required')
    return True


def publish(request, tarball, network, scratch, env, token):
    if observe_registry(request, tarball, network, scratch, token):
        return 'reconciled'
    config = scratch / 'auth.npmrc'
    registry = urllib.parse.urlsplit(request['registry'])
    config.write_text('//' + registry.netloc + registry.path + ':_authToken=' + token + '\n')
    config.chmod(0o600)
    publish_env = {**env, 'NPM_CONFIG_USERCONFIG': str(config)}
    tested = integrity(tarball)
    for attempt in range(2):
        require(integrity(tarball) == tested, 'retained tarball changed after installation check')
        succeeded = True
        try:
            run(['npm', 'publish', tarball, '--ignore-scripts', '--access', 'public', '--tag',
                 request.get('dist_tag', 'latest'), '--registry', request['registry'], '--provenance=false'],
                scratch, publish_env, 120)
        except Cancelled:
            raise
        except Failure:
            succeeded = False
        # Includes success, conflict, lost response and timeout: read before any
        # retry. Failed reads propagate; they can never authorize a blind retry.
        if observe_registry(request, tarball, network, scratch, token):
            return 'published' if succeeded else 'reconciled'
        require(not succeeded, 'publish returned success but registry version is absent; remote outcome unknown')
        if attempt == 0:
            # Fresh authoritative absence immediately before the sole retry.
            if observe_registry(request, tarball, network, scratch, token):
                return 'reconciled'
    raise Failure('publication remains absent after bounded retry; retain tarball for resume')


def execute(request):
    json_bytes(request)
    validate(request)
    intent = request['intent']
    require(intent['tag'] == 'v' + intent['version'], 'release tag/version mismatch')
    require(all(p not in ('.', '..') for p in intent['repository'].split('/')[1:]), 'noncanonical GitHub repository')
    require(intent['version'] in request['check']['stdout'], 'version check must include allocated version')
    registry = https_url(request['registry'])
    require(not registry.query and registry.path.endswith('/') and request.get('access', 'public') == 'public', 'invalid registry destination')
    roots = []
    for key in ('workspace', 'inbox', 'outbox'):
        p = Path(request[key])
        require(p.is_absolute() and p == p.resolve() and p.is_dir(), 'bound roots must be existing canonical directories without symlinks')
        roots.append(p)
    require(all(not a.is_relative_to(b) and not b.is_relative_to(a) for i, a in enumerate(roots) for b in roots[i+1:]), 'workspace/inbox/outbox roots overlap')
    workspace, inbox, outbox = roots
    phase = request.get('phase')
    token = None
    if phase != 'prepare':
        token_path = Path(request['registry_token_file'])
        require(token_path.is_file() and not token_path.is_symlink() and token_path == token_path.resolve() and
                not any(token_path.is_relative_to(p) for p in roots) and stat.S_IMODE(token_path.stat().st_mode) & 0o077 == 0,
                'explicit private registry token file required outside bound roots; Actions OIDC is unsupported')
        token = token_path.read_text().strip()
        require(re.fullmatch(r'[A-Za-z0-9_./+=-]{8,4096}', token), 'invalid registry token file; Actions OIDC is unsupported')
    # Resolve configuration without tempfile's writable-directory probe, which
    # could briefly create a file in a forbidden checkout before rejection.
    configured_temp = tempfile.tempdir or next((os.environ[k] for k in ('TMPDIR', 'TEMP', 'TMP') if os.environ.get(k)), '/tmp')
    temp_root = Path(os.fsdecode(configured_temp)).resolve()
    require(not any(temp_root.is_relative_to(p) for p in roots), 'scratch must be outside bound roots')
    with tempfile.TemporaryDirectory(prefix='c2ops-npm-', dir=temp_root) as tmp:
        scratch = Path(tmp).resolve()
        require(not any(scratch.is_relative_to(p) for p in roots), 'scratch must be outside bound roots')
        git(workspace, 'check-ref-format', 'refs/heads/' + intent['target_branch'], temp_dir=scratch)
        assets = scratch / 'assets'
        assets.mkdir()
        validate_files(request, inbox, assets)
        name = 'npm/{}-{}.tgz'.format(intent['binary_name'], intent['version'])
        candidate = scratch / 'candidate.tgz'
        if request.get('resume', False):
            desc = request['retained_tarball']
            require(all(desc[k] == intent[k] for k in IDENTITY) and desc['name'] == name, 'retained tarball identity mismatch')
            original = path_under(inbox, desc['name'])
            require(original.is_file() and original.stat().st_size == desc['size'], 'retained tarball missing or truncated; cannot repack a resume')
            shutil.copyfile(original, candidate)
            require(digest(candidate) == desc['sha256'], 'retained tarball hash mismatch')
        tree = project(workspace, intent['source_commit'], scratch / 'source')
        wrapper = scratch / 'wrapper'
        pkg, expected = expected_wrapper(request, scratch / 'source')
        if not request.get('resume', False):
            prepare(request, scratch / 'source', wrapper)
        network = Network()
        public_release(request, network, scratch, assets)
        env = clean_env(scratch, request['registry'])
        if not request.get('resume', False):
            packed = json.loads(run(['npm', 'pack', '--json', '--ignore-scripts', '--pack-destination', scratch], wrapper, env))
            require(isinstance(packed, list) and len(packed) == 1, 'npm pack did not produce one tarball')
            packed_file = path_under(scratch, packed[0]['filename'])
            shutil.move(packed_file, candidate)
        verify_tarball(candidate, expected, pkg)
        retained = path_under(outbox, name)
        retained.parent.mkdir(parents=True, exist_ok=True)
        if retained.exists():
            require(digest(retained) == digest(candidate), 'retained output conflicts; resume with the original tarball')
        else:
            # Exclusive creation avoids replacing earlier tested bytes.
            with retained.open('xb') as f, candidate.open('rb') as src:
                shutil.copyfileobj(src, f)
        retained.chmod(0o444)
        tested = integrity(candidate)
        desc = {k: intent[k] for k in IDENTITY}
        desc.update(name=name, size=candidate.stat().st_size, sha256=digest(candidate), kind='npm_tarball')
        checked = install_check(request, candidate, assets, scratch, env)
        require(integrity(candidate) == tested and integrity(retained) == tested, 'tarball changed during installation')
        output = {'intent': intent, 'files': [desc], 'provenance': {'source_tree': tree,
            'wrapper_path': request['wrapper_path'], 'node_version': run(['node', '--version'], scratch, env).decode().strip(),
            'npm_version': run(['npm', '--version'], scratch, env).decode().strip()},
            'package': {'name': pkg['name'], 'version': pkg['version']}, 'release': request['release'],
            'install_check': checked, 'registry': {'url': request['registry'], 'integrity': tested,
            'observed_integrity': tested, 'outcome': 'reconciled'}}
        if phase is not None:
            output['phase'] = phase
        if phase == 'prepare':
            output['registry'].pop('observed_integrity')
            output['registry']['outcome'] = 'not_attempted'
        validate(output, SCHEMA['$defs']['output'])
        json_bytes({'output': output})  # Before registry mutation, too.
        if phase != 'prepare':
            output['registry']['outcome'] = publish(request, candidate, network, scratch, env, token)
        require(integrity(candidate) == tested and integrity(retained) == tested, 'tarball changed during publication; reconcile retained bytes')
        return {'output': output}


def stop(signum, _frame):
    if ACTIVE is not None:
        try:
            os.killpg(ACTIVE.pid, signal.SIGKILL)
            ACTIVE.wait()
        except ProcessLookupError:
            pass
    raise Cancelled('operation cancelled or timed out; reconcile any remote effects with retained tarball')


def main():
    os.umask(0o077)
    for sig in (signal.SIGTERM, signal.SIGINT, signal.SIGALRM):
        signal.signal(sig, stop)
    signal.alarm(1200)
    try:
        data = sys.stdin.buffer.read(LIMIT + 1)
        require(len(data) <= LIMIT, 'request JSON exceeds 128 KiB')
        result = execute(json.loads(data))
        sys.stdout.buffer.write(json_bytes(result) + b'\n')
    except Failure as error:
        print('npm.publish: ' + str(error), file=sys.stderr)
        return 1
    except Exception:
        # No exception values/tracebacks: npm, URLs and filesystem failures may
        # contain authorization headers, response bodies or credential paths.
        print('npm.publish: invalid input or execution failure; details suppressed to protect credentials', file=sys.stderr)
        return 1
    finally:
        signal.alarm(0)
    return 0


if __name__ == '__main__':
    sys.exit(main())
