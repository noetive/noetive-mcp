VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"
BINARY  := noetive-mcp

.PHONY: build test fuzz lint mutate bench emit installer docker clean hooks

# The hooks are the only part of setup that lives in .git/config instead of the
# tree, which makes them the only part that can silently not be there. Wired
# into build and test because those are the first things anyone runs. Writing to
# .git/config is a real side effect, so it says so — and says nothing on the
# runs where it changes nothing.
#
# The probe compares the repository root to the working directory, not merely
# "am I inside a repository": a tarball unpacked anywhere inside an unrelated
# checkout would otherwise repoint *that* repository's core.hooksPath at a
# .githooks it does not have, silently disabling its hooks.
hooks:
	@test "$$(git rev-parse --show-toplevel 2>/dev/null)" = "$$(pwd -P)" || exit 0; \
	 test "$$(git config --get core.hooksPath)" = .githooks || { \
	   git config core.hooksPath .githooks && \
	   echo "hooks: core.hooksPath -> .githooks"; \
	 }

# Builds into the npm wrapper's bin directory, which is where the wrapper looks
# when no platform package is installed. That makes `node installer/bin/noetive-mcp.js`
# exercise the real chain locally instead of a shortcut.
build: hooks
	go build $(LDFLAGS) -o installer/bin/$(BINARY) ./cmd/noetive-mcp

test: hooks
	go clean -testcache
	go test -race ./...

# Short by design: replays the corpus and the seeds. Long campaigns belong in a
# scheduled run, not in the loop a developer waits on.
#
# Enumerated rather than listed: a hand-written list silently stops covering the
# target somebody added last week, and a new tool argument is exactly when
# fuzzing earns its keep. CI enumerates the same way.
#
# The list is captured and checked before the loop: `go test -list` exits 0 and
# writes the build error to stderr when the package does not compile, so looping
# straight over its output reports a broken package as fuzzing that passed.
fuzz:
	@targets=$$(go test -list '^Fuzz' ./internal/broker | grep '^Fuzz') || exit 1; \
	 test -n "$$targets" || { echo "no fuzz targets found in ./internal/broker" >&2; exit 1; }; \
	 for target in $$targets; do \
	   echo "fuzz: $$target"; \
	   go test -run "^$$" -fuzz "^$$target$$" -fuzztime 30s ./internal/broker || exit 1; \
	 done

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

# Also removes what `make bench` and `go test -c` leave behind: both are
# gitignored, both are large, and a stray 9 MB broker.test in the tree is one
# `git add -f` from being permanent. Not .mutation-salvage.json — that file is
# how scripts/mutation-test.js restores source left mutated by a killed run, and
# deleting it would strand a deliberate bug with nothing left to announce it.
#
# -exec rm, not -delete: -delete implies -depth, and -prune does nothing under
# -depth. GNU find says so and exits non-zero; BSD find says nothing and reaches
# into installer/node_modules, deleting package files that happen to end in
# .test.
clean:
	rm -rf installer/bin/$(BINARY) installer/dist dist/ tmp/ mem.out cpu.out
	@find . -path ./installer/node_modules -prune -o -name '*.test' -type f -print -exec rm -f {} +
