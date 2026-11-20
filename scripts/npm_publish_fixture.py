"""Independent HTTP asset and registry fixtures, with actual npm subprocesses."""
import base64
import contextlib
import copy
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import threading
import urllib.parse
import urllib.request
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('npm_publish', os.environ.get('C2OPS_NPM_TEST_MODULE', ROOT / 'npm_publish/main.py'))
op = importlib.util.module_from_spec(spec)
spec.loader.exec_module(op)
MODULE = Path(op.__file__)
FIXTURES = Path(__file__).parent / 'fixtures'
TOKEN = 'fixture_native_secret_please_never_log'


def git(root, *args):
    result = subprocess.run(['git', '-C', str(root), *args], check=True,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        env={**os.environ, 'GIT_CONFIG_GLOBAL': os.devnull, 'GIT_CONFIG_SYSTEM': os.devnull})
    return result.stdout.decode().strip()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def reply(self, code, data):
        data = data if isinstance(data, bytes) else json.dumps(data).encode()
        self.send_response(code)
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        fixture = self.server.fixture
        if self.server.role == 'registry':
            fixture.events.append('GET')
            if fixture.registry_unavailable:
                return self.reply(503, {'error': TOKEN})
            if self.headers.get('Authorization') != 'Bearer ' + TOKEN:
                return self.reply(403, {'error': TOKEN})
            if self.path == '/tarball.tgz':
                return self.reply(200, fixture.registry_bytes)
            if fixture.registry_responses:
                return self.reply(200, fixture.registry_responses.pop(0))
            if fixture.registry_bytes is None:
                return self.reply(404, {'error': 'not_found'})
            dist = {'tarball': 'https://registry.fixture.test/tarball.tgz'}
            if not fixture.no_integrity:
                dist['integrity'] = 'sha512-' + base64.b64encode(hashlib.sha512(fixture.registry_bytes).digest()).decode()
            return self.reply(200, {'name': fixture.request['package_name'], 'version': '1.2.3', 'dist': dist})
        fixture.asset_requests.append(self.path)
        base = '/repos/colony-2/c2j'
        release = fixture.request['release']
        if self.path == base:
            return self.reply(200, {'private': fixture.private, 'full_name': 'colony-2/c2j'})
        if self.path == base + '/releases/123':
            return self.reply(200, {'id': 123, 'draft': fixture.draft, 'tag_name': release['tag'], 'html_url': release['url']})
        if self.path == base + '/git/ref/tags/v1.2.3':
            return self.reply(200, {'object': {'type': 'commit', 'sha': fixture.tag_commit or release['commit']}})
        if self.path == base + '/releases/123/assets?per_page=100':
            return self.reply(200, [{'name': a['name'], 'size': a['size'], 'state': 'uploaded',
                'browser_download_url': a['download_url']} for a in release['assets']][:-1] if fixture.incomplete else [
                {'name': a['name'], 'size': a['size'], 'state': 'uploaded', 'browser_download_url': a['download_url']} for a in release['assets']])
        name = self.path.rsplit('/', 1)[-1]
        if name in fixture.asset_bytes:
            # Corruption only on the installer's second download verifies that
            # postinstall failure blocks publication after public preflight.
            count = fixture.asset_requests.count(self.path)
            data = fixture.asset_bytes[name]
            if fixture.corrupt_install and count > 1 and name.endswith('.tar.gz'):
                data = data[:-1] + bytes([data[-1] ^ 1])
            return self.reply(200, data)
        return self.reply(404, {})

    def do_PUT(self):
        fixture = self.server.fixture
        assert self.server.role == 'registry'
        fixture.events.append('PUT')
        if self.headers.get('Authorization') != 'Bearer ' + TOKEN:
            return self.reply(403, {'error': TOKEN})
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        attachments = list(body['_attachments'].values())
        assert len(attachments) == 1
        data = base64.b64decode(attachments[0]['data'])
        fixture.publications.append(data)
        if not fixture.drop_without_store:
            fixture.registry_bytes = data
        if fixture.lose_observation:
            fixture.registry_unavailable = True
        if fixture.ambiguous or fixture.drop_without_store:
            self.close_connection = True
            return
        return self.reply(201, {'ok': True})


class Fixture:
    def __init__(self):
        self.temp = tempfile.TemporaryDirectory(prefix='npm-fixture-')
        self.root = Path(self.temp.name)
        self.workspace, self.inbox, self.outbox = (self.root / n for n in ('workspace', 'inbox', 'outbox'))
        for p in (self.workspace, self.inbox, self.outbox):
            p.mkdir()
        shutil.copytree(FIXTURES / 'npm_wrapper', self.workspace / 'npm/c2j')
        for p in (self.workspace / 'npm/c2j').rglob('*'):
            if p.is_file():
                p.chmod(0o755 if p.stat().st_mode & 0o111 else 0o644)
        (self.workspace / '.gitignore').write_text('ignored.txt\n')
        (self.workspace / '.gitattributes').write_text('README.md export-ignore\n')
        (self.workspace / 'README.md').write_text('Committed documentation\n')
        (self.workspace / 'LICENSE').write_text('Fixture license\n')
        git(self.workspace, 'init', '-q')
        git(self.workspace, 'add', '.')
        git(self.workspace, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.test', 'commit', '-qm', 'fixture')
        (self.workspace / 'untracked.txt').write_text('untracked sentinel')
        (self.workspace / 'untracked-link').symlink_to('untracked.txt')
        self.token = self.root / 'token'
        self.token.write_text(TOKEN)
        self.token.chmod(0o600)
        schema = op.SCHEMA
        self.request = copy.deepcopy(schema['examples'][0])
        self.request.update(workspace=str(self.workspace), inbox=str(self.inbox), outbox=str(self.outbox),
            registry='https://registry.fixture.test/', registry_token_file=str(self.token),
            wrapper_path='npm/c2j', package_name='@colony2/c2j',
            check={'bin': 'c2j', 'args': ['version'], 'stdout': 'c2j version 1.2.3\n', 'timeout_seconds': 10})
        intent = self.request['intent']
        intent.update(repository='github.com/colony-2/c2j', source_commit=git(self.workspace, 'rev-parse', 'HEAD'), binary_name='c2j')
        self.asset_bytes = {}
        files = []
        for goos, goarch in sorted(op.TARGETS):
            name = 'c2j_1.2.3_{}_{}.tar.gz'.format({'linux': 'Linux', 'darwin': 'Darwin'}[goos], {'amd64': 'x86_64', 'arm64': 'arm64'}[goarch])
            buf = io.BytesIO()
            with tarfile.open(fileobj=buf, mode='w:gz') as archive:
                data = b'#!/bin/sh\nprintf "c2j version 1.2.3\\n"\n'
                member = tarfile.TarInfo('c2j'); member.size = len(data); member.mode = 0o755; member.mtime = 0
                archive.addfile(member, io.BytesIO(data))
            self.asset_bytes[name] = buf.getvalue()
            files.append({**{k: intent[k] for k in op.IDENTITY}, 'name': name, 'kind': 'archive',
                'goos': goos, 'goarch': goarch, 'size': len(buf.getvalue()), 'sha256': hashlib.sha256(buf.getvalue()).hexdigest()})
        checksums = ''.join(f"{f['sha256']}  {f['name']}\n" for f in files).encode()
        self.asset_bytes['checksums.txt'] = checksums
        files.append({**{k: intent[k] for k in op.IDENTITY}, 'name': 'checksums.txt', 'kind': 'checksums',
            'size': len(checksums), 'sha256': hashlib.sha256(checksums).hexdigest()})
        for name, data in self.asset_bytes.items():
            (self.inbox / name).write_bytes(data)
        self.request['files'] = files
        self.request['release'] = {'id': 123, 'url': 'https://github.com/colony-2/c2j/releases/tag/v1.2.3',
            'tag': 'v1.2.3', 'commit': intent['source_commit'], 'public': True, 'draft': False,
            'assets': [{**f, 'download_url': 'https://github.com/colony-2/c2j/releases/download/v1.2.3/' + f['name']} for f in files]}
        self.events, self.asset_requests, self.publications, self.commands = [], [], [], []
        self.registry_bytes = None
        self.registry_responses = []
        self.private = self.draft = self.incomplete = self.corrupt_install = False
        self.ambiguous = self.drop_without_store = self.no_integrity = self.registry_unavailable = False
        self.tag_commit = None
        self.lose_observation = False
        self.servers = []
        for role in ('assets', 'registry'):
            server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
            server.fixture = self; server.role = role
            thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
            self.servers.append(server)

    def close(self):
        for server in self.servers:
            server.shutdown(); server.server_close()
        self.temp.cleanup()

    def ports(self):
        return {host: server.server_port for host, server in (
            ('github.com', self.servers[0]), ('api.github.com', self.servers[0]), ('registry.fixture.test', self.servers[1]))}

    def routing(self):
        return routing_ports(self.ports())

    def execute(self):
        with recorded_commands(self), self.routing():
            return op.execute(self.request)

    def resume(self, result):
        desc = result['output']['files'][0]
        dest = self.inbox / desc['name']; dest.parent.mkdir(exist_ok=True)
        shutil.copyfile(self.outbox / desc['name'], dest)
        self.request.update(resume=True, retained_tarball=desc)


@contextlib.contextmanager
def fixture():
    f = Fixture()
    try:
        yield f
    finally:
        f.close()


@contextlib.contextmanager
def routing_ports(ports):
    class Opener:
        def open(self, request, timeout):
            parsed = urllib.parse.urlsplit(request.full_url)
            assert parsed.hostname in ports, 'external network forbidden'
            routed = urllib.request.Request('http://127.0.0.1:' + str(ports[parsed.hostname]) + parsed.path +
                ('?' + parsed.query if parsed.query else ''), headers=dict(request.headers))
            return urllib.request.build_opener(urllib.request.ProxyHandler({})).open(routed, timeout=timeout)
    real_init, real_env = op.Network.__init__, op.clean_env
    def init(network):
        real_init(network); network.opener = Opener()
    def env(scratch, registry):
        return {**real_env(scratch, registry), 'NODE_OPTIONS': '--require=' + str(FIXTURES / 'npm-transport.cjs'),
            'FIXTURE_ASSETS_PORT': str(ports['github.com']),
            'FIXTURE_REGISTRY_PORT': str(ports['registry.fixture.test'])}
    with patch.object(op.Network, '__init__', init), patch.object(op, 'clean_env', env):
        yield


def snapshot(workspace):
    """Git identity plus every non-administrative path, including ignored files."""
    inventory = {}
    for p in workspace.rglob('*'):
        name = str(p.relative_to(workspace))
        if name == '.git' or name.startswith('.git/'):
            continue
        if p.is_symlink():
            value = ('symlink', os.readlink(p))
        elif p.is_dir():
            value = ('directory',)
        else:
            value = ('file', p.read_bytes())
        inventory[name] = (p.lstat().st_mode, value)
    return git(workspace, 'rev-parse', 'HEAD'), (workspace / '.git/index').read_bytes(), inventory


@contextlib.contextmanager
def recorded_commands(f):
    real = op.run
    before = snapshot(f.workspace)
    def run(argv, cwd, env=None, timeout=120):
        record = {'argv': [str(x) for x in argv], 'cwd': str(cwd), 'env': dict(env or {})}
        f.commands.append(record)
        assert_external_paths(argv, cwd, env, f.workspace)
        return real(argv, cwd, env, timeout)
    try:
        with patch.object(op, 'run', run):
            yield
    finally:
        assert snapshot(f.workspace) == before, 'operation changed checkout contents, modes, HEAD or index'


def prepare_artifact(f):
    (f.workspace / 'ignored.txt').write_text('ignored sentinel')
    f.request['phase'] = 'prepare'
    f.request.pop('registry_token_file', None)
    result = f.execute()
    assert f.events == [], 'prepare contacted registry'
    return result


def transfer_artifacts(f, result):
    """Capture six immutable artifacts, then hydrate into a separate workspace."""
    store = f.root / 'artifact-store'
    store.mkdir(exist_ok=True)
    descriptors = f.request['files'] + result['output']['files']
    for desc in descriptors:
        original = (f.outbox if desc['kind'] == 'npm_tarball' else f.inbox) / desc['name']
        dest = store / desc['name']
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_bytes(original.read_bytes())
    target = f.root / ('transferred-' + str(len(list(f.root.glob('transferred-*')))))
    target.mkdir()
    workspace, inbox, outbox = (target / n for n in ('workspace', 'inbox', 'outbox'))
    subprocess.run(['git', 'clone', '-q', '--no-local', str(f.workspace), str(workspace)], check=True)
    inbox.mkdir(); outbox.mkdir()
    for desc in descriptors:
        dest = inbox / desc['name']
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(store / desc['name'], dest)
    # An old path cannot serve as an accidental source for subsequent invocations.
    for previous in (f.workspace, f.inbox, f.outbox):
        shutil.rmtree(previous)
    f.workspace, f.inbox, f.outbox = workspace, inbox, outbox
    f.request.update(workspace=str(workspace), inbox=str(inbox), outbox=str(outbox),
                     resume=True, retained_tarball=copy.deepcopy(result['output']['files'][0]))
    return {d['name']: (store / d['name']).read_bytes() for d in descriptors}


def assert_external_paths(argv, cwd, env, workspace):
    if str(argv[0]) == 'git':
        return
    assert not Path(cwd).resolve().is_relative_to(workspace)
    for key in ('HOME', 'TMPDIR', 'NPM_CONFIG_CACHE', 'NPM_CONFIG_USERCONFIG', 'NPM_CONFIG_GLOBALCONFIG'):
        if env and key in env:
            assert not Path(env[key]).resolve().is_relative_to(workspace), key
