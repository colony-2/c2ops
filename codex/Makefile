SHELL := /bin/bash

.PHONY: build test install-test-deps

build:
	go build ./...

install-test-deps:
	python3 ../scripts/with-codex.py

test:
	python3 ../scripts/with-codex.py -- go test -count=1 ./...
