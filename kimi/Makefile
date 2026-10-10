SHELL := /bin/bash

.PHONY: build test install-test-deps

build:
	python3 -c "from pathlib import Path; compile(Path('main.py').read_text(), 'main.py', 'exec')"

install-test-deps:
	npm exec --yes --package=@moonshot-ai/kimi-code@2.1.1 -- kimi --version

test:
	npm exec --yes --package=@moonshot-ai/kimi-code@2.1.1 -- uv run --with jsonschema python -m unittest -v test_main.py
