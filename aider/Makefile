SHELL := /bin/bash

.PHONY: build test install-test-deps

build:
	python3 -c "from pathlib import Path; compile(Path('main.py').read_text(), 'main.py', 'exec')"

install-test-deps:
	nix build ../\#aider --no-link

test:
	go test -count=1 ./...
