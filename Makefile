# La Roca: one static executable per platform and nothing else.
#
# The release artefacts are produced by the channel (GitHub Actions), not by
# this Makefile: here we build to work and to test locally.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BIN     ?= bin/roca

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

# `make classifier` is gone: the classifier is a personal artefact generated
# next to the operator's database by `roca calibrate` / `roca init`, never a
# file in this tree (2026-08-05). Shipping somebody's training data
# inside the binary is a serious leak.
# `make dictionary` is gone for the same reason and a stronger one: v1.0 search
# is lexical (FTS5) only, so there is no static coordinate dictionary to
# distill and nothing in the build needs it.

.PHONY: test
test: ## Unit and contract tests
	go test ./...

.PHONY: accept accept-index
accept: build accept-index ## The godog acceptance suites against the real binary
	go test -tags=acceptance ./test/acceptance -count=1

accept-index: ## List and verify the per-domain acceptance scenarios
	@files="$$(find features -mindepth 2 -name '*.feature' -type f | sort)"; \
		test -n "$$files" || { echo "no per-domain acceptance features found"; exit 1; }; \
		for file in $$files; do \
			grep -HnE '^[[:space:]]*Scenario( Outline)?:' "$$file" || exit 1; \
		done

.PHONY: check
check: build fmt vet test accept slop ## What CI requires before merging

# The slop gate blocks. `--enforce` fails both ways on purpose: over the ceiling
# is a regression, under it is an improvement that was not committed, so the base
# branch always states the truth. The ceilings live in .slop/ceilings.yml and the
# ratchet is monotonic: raising one to make a build pass is not a fix.
.PHONY: slop
slop: ## The duplication gate, blocking (see .slop/config.yml)
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
