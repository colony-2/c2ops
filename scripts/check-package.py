#!/usr/bin/env python3
"""Exercise an installed op's process boundary without a provider or network."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

package = Path(sys.argv[1])
manifest = json.loads((package / "share/c2j/op.json").read_text())
assert manifest == json.loads(Path(sys.argv[2]).read_text()), "manifest mismatch"
coordinate = sys.argv[3]
assert coordinate.startswith("nix:github:colony-2/c2ops/main#"), coordinate
attribute = coordinate.split("#", 1)[1]
assert manifest["name"] == ("skill.run" if attribute == "skill-run" else attribute)
command = manifest["command"]
assert command[0] == f"bin/{attribute}", "coordinate selected the wrong executable"
assert command[0].startswith("bin/") and ".." not in Path(command[0]).parts
executable = package / command[0]
assert executable.is_file() and os.access(executable, os.X_OK)
assert not any(ref.startswith("nix:") for ref in manifest.get("dependencies", []))

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
