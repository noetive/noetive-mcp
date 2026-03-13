VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"
BINARY := noetive-mcp

.PHONY: build test build-all docker clean

build:
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/noetive-mcp

test:
	go clean -testcache
	go test ./...

build-all:
	GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY)-linux-amd64   ./cmd/noetive-mcp
	GOOS=linux   GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY)-linux-arm64   ./cmd/noetive-mcp
	GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY)-darwin-amd64  ./cmd/noetive-mcp
	GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY)-darwin-arm64  ./cmd/noetive-mcp
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY)-windows-amd64.exe ./cmd/noetive-mcp

docker:
	docker build -t $(BINARY) .

clean:
	rm -rf bin/ tmp/
