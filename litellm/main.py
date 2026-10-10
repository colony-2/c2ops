#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = [
#   "litellm==1.104.2",
# ]
# ///

import json
import sys
from typing import Any

from litellm import completion


def qualify_model(provider: str, model: str) -> str:
    provider = (provider or "").strip()
    model = (model or "").strip()
    if ":" in model or "/" in model:
        return model
    if not provider:
        return model
    return f"{provider}/{model}"


def extract_content(choice: Any) -> str:
    message = getattr(choice, "message", None)
    if message is None and isinstance(choice, dict):
        message = choice.get("message", {})

    if hasattr(message, "content"):
        content = message.content
    elif isinstance(message, dict):
        content = message.get("content")
    else:
        content = message

    if isinstance(content, list):
        parts: list[str] = []
        for item in content:
            if isinstance(item, dict):
                text = item.get("text")
                if isinstance(text, str):
                    parts.append(text)
            elif isinstance(item, str):
                parts.append(item)
        return "".join(parts)

    if content is None:
        return ""
    return str(content)


def extract_usage(raw_usage: Any) -> dict[str, int]:
    if raw_usage is None:
        return {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
    if hasattr(raw_usage, "model_dump"):
        raw_usage = raw_usage.model_dump()
    prompt_tokens = int(getattr(raw_usage, "prompt_tokens", raw_usage.get("prompt_tokens", 0)) or 0)
    completion_tokens = int(getattr(raw_usage, "completion_tokens", raw_usage.get("completion_tokens", 0)) or 0)
    total_tokens = int(getattr(raw_usage, "total_tokens", raw_usage.get("total_tokens", 0)) or 0)
    if total_tokens == 0:
        total_tokens = prompt_tokens + completion_tokens
    return {
        "prompt_tokens": prompt_tokens,
        "completion_tokens": completion_tokens,
        "total_tokens": total_tokens,
    }


def request(payload: dict[str, Any], prompt: str, use_schema_hint: bool) -> Any:
    messages: list[dict[str, Any]] = []
    system_prompt = payload.get("system_prompt")
    if system_prompt:
        messages.append({"role": "system", "content": system_prompt})
    messages.append({"role": "user", "content": prompt})

    kwargs: dict[str, Any] = {
        "model": qualify_model(payload.get("provider", ""), payload.get("model", "")),
        "messages": messages,
    }

    temperature = payload.get("temperature")
    if temperature is not None:
        kwargs["temperature"] = temperature
    max_tokens = int(payload.get("max_tokens") or 0)
    if max_tokens > 0:
        kwargs["max_tokens"] = max_tokens
    top_p = payload.get("top_p")
    if top_p is not None:
        kwargs["top_p"] = top_p
    stop_sequences = payload.get("stop_sequences") or []
    if stop_sequences:
        kwargs["stop"] = stop_sequences

    response_schema = payload.get("response_schema")
    if use_schema_hint and response_schema:
        kwargs["response_format"] = {
            "type": "json_schema",
            "json_schema": {
                "name": "response",
                "schema": response_schema,
            },
        }

    return completion(**kwargs)


def decode_response_content(payload: dict[str, Any], content: str) -> Any:
    if not payload.get("response_schema"):
        return content

    parsed = json.loads(content)
    if isinstance(parsed, str):
        stripped = parsed.strip()
        if stripped.startswith("{") or stripped.startswith("["):
            parsed = json.loads(stripped)
    return parsed


def validate_input(payload: dict[str, Any]) -> None:
    if not str(payload.get("provider", "")).strip():
        raise RuntimeError("provider is required")
    if not str(payload.get("model", "")).strip():
        raise RuntimeError("model is required")
    if not str(payload.get("prompt", "")).strip():
        raise RuntimeError("prompt is required")


def main() -> int:
    payload = json.load(sys.stdin)
    validate_input(payload)

    prompt = payload.get("prompt", "")
    try:
        response = request(payload, prompt, use_schema_hint=True)
    except Exception:
        if payload.get("response_schema"):
            schema_prompt = (
                f"{prompt}\n\n"
                "Return only JSON that matches this schema:\n"
                f"{json.dumps(payload['response_schema'], separators=(',', ':'))}"
            )
            response = request(payload, schema_prompt, use_schema_hint=False)
        else:
            raise

    choices = getattr(response, "choices", None)
    if choices is None and isinstance(response, dict):
        choices = response.get("choices", [])
    if not choices:
        raise RuntimeError("model returned no choices")

    choice = choices[0]
    finish_reason = getattr(choice, "finish_reason", None)
    if finish_reason is None and isinstance(choice, dict):
        finish_reason = choice.get("finish_reason")

    content = extract_content(choice)
    output = {
        "response": decode_response_content(payload, content),
        "model": getattr(response, "model", None)
        or (response.get("model") if isinstance(response, dict) else None)
        or qualify_model(payload.get("provider", ""), payload.get("model", "")),
        "finish_reason": finish_reason or "stop",
        "usage": extract_usage(
            getattr(response, "usage", None) or (response.get("usage") if isinstance(response, dict) else None)
        ),
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
