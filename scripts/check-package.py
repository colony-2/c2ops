#!/usr/bin/env python3
"""Exercise an installed op's process boundary without a provider or network."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("package", type=Path)
parser.add_argument("manifest", type=Path)
parser.add_argument("coordinate")
parser.add_argument("--compiled-executable", type=Path)
parser.add_argument("--system", choices=("x86_64-linux", "aarch64-linux"))
args = parser.parse_args()

package = args.package
manifest = json.loads((package / "share/c2j/op.json").read_text())
assert manifest == json.loads(args.manifest.read_text()), "manifest mismatch"
coordinate = args.coordinate
assert coordinate.startswith("nix:github:colony-2/c2ops/published#"), coordinate
attribute = coordinate.split("#", 1)[1]
assert manifest["name"] == ("skill.run" if attribute == "skill-run" else attribute)
command = manifest["command"]
assert command[0] == f"bin/{attribute}", "coordinate selected the wrong executable"
assert command[0].startswith("bin/") and ".." not in Path(command[0]).parts
executable = package / command[0]
assert executable.is_file() and os.access(executable, os.X_OK)
assert not any(ref.startswith("nix:") for ref in manifest.get("dependencies", []))

if args.compiled_executable:
    compiled = args.compiled_executable
    assert os.access(compiled, os.X_OK), "compiled payload must be executable"
    with compiled.open("rb") as binary:
        header = binary.read(20)
    assert header[:6] == b"\x7fELF\x02\x01", "expected a compiled ELF64 executable, not a source launcher"
    machine = {"x86_64-linux": 62, "aarch64-linux": 183}[args.system]
    assert int.from_bytes(header[18:20], "little") == machine, "wrong binary architecture"
    assert (executable.resolve() == compiled.resolve()
            or os.fsencode(compiled) in executable.read_bytes()), "launcher must reference the compiled payload"
    for root in (package, compiled.parent.parent):
        assert not list(root.rglob("*.go")), "Go sources must not be shipped in the op output"
        assert not list(root.rglob("go.mod")), "Go module sources must not be shipped in the op output"

# Missing required input must fail cleanly, without trying to install tools or
# writing to the read-only package. An empty PATH catches unbound interpreters.
with tempfile.TemporaryDirectory() as home:
    env = {"HOME": home, "PATH": "", **manifest.get("env", {})}
    result = subprocess.run(
        [str(executable), *command[1:]], input="{}", text=True,
        capture_output=True, cwd=package, env=env, timeout=60,
    )
    assert result.returncode != 0, result.stdout
    assert result.stderr.strip(), "missing input must produce a diagnostic"
    for unexpected in ("ModuleNotFoundError", "command not found", "No such file or directory"):
        assert unexpected not in result.stderr, result.stderr
    if manifest["name"] == "rule_gate":
        result = subprocess.run(
            [str(executable)], input=json.dumps({"rules": [
                {"id": "ready", "type": "assert", "value": True, "message": "ready"}
            ]}), text=True, capture_output=True, cwd=package, env=env, timeout=10,
        )
        assert result.returncode == 0, result.stderr
        assert json.loads(result.stdout)["output"]["ok"] is True
