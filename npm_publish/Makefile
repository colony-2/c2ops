.PHONY: build test install-test-deps
build install-test-deps:
	test -f main.py
test:
	uv run --with jsonschema==4.26.0 python -m unittest discover -s ../scripts -p 'test_npm_publish*.py' -v
