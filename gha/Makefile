SHELL := /bin/bash

.PHONY: build test test-fast test-integration install-test-deps

build:
	go build ./...

install-test-deps:
	@true

test:
	go test -count=1 ./...
	$(MAKE) test-integration

test-integration:
	python3 ../scripts/run-gha-integration.py

# No Nix, Docker daemon, external actions or service credentials are required.
test-fast:
	go test -count=1 ./...
	$(MAKE) test-integration
