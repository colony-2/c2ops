"""Check native payload validation without needing to execute another platform."""

import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


class CompiledPackageTests(unittest.TestCase):
    def check(self, system, header):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            package = root / "package"
            (package / "bin").mkdir(parents=True)
            (package / "share/c2j").mkdir(parents=True)
            manifest = package / "share/c2j/op.json"
            manifest.write_text(json.dumps({"name": "llm", "command": ["bin/llm"]}))
            compiled = root / "payload/bin/llm"
            compiled.parent.mkdir(parents=True)
            compiled.write_bytes(header)
            compiled.chmod(0o755)
            executable = package / "bin/llm"
            # The process check exercises missing-input handling; the payload
            # header is checked independently and never executed on this host.
            executable.write_text(f"#!/bin/sh\n# {compiled}\necho 'missing input' >&2\nexit 1\n")
            executable.chmod(0o755)
            return subprocess.run([
                sys.executable, str(Path(__file__).with_name("check-package.py")),
                str(package), str(manifest), "nix:github:colony-2/c2ops/published#llm",
                "--compiled-executable", str(compiled), "--system", system,
            ], capture_output=True, text=True)

    @staticmethod
    def macho(cpu=0x0100000C, filetype=2):
        return (b"\xcf\xfa\xed\xfe" + cpu.to_bytes(4, "little") + bytes(4)
                + filetype.to_bytes(4, "little") + bytes(16))

    @staticmethod
    def elf(machine):
        return b"\x7fELF\x02\x01" + bytes(12) + machine.to_bytes(2, "little")

    def test_native_payloads(self):
        for system, header in (("aarch64-darwin", self.macho()),
                               ("aarch64-linux", self.elf(183)),
                               ("x86_64-linux", self.elf(62))):
            with self.subTest(system=system):
                result = self.check(system, header)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_rejects_wrong_darwin_payloads(self):
        for header in (self.elf(183), self.macho(cpu=0x01000007),
                       self.macho(filetype=6), b"#!/bin/sh\n", b"\xcf\xfa\xed\xfe"):
            with self.subTest(header=header):
                result = self.check("aarch64-darwin", header)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("AssertionError", result.stderr)

    def test_npm_contract_is_bundled_and_matches_the_manifest(self):
        source = Path(__file__).resolve().parents[1]
        manifest_value = json.loads((source / 'npm_publish/op.json').read_text())
        for damage in ('none', 'missing', 'mismatch', 'launcher'):
            with self.subTest(damage=damage), tempfile.TemporaryDirectory() as directory:
                package = Path(directory)
                for name in ('bin', 'libexec', 'share/c2j'):
                    (package / name).mkdir(parents=True)
                manifest = package / 'share/c2j/op.json'
                manifest.write_text(json.dumps(manifest_value))
                contract = package / 'libexec/contract.json'
                if damage != 'missing':
                    contract.write_text(json.dumps({} if damage == 'mismatch' else manifest_value['input_schema']))
                launcher = package / 'bin/npm-publish'
                tools = 'node npm git tar gzip' if damage != 'launcher' else ''
                launcher.write_text("#!/bin/sh\n# " + tools + "\necho 'missing input' >&2\nexit 1\n")
                launcher.chmod(0o755)
                result = subprocess.run([sys.executable, str(source / 'scripts/check-package.py'),
                    str(package), str(manifest), 'nix:github:colony-2/c2ops/published#npm-publish'],
                    capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, damage == 'none', result.stderr)
