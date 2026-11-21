.PHONY: build test install-test-deps
build:
	go build -mod=readonly -o ../bin/github-release .
test:
	go test -mod=readonly -count=1 ./...
install-test-deps:
	@true
