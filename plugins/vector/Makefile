VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BIN     ?= ../../.tmp/roca-vector
PACKAGE ?= ../../.tmp/vector-package
DIST    ?= ../../bin
LLAMA_COMMIT ?= b21e4de74567f5eef213765c9476a843c2e43f0d
LLAMA_DIR ?= $(abspath ../../.tmp/llama.cpp)
LLAMA_BUILD ?= $(LLAMA_DIR)/build-native
LLAMA_JOBS ?= 2
HOST_OS ?= $(shell go env GOOS)
HOST_ARCH ?= $(shell go env GOARCH)

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

ifeq ($(HOST_OS),darwin)
CMAKE_FLAGS := -DGGML_METAL=ON -DGGML_METAL_EMBED_LIBRARY=ON -DGGML_BLAS=ON -DGGML_BLAS_VENDOR=Apple -DGGML_NATIVE=ON
else
CMAKE_FLAGS := -DGGML_METAL=OFF -DGGML_BLAS=OFF -DGGML_NATIVE=OFF
endif

.PHONY: llama
llama:
	LLAMA_COMMIT=$(LLAMA_COMMIT) LLAMA_DIR=$(LLAMA_DIR) sh scripts/fetch-llama.sh
	cmake -S $(LLAMA_DIR) -B $(LLAMA_BUILD) -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
		$(CMAKE_FLAGS) -DLLAMA_BUILD_TESTS=OFF -DLLAMA_BUILD_EXAMPLES=OFF -DLLAMA_BUILD_SERVER=OFF -DLLAMA_CURL=OFF
	cmake --build $(LLAMA_BUILD) --config Release --target llama -j$(LLAMA_JOBS)

.PHONY: build-native
build-native: llama
	CGO_ENABLED=1 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/roca-vector

.PHONY: build
build:
ifeq ($(HOST_OS),windows)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/roca-vector
else
	$(MAKE) build-native BIN=$(BIN)
endif

.PHONY: package
package: build
	go run ./cmd/package --binary $(BIN) --out $(PACKAGE) --version $(VERSION)

.PHONY: darwin-arm64 linux-arm64 linux-amd64 windows-amd64
darwin-arm64:
	mkdir -p $(DIST)
ifneq ($(HOST_OS)-$(HOST_ARCH),darwin-arm64)
	$(error darwin-arm64 needs a native macOS ARM builder)
endif
	$(MAKE) build-native BIN=../../.tmp/roca-vector-darwin-arm64
	go run ./cmd/package --binary ../../.tmp/roca-vector-darwin-arm64 --out ../../.tmp/vector-package-darwin-arm64 --version $(VERSION) --target-os darwin
	tar -C ../../.tmp/vector-package-darwin-arm64 -czf $(DIST)/roca-vector-$(VERSION)-darwin-arm64.tar.gz .

linux-arm64:
	mkdir -p $(DIST)
ifneq ($(HOST_OS)-$(HOST_ARCH),linux-arm64)
	$(error linux-arm64 needs a native Linux ARM builder)
endif
	$(MAKE) build-native BIN=../../.tmp/roca-vector-linux-arm64
	go run ./cmd/package --binary ../../.tmp/roca-vector-linux-arm64 --out ../../.tmp/vector-package-linux-arm64 --version $(VERSION) --target-os linux
	tar -C ../../.tmp/vector-package-linux-arm64 -czf $(DIST)/roca-vector-$(VERSION)-linux-arm64.tar.gz .

linux-amd64:
	mkdir -p $(DIST)
ifneq ($(HOST_OS)-$(HOST_ARCH),linux-amd64)
	$(error linux-amd64 needs a native Linux AMD64 builder)
endif
	$(MAKE) build-native BIN=../../.tmp/roca-vector-linux-x64
	go run ./cmd/package --binary ../../.tmp/roca-vector-linux-x64 --out ../../.tmp/vector-package-linux-x64 --version $(VERSION) --target-os linux
	tar -C ../../.tmp/vector-package-linux-x64 -czf $(DIST)/roca-vector-$(VERSION)-linux-x64.tar.gz .

windows-amd64:
	mkdir -p $(DIST)
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ../../.tmp/roca-vector-windows-x64.exe ./cmd/roca-vector
	go run ./cmd/package --binary ../../.tmp/roca-vector-windows-x64.exe --out ../../.tmp/vector-package-windows-x64 --version $(VERSION) --target-os windows
	tar -C ../../.tmp/vector-package-windows-x64 -czf $(DIST)/roca-vector-$(VERSION)-windows-x64.tar.gz .

.PHONY: dist
dist: windows-amd64
ifeq ($(HOST_OS)-$(HOST_ARCH),darwin-arm64)
	$(MAKE) darwin-arm64
endif
ifeq ($(HOST_OS)-$(HOST_ARCH),linux-amd64)
	$(MAKE) linux-amd64
endif
ifeq ($(HOST_OS)-$(HOST_ARCH),linux-arm64)
	$(MAKE) linux-arm64
endif

.PHONY: test
test:
	CGO_ENABLED=0 go test ./...

.PHONY: check
check:
	@test -z "$$(gofmt -l cmd internal)" || \
		(echo "gofmt pending in:"; gofmt -l cmd internal; exit 1)
	CGO_ENABLED=0 go vet ./...
	CGO_ENABLED=0 go test ./...
	CGO_ENABLED=0 go build -o ../../.tmp/roca-vector-check ./cmd/roca-vector
