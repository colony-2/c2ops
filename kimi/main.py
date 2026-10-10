#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = []
# ///
"""Run Kimi Code's noninteractive CLI behind the extension op contract."""

import json
import os
import re
import signal
import subprocess
import sys
from pathlib import Path


# Must match the declared CLI dependency in op.yaml/op.json.
KIMI_PACKAGE = "@moonshot-ai/kimi-code@2.1.1"


def execute(args, worktree, env, timeout):
    # A coding CLI can spawn shells. Stop the entire process group on timeout.
    with subprocess.Popen(args, stdin=subprocess.DEVNULL, cwd=worktree, env=env,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                          text=True, start_new_session=(os.name == "posix")) as proc:
        try:
            stdout, stderr = proc.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            if os.name == "posix":
                try:
                    os.killpg(proc.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            else:
                proc.kill()
            stdout, stderr = proc.communicate()
            raise subprocess.TimeoutExpired(args, timeout, output=stdout, stderr=stderr)
    return subprocess.CompletedProcess(args, proc.returncode, stdout, stderr)


def run(payload):
    if not isinstance(payload, dict):
        raise ValueError("input must be an object")
    prompt = payload.get("prompt")
    if not isinstance(prompt, str) or not prompt.strip():
        raise ValueError("prompt is required")
    worktree_value = payload.get("worktree_path")
    if not isinstance(worktree_value, str) or not worktree_value.strip():
        raise ValueError("worktree_path is required")
    worktree = Path(worktree_value).resolve(strict=True)
    if not worktree.is_dir():
        raise ValueError("worktree_path must be a directory")
    session = payload.get("sessionId") or ""
    if not isinstance(session, str) or (session and not re.fullmatch(r"[a-zA-Z0-9_-]{1,128}", session)):
        raise ValueError("sessionId must contain only letters, numbers, underscores, or hyphens")
    workdir = Path(payload.get("workdir_path") or worktree).resolve()
    outbox = Path(payload.get("artifact_outbox_path") or workdir / "outbox").resolve()
    outbox.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ)
    extra_env = payload.get("env") or {}
    if not isinstance(extra_env, dict) or any(not isinstance(v, str) for v in extra_env.values()):
        raise ValueError("env must be an object of strings")
    env.update(extra_env)
    env.setdefault("KIMI_CODE_HOME", str(workdir / ".kimi-op"))
    env.setdefault("KIMI_DISABLE_TELEMETRY", "1")
    args = ["pnpm", "--package=" + KIMI_PACKAGE, "dlx", "kimi",
            "--prompt", prompt, "--output-format", "stream-json"]
    if session:
        args.extend(["--session", session])
    if payload.get("model"):
        args.extend(["--model", str(payload["model"])])
    for directory in payload.get("skills_dirs") or []:
        args.extend(["--skills-dir", str(Path(directory).resolve())])
    timeout = payload.get("timeout_seconds", 1740)
    if type(timeout) not in (int, float) or not 0 < timeout < 1800:
        raise ValueError("timeout_seconds must be between 0 and 1800")
    code = 1
    stdout = stderr = ""
    try:
        proc = execute(args, worktree, env, timeout)
        code, stdout, stderr = proc.returncode, proc.stdout, proc.stderr
    except subprocess.TimeoutExpired as exc:
        stdout = exc.stdout or b""
        stderr = exc.stderr or b""
        stdout = stdout.decode(errors="replace") if isinstance(stdout, bytes) else stdout
        stderr = stderr.decode(errors="replace") if isinstance(stderr, bytes) else stderr
        stderr += "\nkimi execution timed out"
        code = 124
    except OSError as exc:
        stderr = str(exc)
    (outbox / "stdout.jsonl").write_text(stdout, encoding="utf-8")
    summary = ""
    try:
        for line in stdout.splitlines():
            if not line.strip():
                continue
            message = json.loads(line)
            if not isinstance(message, dict):
                raise ValueError("expected JSON message object")
            if message.get("type") == "session.resume_hint":
                returned_session = message.get("session_id")
                if not isinstance(returned_session, str) or not re.fullmatch(r"[a-zA-Z0-9_-]{1,128}", returned_session):
                    raise ValueError("invalid session ID")
                session = returned_session
            if message.get("role") == "assistant":
                content = message.get("content", "")
                if isinstance(content, list):
                    content = "\n".join(part.get("text", "") for part in content
                                        if isinstance(part, dict) and part.get("type") == "text")
                if isinstance(content, str) and content.strip():
                    summary = content.strip()
    except (ValueError, TypeError):
        code = code or 1
        stderr += "\ninvalid Kimi JSON output"
    if code == 0 and not summary:
        code = 1
        stderr += "\nKimi returned no assistant message"
    if code == 0 and not session:
        code = 1
        stderr += "\nKimi returned no session ID"
    (outbox / "stderr.txt").write_text(stderr, encoding="utf-8")
    status = "completed" if code == 0 else "error"
    reason = stderr.strip() or (f"kimi exited with status {code}" if code else "")
    output = {"status": status, "sessionId": session, "assistantSummary": summary,
              "incompleteReason": reason if code else "",
              "incompleteCategory": "kimi_error" if code else "",
              "pendingDependencies": [], "skills_installed": [], "exitCode": code}
    return output, code


def main():
    try:
        output, code = run(json.load(sys.stdin))
        print(json.dumps({"output": output}))
        if code:
            print(output["incompleteReason"], file=sys.stderr)
        return code if code >= 0 else 1
    except Exception as exc:
        print(str(exc), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
