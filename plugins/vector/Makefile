VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BIN     ?= ../../.tmp/roca-vector
PACKAGE ?= ../../.tmp/vector-package

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

.PHONY: build
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/roca-vector

.PHONY: package
package: build
	go run ./cmd/package --binary $(BIN) --out $(PACKAGE) --version $(VERSION)

.PHONY: test
test:
	go test ./...

.PHONY: check
check:
	@test -z "$$(gofmt -l cmd internal)" || \
		(echo "gofmt pending in:"; gofmt -l cmd internal; exit 1)
	go vet ./...
	go test ./...
	CGO_ENABLED=0 go build -o ../../.tmp/roca-vector-check ./cmd/roca-vector
