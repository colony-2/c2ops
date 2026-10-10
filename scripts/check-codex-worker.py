#!/usr/bin/env python3
"""Exercise real packaged Codex launchers in the minimal production worker image."""

import json
import os
from pathlib import Path
import subprocess
import tempfile

from op_test import ROOT, nix, reference

# Keep the regression environment that exposed the missing launcher utilities.
IMAGE = "ghcr.io/colony-2/base:build-38077543859-1"


def checked(*args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)


def main():
    packages = {}
    for name in ("codex", "skill-run"):
        coordinate = f"nix:github:colony-2/c2ops/main#{name}"
        packages[name] = json.loads(nix(
            "eval", "--json", "--apply",
            "p: { manifest = p.c2j; store_path = p.outPath; }",
            reference(coordinate).removeprefix("nix:"),
        ))
    paths = [package["store_path"] for package in packages.values()]
    nix("build", "--no-link", "--max-jobs", "0", "--builders", "", *paths)

    with tempfile.TemporaryDirectory(prefix="c2ops-worker-check-") as directory:
        bundle = Path(directory)
        checked("go", "build", "-o", str(bundle / "with-tools"),
                "../scripts/with-tools.go", cwd=ROOT / "codex",
                env={**os.environ, "CGO_ENABLED": "0"})
        # Transfer this checkout's built closures, not the published main output.
        nix("copy", "--to", (bundle / "cache").as_uri(), *paths)
        container = checked(
            "docker", "run", "-d", "--rm", "--entrypoint", "/bin/sh",
            "-v", f"{bundle}:/fixture:ro", IMAGE, "-c", "sleep infinity",
            capture_output=True,
        ).stdout.strip()
        try:
            def run(*args, **kwargs):
                return subprocess.run(["docker", "exec", "-i", container, *args],
                                      text=True, capture_output=True, timeout=180, **kwargs)

            result = run("/bin/sh", "-c", "command -v sed")
            if result.returncode != 1:
                raise AssertionError("regression image must not supply ambient sed")
            # These unsigned artifacts were just exported from our own builder.
            # No remote cache signature policy is changed.
            result = run("nix", "copy", "--no-check-sigs", "--from",
                         "file:///fixture/cache", *paths)
            if result.returncode:
                raise RuntimeError(result.stderr)

            for name, package in packages.items():
                store = package["store_path"]
                manifest = package["manifest"]
                cli = manifest["dependencies"][0]
                expected_version = cli.rsplit("@", 1)[1]
                for outcome in ("prepared", "reused"):
                    # Invalid checkpoint deliberately stops after the real version
                    # probe, before credentials or a provider request are needed.
                    request = {
                        "prompt": "version probe",
                        "session": {}, "workdir_path": f"/tmp/{name}/work",
                        "worktree_path": f"/tmp/{name}/worktree",
                    }
                    if name == "skill-run":
                        request["skill"] = "version-probe"
                    result = run(
                        "env", f"C2J_TOOL_CACHE_DIR=/tmp/{name}/tools",
                        f"C2J_OBJECT_OUTBOX=/tmp/{name}/objects",
                        "/fixture/with-tools", "--diagnostics",
                        "--manifest", f"{store}/share/c2j/op.json", "--cwd", store,
                        "--", f"{store}/{manifest['command'][0]}",
                        *manifest["command"][1:], input=json.dumps(request),
                    )
                    diagnostics = [json.loads(line) for line in result.stderr.splitlines()
                                   if line.startswith('{"')]
                    if not diagnostics or not any(
                        tool["reference"] == cli and tool["outcome"] == outcome
                        for diagnostic in diagnostics for tool in diagnostic.get("tools", [])
                    ):
                        raise AssertionError(f"expected {outcome} dependency: {result.stderr}")
                    phases = [json.loads(line.split(": ", 1)[1])
                              for line in result.stderr.splitlines()
                              if line.startswith('codex-op: {')]
                    if (result.returncode != 1
                            or "restore session: invalid or unsupported session metadata" not in result.stderr
                            or not any(phase.get("phase") == "version.ready"
                                       and phase.get("version") == expected_version
                                       for phase in phases)):
                        raise AssertionError(f"{name} ({outcome}) failed: {result.stderr}")
                    print(f"{name}: {outcome} cache, Codex {expected_version}, packaged version probe passed", flush=True)
        finally:
            checked("docker", "rm", "-f", container, stdout=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
