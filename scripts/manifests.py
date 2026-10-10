#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = ["pyyaml==6.0.3"]
# ///
"""Generate the evaluation-time Nix manifests from the source op schemas."""

import argparse
import json
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    catalog = json.loads((ROOT / "nix/ops.json").read_text())
    discovered = {str(p.parent.relative_to(ROOT)) for p in ROOT.glob("**/op.yaml")}
    assert discovered == {op["path"] for op in catalog.values()}, "op catalog is incomplete"
    stale = []
    for name, op in catalog.items():
        source = ROOT / op["path"] / "op.yaml"
        manifest = yaml.safe_load(source.read_text())
        for key in ("input_schema", "output_schema"):
            assert isinstance(manifest[key], dict), f"{source}: missing {key}"
        manifest.pop("run", None)
        manifest.pop("shell", None)
        manifest["command"] = [f"bin/{name}"]
        # Nix runtimes are bound by the package wrapper, never declared twice.
        manifest["dependencies"] = [
            ref for ref in manifest.get("dependencies", []) if not ref.startswith("nix:")
        ]
        if not manifest["dependencies"]:
            del manifest["dependencies"]
        target = source.with_name("op.json")
        content = json.dumps(manifest, indent=2, ensure_ascii=False) + "\n"
        if args.check:
            if not target.exists() or target.read_text() != content:
                stale.append(str(target.relative_to(ROOT)))
        else:
            target.write_text(content)
    if stale:
        parser.exit(1, "Stale manifests (run make manifests): " + ", ".join(stale) + "\n")


if __name__ == "__main__":
    main()
