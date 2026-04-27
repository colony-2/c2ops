SHELL := /bin/bash

.PHONY: build test

build:
	python3 -c "from pathlib import Path; compile(Path('main.py').read_text(), 'main.py', 'exec')"

test:
	go test -count=1 ./...
