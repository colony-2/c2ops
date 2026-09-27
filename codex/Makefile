SHELL := /bin/bash
CODEX_VERSION := 0.157.1

.PHONY: build test install-test-deps

build:
	go build ./...

install-test-deps:
	npm exec --yes --package=@openai/codex@$(CODEX_VERSION) -- codex --version

test:
	npm exec --yes --package=@openai/codex@$(CODEX_VERSION) -- go test -count=1 ./...
