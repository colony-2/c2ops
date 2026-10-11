#!/usr/bin/env python3
"""Run Codex tests against the CLI in the selected Nix flake."""

import argparse
import json
import os

from op_test import PUBLIC_FLAKE, nix


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    flake = os.environ.get("C2OPS_TEST_FLAKE", PUBLIC_FLAKE)
    if not flake or "#" in flake:
        parser.error("C2OPS_TEST_FLAKE must be a flake reference without an attribute")
    package = json.loads(nix(
        "eval", "--json", "--apply", "p: { path = p.outPath; version = p.version; }",
        f"{flake}#codex-cli",
    ))
    nix("build", "--no-link", "--max-jobs", "0", "--builders", "", package["path"])
    binary = package["path"] + "/bin/codex"
    command = args.command
    if command[:1] == ["--"]:
        command = command[1:]
    if not command:
        print(binary)
        return
    env = {**os.environ,
           "C2OPS_CODEX_BIN": binary,
           "C2OPS_CODEX_COMPAT": "1",
           "C2OPS_CODEX_COMPAT_VERSION": package["version"],
           "C2OPS_CODEX_COMPAT_RESUME_VERSION": package["version"]}
    env.pop("C2OPS_CODEX_COMPAT_RESUME_BIN", None)
    os.execvpe(command[0], command, env)


if __name__ == "__main__":
    main()
