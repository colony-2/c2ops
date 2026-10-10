SHELL := /bin/bash
MAKEFLAGS += --no-print-directory

OP_DIRS := $(sort $(patsubst %/op.yaml,%,$(wildcard */op.yaml)))

.PHONY: ops build test install-test-deps manifests check-manifests nix-check test-local test-tools

ops:
	@for dir in $(OP_DIRS); do \
		echo "$$dir"; \
	done

build test install-test-deps:
	@set -euo pipefail; \
	for dir in $(OP_DIRS); do \
		echo "==> $$dir $@"; \
		$(MAKE) -C "$$dir" $@; \
	done

manifests:
	uv run scripts/manifests.py

check-manifests:
	uv run scripts/manifests.py --check

nix-check:
	nix flake check -L

test-tools:
	python3 -m unittest discover -s scripts -p 'test_*.py'

test: test-tools

test-local: nix-check
	C2OPS_TEST_FLAKE="path:$(CURDIR)" $(MAKE) test
