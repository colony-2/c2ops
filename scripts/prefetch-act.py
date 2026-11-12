#!/usr/bin/env python3
"""Verify and import GHA's private source before starting sandboxed Nix builds."""

import argparse
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def command(program, *args):
    result = subprocess.run([program, *args], capture_output=True, text=True)
    if result.returncode:
        # Git/fetcher diagnostics can include credentials from ambient helpers.
        # Keep the action and exit code; do not echo those diagnostics.
        raise ValueError(
            f"{program} {args[0]} failed (exit {result.returncode}); pinned act source "
            "is unavailable. Check the authorized private Git route and Nix store access."
        )
    return result.stdout.strip()


def nix(*args):
    return command("nix", *args)


def prefetch(pin, source=None):
    if pin["url"] != "https://github.com/colony-2/act.git":
        raise ValueError("act source must use the credential-free canonical repository URL")
    if not re.fullmatch(r"[0-9a-f]{40}", pin["rev"]):
        raise ValueError("act source requires a full immutable commit SHA")
    if not re.fullmatch(r"sha256-[A-Za-z0-9+/]{43}=", pin["hash"]):
        raise ValueError("act source requires a SHA256 NAR hash")
    if not source:
        # Normal Git honors the caller's authorized credential helper. Avoid
        # fetcher caches and evaluation-time private flake inputs altogether.
        with tempfile.TemporaryDirectory(prefix="c2ops-act-") as temporary:
            checkout = Path(temporary) / "source"
            command("git", "init", "--quiet", "--template=", "--object-format=sha1", str(checkout))
            git = ("-C", str(checkout), "-c", "core.autocrlf=false", "-c", "core.hooksPath=/dev/null")
            command("git", *git, "fetch", "--quiet", "--depth=1", pin["url"], pin["rev"])
            command("git", *git, "checkout", "--quiet", "--detach", "FETCH_HEAD")
            if command("git", *git, "rev-parse", "HEAD") != pin["rev"]:
                raise ValueError("act source revision mismatch")
            shutil.rmtree(checkout / ".git")
            return prefetch(pin, checkout)
    source = str(Path(source).resolve(strict=True))
    actual = nix("hash", "path", source)
    if actual != pin["hash"]:
        raise ValueError(f"act source hash mismatch: expected {pin['hash']}, got {actual}")
    imported = nix("store", "add", "--mode", "nar", "--name", "act-source", source)
    return {"revision": pin["rev"], "hash": actual, "store_path": imported}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pin", type=Path, default=ROOT / "nix/act-source.json")
    parser.add_argument("--source", type=Path, help="Already acquired tree to verify and import")
    args = parser.parse_args()
    try:
        print(json.dumps(prefetch(json.loads(args.pin.read_text()), args.source), indent=2))
    except (ValueError, OSError, KeyError) as error:
        print(f"act prefetch: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
