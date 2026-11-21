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
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', type=Path, required=True, help='Scratch directory outside the repository')
    parser.add_argument('--case', choices=('absent', 'null', 'value', 'failed', 'native', 'gha', 'release-pack', 'release-pack-failed', 'tooling', 'tooling-missing-makefile'),
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
    for case in ((args.case,) if args.case else ('absent', 'null', 'value', 'failed', 'release-pack-failed')):
        mocks = []
        for name in verify.TASKS:
            log = 'fixture command evidence for ' + name + '\n'
            status = 'failed' if ((case == 'failed' and name == 'manifests') or
                                  (case == 'release-pack-failed' and name == 'release-pack')) else 'passed'
            evidence = {'task': name, 'status': status,
                        'commands': [{'argv': ['fixture'], 'returncode': 1 if status == 'failed' else 0}],
                        'log_sha256': hashlib.sha256(log.encode()).hexdigest()}
            behavior = {'mode': 'return', 'outputs': {'stdout': status},
                        'artifacts': {name + '.log': log, name + '.json': json.dumps(evidence)}}
            if (case == 'native' and name == 'manifests') or (case == 'release-pack' and name == 'release-pack') or (case == 'gha' and name in ('tooling', 'gha', 'gha-runtime')) or (case in ('tooling', 'tooling-missing-makefile') and name == 'tooling'):
                behavior = {'mode': 'passthrough'}
            mocks.append({'match': {'op': 'command_execution'}, 'behavior': behavior})
        mocks.append({'match': {'op': 'command_execution'}, 'behavior': {'mode': 'passthrough'}})
        inputs = {'api_version': 'c2.build-hook/v1', 'request': 'literal $(false); `false`',
                  'target_directory': '$(false); `false`', 'candidate': 'candidate', 'base': 'base', 'documents': {}}
        if case in ('gha', 'tooling', 'tooling-missing-makefile'):
            inputs.update(target_directory='.', candidate=subprocess.check_output(
                ['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip())
        if case == 'null':
            inputs['settings'] = None
        if case == 'value':
            inputs['settings'] = {'kind': 'value', 'format': 'json', 'value': {'variables': {'sample': '$(false)'}}}
        wrapper = {'id': 'caller', 'sequence': [{'id': 'hook',
            'include': 'cell://.c2/recipes/hooks/build/verify.yaml', 'inputs': inputs,
            'expect_outputs': {'type': 'object', 'required': ['api_version', 'status', 'summary', 'artifact_refs'],
                'properties': {'api_version': {'enum': ['c2.build-hook/v1']},
                               'status': {'enum': ['passed', 'failed', 'skipped']},
                               'summary': {'type': 'string'}, 'artifact_refs': {'type': 'object'}},
                'additionalProperties': False}}],
            'outputs': {key: '${{ sequence.hook.outputs.' + key + ' }}'
                        for key in ('api_version', 'status', 'summary', 'artifact_refs')}}
        files = {name: os.path.relpath(root / name, out)
                 for name in ('.c2/recipes/hooks/build/verify.yaml', 'scripts/verify.py')}
        if case in ('native', 'gha', 'release-pack', 'tooling', 'tooling-missing-makefile'):
            files = {str(path.relative_to(root)): os.path.relpath(path, out)
                     for path in root.rglob('*') if path.is_file()
                     and '.git' not in path.relative_to(root).parts
                     and '__pycache__' not in path.parts}
        if case == 'tooling-missing-makefile':
            # Omit only from the runtime snapshot, never the managed candidate.
            del files['github_release/Makefile']
        expected = 'failed' if case in ('failed', 'release-pack-failed', 'tooling-missing-makefile') else 'passed'
        assertions = [
            {'type': 'output_equals', 'path': 'api_version', 'value': 'c2.build-hook/v1'},
            {'type': 'output_equals', 'path': 'status', 'value': expected},
            {'type': 'output_equals', 'path': 'summary', 'value':
                'Verification: ' + expected +
                '. release.pack archive test (release_pack / release-pack): ' +
                ('failed' if case == 'release-pack-failed' else 'passed') +
                '. Inspect verification.md and release-pack.log/json for task evidence; exclusions are recorded.'},
            {'type': 'cel_true', 'expr': f'size(outputs.artifact_refs) == {2 * len(verify.TASKS) + 2} && outputs.artifact_refs.all(k, outputs.artifact_refs[k].kind == "stored")'},
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
assert all(t['status'] == (expected if t['task'] == 'tooling' else 'passed') for t in report['tasks'])
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
            'runtime': {'cells': {'root': {'file_sources': files}}},
            'mocks': {'ops': mocks}, 'assertions': assertions}]}
        suite_path = out / (case + '.test.json')
        suite_path.write_text(json.dumps(suite, indent=2))
        command = ['c2j', 'test', 'run', '--file', str(suite_path), '--out-dir', str(out / case),
                   '--case-timeout', '35m' if case == 'gha' else ('20m' if case in ('tooling', 'tooling-missing-makefile') else '2m'), '--artifact-mode', 'inline',
                   '--artifact-max-bytes', '4194304']
        with (out / (case + '.log')).open('wb') as log:
            result = subprocess.run(command, stdout=log, stderr=subprocess.STDOUT)
        results.append({'case': case, 'command': command, 'returncode': result.returncode})
        print(case, result.returncode, flush=True)
    (out / 'harness-results.json').write_text(json.dumps(results, indent=2) + '\n')
    return 0 if all(r['returncode'] == 0 for r in results) else 1


if __name__ == '__main__':
    raise SystemExit(main())
