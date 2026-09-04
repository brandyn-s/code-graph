.PHONY: build release test lint clean install check bench-memory bench-negative bench-negative-baseline bench-post-battery bench-rust-reqwest bench-react-fetch bench-handler-resolution

BINARY=code-graph
MODULE=github.com/brandyn-s/code-graph

# Stamp git describe into the binary so callers can audit which fixes
# are loaded. Falls back to "dev" when not in a git checkout (e.g.,
# building from a downloaded tarball). The version stamp is consumed
# by `main.version` (cmd/code-graph/main.go) and surfaced via
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
    RELEASE_LDFLAGS = -ldflags "-s -w -extldflags '-static' -X main.version=$(VERSION)"
    BINARY_EXT = .exe
else
    BUILD_LDFLAGS = -ldflags "-X main.version=$(VERSION)"
    RELEASE_LDFLAGS = -ldflags "-s -w -X main.version=$(VERSION)"
    BINARY_EXT =
endif

build:
	CGO_ENABLED=1 go build $(BUILD_LDFLAGS) -o bin/$(BINARY)$(BINARY_EXT) ./cmd/code-graph/

# Release build: strip the symbol table and DWARF (-s -w) and remove local
# path prefixes (-trimpath). Saves ~10MB (151 -> 141MB measured 2026-06-10);
# the bulk of the binary is the 26 vendored tree-sitter grammar tables,
# which stripping cannot touch — per-language build tags would be the next
# size lever. Keep `make build` for development (stack traces keep symbols
# either way in Go, but debuggers want the DWARF from the default build).
# Every vendored grammar, including the ones behind the cbm_all build tag
# (currently CUDA). Default builds leave them out to keep the binary small.
build-all:
	CGO_ENABLED=1 go build -tags cbm_all $(BUILD_LDFLAGS) -o bin/$(BINARY)$(BINARY_EXT) ./cmd/code-graph/

release:
	CGO_ENABLED=1 go build -trimpath $(RELEASE_LDFLAGS) -o bin/$(BINARY)$(BINARY_EXT) ./cmd/code-graph/

test:
	go test ./... -v

check: lint test  ## Run lint + tests

lint:  ## Run golangci-lint
	golangci-lint run --timeout=5m ./...

clean:
	rm -rf bin/

install:
	go install ./cmd/code-graph/

bench-memory:  ## Run memory stability benchmark
	go test -run TestMemoryStability -v -count=1 -timeout=5m ./internal/pipeline/

bench-negative: build  ## Run negative-fixture regression gate (fails on phantom-count increase)
	python bench/accuracy/check_negative_fixtures.py --regression-gate

bench-negative-baseline: build  ## Re-pin negative-fixture baselines (use after intentional resolver changes)
	python bench/accuracy/check_negative_fixtures.py --write-baseline

bench-post-battery: build  ## Regression gate for the 12-item PSM post-battery (HTTP_CALLS, axum HANDLES, IMPLEMENTS, SAFETY rationale)
	python bench/accuracy/check_post_battery.py

bench-rust-reqwest: build  ## Regression gate for Rust reqwest URL extraction shapes (literal, const, format!())
	python bench/accuracy/check_rust_reqwest.py

bench-react-fetch: build  ## Regression gate for TS/JSX fetch URL shapes (literal, template-literal prefix, template-literal id slot)
	python bench/accuracy/check_react_fetch.py

bench-handler-resolution: build  ## Adversarial gate for Phase D1 handler resolution (HandlerRef + crate-locality vs name-collision decoy)
	python bench/accuracy/check_handler_resolution.py

homebrew-formula:  ## Render the Homebrew formula for TAG (e.g. make homebrew-formula TAG=v0.9.0)
	scripts/update-homebrew-formula.sh $(TAG)

server-json:  ## Render server.json for the MCP registry from TAG
	scripts/update-server-json.sh $(TAG)
