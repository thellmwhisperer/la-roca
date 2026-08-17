VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BIN     ?= ../../.tmp/roca-vector
PACKAGE ?= ../../.tmp/vector-package
DIST    ?= ../../bin

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

.PHONY: darwin-arm64 linux-arm64 linux-amd64 windows-amd64
darwin-arm64:
	mkdir -p $(DIST)
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ../../.tmp/roca-vector-darwin-arm64 ./cmd/roca-vector
	go run ./cmd/package --binary ../../.tmp/roca-vector-darwin-arm64 --out ../../.tmp/vector-package-darwin-arm64 --version $(VERSION) --target-os darwin
	tar -C ../../.tmp/vector-package-darwin-arm64 -czf $(DIST)/roca-vector-$(VERSION)-darwin-arm64.tar.gz .

linux-arm64:
	mkdir -p $(DIST)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ../../.tmp/roca-vector-linux-arm64 ./cmd/roca-vector
	go run ./cmd/package --binary ../../.tmp/roca-vector-linux-arm64 --out ../../.tmp/vector-package-linux-arm64 --version $(VERSION) --target-os linux
	tar -C ../../.tmp/vector-package-linux-arm64 -czf $(DIST)/roca-vector-$(VERSION)-linux-arm64.tar.gz .

linux-amd64:
	mkdir -p $(DIST)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ../../.tmp/roca-vector-linux-x64 ./cmd/roca-vector
	go run ./cmd/package --binary ../../.tmp/roca-vector-linux-x64 --out ../../.tmp/vector-package-linux-x64 --version $(VERSION) --target-os linux
	tar -C ../../.tmp/vector-package-linux-x64 -czf $(DIST)/roca-vector-$(VERSION)-linux-x64.tar.gz .

windows-amd64:
	mkdir -p $(DIST)
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ../../.tmp/roca-vector-windows-x64.exe ./cmd/roca-vector
	go run ./cmd/package --binary ../../.tmp/roca-vector-windows-x64.exe --out ../../.tmp/vector-package-windows-x64 --version $(VERSION) --target-os windows
	tar -C ../../.tmp/vector-package-windows-x64 -czf $(DIST)/roca-vector-$(VERSION)-windows-x64.tar.gz .

.PHONY: dist
dist: darwin-arm64 linux-arm64 linux-amd64 windows-amd64

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
