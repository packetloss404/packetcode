.PHONY: build test lint verify vulncheck goreleaser-check smoke run clean ci

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BINARY  ?= bin/packetcode
GOVULNCHECK_VERSION ?= v1.3.0
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

build:
	mkdir -p $(dir $(BINARY))
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/packetcode

test:
	go test -race -count=1 ./...

lint:
	golangci-lint run ./...

verify:
	go mod verify

vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

goreleaser-check:
	goreleaser check

smoke: build
	./$(BINARY) --version

run: build
	./$(BINARY)

ci: verify lint test vulncheck build smoke goreleaser-check

clean:
	rm -rf bin/ dist/
