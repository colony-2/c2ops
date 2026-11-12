#!/usr/bin/env python3
"""Exercise candidate hook loading and native artifacts through c2j's local runtime.

By default check operations use deterministic evidence fixtures and the final
aggregation command runs for real. The gha case runs the actual tooling, GHA
source and embedded runtime tasks; other tasks remain explicit fixtures. No
child build, merge, publication, or live service is used.
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
    parser.add_argument('--case', choices=('absent', 'null', 'value', 'failed', 'native', 'gha'),
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
    for case in ((args.case,) if args.case else ('absent', 'null', 'value', 'failed')):
        mocks = []
        for name in verify.TASKS:
            log = 'fixture command evidence for ' + name + '\n'
            status = 'failed' if case == 'failed' and name == 'manifests' else 'passed'
            evidence = {'task': name, 'status': status,
                        'commands': [{'argv': ['fixture'], 'returncode': 1 if status == 'failed' else 0}],
                        'log_sha256': hashlib.sha256(log.encode()).hexdigest()}
            behavior = {'mode': 'return', 'outputs': {'stdout': status},
                        'artifacts': {name + '.log': log, name + '.json': json.dumps(evidence)}}
            if (case == 'native' and name == 'manifests') or (case == 'gha' and name in ('tooling', 'gha', 'gha-runtime')):
                behavior = {'mode': 'passthrough'}
            mocks.append({'match': {'op': 'command_execution'}, 'behavior': behavior})
        mocks.append({'match': {'op': 'command_execution'}, 'behavior': {'mode': 'passthrough'}})
        inputs = {'api_version': 'c2.build-hook/v1', 'request': 'literal $(false); `false`',
                  'target_directory': '$(false); `false`', 'candidate': 'candidate', 'base': 'base', 'documents': {}}
        if case == 'gha':
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
        (out / (case + '-caller.yaml')).write_text(json.dumps(wrapper, indent=2))
        files = {name: os.path.relpath(root / name, out)
                 for name in ('.c2/recipes/hooks/build/verify.yaml', 'scripts/verify.py')}
        if case in ('native', 'gha'):
            files = {str(path.relative_to(root)): os.path.relpath(path, out)
                     for path in root.rglob('*') if path.is_file()
                     and '.git' not in path.relative_to(root).parts
                     and '__pycache__' not in path.parts}
        assertions = [
            {'type': 'output_equals', 'path': 'api_version', 'value': 'c2.build-hook/v1'},
            {'type': 'output_equals', 'path': 'status', 'value': 'failed' if case == 'failed' else 'passed'},
            {'type': 'cel_true', 'expr': 'size(outputs.artifact_refs) == 34 && outputs.artifact_refs.all(k, outputs.artifact_refs[k].kind == "stored")'},
            {'type': 'cel_true', 'expr': 'repositories.root.clean && repositories.root.new_commits == 0'},
        ]
        suite = {'recipe': case + '-caller.yaml', 'cases': [{
            'id': case, 'type': 'integration_case', 'inputs': {},
            'runtime': {'cells': {'root': {'file_sources': files}}},
            'mocks': {'ops': mocks}, 'assertions': assertions}]}
        suite_path = out / (case + '.test.json')
        suite_path.write_text(json.dumps(suite, indent=2))
        command = ['c2j', 'test', 'run', '--file', str(suite_path), '--out-dir', str(out / case),
                   '--case-timeout', '35m' if case == 'gha' else '2m', '--artifact-mode', 'inline',
                   '--artifact-max-bytes', '4194304']
        with (out / (case + '.log')).open('wb') as log:
            result = subprocess.run(command, stdout=log, stderr=subprocess.STDOUT)
        results.append({'case': case, 'command': command, 'returncode': result.returncode})
        print(case, result.returncode, flush=True)
    (out / 'harness-results.json').write_text(json.dumps(results, indent=2) + '\n')
    return 0 if all(r['returncode'] == 0 for r in results) else 1


if __name__ == '__main__':
    raise SystemExit(main())
