#!/usr/bin/env python3
"""Exercise real npm Codex releases against the current op and a local mock API.

Version checks, execution, parsing, checkpoint export and restore use production
code. --spoof-version is available only to reproduce historical experiments.
No global CLI installation, source patch, or paid API call is required.
"""

import argparse
import concurrent.futures
import datetime
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[1]
TESTS = [
    "TestLiveExecuteWithConfiguredSkillCreatesMarker",
    "TestLiveExecuteResumesSessionKnowledge",
    "TestCompatibilityRunAndRunSkill",
]


def run(args, **kwargs):
    return subprocess.run(args, text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT, **kwargs)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--versions", nargs="+", help="default: newest patch of the last 20 stable minor releases")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--jobs", type=int, default=3)
    parser.add_argument("--resume-version", help="run only full-op continuation, switching CLI for resume")
    parser.add_argument("--spoof-version", help="explicitly override --version for historical experiments")
    args = parser.parse_args()
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ)
    env.setdefault("npm_config_cache", "/tmp/c2ops-npm-cache")
    registry = run(["npm", "view", "@openai/codex", "dist-tags", "versions", "--json"], env=env, timeout=60)
    if registry.returncode:
        raise RuntimeError(registry.stdout)
    metadata = json.loads(registry.stdout)
    if isinstance(metadata, list):
        metadata = metadata[0]
    latest = metadata["dist-tags"]["latest"]
    versions = args.versions
    if not versions:
        stable = sorted((v for v in metadata["versions"] if re.fullmatch(r"0\.\d+\.\d+", v)),
                        key=lambda v: tuple(map(int, v.split("."))))
        minors = {}
        for version in stable:
            minors[version.rsplit(".", 1)[0]] = version
        versions = list(minors.values())[-20:]
    for version in versions + [v for v in (args.resume_version, args.spoof_version) if v]:
        if not re.fullmatch(r"\d+\.\d+\.\d+(?:-[a-zA-Z0-9.-]+)?", version):
            raise ValueError(f"invalid version: {version}")
    source_commit = run(["git", "rev-parse", "HEAD"], cwd=ROOT, check=True).stdout.strip()
    report = {"date": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "source_commit": source_commit, "latest_stable": latest,
              "platform": run(["uname", "-sm"], check=True).stdout.strip(),
              "method": "real CLI, local mock provider; " + ("--version spoofed as " + args.spoof_version if args.spoof_version else "unaltered version checks and producer metadata"),
              "resume_version": args.resume_version,
              "versions": versions, "results": []}
    with tempfile.TemporaryDirectory(prefix="codex-compat-") as temp:
        test_binary = Path(temp) / "codex.test"
        build = run(["go", "test", "-c", "-o", str(test_binary), "./pkg/codex"], cwd=ROOT, env=env, timeout=180)
        if build.returncode:
            raise RuntimeError(build.stdout)

        def provision(version, directory):
            install = run(["npm", "exec", "--yes", f"--package=@openai/codex@{version}",
                           "--", "sh", "-c", "command -v codex"], env=env, timeout=180)
            (output / f"{version}-install.log").write_text(install.stdout)
            if install.returncode:
                raise RuntimeError("npm provisioning failed; see install log")
            executable = install.stdout.strip().splitlines()[-1]
            actual = run([executable, "--version"], env=env, timeout=20)
            if actual.returncode or actual.stdout.strip() != f"codex-cli {version}":
                raise RuntimeError(f"wrong CLI resolved: {actual.stdout}")
            # Every invocation delegates to the verified CLI by default.
            shim = Path(temp) / directory
            shim.mkdir()
            script = "#!/bin/sh\n"
            if args.spoof_version:
                script += ("if [ \"$#\" -eq 1 ] && [ \"$1\" = --version ]; then\n"
                           f"  printf '%s\\n' {shlex.quote('codex-cli ' + args.spoof_version)}\n  exit 0\nfi\n")
            (shim / "codex").write_text(script + f"exec {shlex.quote(executable)} \"$@\"\n")
            (shim / "codex").chmod(0o755)
            return shim, actual.stdout.strip()

        resume_shim = None
        if args.resume_version:
            resume_shim, report["actual_resume_version"] = provision(args.resume_version, "resume")

        def check(version):
            result = {"version": version, "tests": {}}
            try:
                shim, result["actual_version"] = provision(version, version)
                test_env = dict(env, PATH=str(shim) + os.pathsep + env["PATH"], C2OPS_CODEX_COMPAT="1")
                test_env["C2OPS_CODEX_COMPAT_VERSION"] = args.spoof_version or version
                test_env["C2OPS_CODEX_COMPAT_RESUME_VERSION"] = args.spoof_version or args.resume_version or version
                if resume_shim:
                    test_env["C2OPS_CODEX_COMPAT_RESUME_PATH"] = str(resume_shim)
                else:
                    test_env.pop("C2OPS_CODEX_COMPAT_RESUME_PATH", None)
                # Discard any caller-selected live model for a deterministic mock.
                test_env.pop("C2OPS_CODEX_LIVE_MODEL", None)
                for name in ([TESTS[-1]] if resume_shim else TESTS):
                    test = run([str(test_binary), "-test.v", f"-test.run=^{name}$", "-test.timeout=60s"],
                               cwd=ROOT, env=test_env, timeout=70)
                    (output / f"{version}-{name}.log").write_text(test.stdout)
                    result["tests"][name] = "works" if test.returncode == 0 else "incompat"
                    if test.returncode:
                        result.setdefault("failures", {})[name] = test.stdout
            except (OSError, subprocess.TimeoutExpired, RuntimeError) as error:
                result["error"] = str(error)
            print(version, json.dumps(result["tests"]), result.get("error", ""), flush=True)
            return result

        with concurrent.futures.ThreadPoolExecutor(max_workers=args.jobs) as pool:
            for result in pool.map(check, versions):
                report["results"].append(result)
                (output / "results.json").write_text(json.dumps(report, indent=2) + "\n")


if __name__ == "__main__":
    main()
