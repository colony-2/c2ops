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
    from pydantic_ai import Agent

    # The slim distribution needs explicitly selected provider libraries.
    # Construct each supported provider without making a network request.
    os.environ.update(OPENAI_API_KEY="test", ANTHROPIC_API_KEY="test", GOOGLE_API_KEY="test")
    model_name = runpy.run_path(str(script))["model_name"]
    for provider, model in (("openai", "gpt-4.1"), ("anthropic", "claude-sonnet-4-5"), ("gemini", "gemini-2.5-flash")):
        Agent(model_name(provider, model))
    Agent(model_name("gemini", "google-gla:gemini-2.5-flash"))
