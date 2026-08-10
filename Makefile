# Everything here runs offline. No test in this repository touches a network,
# so a contributor needs no cloud account to work on it.

BINARY  := upkeep
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: all build test lint fmt cover clean

all: lint test build

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) ./cmd/upkeep

test:
	go test ./...

# The race detector matters here: apply walks a plan whose actions close over
# shared clients.
race:
	go test -race ./...

# -coverpkg matters here: most of this tool is exercised through the CLI
# against internal/testfake, so per-package counting reports transports as
# untested when they are driven on every run.
cover:
	go test -coverpkg=./... -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

lint:
	gofmt -l . | tee /dev/stderr | (! read)
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -f $(BINARY) coverage.out
