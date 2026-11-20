.PHONY: build test install-test-deps
build:
	go build -mod=readonly -o release-pack .
test:
	go test -mod=readonly -count=1 ./...
install-test-deps:
	@true
