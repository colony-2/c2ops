#!/usr/bin/env python3
"""Native build-hook checks. Commands are fixed argv; caller text is metadata only."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import subprocess
import sys
import tempfile
import time

TASKS = ('manifests', 'tooling', 'llm', 'llm2', 'gha', 'gha-many',
         'gha-runtime', 'codex', 'rule-gate', 'aider', 'litellm', 'pydantic',
         'jev', 'kimi', 'nix-metadata', 'nix-process')
NIX = ['nix', '--extra-experimental-features', 'nix-command flakes']
HELD = {
    'platforms': 'Only native Linux arm64; x86_64-linux and aarch64-darwin matrix held.',
    'worker-image': 'scripts/check-codex-worker.py requires Docker daemon/image execution.',
    'public-actions': 'C2OPS_TEST_GHA_ACTIONS remains unset; real public Actions/service actions and remote CI held.',
    'publication': 'C2OPS_TEST_NIX_PUBLICATION remains unset; four real cache tests, Cachix record/verify/push/pin and promote-published held.',
    'nix-build-checks': 'Pydantic package and native check build locally with one job/core. Other operation checks retain cached realization.',
    'release': 'Signing, publication and deployment require external infrastructure/credentials and are held even if available.',
}
_ACTIVE = None


def stop(signum, frame):
    if _ACTIVE is not None:
        try:
            os.killpg(_ACTIVE.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
    raise SystemExit(128 + signum)


class Checks:
    def __init__(self, root, env, log, timeout=900):
        self.root, self.env, self.log, self.timeout = root, env, log, timeout
        self.commands = []

    def run(self, argv, directory='.', input_text=None):
        global _ACTIVE
        record = {'argv': [str(x) for x in argv], 'directory': directory}
        self.commands.append(record)
        self.log.write(('\n$ ' + json.dumps(record) + '\n').encode())
        self.log.flush()
        started = time.monotonic()
        try:
            _ACTIVE = subprocess.Popen(record['argv'], cwd=self.root / directory,
                env=self.env, stdin=subprocess.PIPE if input_text is not None else subprocess.DEVNULL,
                stdout=self.log, stderr=subprocess.STDOUT, start_new_session=True)
            _ACTIVE.communicate(None if input_text is None else input_text.encode(), timeout=self.timeout)
            record['returncode'] = _ACTIVE.returncode
        except subprocess.TimeoutExpired:
            os.killpg(_ACTIVE.pid, signal.SIGKILL)
            _ACTIVE.communicate()
            record.update(returncode=124, error='timeout; process group killed')
        except OSError as error:
            record.update(returncode=127, error=str(error))
        finally:
            _ACTIVE = None
        record['seconds'] = round(time.monotonic() - started, 3)
        self.log.write(('\n' + json.dumps(record) + '\n').encode())
        self.log.flush()
        return record['returncode'] == 0

    def go(self, directory, args=None):
        return self.run(['go', 'test', '-mod=readonly', '-count=1', '-timeout=8m', *(args or ['./...'])], directory)

    def gha_compiler(self):
        compiler = self.metadata('.#gha', 'p: p.buildCompiler.outPath')
        if compiler is None:
            return False
        # Verification workers consume prepared tools; never compile a missing
        # compiler or fall back to the older general-purpose worker Go.
        if not self.run([*NIX, 'build', '--no-link', '--no-write-lock-file',
                         '--max-jobs', '0', '--builders', '', compiler]):
            self.log.write(b'GHA compiler unavailable; source builders must prepare nix build .#gha.buildCompiler before verification.\n')
            return False
        self.env['PATH'] = compiler + '/bin' + os.pathsep + self.env.get('PATH', '')
        return True

    def metadata(self, selector, expression):
        # Structured metadata is captured in scratch, never into candidate content.
        dest = self.root.parent / 'metadata.json'
        code = "import subprocess,sys; r=subprocess.run(sys.argv[2:],stdout=open(sys.argv[1],'w'));sys.exit(r.returncode)"
        if not self.run([sys.executable, '-c', code, str(dest), *NIX,
                         'eval', '--json', '--no-write-lock-file', '--apply', expression, selector]):
            return None
        return json.loads(dest.read_text())


def execute(name, checks, race):
    if name == 'manifests':
        checks.run(['make', 'check-manifests'])
        checks.run(['uv', 'run', 'scripts/check-manifest-schemas.py'])
    elif name == 'tooling':
        checks.run(['make', 'test-tools'])
    elif name == 'gha':
        checks.go('gha', ['-race', './...'] if race else None)
    elif name in ('llm', 'llm2', 'gha-many', 'rule-gate', 'aider', 'litellm'):
        checks.go('rule_gate' if name == 'rule-gate' else name)
    elif name == 'pydantic':
        # Build and consume in this task: local paths are evidence, not distribution.
        system = {'arm64': 'aarch64'}.get(platform.machine(), platform.machine()) + '-linux'
        selector = '.#packages.' + system + '.pydantic'
        contract = '.#checks.' + system + '.pydantic'
        if not checks.run([*NIX, 'build', '--no-link', '--no-write-lock-file',
                           '--max-jobs', '1', '--cores', '1', '--print-out-paths', selector, contract]):
            return
        data = checks.metadata(selector, 'p: { path = p.outPath; python = p.pythonEnv.outPath; drv = p.drvPath; }')
        if data is None:
            return
        checks.log.write((json.dumps({'installed_candidate': data, 'system': system}) + '\n').encode())
        # PyYAML is a fixture dependency, not an operation invocation dependency.
        rev = json.loads((checks.root / 'flake.lock').read_text())['nodes']['nixpkgs']['locked']['rev']
        yaml = checks.metadata('github:NixOS/nixpkgs/' + rev + '#python3Packages.pyyaml',
                               'p: { path = p.outPath; site = p.pythonModule.sitePackages; }')
        if yaml is None:
            return
        checks.env['PYTHONPATH'] = yaml['path'] + '/' + yaml['site']
        checks.env['PYDANTIC_TEST_PYTHON'] = data['python'] + '/bin/python'
        checks.run(['make', '-C', 'pydantic', 'test'])
        checks.run([data['python'] + '/bin/python', 'pydantic/test_binding.py',
                    '--module', data['path'] + '/libexec/main.py',
                    '--launcher', data['path'] + '/bin/pydantic'])
        checks.run([*NIX, 'path-info', '--json', '--recursive', data['path']])
    elif name == 'gha-runtime':
        # TestMain builds separate operation executables. Instrument those too,
        # rather than only the test driver that submits their jobs.
        if race:
            checks.env['C2OPS_TEST_GHA_RACE'] = '1'
        checks.go('gha/integration', ['-race', './...'] if race else None)
    elif name == 'codex':
        checks.run(['python3', '../scripts/with-codex.py', '--', 'go', 'test', '-mod=readonly',
                    '-count=1', '-timeout=8m', './...'], 'codex')
    elif name == 'jev':
        checks.run(['uv', 'run', '--with', 'typesafe-sdk==0.7.4', '--with', 'httpx2==2.9.1',
                    '--with', 'jsonschema==4.26.0', 'python', '-m', 'unittest', '-v', 'test_main.py'], 'jev')
    elif name == 'kimi':
        checks.run(['uv', 'run', '--with', 'jsonschema==4.26.0', 'python', '-m', 'unittest', '-v', 'test_main.py'], 'kimi')
    elif name == 'nix-metadata':
        checks.run([*NIX, 'flake', 'check', '--no-build', '--no-write-lock-file', '--system', 'aarch64-linux'])
    elif name == 'nix-process':
        # Same installed process checks as nix/checks.nix, without building
        # runCommand check derivations (unavailable under worker no-build policy).
        catalog = json.loads((checks.root / 'nix/ops.json').read_text())
        pydantic = checks.metadata('github:NixOS/nixpkgs/' + json.loads((checks.root / 'flake.lock').read_text())['nodes']['nixpkgs']['locked']['rev'] + '#python3Packages.pydantic', 'p: p.outPath')
        for op in catalog:
            data = checks.metadata('.#' + op, 'p: { path = p.outPath; manifest = p.c2j; compiled = p.compiledExecutable; python = if p.pythonEnv == null then null else p.pythonEnv.outPath; }')
            if data is None:
                continue
            if not checks.run([*NIX, 'build', '--no-link', '--no-write-lock-file', '--max-jobs', '0', '--builders', '', data['path']]):
                checks.log.write((op + ': dependent process checks not run; realization failed\n').encode())
                continue
            manifest = checks.root.parent / (op + '-manifest.json')
            manifest.write_text(json.dumps(data['manifest']))
            argv = ['python3', 'scripts/check-package.py', data['path'], str(manifest),
                    'nix:github:colony-2/c2ops/published#' + op]
            if data['compiled']:
                argv += ['--compiled-executable', data['compiled'], '--system', 'aarch64-linux']
            checks.run(argv)
            if op in ('pydantic', 'litellm', 'jev') and pydantic is not None:
                checks.run([data['python'] + '/bin/python3', 'scripts/check-python-runtime.py',
                            catalog[op]['path'] + '/main.py', pydantic, op])
        for cli in ('codex-cli', 'aider-cli'):
            data = checks.metadata('.#' + cli, 'p: p.outPath')
            if data and checks.run([*NIX, 'build', '--no-link', '--max-jobs', '0', '--builders', '', data]):
                env_home = checks.env.get('HOME')
                with tempfile.TemporaryDirectory(prefix='c2ops-cli-') as home:
                    checks.env['HOME'] = home
                    checks.run([data + '/bin/' + cli.removesuffix('-cli'), '--version'])
                if env_home is None:
                    checks.env.pop('HOME', None)
                else:
                    checks.env['HOME'] = env_home


def supported_platform():
    return sys.platform == 'linux' and platform.machine() in ('aarch64', 'arm64')


def check(name, root, outbox, *, race=False):
    outbox.mkdir(parents=True, exist_ok=True)
    report = {'task': name, 'status': 'failed', 'commands': [], 'platform': platform.platform(),
              'machine': platform.machine(), 'candidate': os.environ.get('VERIFY_CANDIDATE'),
              'base': os.environ.get('VERIFY_BASE'), 'target_directory': os.environ.get('VERIFY_TARGET'),
              'held': HELD, 'tools': {}, 'race': race}
    log_path = outbox / (name + '.log')
    with log_path.open('wb') as log:
        try:
            if name in ('gha', 'gha-runtime') and not race:
                raise RuntimeError('GHA verification requires --race in the hook command')
            if not supported_platform():
                raise RuntimeError('verification requires native Linux arm64')
            with tempfile.TemporaryDirectory(prefix='c2ops-verify-') as tmp:
                temp = Path(tmp)
                copy = temp / 'candidate'
                shutil.copytree(root, copy, symlinks=False, ignore=shutil.ignore_patterns('.git'))
                env = dict(os.environ)
                # Preserve supplied credential configuration; never edit global Git or go env.
                for key in ('C2OPS_TEST_GHA_ACTIONS', 'C2OPS_TEST_NIX_PUBLICATION', 'C2OPS_CODEX_COMPAT_RESUME_BIN'):
                    env.pop(key, None)
                for key in ('GOCACHE', 'GOMODCACHE', 'GOTMPDIR', 'UV_CACHE_DIR', 'UV_PROJECT_ENVIRONMENT', 'XDG_CACHE_HOME', 'TMPDIR', 'C2J_TOOL_CACHE_DIR'):
                    path = temp / key.lower()
                    path.mkdir()
                    env[key] = str(path)
                env.update(PYTHONDONTWRITEBYTECODE='1', UV_NO_MANAGED_PYTHON='1',
                           GOTOOLCHAIN='local', GOMAXPROCS='2', GOFLAGS='-mod=readonly -p=2', GOPRIVATE='github.com/colony-2/*',
                           C2OPS_TEST_FLAKE='path:' + str(copy),
                           PNPM_CONFIG_ALLOW_BUILDS=(copy / 'nix/pnpm-build-policy.json').read_text())
                checks = Checks(copy, env, log)
                if name == 'gha-runtime':
                    env['C2OPS_GHA_EVIDENCE_DIR'] = str(temp / 'gha-evidence')
                report['commands'] = checks.commands
                if name in ('gha', 'gha-runtime') and not checks.gha_compiler():
                    raise RuntimeError('the pinned GHA compiler could not be prepared; GHA tests did not run')
                for tool, args in {'go':['version'], 'python3':['--version'], 'nix':['--version'],
                                   'uv':['--version'], 'node':['--version'], 'pnpm':['--version'],
                                   'gcc':['-dumpfullversion'], 'git':['--version'], 'make':['--version']}.items():
                    report['tools'][tool] = shutil.which(tool, path=checks.env.get('PATH'))
                    checks.run([tool, *args])
                execute(name, checks, race)
                if name == 'gha-runtime' and (temp / 'gha-evidence/helper-builds.json').exists():
                    report['gha_helper_builds'] = json.loads((temp / 'gha-evidence/helper-builds.json').read_text())
                if checks.commands and all(c.get('returncode') == 0 for c in checks.commands):
                    report['status'] = 'passed'
        except Exception as error:
            report['error'] = f'{type(error).__name__}: {error}'
            log.write((report['error'] + '\n').encode())
    report['log_sha256'] = hashlib.sha256(log_path.read_bytes()).hexdigest()
    (outbox / (name + '.json')).write_text(json.dumps(report, indent=2) + '\n')
    print(report['status'], end='')


def aggregate(inbox, outbox, statuses=None):
    outbox.mkdir(parents=True, exist_ok=True)
    if statuses is None:
        statuses = {name: os.environ.get('VERIFY_STATUS_' + name.upper().replace('-', '_')) for name in TASKS}
    rows, errors = [], []
    for name in TASKS:
        try:
            data = json.loads((inbox / (name + '.json')).read_text())
            log = (inbox / (name + '.log')).read_bytes()
            if statuses.get(name) not in ('passed', 'failed') or statuses[name] != data['status']:
                raise ValueError('missing, malformed or inconsistent command stdout')
            if data['task'] != name or data['status'] not in ('passed', 'failed'):
                raise ValueError('invalid task/status')
            if not log or hashlib.sha256(log).hexdigest() != data['log_sha256']:
                raise ValueError('missing or changed log')
            if data['status'] == 'passed' and (not data['commands'] or any(c.get('returncode') != 0 for c in data['commands'])):
                raise ValueError('invalid successful command evidence')
            rows.append({'task': name, 'status': data['status']})
        except (OSError, ValueError, KeyError, TypeError, AttributeError) as error:
            errors.append(name + ': ' + str(error))
            rows.append({'task': name, 'status': 'failed'})
    status = 'passed' if not errors and all(row['status'] == 'passed' for row in rows) else 'failed'
    (outbox / 'verification.json').write_text(json.dumps({'status': status, 'tasks': rows, 'errors': errors, 'held': HELD}, indent=2) + '\n')
    lines = ['# Native verification', '', 'Status: ' + status, '', '| Task | Status |', '| --- | --- |']
    lines += ['| ' + row['task'] + ' | ' + row['status'] + ' |' for row in rows]
    lines += ['', '## Held coverage', ''] + ['- ' + k + ': ' + v for k, v in HELD.items()]
    lines += ['', '## Evidence errors', ''] + errors
    (outbox / 'verification.md').write_text('\n'.join(lines) + '\n')
    print(status, end='')


def main():
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=('check', 'aggregate'))
    parser.add_argument('name', nargs='?', choices=TASKS)
    parser.add_argument('--race', action='store_true', help='Required for GHA source and embedded helper race checks')
    args = parser.parse_args()
    if args.race and (args.mode != 'check' or args.name not in ('gha', 'gha-runtime')):
        parser.error('--race applies only to check gha or check gha-runtime')
    if args.mode == 'check':
        if args.name is None:
            parser.error('check requires a task')
        check(args.name, Path(os.environ['VERIFY_ROOT']).resolve(), Path(os.environ['VERIFY_OUTBOX']), race=args.race)
    else:
        aggregate(Path(os.environ['VERIFY_INBOX']), Path(os.environ['VERIFY_OUTBOX']))


if __name__ == '__main__':
    main()
