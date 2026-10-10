SHELL := /bin/bash

.PHONY: build test install-test-deps

build:
	python3 -c "from pathlib import Path; compile(Path('main.py').read_text(), 'main.py', 'exec')"

install-test-deps:
	cd ../codex && go run ../scripts/with-tools.go --manifest ../kimi/op.json

test:
	uv run --with jsonschema python -m unittest -v test_main.py
