SHELL := /bin/bash

.PHONY: build test install-test-deps

build:
	go build ./...

install-test-deps:
	@true

test:
	go test -count=1 ./...
