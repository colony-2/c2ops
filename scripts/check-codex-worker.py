#!/usr/bin/env python3
"""Check native Codex ops without ambient CLIs or package managers."""

import json
import os
from pathlib import Path
import subprocess
import tempfile

from op_test import PUBLIC_FLAKE, nix, reference

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
    flake = os.environ.get("C2OPS_TEST_FLAKE", PUBLIC_FLAKE)
    cli = json.loads(nix("eval", "--json", "--apply",
                         "p: { path = p.outPath; version = p.version; }", f"{flake}#codex-cli"))
    paths = [package["store_path"] for package in packages.values()]
    nix("build", "--no-link", "--max-jobs", "0", "--builders", "", *paths)

    for package in packages.values():
        if package["manifest"].get("dependencies"):
            raise AssertionError("packaged Codex must not require external tool setup")
        closure = nix("path-info", "--recursive", package["store_path"]).splitlines()
        if cli["path"] not in closure:
            raise AssertionError("both ops must include the same native Codex derivation")

    with tempfile.TemporaryDirectory(prefix="c2ops-worker-check-") as directory:
        bundle = Path(directory)
        poison = bundle / "poison"
        poison.mkdir()
        for name in ("codex", "pnpm", "node", "npm", "npx", "nix"):
            launcher = poison / name
            launcher.write_text("#!/bin/sh\necho 'unexpected ambient CLI or package manager' >&2\nexit 127\n")
            launcher.chmod(0o755)
        # Transfer this checkout's built closures, not the published main output.
        nix("copy", "--to", (bundle / "cache").as_uri(), *paths)
        # A newer reader may initialize extra cache metadata directories (e.g.
        # build-trace-v2) even during import. Only this disposable cache needs
        # write access; keep the launchers and other fixtures read-only.
        container = checked(
            "docker", "run", "-d", "--rm", "--network=none", "--entrypoint", "/bin/sh",
            "-v", f"{bundle}:/fixture:ro",
            "-v", f"{bundle / 'cache'}:/fixture/cache:rw",
            IMAGE, "-c", "sleep infinity",
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
                expected_version = cli["version"]
                for outcome in ("first", "repeated"):
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
                        "env", "PATH=/fixture/poison",
                        f"C2J_OBJECT_OUTBOX=/tmp/{name}/objects",
                        f"{store}/{manifest['command'][0]}",
                        *manifest["command"][1:], input=json.dumps(request),
                    )
                    phases = [json.loads(line.split(": ", 1)[1])
                              for line in result.stderr.splitlines()
                              if line.startswith('codex-op: {')]
                    if (result.returncode != 1
                            or "restore session: invalid or unsupported session metadata" not in result.stderr
                            or not any(phase.get("phase") == "version.ready"
                                       and phase.get("version") == expected_version
                                       for phase in phases)):
                        raise AssertionError(f"{name} ({outcome}) failed: {result.stderr}")
                    print(f"{name}: {outcome} invocation, native Codex {expected_version}, packaged version probe passed", flush=True)
        finally:
            checked("docker", "rm", "-f", container, stdout=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
