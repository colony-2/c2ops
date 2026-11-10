#!/usr/bin/env python3
"""Check source dependency pins and provider imports in the packaged interpreter."""

import importlib.metadata
import importlib.util
import os
from pathlib import Path
import runpy
import sys
import tomllib

script = Path(sys.argv[1])
metadata = script.read_text().split("# /// script\n", 1)[1].split("# ///", 1)[0]
requirements = tomllib.loads("\n".join(
    line.removeprefix("# ") for line in metadata.splitlines()
))["dependencies"]
for requirement in requirements:
    distribution, version = requirement.split("==")
    distribution = distribution.split("[", 1)[0]
    actual = importlib.metadata.version(distribution)
    assert actual == version, f"{script}: expected {requirement}, packaged {actual}"

# All three library ops must import the unchanged nixpkgs Pydantic derivation,
# rather than private copies generated from separate dependency resolutions.
origin = Path(importlib.util.find_spec("pydantic").origin).resolve()
assert origin.is_relative_to(Path(sys.argv[2])), origin

if sys.argv[3] == "pydantic":
    from pydantic_ai.models.openai import OpenAIChatModel
    from pydantic_ai.providers.openai import OpenAIProvider
    from pydantic_ai.output import StructuredDict
    from pydantic_ai import Agent
    Agent(OpenAIChatModel("fixture", provider=OpenAIProvider(base_url="https://example.invalid/root/", api_key="fixture")), output_type=StructuredDict({"type": "object"}))
