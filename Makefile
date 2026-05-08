.PHONY: build test lint clean install check bench-memory bench-negative bench-negative-baseline

BINARY=codebase-memory-mcp
MODULE=github.com/DeusData/codebase-memory-mcp

# Stamp git describe into the binary so callers can audit which fixes
# are loaded. Falls back to "dev" when not in a git checkout (e.g.,
# building from a downloaded tarball). The version stamp is consumed
# by `main.version` (cmd/codebase-memory-mcp/main.go) and surfaced via
# the MCP server's startup banner + `--version` flag.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Windows: statically link the MinGW pthread runtime so the resulting .exe
# does not depend on libwinpthread-1.dll being on the caller's PATH. Matches
# the release.yml build-windows job. Caller's parent (e.g. Claude Code MCP
# spawn) often has a stripped PATH; missing DLL → STATUS_ENTRYPOINT_NOT_FOUND.
#
# The version stamp uses -X main.version=$(VERSION). On Windows this is
# concatenated INSIDE the same -ldflags string as `-extldflags '-static'`
# because Go only honors a single -ldflags argument; the second one wins.
ifeq ($(OS),Windows_NT)
    BUILD_LDFLAGS = -ldflags "-extldflags '-static' -X main.version=$(VERSION)"
    BINARY_EXT = .exe
else
    BUILD_LDFLAGS = -ldflags "-X main.version=$(VERSION)"
    BINARY_EXT =
endif

build:
	CGO_ENABLED=1 go build $(BUILD_LDFLAGS) -o bin/$(BINARY)$(BINARY_EXT) ./cmd/codebase-memory-mcp/

test:
	go test ./... -v

check: lint test  ## Run lint + tests

lint:  ## Run golangci-lint
	golangci-lint run --timeout=5m ./...

clean:
	rm -rf bin/

install:
	go install ./cmd/codebase-memory-mcp/

bench-memory:  ## Run memory stability benchmark
	go test -run TestMemoryStability -v -count=1 -timeout=5m ./internal/pipeline/

bench-negative: build  ## Run negative-fixture regression gate (fails on phantom-count increase)
	python bench/accuracy/check_negative_fixtures.py --regression-gate

bench-negative-baseline: build  ## Re-pin negative-fixture baselines (use after intentional resolver changes)
	python bench/accuracy/check_negative_fixtures.py --write-baseline
