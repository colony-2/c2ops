#!/usr/bin/env python3
"""Resolve public op coordinates, optionally against a locally built test flake."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
PUBLIC_FLAKE = "github:colony-2/c2ops/main"


def reference(coordinate):
    prefix = f"nix:{PUBLIC_FLAKE}#"
    if not coordinate.startswith(prefix):
        raise ValueError(f"expected a public c2ops coordinate, got {coordinate!r}")
    name = coordinate[len(prefix):]
    catalog = json.loads((ROOT / "nix/ops.json").read_text())
    if name not in catalog:
        raise ValueError(f"unknown op attribute {name!r}")
    flake = os.environ.get("C2OPS_TEST_FLAKE", PUBLIC_FLAKE)
    if not flake or "#" in flake:
        raise ValueError("C2OPS_TEST_FLAKE must be a flake reference without an attribute")
    return f"nix:{flake}#{name}"


def nix(*args):
    result = subprocess.run([
        "nix", "--extra-experimental-features", "nix-command flakes", *args
    ], text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError(result.stderr.strip())
    return result.stdout


def run(coordinate):
    selected = reference(coordinate)
    metadata = json.loads(nix(
        "eval", "--json", "--option", "allow-import-from-derivation", "false",
        "--apply", "p: { manifest = p.c2j; store_path = p.outPath; }",
        "--", selected.removeprefix("nix:"),
    ))
    package = Path(metadata["store_path"])
    # Realize the exact inspected output. Tests must build their override first;
    # a missing cache entry never silently switches to source execution.
    nix("build", "--no-link", "--max-jobs", "0", "--builders", "", "--", str(package))
    manifest = json.loads((package / "share/c2j/op.json").read_text())
    if manifest != metadata["manifest"]:
        raise ValueError(f"installed manifest differs from metadata for {selected}")
    command = manifest["command"]
    entry = Path(command[0])
    if entry.is_absolute() or entry.parts[0] != "bin" or ".." in entry.parts:
        raise ValueError(f"invalid packaged command: {command}")
    env = {**os.environ, **manifest.get("env", {})}
    if manifest.get("dependencies"):
        # Use c2j's real preparer and qualified dispatchers, exactly as recipe
        # setup does. Do not supply CLI binaries through npm or ambient PATH.
        prepared = subprocess.run([
            "go", "run", str(ROOT / "scripts/with-tools.go"),
            "--manifest", str(package / "share/c2j/op.json"),
        ], cwd=ROOT / "codex", text=True, capture_output=True)
        if prepared.returncode:
            raise RuntimeError(prepared.stderr.strip())
        bindings = prepared.stdout.strip()
        env["PATH"] = bindings + os.pathsep + env.get("PATH", "")
    os.chdir(package)
    os.execve(package / entry, [str(package / entry), *command[1:]], env)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["reference", "run"])
    parser.add_argument("coordinate")
    args = parser.parse_args()
    try:
        if args.action == "reference":
            print(reference(args.coordinate))
        else:
            run(args.coordinate)
    except (OSError, ValueError, RuntimeError) as error:
        parser.exit(1, f"{args.coordinate}: {error}\n")


if __name__ == "__main__":
    main()
