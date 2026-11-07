"""Exercise the real pinned validator, including later catalog entries."""
import json
from pathlib import Path
import subprocess
import tempfile
import unittest


class ManifestSchemaTests(unittest.TestCase):
    def test_every_operation_and_both_fields(self):
        script = Path(__file__).with_name('check-manifest-schemas.py').resolve()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'nix').mkdir()
            catalog = {name: {'path': name} for name in ('first', 'last')}
            (root / 'nix/ops.json').write_text(json.dumps(catalog))
            for name in catalog:
                (root / name).mkdir()
            for invalid_name, invalid_key in [(None, None), *[(n, k) for n in catalog
                    for k in ('input_schema', 'output_schema')]]:
                with self.subTest(operation=invalid_name, field=invalid_key):
                    for name in catalog:
                        manifest = {k: {'type': 'object'} for k in ('input_schema', 'output_schema')}
                        if name == invalid_name:
                            manifest[invalid_key] = {'type': 42}
                        (root / name / 'op.json').write_text(json.dumps(manifest))
                    result = subprocess.run(['uv', 'run', str(script), str(root)],
                                            text=True, capture_output=True)
                    if invalid_name is None:
                        self.assertEqual(result.returncode, 0, result.stderr)
                        for name in catalog:
                            for key in ('input_schema', 'output_schema'):
                                self.assertIn(f'{name}: {key}', result.stdout)
                    else:
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn(f'{invalid_name}: {invalid_key}', result.stdout)
                        self.assertIn('SchemaError', result.stderr)
