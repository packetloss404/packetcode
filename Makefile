.PHONY: build test lint verify vulncheck goreleaser-check release-dry-run release-check install-test smoke smoke-e2e browser-test run clean ci tui-deps tui-snapshots tui-snapshots-claude tui-golden-update tui-golden-check

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BINARY  ?= bin/packetcode
GOVULNCHECK_VERSION ?= v1.3.0
TUI_PYTHON ?= python3
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

# Build the full set of release artifacts locally and assert they are usable.
# Signing takes its skip paths without credentials, which is the same path a
# fork and a pull request take.
release-dry-run:
	goreleaser release --snapshot --clean --skip=publish
	sh scripts/check-release-artifacts.sh dist

# Assert an already-built dist/ without rebuilding it.
release-check:
	sh scripts/check-release-artifacts.sh dist

# The installers' signature check, against stubbed curl and cosign. `curl |
# bash` cannot be dry-run any other way, and it is the code most users meet
# first.
install-test:
	bash scripts/test-install-verify.sh

smoke: build
	./$(BINARY) --version
	./$(BINARY) run --help

# End-to-end smoke: drives the real agent loop against a stdlib-only stub
# provider (tools/smokestub) with an isolated home, asserting credential
# resolution, an approved write, the fail-closed approval path, the dotenv
# secret refusal, and the compound-command deny floor. No credentials, no
# network, no new dependencies. The CI smoke job runs the same script on
# ubuntu, macos and windows, and the script exits nonzero if any assertion
# fails, so it gates rather than reports.
smoke-e2e:
	bash smoke.sh

# Chromium exercises the offline HTML manual. Install its pinned dependencies
# with npm ci and npx playwright install chromium first.
browser-test:
	npm run test:browser

tui-snapshots: build
	sh scripts/tui_snapshot_suite.sh packetcode

tui-snapshots-claude:
	sh scripts/tui_snapshot_suite.sh claude

tui-deps:
	$(TUI_PYTHON) -m pip install -r scripts/requirements-tui.txt

tui-golden-update: build
	$(TUI_PYTHON) -m unittest scripts/tui_capture_test.py
	sh scripts/tui_golden.sh update

tui-golden-check: build
	$(TUI_PYTHON) -m unittest scripts/tui_capture_test.py
	sh scripts/tui_golden.sh check

run: build
	./$(BINARY)

ci: verify lint test vulncheck build smoke smoke-e2e browser-test goreleaser-check release-dry-run install-test

clean:
	rm -rf bin/ dist/
