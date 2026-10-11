import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from publication import PUBLIC_FLAKE, package_names, pin, read_manifest, verify


class PublicationTests(unittest.TestCase):
    def setUp(self):
        self.data = {"revision": "a" * 40, "system": "aarch64-linux", "packages": {
            name: {"selector": f"nix:{PUBLIC_FLAKE}#{name}",
                   "store_path": f"/nix/store/{'0' * 32}-{name}"}
            for name in package_names()
        }}

    def read(self, data):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "packages.json"
            path.write_text(json.dumps(data))
            return read_manifest(path, "a" * 40, "aarch64-linux")

    def test_artifact_identity_and_completeness(self):
        self.assertEqual(self.read(self.data), self.data)
        for field, value in (("revision", "b" * 40), ("system", "x86_64-linux")):
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.read({**self.data, field: value})
        data = copy.deepcopy(self.data)
        del data["packages"]["codex-cli"]
        with self.assertRaises(ValueError):
            self.read(data)

    def test_rejects_source_derivations_and_wrong_selectors(self):
        for field, value in (("store_path", "/tmp/output"),
                             ("store_path", f"/nix/store/{'0' * 32}-codex.drv"),
                             ("selector", "nix:github:colony-2/c2ops/main#codex")):
            data = copy.deepcopy(self.data)
            data["packages"]["codex"][field] = value
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.read(data)

    def test_refuses_warm_store_before_running_nix(self):
        with tempfile.TemporaryDirectory() as directory, patch("publication.run") as run:
            with self.assertRaises(FileExistsError):
                verify(self.data, directory, "colony2")
            run.assert_not_called()

    def test_failed_download_stops_before_verification(self):
        with tempfile.TemporaryDirectory() as directory, patch("publication.run") as run:
            run.side_effect = RuntimeError("missing output")
            with self.assertRaisesRegex(RuntimeError, "missing output"):
                verify(self.data, Path(directory) / "store", "colony2")
            self.assertEqual(run.call_count, 1)

    def test_failed_pin_stops_the_job(self):
        with patch("publication.run", side_effect=RuntimeError("pin denied")) as run:
            with self.assertRaisesRegex(RuntimeError, "pin denied"):
                pin(self.data, "/tmp/verified-store", "colony2")
            self.assertEqual(run.call_count, 1)
