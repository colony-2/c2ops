#!/usr/bin/env python3
"""Run mandatory Pydantic suites using the candidate's pinned Python environment."""
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
NIX = ['nix', '--extra-experimental-features', 'nix-command flakes']


def main():
    env = dict(os.environ)
    interpreter = env.get('PYDANTIC_TEST_PYTHON')
    if not interpreter:
        flake = env.get('C2OPS_TEST_FLAKE', 'path:' + str(ROOT))
        def evaluate(selector, expression):
            return json.loads(subprocess.check_output([*NIX, 'eval', '--json', '--no-write-lock-file',
                                                       '--apply', expression, selector], text=True))
        python = evaluate(flake + '#pydantic', 'p: p.pythonEnv.outPath')
        rev = json.loads((ROOT / 'flake.lock').read_text())['nodes']['nixpkgs']['locked']['rev']
        yaml = evaluate('github:NixOS/nixpkgs/' + rev + '#python3Packages.pyyaml',
                        'p: { path = p.outPath; site = p.pythonModule.sitePackages; }')
        subprocess.run([*NIX, 'build', '--no-link', python, yaml['path']], check=True)
        interpreter = python + '/bin/python'
        env['PYTHONPATH'] = yaml['path'] + '/' + yaml['site']
    commands = [(['go', 'test', '-mod=readonly', '-count=1', '-timeout=8m', './...'], ROOT / 'pydantic'),
                ([interpreter, 'pydantic/test_binding.py'], ROOT)]
    failed = False
    for command, directory in commands:
        print('$ ' + json.dumps(command), flush=True)
        result = subprocess.run(command, cwd=directory, env=env)
        print('suite returncode=' + str(result.returncode), flush=True)
        failed |= result.returncode != 0
    return int(failed)


if __name__ == '__main__':
    sys.exit(main())
