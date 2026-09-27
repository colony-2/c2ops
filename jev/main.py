#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = ["typesafe-sdk==0.7.2"]
# ///
"""Evaluate typed questions with TypeSafe's System One API."""

import json
import os
import sys

from typesafe_sdk import Choice, Noul, Score, TypeSafeClient


def run(payload):
    if not isinstance(payload, dict):
        raise ValueError("input must be an object")
    if not isinstance(payload.get("state"), (str, dict, list)):
        raise ValueError("state must be a string, object, or array")
    questions = payload.get("questions")
    if not isinstance(questions, dict) or not questions:
        raise ValueError("questions must be a nonempty object")
    typed = {}
    for name, question in questions.items():
        if not isinstance(question, dict) or not isinstance(question.get("instructions"), (str, dict, list)):
            raise ValueError(f"question {name!r} requires instructions")
        kind = question.get("type")
        constructors = {"noul": Noul, "choice": Choice, "score": Score}
        if kind not in constructors:
            raise ValueError(f"question {name!r} type must be noul, choice, or score")
        criteria = question.get("criteria")
        if kind == "choice" and (not isinstance(criteria, dict) or not 1 <= len(criteria) <= 255):
            raise ValueError(f"question {name!r} requires 1 to 255 choice criteria")
        if kind == "score" and (not isinstance(criteria, list) or not 2 <= len(criteria) <= 10):
            raise ValueError(f"question {name!r} requires 2 to 10 score criteria")
        typed[name] = constructors[kind](**question)
    api_key = payload.get("api_key") or os.environ.get("TYPESAFE_API_KEY")
    if not isinstance(api_key, str) or not api_key.strip():
        raise ValueError("api_key or TYPESAFE_API_KEY is required")
    timeout = payload.get("timeout_seconds", 60)
    if type(timeout) not in (float, int) or not 0 < timeout <= 240:
        raise ValueError("timeout_seconds must be between 0 and 240")
    with TypeSafeClient(api_key=api_key, base_url=payload.get("base_url"), timeout=timeout) as client:
        response = client.system_one(state=payload["state"], questions=typed,
                                     model=payload.get("model") or "jev-latest")
    if set(response.answers) != set(typed):
        raise ValueError("Jev response did not include every requested answer")
    return response.model_dump(mode="json")


def main():
    try:
        print(json.dumps({"output": run(json.load(sys.stdin))}))
        return 0
    except Exception as exc:
        # SDK validation errors can include request data. Keep credentials and
        # state out of diagnostics; the type still identifies API/auth failures.
        print(f"jev failed: {type(exc).__name__}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
