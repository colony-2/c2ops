# /// script
# requires-python = ">=3.11"
# dependencies = ["jsonschema==4.26.0"]
# ///
"""Run the service-free schema assertion from nix/checks.nix."""
import json
from pathlib import Path
import sys

from jsonschema import Draft202012Validator


def check(root):
    catalog = json.loads((root / 'nix/ops.json').read_text())
    for name, op in catalog.items():
        manifest = json.loads((root / op['path'] / 'op.json').read_text())
        for key in ('input_schema', 'output_schema'):
            print(f'{name}: {key}', flush=True)
            Draft202012Validator.check_schema(manifest[key])
    print(f'Validated both schemas for {len(catalog)} catalog operations.')


if __name__ == '__main__':
    check(Path(sys.argv[1]) if len(sys.argv) > 1 else Path('.'))
