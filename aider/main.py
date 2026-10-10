#!/usr/bin/env python3

import json
import os
import subprocess
import sys
import uuid
from pathlib import Path
from typing import Any


AIDER_REFERENCE = "github:colony-2/c2ops/main#aider-cli"


def aider_command() -> list[str]:
    # The Nix wrapper binds the packaged CLI directly. Source ops use c2j's
    # qualified dispatch for the same manifest-declared derivation.
    binary = os.environ.get("C2OPS_AIDER_BIN")
    return [binary] if binary else ["nix", "run", AIDER_REFERENCE, "--"]


def validate_input(payload: dict[str, Any]) -> None:
    if not str(payload.get("prompt", "")).strip():
        raise RuntimeError("prompt is required")
    if not str(payload.get("worktree_path", "")).strip():
        raise RuntimeError("worktree_path is required")


def ensure_directory(path: Path) -> None:
    path.mkdir(parents=True, exist_ok=True)


def is_git_repo(path: Path) -> bool:
    return (path / ".git").exists()


def build_prompt(payload: dict[str, Any]) -> str:
    parts: list[str] = []
    skill = str(payload.get("skill", "")).strip()
    if skill:
        parts.append(f"Requested skill: {skill}")
    skills = payload.get("skills") or []
    if skills:
        parts.append(f"Available skills: {', '.join(str(item) for item in skills)}")
    parts.append(str(payload["prompt"]).strip())
    return "\n\n".join(part for part in parts if part)


def summarize_output(stdout_text: str, stderr_text: str) -> str:
    lines = [line.strip() for line in stdout_text.splitlines() if line.strip()]
    if lines:
        return "\n".join(lines[-8:])
    stderr_text = stderr_text.strip()
    if stderr_text:
        return stderr_text
    return "aider completed"


def write_stdout_jsonl(path: Path, stdout_text: str) -> None:
    with path.open("w", encoding="utf-8") as handle:
        for line in stdout_text.splitlines():
            if not line.strip():
                continue
            json.dump({"type": "stdout", "content": line}, handle)
            handle.write("\n")


def build_output(payload: dict[str, Any], session_id: str, summary: str, status: str) -> dict[str, Any]:
    is_error = status == "error"
    return {
        "status": status,
        "sessionId": session_id,
        "assistantSummary": summary,
        "incompleteReason": summary if is_error else "",
        "incompleteCategory": "aider_error" if is_error else "",
        "pendingDependencies": [],
        "skills_installed": [],
        "outcome": {
            "summary": {
                "human": summary,
                "reason": "aider execution failed" if is_error else "aider completed successfully",
            },
            "skill": {
                "executed": "aider",
                "selectionMode": str(payload.get("skill_selection_mode", "") or ""),
            },
            "checkpoint": {
                "status": status,
                "scope": "aider",
                "stack": [],
                "returnTriggered": False,
                "contractErrors": [],
            },
            "routing": {},
        },
    }


def main() -> int:
    payload = json.load(sys.stdin)
    validate_input(payload)

    worktree = Path(str(payload.get("worktree_path"))).resolve()
    workdir_value = str(payload.get("workdir_path") or worktree)
    workdir = Path(workdir_value).resolve()
    outbox_value = str(payload.get("artifact_outbox_path") or (workdir / "outbox"))
    outbox = Path(outbox_value).resolve()
    ensure_directory(outbox)

    session_id = str(payload.get("sessionId") or uuid.uuid4())
    session_dir = workdir / ".aider-op" / "sessions" / session_id
    ensure_directory(session_dir)

    prompt_file = session_dir / "prompt.txt"
    prompt_file.write_text(build_prompt(payload), encoding="utf-8")

    args = aider_command() + [
        "--message-file",
        str(prompt_file),
        "--exit",
        "--chat-history-file",
        str(session_dir / "chat.history.md"),
        "--input-history-file",
        str(session_dir / "input.history"),
        "--llm-history-file",
        str(session_dir / "llm.history.jsonl"),
        "--yes-always",
        "--no-stream",
        "--no-pretty",
        "--no-auto-commits",
        "--no-dirty-commits",
        "--no-watch-files",
        "--no-check-update",
        "--analytics-disable",
        "--no-show-model-warnings",
        "--no-fancy-input",
        "--map-tokens",
        "0",
        "--skip-sanity-check-repo",
    ]
    model = str(payload.get("model", "")).strip()
    if model:
        args.extend(["--model", model])
    if payload.get("sessionId"):
        args.append("--restore-chat-history")
    else:
        args.append("--no-restore-chat-history")
    if not is_git_repo(worktree):
        args.append("--no-git")

    env = dict(os.environ)
    env.update({str(key): str(value) for key, value in (payload.get("env") or {}).items()})

    completed = subprocess.run(
        args,
        cwd=str(worktree),
        env=env,
        check=False,
        capture_output=True,
        text=True,
    )

    stdout_text = completed.stdout or ""
    stderr_text = completed.stderr or ""
    write_stdout_jsonl(outbox / "stdout.jsonl", stdout_text)
    (outbox / "stderr.txt").write_text(stderr_text, encoding="utf-8")

    summary = summarize_output(stdout_text, stderr_text)
    status = "completed" if completed.returncode == 0 else "error"
    output = build_output(payload, session_id, summary, status)
    json.dump({"output": output}, sys.stdout)
    sys.stdout.write("\n")

    if completed.returncode != 0:
        print(f"aider exited with status {completed.returncode}", file=sys.stderr)
        if stderr_text.strip():
            print(stderr_text.rstrip(), file=sys.stderr)
        return completed.returncode
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(str(exc), file=sys.stderr)
        raise
