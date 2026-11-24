#!/usr/bin/env python3
"""Record, download-check, and retain the exact outputs of a CI candidate."""

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys

from op_test import PUBLIC_FLAKE, ROOT

SYSTEMS = ("x86_64-linux", "aarch64-linux", "aarch64-darwin")
STORE_PATH = re.compile(r"/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[^/\s]+")


def run(*args, **kwargs):
    try:
        return subprocess.run(args, check=True, text=True, **kwargs)
    except subprocess.CalledProcessError as error:
        if error.stderr:
            print(error.stderr, file=sys.stderr, end="")
        raise


def package_names():
    return set(json.loads((ROOT / "nix/ops.json").read_text())) | {"codex-cli", "aider-cli"}


def read_manifest(path, revision, system):
    data = json.loads(Path(path).read_text())
    if data["revision"] != revision or data["system"] != system:
        raise ValueError("package artifact does not match the candidate revision and system")
    if system not in SYSTEMS or not re.fullmatch(r"[0-9a-f]{40}", revision):
        raise ValueError("invalid candidate revision or system")
    if set(data["packages"]) != package_names():
        raise ValueError("package artifact must contain every op and CLI output")
    for name, package in data["packages"].items():
        if package["selector"] != f"nix:{PUBLIC_FLAKE}#{name}":
            raise ValueError(f"unexpected selector for {name}")
        if not STORE_PATH.fullmatch(package["store_path"]) or package["store_path"].endswith(".drv"):
            raise ValueError(f"expected an output store path for {name}")
    return data


def evaluate(system, flake):
    return json.loads(run(
        "nix", "eval", "--json", f"{flake}#packages.{system}", "--apply",
        "ps: builtins.mapAttrs (_: p: p.outPath) ps", capture_output=True,
    ).stdout)


def record(path, revision, system, flake="."):
    outputs = evaluate(system, flake)
    data = {"revision": revision, "system": system, "packages": {
        name: {"selector": f"nix:{PUBLIC_FLAKE}#{name}", "store_path": output}
        for name, output in outputs.items()
    }}
    Path(path).write_text(json.dumps(data, indent=2) + "\n")
    return read_manifest(path, revision, system)


def verify(data, store, cache):
    store = Path(store).resolve()
    # Never accept a warmed store, even on a retry of the same runner.
    store.mkdir(parents=True, exist_ok=False)
    deny = store / "deny-build"
    deny.write_text("#!/bin/sh\necho 'publication verification forbids builds' >&2\nexit 1\n")
    deny.chmod(0o755)
    env = {**os.environ, "XDG_CACHE_HOME": str(store / "lookup-cache")}
    paths = [p["store_path"] for p in data["packages"].values()]
    command = ["nix", "--extra-experimental-features", "nix-command flakes", "--store", str(store)]
    run(*command, "build", "--no-link", "--max-jobs", "0", "--builders", "",
        "--option", "pre-build-hook", str(deny),
        "--option", "fallback", "false", "--option", "require-sigs", "true",
        "--option", "substituters", f"https://cache.nixos.org https://{cache}.cachix.org",
        "--option", "extra-substituters", "",
        "--option", "narinfo-cache-negative-ttl", "0", "--", *paths, env=env)
    run(*command, "store", "verify", "--recursive", "--", *paths, env=env)
    print(f"Verified every output and runtime closure for {data['revision']} ({data['system']})", flush=True)


def pin(data, store, cache, env=None):
    # Cachix pin submits a store path to its API without validating the local
    # store. Check every closure in the selected store before sending any pins.
    store = str(Path(store).resolve())
    env = {**(env if env is not None else os.environ), "NIX_REMOTE": store}
    paths = [p["store_path"] for p in data["packages"].values()]
    run("nix", "--extra-experimental-features", "nix-command flakes", "--store", store,
        "store", "verify", "--recursive", "--option", "substitute", "false",
        "--max-jobs", "0", "--builders", "", "--", *paths, env=env)
    for name in sorted(json.loads((ROOT / "nix/ops.json").read_text())):
        # Immutable release pins preserve earlier published revisions too.
        # CLI helpers are already dependencies of these op closures.
        run("cachix", "pin", cache, f"c2ops-{data['revision']}-{data['system']}-{name}",
            data["packages"][name]["store_path"], env=env)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("record", "verify", "pin"))
    parser.add_argument("--manifest", default="packages.json")
    parser.add_argument("--revision", required=True)
    parser.add_argument("--system", choices=SYSTEMS, required=True)
    parser.add_argument("--store")
    parser.add_argument("--cache", default="colony2")
    args = parser.parse_args()
    if args.action == "record":
        data = record(args.manifest, args.revision, args.system)
        if "GITHUB_OUTPUT" in os.environ:
            paths = [p["store_path"] for p in data["packages"].values()]
            with open(os.environ["GITHUB_OUTPUT"], "a") as output:
                output.write("paths=" + " ".join(paths) + "\n")
        return
    if not args.store or not re.fullmatch(r"[a-z0-9][a-z0-9-]*", args.cache):
        parser.error("verification and pinning require --store and a valid cache name")
    data = read_manifest(args.manifest, args.revision, args.system)
    {"verify": verify, "pin": pin}[args.action](data, args.store, args.cache)


if __name__ == "__main__":
    main()
