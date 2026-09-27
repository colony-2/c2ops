SHELL := /bin/bash

.PHONY: build test install-test-deps

build:
	python3 -c "from pathlib import Path; compile(Path('main.py').read_text(), 'main.py', 'exec')"

install-test-deps:
	uv sync --script ./main.py --no-progress

test:
	uv run --with typesafe-sdk==0.7.2 --with pyyaml --with jsonschema python -m unittest -v test_main.py
