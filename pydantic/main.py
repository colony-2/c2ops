#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = [
#   "pydantic-ai-slim[anthropic,google,openai]==2.31.0",
#   "anthropic==0.108.0",
#   "openai==2.53.0",
#   "google-genai==2.16.0",
# ]
# ///

import base64
import fnmatch
import json
import os
import sys
import time
from pathlib import Path
from typing import Any

from pydantic_ai import Agent, DeferredToolRequests, ToolReturn
from pydantic_ai.output import StructuredDict
from pydantic_ai.toolsets import ExternalToolset
from pydantic_ai.tools import DeferredToolResults, ToolDefinition


TEXT_FILE_TYPES = {"txt", "code", "config", "markdown"}
DEFAULT_RESTRICTED_PATHS = [
    "/etc",
    "/usr",
    "/bin",
    "/sbin",
    "/proc",
    "/sys",
    "/dev",
    "~/.ssh",
    "~/.aws",
    "~/.kube",
    "~/.config",
]
SENSITIVE_PREFIXES = [
    "/etc/passwd",
    "/etc/shadow",
    "/etc/sudoers",
    "/root",
    "/var/log/secure",
]
SENSITIVE_FILES = [
    ".ssh/id_rsa",
    ".ssh/id_dsa",
    ".ssh/id_ecdsa",
    ".ssh/id_ed25519",
    ".aws/credentials",
    ".kube/config",
    ".docker/config.json",
    ".npmrc",
    ".pypirc",
    ".gitconfig",
    ".git-credentials",
]


def normalize_jsonish(value: Any) -> Any:
    if isinstance(value, str):
        stripped = value.strip()
        if stripped.startswith("{") or stripped.startswith("["):
            return json.loads(stripped)
    return value


def model_name(provider: str, model: str) -> str:
    provider = (provider or "").strip()
    model = (model or "").strip()
    # PydanticAI 2 defaults `openai:` to Responses. Preserve this op's
    # Chat Completions contract, including OpenAI-compatible endpoints.
    if model.startswith("openai:"):
        return "openai-chat:" + model.removeprefix("openai:")
    if model.startswith("google-gla:"):
        return "google:" + model.removeprefix("google-gla:")
    if ":" in model:
        return model
    if provider == "openai":
        return f"openai-chat:{model}"
    if provider == "gemini":
        return f"google:{model}"
    return f"{provider}:{model}"


def set_api_keys(api_keys: dict[str, str]) -> None:
    for provider, value in api_keys.items():
        if not value:
            continue
        if provider == "openai":
            os.environ["OPENAI_API_KEY"] = value
        elif provider == "anthropic":
            os.environ["ANTHROPIC_API_KEY"] = value
        elif provider == "gemini":
            os.environ["GEMINI_API_KEY"] = value
            os.environ.setdefault("GOOGLE_API_KEY", value)


def expand_path(path: str) -> str:
    if path.startswith("~/"):
        return str(Path.home() / path[2:])
    return path


class Sandbox:
    def __init__(self, allowed_paths: list[str], restricted_paths: list[str]):
        self.allowed_paths = [str(Path(expand_path(path)).resolve()) for path in allowed_paths]
        merged_restricted = list(restricted_paths)
        for path in DEFAULT_RESTRICTED_PATHS:
            if path not in merged_restricted:
                merged_restricted.append(path)
        self.restricted_paths = [str(Path(expand_path(path)).resolve()) for path in merged_restricted]

    def validate(self, path: Path) -> None:
        clean = str(path)
        if ".." in clean:
            raise RuntimeError(f"path contains parent directory traversal: {clean}")
        for prefix in SENSITIVE_PREFIXES:
            if clean.startswith(prefix):
                raise RuntimeError(f"path points to sensitive location: {clean}")
        for fragment in SENSITIVE_FILES:
            if fragment in clean:
                raise RuntimeError(f"path contains sensitive file pattern: {fragment}")

        resolved = str(path.resolve())
        for restricted in self.restricted_paths:
            if resolved.startswith(restricted):
                raise RuntimeError(f"path '{clean}' is in restricted area '{restricted}'")
        if self.allowed_paths and not any(resolved.startswith(allowed) for allowed in self.allowed_paths):
            raise RuntimeError(f"path '{clean}' is not in allowed paths")


def decode_file_content(raw: Any) -> bytes:
    if raw in (None, ""):
        return b""
    if isinstance(raw, list):
        return bytes(raw)
    if isinstance(raw, str):
        try:
            return base64.b64decode(raw, validate=True)
        except Exception:
            return raw.encode()
    raise RuntimeError("unsupported file content encoding")


def build_file_context(files: list[dict[str, Any]]) -> str:
    if not files:
        return ""
    parts = ["### File Context ###", ""]
    for file_info in files:
        path = file_info.get("path") or file_info.get("name") or "unnamed"
        file_type = file_info.get("type") or "unknown"
        content = decode_file_content(file_info.get("content"))
        parts.append(f"**File: {path}** (Type: {file_type}, Size: {len(content)} bytes)")
        if file_type in TEXT_FILE_TYPES:
            text = content.decode("utf-8", errors="replace")
            if len(text) > 1000:
                text = text[:1000] + "...\n[Content truncated]"
            parts.append("```")
            parts.append(text)
            parts.append("```")
        else:
            parts.append(f"[{file_type} file - content not displayed]")
        parts.append("")
    return "\n".join(parts)


def tool_definitions(tools: list[dict[str, Any]]) -> list[ToolDefinition]:
    output: list[ToolDefinition] = []
    for tool in tools:
        output.append(
            ToolDefinition(
                name=tool["name"],
                description=tool.get("description"),
                parameters_json_schema=normalize_jsonish(tool.get("parameters"))
                or {"type": "object", "properties": {}},
            )
        )
    return output


def model_settings(payload: dict[str, Any]) -> dict[str, Any]:
    settings: dict[str, Any] = {}
    if payload.get("temperature") is not None:
        settings["temperature"] = payload["temperature"]
    if int(payload.get("max_tokens") or 0) > 0:
        settings["max_tokens"] = int(payload["max_tokens"])
    if payload.get("top_p") is not None:
        settings["top_p"] = payload["top_p"]
    if payload.get("stop_sequences"):
        settings["stop_sequences"] = payload["stop_sequences"]
    return settings


def output_type(payload: dict[str, Any], include_deferred: bool) -> Any:
    schema = normalize_jsonish(payload.get("response_schema"))
    base_type: Any = StructuredDict(schema, name="response") if schema else str
    if include_deferred:
        return [base_type, DeferredToolRequests]
    return base_type


def usage_dict(usage: Any) -> dict[str, int]:
    prompt_tokens = int(getattr(usage, "input_tokens", 0) or 0)
    completion_tokens = int(getattr(usage, "output_tokens", 0) or 0)
    return {
        "prompt_tokens": prompt_tokens,
        "completion_tokens": completion_tokens,
        "total_tokens": int(getattr(usage, "total_tokens", 0) or (prompt_tokens + completion_tokens)),
    }


def serialize_output_value(value: Any) -> str:
    if isinstance(value, str):
        return value
    return json.dumps(value, separators=(",", ":"))


def make_jsonable(value: Any) -> Any:
    if value is None or isinstance(value, (str, int, float, bool)):
        return value
    if isinstance(value, Path):
        return str(value)
    if isinstance(value, dict):
        return {str(key): make_jsonable(item) for key, item in value.items()}
    if isinstance(value, (list, tuple, set)):
        return [make_jsonable(item) for item in value]
    if hasattr(value, "model_dump"):
        return make_jsonable(value.model_dump())
    if hasattr(value, "isoformat"):
        try:
            return value.isoformat()
        except Exception:
            pass
    if hasattr(value, "__dict__"):
        return make_jsonable(vars(value))
    return str(value)


def parse_duration(duration: str) -> float | None:
    text = (duration or "").strip()
    if not text:
        return None
    suffixes = {"ms": 0.001, "s": 1.0, "m": 60.0, "h": 3600.0}
    for suffix, multiplier in suffixes.items():
        if text.endswith(suffix):
            return float(text[: -len(suffix)]) * multiplier
    return float(text)


def resolve_path(base_dir: Path, sandbox: Sandbox | None, path_value: str) -> Path:
    full_path = (base_dir / path_value).resolve()
    if sandbox is not None:
        sandbox.validate(full_path)
    return full_path


def execute_tool(
    tool_name: str,
    arguments: dict[str, Any],
    base_dir: Path,
    sandbox: Sandbox | None,
    files_written: list[str],
    files_read: list[str],
    files_deleted: list[str],
) -> Any:
    if tool_name == "write_file":
        path_value = arguments.get("path")
        content = arguments.get("content")
        if not isinstance(path_value, str) or not isinstance(content, str):
            raise RuntimeError("write_file requires string path and content")
        full_path = resolve_path(base_dir, sandbox, path_value)
        if arguments.get("create_dirs", True):
            full_path.parent.mkdir(parents=True, exist_ok=True)
        full_path.write_text(content)
        files_written.append(path_value)
        return {"path": path_value, "bytes": len(content), "success": True}

    if tool_name == "read_file":
        path_value = arguments.get("path")
        if not isinstance(path_value, str):
            raise RuntimeError("read_file requires string path")
        full_path = resolve_path(base_dir, sandbox, path_value)
        content = full_path.read_text()
        files_read.append(path_value)
        return {"path": path_value, "content": content, "size": len(content), "success": True}

    if tool_name == "delete_file":
        path_value = arguments.get("path")
        if not isinstance(path_value, str):
            raise RuntimeError("delete_file requires string path")
        full_path = resolve_path(base_dir, sandbox, path_value)
        full_path.unlink()
        files_deleted.append(path_value)
        return {"path": path_value, "deleted": True, "success": True}

    if tool_name == "list_files":
        path_value = arguments.get("path", ".")
        if not isinstance(path_value, str):
            raise RuntimeError("list_files path must be a string")
        pattern = arguments.get("pattern", "")
        recursive = bool(arguments.get("recursive", False))
        full_path = resolve_path(base_dir, sandbox, path_value)
        if not full_path.is_dir():
            raise RuntimeError(f"path is not a directory: {path_value}")
        files: list[dict[str, Any]] = []
        if recursive:
            for root, dirnames, filenames in os.walk(full_path):
                for name in list(dirnames) + list(filenames):
                    candidate = Path(root) / name
                    rel = candidate.relative_to(full_path)
                    if pattern and not fnmatch.fnmatch(name, pattern):
                        continue
                    stat = candidate.stat()
                    files.append(
                        {
                            "name": name,
                            "path": str(rel),
                            "size": stat.st_size,
                            "is_dir": candidate.is_dir(),
                            "modified": int(stat.st_mtime),
                        }
                    )
        else:
            for candidate in sorted(full_path.iterdir()):
                if pattern and not fnmatch.fnmatch(candidate.name, pattern):
                    continue
                stat = candidate.stat()
                files.append(
                    {
                        "name": candidate.name,
                        "path": candidate.name,
                        "size": stat.st_size,
                        "is_dir": candidate.is_dir(),
                        "modified": int(stat.st_mtime),
                    }
                )
        return {"path": path_value, "files": files, "count": len(files), "success": True}

    if tool_name == "create_directory":
        path_value = arguments.get("path")
        if not isinstance(path_value, str):
            raise RuntimeError("create_directory requires string path")
        full_path = resolve_path(base_dir, sandbox, path_value)
        full_path.mkdir(parents=True, exist_ok=True)
        return {"path": path_value, "created": True, "success": True}

    raise RuntimeError(f"unknown tool: {tool_name}")


def dedupe(items: list[str]) -> list[str]:
    seen: set[str] = set()
    output: list[str] = []
    for item in items:
        if item not in seen:
            seen.add(item)
            output.append(item)
    return output


def validate_input(payload: dict[str, Any]) -> None:
    if not str(payload.get("default_provider", "")).strip():
        raise RuntimeError("default_provider is required")
    if not str(payload.get("default_model", "")).strip():
        raise RuntimeError("default_model is required")

    prompt = str(payload.get("prompt", "") or "").strip()
    files = payload.get("files") or []
    if not prompt and not files:
        raise RuntimeError("prompt or files required")

    total_file_size = 0
    for file_info in files:
        total_file_size += len(decode_file_content(file_info.get("content")))
    max_file_context_size = int(payload.get("max_file_context_size") or 0)
    if max_file_context_size > 0 and total_file_size > max_file_context_size:
        raise RuntimeError(f"total file size {total_file_size} exceeds limit {max_file_context_size}")

    if payload.get("execute_tools"):
        if not payload.get("enable_tool_execution"):
            raise RuntimeError("tool execution is disabled in configuration")
        tools = payload.get("tools") or []
        if not tools:
            raise RuntimeError("tools are required when execute_tools is true")
        for tool in tools:
            if not str(tool.get("name", "")).strip():
                raise RuntimeError("tool name is required")
            if not str(tool.get("description", "")).strip():
                raise RuntimeError(f"tool {tool.get('name', '')} description is required")
            parameters = normalize_jsonish(tool.get("parameters"))
            if not parameters:
                raise RuntimeError(f"tool {tool.get('name', '')} parameters are required")


def main() -> int:
    started = time.time()
    payload = json.load(sys.stdin)
    validate_input(payload)
    set_api_keys(payload.get("api_keys") or {})

    prompt = str(payload.get("prompt") or "")
    file_context = build_file_context(payload.get("files") or [])
    if file_context:
        prompt = f"{prompt}\n\n{file_context}" if prompt else file_context

    tools = payload.get("tools") or []
    include_deferred = bool(tools)
    agent = Agent(
        model_name(payload.get("default_provider", ""), payload.get("default_model", "")),
        output_type=output_type(payload, include_deferred),
        system_prompt=payload.get("system_prompt") or (),
        model_settings=model_settings(payload),
        toolsets=[ExternalToolset(tool_definitions(tools))] if tools else None,
        tool_timeout=parse_duration(payload.get("tool_timeout", "")),
        metadata=payload.get("metadata"),
    )

    sandbox = None
    if payload.get("enable_sandbox"):
        sandbox = Sandbox(payload.get("allowed_paths") or [], payload.get("restricted_paths") or [])
    working_dir = Path(payload.get("tool_working_dir") or payload.get("default_working_dir") or os.getcwd()).resolve()

    files_written: list[str] = []
    files_read: list[str] = []
    files_deleted: list[str] = []
    tool_calls: list[dict[str, Any]] = []
    tool_results: list[dict[str, Any]] = []
    tool_execution_errors: list[str] = []
    usage_totals = {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
    final_result: Any = None
    finish_reason = "stop"
    resolved_model = model_name(payload.get("default_provider", ""), payload.get("default_model", ""))
    provider_metadata: dict[str, Any] | None = None
    message_history = None
    deferred_results = None
    rounds_used = 0
    max_tool_rounds = int(payload.get("max_tool_rounds") or 0)

    while True:
        result = agent.run_sync(
            prompt if message_history is None else None,
            message_history=message_history,
            deferred_tool_results=deferred_results,
            metadata=payload.get("metadata"),
        )
        usage = usage_dict(result.usage)
        for key, value in usage.items():
            usage_totals[key] += value
        if result.response.model_name:
            resolved_model = result.response.model_name
        finish_reason = result.response.finish_reason or finish_reason
        provider_metadata = result.response.provider_details

        if isinstance(result.output, DeferredToolRequests):
            for call in result.output.calls:
                tool_calls.append(
                    {
                        "id": call.tool_call_id,
                        "name": call.tool_name,
                        "arguments": call.args_as_dict(),
                        "timestamp": int(time.time()),
                    }
                )

            if not payload.get("execute_tools"):
                final_result = ""
                finish_reason = "tool_call"
                break

            rounds_used += 1
            if rounds_used > max_tool_rounds:
                tool_execution_errors.append("max_tool_rounds exhausted before final response")
                final_result = ""
                finish_reason = "tool_call"
                break

            call_results: dict[str, Any] = {}
            for call in result.output.calls:
                tool_started = time.time()
                args = call.args_as_dict()
                try:
                    executed = execute_tool(
                        call.tool_name,
                        args,
                        working_dir,
                        sandbox,
                        files_written,
                        files_read,
                        files_deleted,
                    )
                    tool_results.append(
                        {
                            "tool_call_id": call.tool_call_id,
                            "tool_name": call.tool_name,
                            "success": True,
                            "result": executed,
                            "duration_ms": int((time.time() - tool_started) * 1000),
                        }
                    )
                    call_results[call.tool_call_id] = ToolReturn(executed)
                except Exception as exc:
                    error_text = str(exc)
                    tool_execution_errors.append(error_text)
                    tool_results.append(
                        {
                            "tool_call_id": call.tool_call_id,
                            "tool_name": call.tool_name,
                            "success": False,
                            "error": error_text,
                            "duration_ms": int((time.time() - tool_started) * 1000),
                        }
                    )
                    if not payload.get("continue_on_tool_error"):
                        raise
                    call_results[call.tool_call_id] = ToolReturn({"success": False, "error": error_text})

            message_history = result.all_messages()
            deferred_results = DeferredToolResults(calls=call_results)
            continue

        final_result = result.output
        break

    output = {
        "response": serialize_output_value(final_result),
        "model": resolved_model,
        "finish_reason": finish_reason,
        "usage": usage_totals,
        "tool_calls": tool_calls,
        "tool_results": tool_results,
        "tool_execution_errors": tool_execution_errors,
        "tool_rounds_used": rounds_used,
        "files_written": dedupe(files_written),
        "files_read": dedupe(files_read),
        "files_deleted": dedupe(files_deleted),
        "execution_time_ms": int((time.time() - started) * 1000),
        "provider_metadata": make_jsonable(provider_metadata or {}),
        "telemetry": {
            "prompt_tokens": usage_totals["prompt_tokens"],
            "completion_tokens": usage_totals["completion_tokens"],
            "total_tokens": usage_totals["total_tokens"],
        },
    }
    json.dump({"output": output}, sys.stdout)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(str(exc), file=sys.stderr)
        raise
