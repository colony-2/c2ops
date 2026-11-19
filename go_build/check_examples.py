# /// script
# requires-python = ">=3.11"
# dependencies = ["jsonschema==4.26.0"]
# ///
"""Validate the complete v1 wire examples and their source inventory binding."""
import hashlib
import json
from pathlib import Path, PurePosixPath
import sys

from jsonschema import Draft202012Validator


def check(root):
    manifest = json.loads((root / 'op.json').read_text())
    request = json.loads((root / 'examples/input.json').read_text())
    raw = (root / 'examples/output.json').read_bytes()
    assert len(raw) <= 128 * 1024, 'example output exceeds process limit'
    envelope = json.loads(raw)
    assert set(envelope) == {'output'}, 'invalid process envelope'
    output = envelope['output']
    Draft202012Validator(manifest['input_schema']).validate(request)
    Draft202012Validator(manifest['output_schema']).validate(output)
    assert output['intent'] == request['intent'], 'mixed intent'
    identity = ('schema_version', 'release_id', 'repository', 'source_commit', 'version', 'tag')
    names = set()
    targets = []
    for descriptor in output['files']:
        name = descriptor['name']
        path = PurePosixPath(name)
        assert name not in names and not path.is_absolute() and '..' not in path.parts and str(path) == name, 'duplicate or unsafe artifact name'
        names.add(name)
        assert all(descriptor[k] == request['intent'][k] for k in identity), 'mixed file identity'
        if descriptor['kind'] == 'binary':
            targets.append((descriptor['goos'], descriptor['goarch']))
    assert targets == sorted((t['goos'], t['goarch']) for t in request['targets']), 'incomplete targets'
    source = output['provenance']['source']
    data = (root / 'examples/source-tree.json').read_bytes()
    sha = hashlib.sha256(data).hexdigest()
    descriptor, = (f for f in output['files'] if f['name'] == source['inventory_name'])
    assert descriptor['kind'] == 'document' and descriptor['size'] == len(data), 'invalid inventory descriptor'
    assert descriptor['sha256'] == source['inventory_sha256'] == sha, 'inventory hash mismatch'
    inventory = json.loads(data)
    assert inventory['source_commit'] == source['commit'] == request['intent']['source_commit'], 'mixed source commit'
    assert inventory['source_tree'] == source['tree'], 'mixed source tree'
    assert len(inventory['entries']) == source['path_count'], 'incomplete source inventory'
    sdk = output['provenance']['sdk']
    assert sdk['resolved_version'] == sdk['actual_version'], 'unverified compiler'
    print('go.build: complete wire examples and inventory binding validated')


if __name__ == '__main__':
    check(Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).resolve().parent)
