#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = [
#   "pydantic-ai-slim[anthropic,google,openai]==2.31.0",
#   "anthropic==0.108.0",
#   "openai==2.53.0",
#   "google-genai==2.16.0",
#   "jsonschema==4.26.0",
# ]
# ///

import asyncio
import json
import copy
import logging
import math
import re
import sys
import time
from urllib.parse import urlsplit

import httpx
import jsonschema
from referencing import Registry
from openai import AsyncOpenAI, AuthenticationError
from pydantic_ai import Agent, ModelRetry
from pydantic_ai.exceptions import UnexpectedModelBehavior, ModelHTTPError
from pydantic_ai.models.openai import OpenAIChatModel
from pydantic_ai.output import StructuredDict
from pydantic_ai.providers.openai import OpenAIProvider
from pydantic_ai.profiles.openai import OpenAIJsonSchemaTransformer
from pydantic import TypeAdapter

# SDK debug logging can include provider bodies. Never enable it for this operation.
for _logger_name in ("openai", "httpx", "httpcore", "pydantic_ai", "pydantic_graph"):
    logging.getLogger(_logger_name).setLevel(logging.CRITICAL + 1)


class SafeError(Exception):
    pass


def binding_string(value):
    if not isinstance(value, str) or not value.strip() or value != value.strip() or any(c.isspace() or ord(c) < 33 or 127 <= ord(c) <= 159 for c in value):
        raise SafeError("validation")
    return value


def api_root(value, *, test_loopback=False):
    value = binding_string(value)
    if "\\" in value or "%" in value or "?" in value or "#" in value:
        raise SafeError("validation")
    try:
        u = urlsplit(value)
        port = u.port
        if u.netloc.endswith(":") or not u.hostname or u.username is not None or u.password is not None or (port is not None and port < 1):
            raise ValueError()
        if u.scheme != "https" and not (test_loopback and u.scheme == "http" and u.hostname in ("127.0.0.1", "::1")):
            raise ValueError()
        if any(part in (".", "..") for part in u.path.split("/")) or "//" in u.path:
            raise ValueError()
        if not re.fullmatch(r"[A-Za-z0-9.\-:\[\]]+", u.netloc):
            raise ValueError()
    except ValueError:
        raise SafeError("validation") from None
    return value.rstrip("/") + "/"


def validate(payload, *, test_loopback=False):
    if not isinstance(payload, dict):
        raise SafeError("validation")
    allowed = {"api", "default_provider", "default_model", "response_schema", "invocation_timeout", "prompt", "system_prompt", "temperature", "max_tokens", "top_p", "stop_sequences", "metadata"}
    if payload.keys() - allowed:
        raise SafeError("validation")
    api = payload.get("api")
    if not isinstance(api, dict) or set(api) != {"base_url", "api_key"}:
        raise SafeError("validation")
    root = api_root(api["base_url"], test_loopback=test_loopback)
    key = binding_string(api["api_key"])
    model = binding_string(payload.get("default_model"))
    if ":" in model or "/" in model or payload.get("default_provider") != "openai":
        raise SafeError("validation")
    timeout = payload.get("invocation_timeout")
    if type(timeout) not in (int, float) or not math.isfinite(timeout) or timeout <= 0:
        raise SafeError("validation")
    for field in ("prompt", "system_prompt"):
        if field in payload and not isinstance(payload[field], str):
            raise SafeError("validation")
    if not payload.get("prompt", "").strip():
        raise SafeError("validation")
    for field in ("temperature", "top_p"):
        if field in payload and (type(payload[field]) not in (int, float) or not math.isfinite(payload[field])):
            raise SafeError("validation")
    if "max_tokens" in payload and (type(payload["max_tokens"]) is not int or payload["max_tokens"] <= 0):
        raise SafeError("validation")
    if "stop_sequences" in payload and (not isinstance(payload["stop_sequences"], list) or any(not isinstance(s, str) for s in payload["stop_sequences"])):
        raise SafeError("validation")
    if "metadata" in payload and not isinstance(payload["metadata"], dict):
        raise SafeError("validation")
    schema = payload.get("response_schema")
    try:
        if not isinstance(schema, dict) or schema.get("type") != "object":
            raise ValueError()
        # Remote references could otherwise trigger unbound requests during validation.
        def refs(node):
            if isinstance(node, dict):
                if any(k in node for k in ("$id", "$dynamicRef", "$recursiveRef")):
                    raise ValueError()
                if "$ref" in node:
                    ref = node["$ref"]
                    if not isinstance(ref, str) or not ref.startswith("#/") or "%" in ref:
                        raise ValueError()
                    target = schema
                    for part in ref[2:].split("/"):
                        target = target[part.replace("~1", "/").replace("~0", "~")]
                    if not isinstance(target, (dict, bool)):
                        raise ValueError()
                for v in node.values(): refs(v)
            elif isinstance(node, list):
                for v in node: refs(v)
        refs(schema)
        jsonschema.Draft202012Validator.check_schema(schema)
        output = StructuredDict(copy.deepcopy(schema))
        OpenAIJsonSchemaTransformer(TypeAdapter(output).json_schema()).walk()
        # Prepare the output machinery before creating any client.
        Agent(output_type=output)
    except Exception:
        raise SafeError("validation") from None
    return root, key, model, timeout, output


def contains_secret(value, secret):
    if isinstance(value, str):
        return secret in value
    if isinstance(value, dict):
        return any(contains_secret(k, secret) or contains_secret(v, secret) for k, v in value.items())
    if isinstance(value, (list, tuple)):
        return any(contains_secret(v, secret) for v in value)
    return False


async def infer(payload, *, test_loopback=False):
    started = time.monotonic()
    root, key, model, timeout, output_type = validate(payload, test_loopback=test_loopback)
    if time.monotonic() >= started + timeout:
        raise SafeError("timeout")
    async def guard(request):
        # httpx has already merged SDK headers: remove the credential until URL validation.
        request.headers.pop("authorization", None)
        if time.monotonic() >= started + timeout:
            raise SafeError("timeout")
        if request.url != httpx.URL(root + "chat/completions"):
            raise SafeError("provider")
        # The SDK imports OPENAI_CUSTOM_HEADERS independently of trust_env.
        # Rebuild headers rather than trying to enumerate ambient header names.
        request.headers.clear()
        request.headers.update({
            "host": request.url.netloc.decode("ascii"),
            "authorization": "Bearer " + key,
            "content-type": "application/json",
            "accept": "application/json",
            "content-length": str(len(request.content)),
        })
    try:
        async with asyncio.timeout_at(started + timeout):
            async with httpx.AsyncClient(trust_env=False, follow_redirects=False, verify=True, event_hooks={"request": [guard]}) as http:
                async with AsyncOpenAI(api_key=key, base_url=root, organization="", project="", http_client=http, max_retries=1, timeout=timeout) as client:
                    agent = Agent(OpenAIChatModel(model, provider=OpenAIProvider(openai_client=client)), output_type=output_type, retries=1, system_prompt=payload.get("system_prompt", ""), model_settings={k: payload[k] for k in ("temperature", "max_tokens", "top_p", "stop_sequences") if k in payload})
                    agent.instrument = False
                    @agent.output_validator
                    def check_result(ctx, result):
                        try:
                            jsonschema.Draft202012Validator(payload["response_schema"], registry=Registry()).validate(result)
                        except Exception:
                            raise ModelRetry("Result does not satisfy the required schema") from None
                        return result
                    result = await agent.run(payload["prompt"])
                    usage = result.usage
                    out = {"response": json.dumps(result.output), "model": model, "usage": {"prompt_tokens": usage.input_tokens, "completion_tokens": usage.output_tokens, "total_tokens": usage.total_tokens}, "provider_metadata": {}, "execution_time_ms": int((time.monotonic()-started)*1000)}
                    if contains_secret(result.output, key) or contains_secret(out, key):
                        raise SafeError("malformed-result")
                    return out
    except SafeError:
        raise
    except TimeoutError:
        raise SafeError("timeout") from None
    except AuthenticationError:
        raise SafeError("authentication") from None
    except ModelHTTPError as exc:
        raise SafeError("authentication" if exc.status_code in (401, 403) else "provider") from None
    except UnexpectedModelBehavior:
        raise SafeError("malformed-result") from None
    except Exception:
        raise SafeError("provider") from None


def main(*, test_loopback=False):
    payload = None
    def safe_diagnostic(category):
        message = "pydantic: " + category
        api = payload.get("api") if isinstance(payload, dict) else None
        secret = api.get("api_key") if isinstance(api, dict) else None
        if not isinstance(secret, str) or not secret or secret not in message:
            print(message, file=sys.stderr)
    try:
        try:
            payload = json.load(sys.stdin)
        except Exception:
            raise SafeError("validation") from None
        output = asyncio.run(infer(payload, test_loopback=test_loopback))
        serialized = json.dumps({"output": output})
        if contains_secret({"output": output}, payload["api"]["api_key"]):
            raise SafeError("malformed-result")
        print(serialized)
        return 0
    except SafeError as exc:
        # Categories contain no invocation or provider-controlled material.
        safe_diagnostic(str(exc))
        return 1
    except Exception:
        safe_diagnostic("validation")
        return 1


if __name__ == "__main__":
    # Only the trusted process invoker can select fixtures; no JSON input enables HTTP.
    if sys.argv[1:] not in ([], ["--fixture-loopback"]):
        sys.exit(1)
    sys.exit(main(test_loopback=bool(sys.argv[1:])))
