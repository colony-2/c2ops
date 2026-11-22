"""General ops defer limits; the approved deployment contract is bounded."""

import json
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]


class TimeoutPolicyTests(unittest.TestCase):
    def test_operation_timeout_policy(self):
        catalog = json.loads((ROOT / "nix/ops.json").read_text())
        for name, op in catalog.items():
            with self.subTest(op=name):
                manifest = json.loads((ROOT / op["path"] / "op.json").read_text())
                self.assertNotIn("timeout", manifest)
                for field, schema in manifest["input_schema"]["properties"].items():
                    if "timeout" not in field:
                        continue
                    if name == "opentofu-up":
                        expected = {"timeout": "30m", "lock_timeout": "2m"}
                        self.assertEqual(schema.get("default"), expected[field])
                        self.assertEqual(schema.get("pattern"), "^[1-9][0-9]*[sm]$")
                    else:
                        self.assertNotIn("default", schema)
