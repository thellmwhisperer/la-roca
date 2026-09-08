# La Roca: static core binaries carrying matched vector payloads for every
# platform.
# The release channel publishes the artefacts; `make dist` reproduces them
# locally, while the individual targets support development and verification.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BIN     ?= bin/roca

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

# The core stays CGO_ENABLED=0. The vector payload links the local embedding
# engine on macOS and Linux; Windows keeps the previous path until its own
# native lane exists.
GO_BUILD := CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)"
VECTOR_BUILD := $(MAKE) -C plugins/vector build VERSION=$(VERSION) COMMIT=$(COMMIT) DATE=$(DATE)
VECTOR_TMP := $(abspath .tmp)
VECTOR_BUNDLE := go run ./cmd/bundle-vector
HOST_OS ?= $(shell go env GOOS)
HOST_ARCH ?= $(shell go env GOARCH)

DIST_TARGETS := windows-amd64
ifeq ($(HOST_OS)-$(HOST_ARCH),darwin-arm64)
DIST_TARGETS += darwin-arm64
endif
ifeq ($(HOST_OS)-$(HOST_ARCH),linux-amd64)
DIST_TARGETS += linux-amd64
endif
ifeq ($(HOST_OS)-$(HOST_ARCH),linux-arm64)
DIST_TARGETS += linux-arm64
endif

.PHONY: build
build: ## Build the binary for this machine
	$(VECTOR_BUILD) BIN=$(VECTOR_TMP)/roca-vector-native
	$(GO_BUILD) -o $(BIN) ./cmd/roca
	$(VECTOR_BUNDLE) --binary $(BIN) --payload $(VECTOR_TMP)/roca-vector-native

.PHONY: darwin-arm64 linux-arm64 linux-amd64 windows-amd64
darwin-arm64: ## Build the macOS ARM64 artefact
	$(MAKE) -C plugins/vector darwin-arm64 VERSION=$(VERSION) COMMIT=$(COMMIT) DATE=$(DATE)
	GOOS=darwin GOARCH=arm64 $(GO_BUILD) -o bin/roca-$(VERSION)-darwin-arm64 ./cmd/roca
	$(VECTOR_BUNDLE) --binary bin/roca-$(VERSION)-darwin-arm64 --payload $(VECTOR_TMP)/roca-vector-darwin-arm64

linux-arm64:
	$(MAKE) -C plugins/vector linux-arm64 VERSION=$(VERSION) COMMIT=$(COMMIT) DATE=$(DATE)
	GOOS=linux GOARCH=arm64 $(GO_BUILD) -o bin/roca-$(VERSION)-linux-arm64 ./cmd/roca
	$(VECTOR_BUNDLE) --binary bin/roca-$(VERSION)-linux-arm64 --payload $(VECTOR_TMP)/roca-vector-linux-arm64

linux-amd64:
	$(MAKE) -C plugins/vector linux-amd64 VERSION=$(VERSION) COMMIT=$(COMMIT) DATE=$(DATE)
	GOOS=linux GOARCH=amd64 $(GO_BUILD) -o bin/roca-$(VERSION)-linux-x64 ./cmd/roca
	$(VECTOR_BUNDLE) --binary bin/roca-$(VERSION)-linux-x64 --payload $(VECTOR_TMP)/roca-vector-linux-x64

# The one artefact whose name is a different shape, because Windows will not run
# a file without the extension. `release.ArtefactName` says the same thing in Go
# and a test reads this file to prove the two agree.
windows-amd64:
	$(MAKE) -C plugins/vector windows-amd64 VERSION=$(VERSION) COMMIT=$(COMMIT) DATE=$(DATE)
	GOOS=windows GOARCH=amd64 $(GO_BUILD) -o bin/roca-$(VERSION)-windows-x64.exe ./cmd/roca
	$(VECTOR_BUNDLE) --binary bin/roca-$(VERSION)-windows-x64.exe --payload $(VECTOR_TMP)/roca-vector-windows-x64.exe

.PHONY: vector-dist
vector-dist:
	$(MAKE) -C plugins/vector dist VERSION=$(VERSION)

.PHONY: dist
dist: $(DIST_TARGETS) vector-dist ## Build native vector payloads plus the Windows compatibility payload

.PHONY: test
test: ## Unit and contract tests
	go test ./...

.PHONY: accept accept-index split-oracle e2e-smoke
# Pin the suite to the artefact this recipe's `build` just wrote. An inherited
# ROCA_BIN, including a stub, cannot select a different binary.
accept: build accept-index playground-fixture ## The godog acceptance suites against the real binary
	ROCA_BIN=$(BIN) ROCA_PLAYGROUND_BIN="$(CURDIR)/$(PLAYGROUND_DIR)/bin/roca-playground" go test -tags=acceptance ./test/acceptance -count=1

e2e-smoke: build ## Real binary in a disposable home: init, ingest, query, plugin install and update
	ROCA_BIN=$(BIN) go test -tags=acceptance ./test/acceptance -run '^TestRealBinaryDisposableHomeSmoke$$' -count=1

split-oracle: build playground-fixture ## Record and replay the DATA SPLIT compatibility goldens
	ROCA_PLAYGROUND_BIN="$(CURDIR)/$(PLAYGROUND_DIR)/bin/roca-playground" go test -tags=acceptance ./test/acceptance -run '^TestDataSplitCompatibilityOracle$$' -count=1

accept-index: ## List and verify the per-domain acceptance scenarios
	@files="$$(find features -name '*.feature' -type f | sort)"; \
		test -n "$$files" || { echo "no per-domain acceptance features found"; exit 1; }; \
		unexpected="$$(printf '%s\n' "$$files" | grep -Ev '^features/(store|ingest|provider|distribution)/[^/]+\.feature$$' || true)"; \
		test -z "$$unexpected" || { echo "features must live directly under store, ingest, provider or distribution:"; echo "$$unexpected"; exit 1; }; \
		for file in $$files; do \
			grep -HnE '^[[:space:]]*Scenario( Outline)?:' "$$file" || exit 1; \
		done

.PHONY: check
check: build fmt vet test accept slop ## What CI requires before merging

# UPGRADE_HOME selects a single frozen version; empty runs every one of them,
# which is what a contributor wants locally and what CI splits across runners.
.PHONY: upgrade-gauntlet
upgrade-gauntlet: build ## Upgrade frozen old-version homes through the current binary
	./scripts/upgrade-gauntlet.sh $(BIN) $(UPGRADE_HOME)

# The slop gate blocks duplication and orphan regressions, verifies every
# catalogued public surface still has a live acceptance claim, and fails when a
# removed adjacent-feature dragon's forbid reappears. `--enforce` fails both
# ways on a ceiling: over is a regression, under is an uncommitted
# improvement. The ratchets in .slop/ceilings.yml are monotonic.
.PHONY: slop
slop: ## Duplication, orphan, public-surface and adjacent-feature gates
	./scripts/slopslint.sh check --classify --enforce
	./scripts/check-dragons.sh

.PHONY: fmt
fmt:
	@test -z "$$(gofmt -l cmd internal data pkg test)" || \
		(echo "gofmt pending in:"; gofmt -l cmd internal data pkg test; exit 1)

.PHONY: vet
vet:
	go vet -tags acceptance ./...

.PHONY: clean
clean:
	rm -rf bin

.PHONY: help
help:
	@grep -E '^[a-z-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

PLAYGROUND_DIR ?= .tmp/playground
PLAYGROUND_REF ?= fm/extract-s1
.PHONY: playground-fixture playground-test
playground-fixture:
	@test -f "$(PLAYGROUND_DIR)/go.mod" || git clone --depth 1 --branch "$(PLAYGROUND_REF)" https://github.com/thellmwhisperer/roca-playground.git "$(PLAYGROUND_DIR)"
	$(MAKE) -C "$(PLAYGROUND_DIR)" build CORE_DIR="$(CURDIR)"

playground-test: playground-fixture
	$(MAKE) -C "$(PLAYGROUND_DIR)" check CORE_DIR="$(CURDIR)"
	@command -v roca >/dev/null || { echo "published roca binary required for before/after evidence" >&2; exit 1; }
	ROCA_BIN="$(CURDIR)/$(PLAYGROUND_DIR)/bin/roca" ROCA_PUBLISHED_BIN="$$(command -v roca)" go test -tags acceptance ./test/acceptance -run '^TestCostPlayground$$' -count=1 -v
