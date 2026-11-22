#!/usr/bin/env python3
"""Exercise candidate hook loading and native artifacts through c2j's local runtime.

By default check operations use deterministic evidence fixtures and the final
aggregation command runs for real. The gha case runs the actual tooling, GHA
source and embedded runtime tasks; other tasks remain explicit fixtures. No
child build, merge, publication, or live service is used.

The tooling cases run the real tooling task and inspect exported receipts,
logs and aggregate evidence. The negative case removes the GitHub release
Makefile only from its runtime snapshot and requires the resulting failure.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import shlex
import sys


def credential_permission_cases(root, out):
    """Probe repository access, cloud ceilings and the actual pending command.

    Local runtime repositories are seeded without authenticated cloning. The Git
    probe exercises the same named-secret authority needed before managed command
    startup. These fixture probes do not establish private-clone or Google success.
    """
    hook = (root / '.c2/recipes/hooks/build/verify.yaml').read_text()
    start = hook.index('- id: opentofu_credentials\n')
    end = hook.index('- id: report\n', start)
    original = hook[start:end]
    git_grant = 'resolve: [gitvend_base_token]'
    if 'const: false' not in original or git_grant not in original:
        raise RuntimeError('live leaf freshness/repository permission contract changed')
    command = 'python3 "$VERIFY_ROOT/scripts/verify.py" check opentofu_credentials'
    git_probe = ('import subprocess;'
                 'r=subprocess.run(["c2j","secret","get","gitvend_base_token","--format=raw"],capture_output=True);'
                 'assert r.returncode==0 and r.stdout==b"local-git-fixture";')
    results = []
    cases = ('credentials-git-only-pending', 'credentials-google-denied',
             'credentials-allowed-first', 'credentials-allowed-fresh', 'credentials-ceiling-denied')
    for case in cases:
        denied = case.endswith('ceiling-denied')
        configured = case in ('credentials-allowed-first', 'credentials-allowed-fresh', 'credentials-ceiling-denied')
        names = ['gitvend_base_token'] + (['literal_test_token', 'literal_test_key'] if configured else [])
        leaf = original.replace(git_grant, 'resolve: [' + ', '.join(names) + ']')
        leaf = leaf.replace('packages: *id001', 'packages: [nix:github:NixOS/nixpkgs/e7439b6b14ad3cc35d05608ebca9bce01a25f5f8#python3]')
        probe = git_probe
        expected = 'fresh literal resolution'
        if case == 'credentials-git-only-pending':
            # Run the real verifier, including public-profile parsing and receipt
            # creation, after proving the separate repository prerequisite.
            run = 'python3 -c ' + shlex.quote(probe) + '\n' + command
            expected = 'pending'
        else:
            if case == 'credentials-google-denied':
                probe += ('r=subprocess.run(["c2j","secret","get","literal_test_token","--format=raw"],capture_output=True);'
                          'assert r.returncode!=0 and b"local-permission-fixture" not in r.stdout;'
                          'print("git available; Google denied",end="")')
                expected = 'git available; Google denied'
            else:
                probe += ('import json,os;'
                          'assert json.loads(os.environ["VERIFY_OPENTOFU_PROFILE"])["token_secret"]=="literal_test_token";'
                          'r=subprocess.run(["c2j","secret","get","literal_test_token","--format=raw"],capture_output=True);'
                          'assert r.returncode==0 and r.stdout==b"local-permission-fixture";'
                          'r=subprocess.run(["c2j","secret","get","literal_test_key","--format=raw"],capture_output=True);'
                          'assert r.returncode==0 and r.stdout==b"local-key-fixture";'
                          'print("fresh literal resolution",end="")')
            run = 'python3 -c ' + shlex.quote(probe)
        # JSON is a YAML scalar and preserves newlines without shell evaluation.
        leaf = leaf.replace('run: ' + command, 'run: ' + json.dumps(run))
        projection = (hook[:hook.index('sequence:\n')] + 'sequence:\n' + leaf +
                      'outputs:\n  stdout: ${{ sequence.opentofu_credentials.outputs.stdout }}\n')
        projection_path = out / (case + '-leaf.yaml')
        projection_path.write_text(projection)
        inputs = {'target_directory': '.', 'candidate': 'candidate', 'base': 'base'}
        if configured:
            inputs['settings'] = {'value': {'variables': {'opentofu_google': {'token_secret': 'literal_test_token'}}}}
        grant = {'jobdb': {'secrets': {'resolve': names}}}
        caller = {'id': 'credential-caller', 'perms': {'limit': {'jobdb': {}}} if denied else grant,
                  'sequence': [{'id': 'hook', 'perms': grant, 'include': 'cell://.c2/recipes/hooks/build/verify.yaml', 'inputs': inputs}],
                  'outputs': {'stdout': '${{ sequence.hook.outputs.stdout }}'}}
        (out / (case + '-caller.json')).write_text(json.dumps(caller))
        files = {'.c2/recipes/hooks/build/verify.yaml': projection_path.name}
        files.update({name: os.path.relpath(root / name, out)
                      for name in ('scripts/verify.py', 'scripts/opentofu_google.py')})
        runtime = {'cells': {'root': {'file_sources': files}},
                   'secrets': {'gitvend_base_token': 'local-git-fixture', 'literal_test_token': 'local-permission-fixture',
                               'literal_test_key': 'local-key-fixture'},
                   'config': {'_c2j_hook_policy_tenant': {'version': 1, 'hooks': {
                       '.c2/recipes/hooks/build/verify.yaml': {'perms': grant,
                           'source_trust': {'jobdb.secrets.resolve': {n: {'mode': 'candidate'} for n in names}}}}}}}
        if denied:
            runtime['expect_error'] = 'exceed'
        suite = {'recipe': case + '-caller.json', 'cases': [{'id': case, 'type': 'integration_case', 'runtime': runtime,
                 'mocks': {'ops': [] if denied else [{'match': {'op': 'command_execution'}, 'repeat': True, 'behavior': {'mode': 'passthrough'}}]},
                 'assertions': [] if denied else [{'type': 'output_equals', 'path': 'stdout', 'value': expected}]}]}
        suite_path = out / (case + '.test.json'); suite_path.write_text(json.dumps(suite))
        argv = ['c2j', 'test', 'run', '--file', str(suite_path), '--out-dir', str(out / case), '--case-timeout', '2m']
        with (out / (case + '.log')).open('wb') as log:
            result = subprocess.run(argv, stdout=log, stderr=subprocess.STDOUT)
        results.append({'case': case, 'command': argv, 'returncode': result.returncode})
        print(case, result.returncode, flush=True)
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', type=Path, required=True, help='Scratch directory outside the repository')
    parser.add_argument('--case', choices=('absent', 'null', 'value', 'failed', 'native', 'gha', 'release-pack', 'release-pack-failed', 'tooling', 'tooling-missing-makefile', 'opentofu-failed', 'opentofu', 'credentials'),
                        help='native runs manifests; gha runs tooling and both GHA suites using declared operation tools')
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    out = args.out.resolve()
    if out == root or root in out.parents:
        parser.error('--out must be outside the repository')
    out.mkdir(parents=True, exist_ok=True)
    sys.dont_write_bytecode = True
    spec = importlib.util.spec_from_file_location('verify', root / 'scripts/verify.py')
    verify = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(verify)
    results = []
    if args.case in (None, 'credentials'):
        results.extend(credential_permission_cases(root, out))
    for case in (() if args.case == 'credentials' else (args.case,) if args.case else ('absent', 'null', 'value', 'failed', 'release-pack-failed', 'opentofu-failed')):
        mocks = []
        for name in verify.ALL_TASKS:
            log = 'fixture command evidence for ' + name + '\n'
            status = 'failed' if ((case == 'failed' and name == 'manifests') or
                                  (case == 'release-pack-failed' and name == 'release-pack') or
                                  (case == 'opentofu-failed' and name == 'opentofu')) else 'passed'
            if name == verify.LIVE_TASK:
                status = 'pending'
            evidence = {'task': name, 'status': status, 'candidate': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip() if case in ('gha', 'opentofu', 'tooling', 'tooling-missing-makefile') else 'candidate', 'base': 'base',
                        'commands': [] if name == verify.LIVE_TASK else [{'argv': ['fixture'], 'returncode': 1 if status == 'failed' else 0}],
                        'log_sha256': hashlib.sha256(log.encode()).hexdigest()}
            behavior = {'mode': 'return', 'outputs': {'stdout': status},
                        'artifacts': {name + '.log': log, name + '.json': json.dumps(evidence)}}
            if (case == 'native' and name == 'manifests') or (case == 'release-pack' and name == 'release-pack') or (case == 'gha' and name in ('tooling', 'gha', 'gha-runtime')) or (case in ('tooling', 'tooling-missing-makefile') and name == 'tooling') or (case == 'opentofu' and name == 'opentofu'):
                behavior = {'mode': 'passthrough'}
            mocks.append({'match': {'op': 'command_execution'}, 'behavior': behavior})
        mocks.append({'match': {'op': 'command_execution'}, 'behavior': {'mode': 'passthrough'}})
        inputs = {'api_version': 'c2.build-hook/v1', 'request': 'literal $(false); `false`',
                  'target_directory': '$(false); `false`', 'candidate': 'candidate', 'base': 'base', 'documents': {}}
        if case in ('gha', 'tooling', 'tooling-missing-makefile', 'opentofu'):
            inputs.update(target_directory='.', candidate=subprocess.check_output(
                ['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip())
        if case == 'null':
            inputs['settings'] = None
        if case == 'value':
            inputs['settings'] = {'kind': 'value', 'format': 'json', 'value': {'variables': {'sample': '$(false)'}}}
        repository_grant = {'jobdb': {'secrets': {'resolve': ['gitvend_base_token']}}}
        wrapper = {'id': 'caller', 'perms': repository_grant, 'sequence': [{'id': 'hook', 'perms': repository_grant,
            'include': 'cell://.c2/recipes/hooks/build/verify.yaml', 'inputs': inputs,
            'expect_outputs': {'type': 'object', 'required': ['api_version', 'status', 'summary', 'artifact_refs'],
                'properties': {'api_version': {'enum': ['c2.build-hook/v1']},
                               'status': {'enum': ['passed', 'failed', 'skipped']},
                               'summary': {'type': 'string'}, 'artifact_refs': {'type': 'object'}},
                'additionalProperties': False}}],
            'outputs': {key: '${{ sequence.hook.outputs.' + key + ' }}'
                        for key in ('api_version', 'status', 'summary', 'artifact_refs')}}
        files = {name: os.path.relpath(root / name, out)
                 for name in ('.c2/recipes/hooks/build/verify.yaml', 'scripts/verify.py', 'scripts/opentofu_google.py')}
        if case in ('native', 'gha', 'release-pack', 'tooling', 'tooling-missing-makefile', 'opentofu'):
            files = {str(path.relative_to(root)): os.path.relpath(path, out)
                     for path in root.rglob('*') if path.is_file()
                     and '.git' not in path.relative_to(root).parts
                     and '__pycache__' not in path.parts}
        if case == 'tooling-missing-makefile':
            # Omit only from the runtime snapshot, never the managed candidate.
            del files['github_release/Makefile']
        expected = 'failed' if case in ('failed', 'release-pack-failed', 'tooling-missing-makefile', 'opentofu-failed') else 'passed'
        assertions = [
            {'type': 'output_equals', 'path': 'api_version', 'value': 'c2.build-hook/v1'},
            {'type': 'output_equals', 'path': 'status', 'value': expected},
            {'type': 'output_equals', 'path': 'summary', 'value':
                'Verification: ' + expected +
                '. release.pack archive test (release_pack / release-pack): ' +
                ('failed' if case == 'release-pack-failed' else 'passed') +
                '. Inspect verification.md and release-pack.log/json for task evidence; exclusions are recorded.'},
            {'type': 'cel_true', 'expr': f'size(outputs.artifact_refs) == {2 * len(verify.ALL_TASKS) + 2} && outputs.artifact_refs.all(k, outputs.artifact_refs[k].kind == "stored")'},
            {'type': 'cel_true', 'expr': 'repositories.root.clean && repositories.root.new_commits == 0'},
        ]
        if case in ('tooling', 'tooling-missing-makefile'):
            # Read the exported artifacts through the real runtime, so an
            # unrelated failure or missing receipt cannot satisfy this case.
            inspect = '''import hashlib,json,os
from pathlib import Path
root = Path(os.environ['EVIDENCE_INBOX'])
expected = os.environ['EXPECTED_STATUS']
tooling = json.loads((root / 'tooling.json').read_text())
log = (root / 'tooling.log').read_bytes()
report = json.loads((root / 'verification.json').read_text())
assert tooling['status'] == expected
assert tooling['log_sha256'] == hashlib.sha256(log).hexdigest()
commands = [c for c in tooling['commands'] if c['argv'] == ['make', 'test-tools']]
assert len(commands) == 1 and (commands[0]['returncode'] == 0) == (expected == 'passed')
assert report['status'] == expected and report['errors'] == []
assert all(t['status'] == (expected if t['task'] == 'tooling' else 'pending' if t['task'] == 'opentofu_credentials' else 'passed') for t in report['tasks'])
if expected == 'failed':
    assert b'github_release: missing Makefile' in log
print('checked', end='')
'''
            wrapper['sequence'].append({
                'id': 'inspect_evidence', 'op': 'command_execution', 'const': True,
                'execution_needs': {'packages': ['nix:github:NixOS/nixpkgs/e7439b6b14ad3cc35d05608ebca9bce01a25f5f8#python3']},
                'artifacts': {name: "${{ sequence.hook.outputs.artifact_refs['" + name + "'] }}"
                              for name in ('tooling.json', 'tooling.log', 'verification.json')},
                'inputs': {'env': {'EVIDENCE_INBOX': '${{ context.environment.op.inbox }}', 'EXPECTED_STATUS': expected},
                           'run': "python3 - <<'PY'\n" + inspect + 'PY\n'}})
            mocks.append({'match': {'op': 'command_execution'}, 'behavior': {'mode': 'passthrough'}})
            wrapper['outputs']['evidence_status'] = '${{ sequence.inspect_evidence.outputs.stdout }}'
            assertions.append({'type': 'output_equals', 'path': 'evidence_status', 'value': 'checked'})
        (out / (case + '-caller.yaml')).write_text(json.dumps(wrapper, indent=2))
        suite = {'recipe': case + '-caller.yaml', 'cases': [{
            'id': case, 'type': 'integration_case', 'inputs': {},
            'runtime': {'cells': {'root': {'file_sources': files}},
                'config': {'_c2j_hook_policy_tenant': {'version': 1, 'hooks': {
                    '.c2/recipes/hooks/build/verify.yaml': {'perms': repository_grant,
                        'source_trust': {'jobdb.secrets.resolve': {
                            'gitvend_base_token': {'mode': 'candidate'}}}}}}}},
            'mocks': {'ops': mocks}, 'assertions': assertions}]}
        if case == 'opentofu':
            suite['cases'][0]['runtime']['observe'] = {'scopes': [
                {'node_path': 'caller/hook/c2ops-native-verify/opentofu',
                 'outputs': ['stdout'], 'retain_artifacts': ['opentofu.json', 'opentofu.log']}]}
            assertions.append({'type': 'cel_true', 'expr':
                'observation_collection.complete && observations.size() == 1'})
        suite_path = out / (case + '.test.json')
        suite_path.write_text(json.dumps(suite, indent=2))
        command = ['c2j', 'test', 'run', '--file', str(suite_path), '--out-dir', str(out / case),
                   '--case-timeout', '35m' if case in ('gha', 'opentofu') else ('20m' if case in ('tooling', 'tooling-missing-makefile') else '2m'), '--artifact-mode', 'inline',
                   '--artifact-max-bytes', '4194304']
        with (out / (case + '.log')).open('wb') as log:
            result = subprocess.run(command, stdout=log, stderr=subprocess.STDOUT)
        results.append({'case': case, 'command': command, 'returncode': result.returncode})
        print(case, result.returncode, flush=True)
    (out / 'harness-results.json').write_text(json.dumps(results, indent=2) + '\n')
    return 0 if all(r['returncode'] == 0 for r in results) else 1


if __name__ == '__main__':
    raise SystemExit(main())
