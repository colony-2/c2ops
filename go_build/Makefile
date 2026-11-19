.PHONY: build test install-test-deps
build:
	go build -mod=readonly -o ../bin/go-build .
test:
	go test -mod=readonly -count=1 ./...
install-test-deps:
	go mod download
