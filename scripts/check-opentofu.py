#!/usr/bin/env python3
"""Run real Google clients and installed/native success, recovery and replay fixtures."""
import os
from pathlib import Path
import subprocess
import tempfile
import sys

ROOT = Path(__file__).resolve().parents[1]
# The installed/runtime fixture measured 370s under the verifier's two-core
# isolation. Give it its own process instead of sharing its 8m alarm with all
# Google renewal and lock fixtures. The complement includes every future test.
PARTITIONS = (('-run', '^TestNativePackagedRecovery$'),
              ('-skip', '^TestNativePackagedRecovery$'))

def realize(expression):
    return subprocess.check_output([
        "nix", "build", "--max-jobs", "2", "--no-link", "--print-out-paths", "--impure", "--expr", expression
    ], cwd=ROOT, text=True).strip()

def qualify(env, extra=()):
    failed = False
    for selection in PARTITIONS:
        result = subprocess.run(["go", "test", "-count=1", "-timeout=8m", "-v", *selection,
                                 "./...", *extra], cwd=ROOT / "opentofu-up", env=env)
        failed |= result.returncode != 0
    result = subprocess.run(["go", "test", "-count=1", "-timeout=2m", "./..."],
                            cwd=ROOT / "opentofu-runtime-fixture", env=env)
    return 1 if failed or result.returncode else 0


def main():
    pkgs = "(builtins.getFlake (toString ./.)).inputs.nixpkgs.legacyPackages.${builtins.currentSystem}"
    tofu = realize(f"let pkgs = {pkgs}; in pkgs.opentofu")
    provider = realize(f"import ./nix/opentofu-google-fixture.nix {{ pkgs = {pkgs}; }}")
    env = dict(os.environ, C2OPS_TEST_TOFU=f"{tofu}/bin/tofu", C2OPS_TEST_GOOGLE_ZIP=provider,
               C2OPS_TEST_NATIVE="1")
    # Prepare locked downloads over HTTP/1.1 without changing the transports
    # exercised by test binaries or either Google client.
    download_env = dict(env)
    download_env["GODEBUG"] = ",".join(filter(None, [env.get("GODEBUG"), "http2client=0"]))
    for module in ("opentofu-up", "opentofu-runtime-fixture", "opentofu-runtime-fixture/emulator"):
        print(f"Preparing {module} Go modules over HTTP/1.1 (120s limit)", flush=True)
        subprocess.run(["go", "mod", "download"], cwd=ROOT / module,
                       env=download_env, check=True, timeout=120)
    with tempfile.TemporaryDirectory(prefix="opentofu-emulator-") as tmp:
        emulator = str(Path(tmp) / "gcs-emulator")
        subprocess.run(["go", "build", "-mod=readonly", "-o", emulator, "."],
                       cwd=ROOT / "opentofu-runtime-fixture/emulator", env=env, check=True, timeout=480)
        env["C2OPS_TEST_GCS_EMULATOR"] = emulator
        return qualify(env, sys.argv[1:])


if __name__ == '__main__':
    raise SystemExit(main())
