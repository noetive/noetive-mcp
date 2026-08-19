VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"
BINARY  := noetive-mcp

.PHONY: build test fuzz lint mutate bench emit installer docker clean

# Builds into the npm wrapper's bin directory, which is where the wrapper looks
# when no platform package is installed. That makes `node installer/bin/noetive-mcp.js`
# exercise the real chain locally instead of a shortcut.
build:
	go build $(LDFLAGS) -o installer/bin/$(BINARY) ./cmd/noetive-mcp

test:
	go clean -testcache
	go test -race ./...

# Short by design: replays the corpus and the seeds. Long campaigns belong in a
# scheduled run, not in the loop a developer waits on.
fuzz:
	go test -run "^$$" -fuzz FuzzPublishArguments -fuzztime 30s ./internal/broker
	go test -run "^$$" -fuzz FuzzSearchArguments -fuzztime 30s ./internal/broker
	go test -run "^$$" -fuzz FuzzLintArguments -fuzztime 30s ./internal/broker
	go test -run "^$$" -fuzz FuzzArgumentTypeConfusion -fuzztime 30s ./internal/broker

lint:
	golangci-lint run --config ./.golangci.yml ./...

# Coverage says a line ran; it says nothing about whether a test would notice if
# that line were wrong. This breaks the implementation on purpose and reports
# every change no test caught. Slow by nature — it runs the suite once per
# mutant — so it belongs in a deliberate pass, not the edit loop.
mutate:
	node scripts/mutation-test.js

# Allocation counts for the tool handlers, measured against a stub broker so the
# numbers are the handler's own cost. Record results in BENCH_TRACKER.md.
bench:
	go test -run "^$$" -bench=. -benchmem -benchtime=20000x -memprofile=mem.out ./internal/broker/
	go tool pprof -top -alloc_objects -nodecount=10 mem.out

# Regenerates both plugin formats from tools/manifest.yaml. Never hand-edit
# packaging/claude-plugin or packaging/kiro-power; CI fails when they differ.
emit:
	go run ./packaging/emit

installer:
	cd installer && npm ci --ignore-scripts && npm test

docker:
	docker build -t $(BINARY) .

clean:
	rm -rf installer/bin/$(BINARY) installer/dist dist/ tmp/
