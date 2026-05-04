SHELL := /bin/bash

PYTHON_FOR_UV ?= $(shell command -v python3 || command -v python)

.PHONY: build test install-test-deps

build:
	python3 -c "from pathlib import Path; compile(Path('main.py').read_text(), 'main.py', 'exec')"

install-test-deps:
	uv sync --script ./main.py --python "$(PYTHON_FOR_UV)" --no-managed-python --no-progress

test:
	go test -count=1 ./...
