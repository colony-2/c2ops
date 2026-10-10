SHELL := /bin/bash
MAKEFLAGS += --no-print-directory

OP_DIRS := $(sort $(patsubst %/op.yaml,%,$(wildcard */op.yaml)))

.PHONY: ops build test install-test-deps manifests check-manifests nix-check

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
