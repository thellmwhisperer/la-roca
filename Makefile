# La Roca: one static executable per platform and nothing else.
#
# The release artefacts are produced by the channel (GitHub Actions), not by
# this Makefile: here we build to work and to test locally.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BIN     ?= bin/roca
EVAL_MODE ?= replay
EVAL_FORMAT ?= human
EVAL_PROVIDER ?=
EVAL_MODEL ?=
EVAL_CASES ?=
EVAL_DB ?=

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

# CGO_ENABLED=0 is the product's premise: a static binary, cross compilation
# with no toolchain and a single release lane instead of three.
GO_BUILD := CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)"

.PHONY: build
build: ## Build the binary for this machine
	$(GO_BUILD) -o $(BIN) ./cmd/roca

.PHONY: darwin-arm64 linux-arm64 linux-amd64 windows-amd64
darwin-arm64: ## Build the macOS ARM64 artefact
	GOOS=darwin GOARCH=arm64 $(GO_BUILD) -o bin/roca-$(VERSION)-darwin-arm64 ./cmd/roca

linux-arm64:
	GOOS=linux GOARCH=arm64 $(GO_BUILD) -o bin/roca-$(VERSION)-linux-arm64 ./cmd/roca

linux-amd64:
	GOOS=linux GOARCH=amd64 $(GO_BUILD) -o bin/roca-$(VERSION)-linux-x64 ./cmd/roca

# The one artefact whose name is a different shape, because Windows will not run
# a file without the extension. `release.ArtefactName` says the same thing in Go
# and a test reads this file to prove the two agree.
windows-amd64:
	GOOS=windows GOARCH=amd64 $(GO_BUILD) -o bin/roca-$(VERSION)-windows-x64.exe ./cmd/roca

.PHONY: dist
dist: darwin-arm64 linux-arm64 linux-amd64 windows-amd64 ## Build the four targets from a single runner

.PHONY: test
test: ## Unit and contract tests
	go test ./...

.PHONY: eval
eval: ## Measure retrieval against the synthetic golden set
	@$(MAKE) --no-print-directory build >/dev/null
	@$(BIN) eval --mode $(EVAL_MODE) --format $(EVAL_FORMAT) $(if $(EVAL_PROVIDER),--provider "$(EVAL_PROVIDER)") $(if $(EVAL_MODEL),--model "$(EVAL_MODEL)") $(if $(EVAL_CASES),--cases "$(EVAL_CASES)") $(if $(EVAL_DB),--db "$(EVAL_DB)")

.PHONY: accept accept-index
accept: build accept-index ## The godog acceptance suites against the real binary
	go test -tags=acceptance ./test/acceptance -count=1

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

# The slop gate blocks duplication and orphan regressions, and verifies every
# catalogued public surface still has a live acceptance claim. `--enforce` fails
# both ways on a ceiling: over is a regression, under is an uncommitted
# improvement. The ratchets in .slop/ceilings.yml are monotonic.
.PHONY: slop
slop: ## Duplication, orphan and public-surface claims gates
	./scripts/slopslint.sh check --classify --enforce

.PHONY: fmt
fmt:
	@test -z "$$(gofmt -l cmd internal data test)" || \
		(echo "gofmt pending in:"; gofmt -l cmd internal data test; exit 1)

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
