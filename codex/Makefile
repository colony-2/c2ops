SHELL := /bin/bash

.PHONY: build test install-test-deps

build:
	go build ./...

install-test-deps:
	go run ../scripts/with-tools.go --manifest op.json

test:
	go run ../scripts/with-tools.go --manifest op.json -- go test -count=1 ./...
