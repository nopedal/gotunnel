VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS = -s -w \
	-X main.buildVersion=$(VERSION) \
	-X main.buildCommit=$(COMMIT) \
	-X main.buildDate=$(DATE)

.PHONY: all build server client clean cross

all: build

build: server client

server:
	@echo "building tunnel-server..."
	@go build -ldflags "$(LDFLAGS)" -o bin/tunnel-server ./cmd/tunnel-server

client:
	@echo "building tunnel..."
	@go build -ldflags "$(LDFLAGS)" -o bin/tunnel ./cmd/tunnel

clean:
	@rm -rf bin/

# Cross-compilation targets
cross: cross-linux-amd64 cross-darwin-arm64 cross-darwin-amd64

cross-linux-amd64:
	@echo "building linux/amd64..."
	@GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/tunnel-server-linux-amd64 ./cmd/tunnel-server
	@GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/tunnel-linux-amd64 ./cmd/tunnel

cross-darwin-arm64:
	@echo "building darwin/arm64..."
	@GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/tunnel-server-darwin-arm64 ./cmd/tunnel-server
	@GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/tunnel-darwin-arm64 ./cmd/tunnel

cross-darwin-amd64:
	@echo "building darwin/amd64..."
	@GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/tunnel-server-darwin-amd64 ./cmd/tunnel-server
	@GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/tunnel-darwin-amd64 ./cmd/tunnel

.PHONY: docker
docker:
	docker build -t tunnel-server .
