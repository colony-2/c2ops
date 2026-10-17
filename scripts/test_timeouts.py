"""Published ops leave execution limits to the caller by default."""

import json
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]


class TimeoutPolicyTests(unittest.TestCase):
    def test_all_published_ops_have_no_default_timeout(self):
        catalog = json.loads((ROOT / "nix/ops.json").read_text())
        for name, op in catalog.items():
            with self.subTest(op=name):
                manifest = json.loads((ROOT / op["path"] / "op.json").read_text())
                self.assertNotIn("timeout", manifest)
                for field, schema in manifest["input_schema"]["properties"].items():
                    if "timeout" in field:
                        self.assertNotIn("default", schema)
