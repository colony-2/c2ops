#!/usr/bin/env python3
"""Minimal caller fixture: retain safe deployment data before enforcing failure."""
import json
import pathlib
import shutil
import sys

request = json.load(sys.stdin)
if len(sys.argv) > 1 and sys.argv[1] == "assert":
    print(json.dumps({"output": {"ok": request.get("accepted") is True}}))
else:
    result = dict(request["result"])
    recovery = request.get("recovery") or result.get("recovery")
    if recovery is not None:
        # Hydration exposes a local ciphertext plus the COMPLETE durable ref.
        result["recovery"] = recovery["ref"]
    target = pathlib.Path(request["artifact_outbox_path"])
    target.mkdir(parents=True, exist_ok=True)
    (target / "deployment.json").write_text(json.dumps(result))
    if recovery is not None:
        shutil.copyfile(recovery["files"]["ciphertext"], target / "recovery.age")
    expected = request["expected"]
    allowed = ("applied", "no_changes") if expected["mode"] == "up" else ("planned", "no_changes")
    accepted = (result.get("ok") is True and result.get("outcome") in allowed
                and all(result.get(k) == expected[k] for k in ("revision", "root", "backend")))
    output = {"accepted": accepted, "result": result}
    if recovery is not None:
        output["recovery"] = recovery["ref"]
    print(json.dumps({"output": output}))
