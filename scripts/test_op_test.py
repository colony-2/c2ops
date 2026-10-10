import os
import unittest
from unittest.mock import patch

from op_test import reference


class ReferenceTests(unittest.TestCase):
    def test_public_coordinate_is_default(self):
        with patch.dict(os.environ, {}, clear=True):
            self.assertEqual(reference("nix:github:colony-2/c2ops/main#codex"),
                             "nix:github:colony-2/c2ops/main#codex")

    def test_override_preserves_attribute(self):
        with patch.dict(os.environ, {"C2OPS_TEST_FLAKE": "path:/tmp/checkout"}):
            self.assertEqual(reference("nix:github:colony-2/c2ops/main#skill-run"),
                             "nix:path:/tmp/checkout#skill-run")

    def test_override_cannot_change_attribute(self):
        with patch.dict(os.environ, {"C2OPS_TEST_FLAKE": "path:/tmp/checkout#codex"}):
            with self.assertRaisesRegex(ValueError, "without an attribute"):
                reference("nix:github:colony-2/c2ops/main#skill-run")

    def test_unknown_or_legacy_coordinates_fail(self):
        for value in ["./codex", "codex", "nix:github:colony-2/c2ops/main#missing",
                      "nix:github:colony-2/c2ops/main#skill.run"]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                reference(value)
