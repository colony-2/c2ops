SHELL := /bin/bash
MAKEFLAGS += --no-print-directory

OP_DIRS := $(sort $(patsubst %/op.yaml,%,$(wildcard */op.yaml)))

.PHONY: ops build test

ops:
	@for dir in $(OP_DIRS); do \
		echo "$$dir"; \
	done

build test:
	@set -euo pipefail; \
	for dir in $(OP_DIRS); do \
		echo "==> $$dir $@"; \
		$(MAKE) -C "$$dir" $@; \
	done
