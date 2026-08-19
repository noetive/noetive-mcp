# Contributing

## Getting set up

```sh
git config core.hooksPath .githooks
make build test lint
cd installer && npm ci --ignore-scripts && npm test
```

The Go server depends on `github.com/noetive/noetive-sdk-go/semantik`, which is not on the public module proxy. Building needs `GOPRIVATE=github.com/noetive/*` and read access to that repository.

## Things that are load-bearing

**The bare invocation must serve stdio.** `npx @noetive/mcp-server` with no arguments is what the Add to Kiro deeplink runs and what every editor config spawns. Anything else as the default breaks every advertised install path.

**The package name is `@noetive/mcp-server`.** It appears in the commands on noetive.io/mcp, in dev-docs, and inside the Kiro deeplink. Changing it breaks published instructions.

**Namespace, model and dimensions are never defaulted.** An omitted field is refused before the request is sent. This is a data-isolation boundary, not an ergonomics choice — see [docs/security.md](docs/security.md). Do not add a flag to relax it.

**Editor configs belong to the user.** A write may touch only the `noetive` key, must be idempotent, must back up first, and must leave comments intact. Every one of those has a test; keep it that way.

## Adding a tool

Add a file to `internal/broker` holding the whole path for that call: the `mcp.NewTool` descriptor, the argument decoding, the SDK call, and the result shaping. Register it in `internal/mcpserver` and add it to `ToolNames`, then add it to `tools/manifest.yaml` and run `make emit` — the emitter fails when the two disagree.

Tests go beside it: one per behaviour, named for the behaviour, against a fake implementing the narrow interface the tool declares. Add fuzz seeds for any new argument, because tool arguments are model-chosen and untrusted.

## Adding an editor

Usually a `clients.json` entry and a test fixture, with no new code. See [docs/clients.md](docs/clients.md).

## Generated files

`packaging/claude-plugin`, `packaging/kiro-power`, `.claude-plugin/` and `.mcp.json` are emitted from `tools/manifest.yaml`. Hand-edit any of them and CI fails. Run `make emit`.

## Before opening a pull request

```sh
make test lint emit
cd installer && npm test
git diff --exit-code   # emit produced no change
```

Integration tests hit production and need a key: `NOETIVE_KEY_SECRET=keyu_... integration/run.sh`. They skip without one.

## Mutation testing

Coverage says a line ran. It says nothing about whether a test would notice if
that line were wrong, and a suite can execute every branch while asserting
nothing that matters.

```sh
make mutate                                        # everything
node scripts/mutation-test.js --lang go            # one language
node scripts/mutation-test.js --file subscribe.go  # one file
node scripts/mutation-test.js --list               # enumerate without running
```

Every survivor is a change no test caught. Treat it as one of two things: the
behaviour is untested, or the test covering it asserts something too weak to
notice. Both are worth fixing; neither is fixed by deleting the operator.

Some survivors are expected, and knowing which keeps the report readable:

- **`main()` in either binary.** Its error branches end in `log.Fatal`, which
  exits the process, so a test cannot observe them. Everything a mistake could
  break was moved out into `run()` for exactly this reason; what is left is
  process glue.
- **Clamping at a boundary.** `bound(v, low, high)` mutated from `v < low` to
  `v <= low` returns `low` either way. The mutant produces identical output for
  every input, so no test can distinguish it.
- **Deep IO branches in `packaging/emit`.** Reaching the write failures nested
  inside an emitter means a filesystem that fails partway through one, and the
  cost of injecting a filesystem into a build tool is not worth what it proves.

Anything else is a hole worth closing.

The run edits source files in place and restores them in a `finally`. A killed
process cannot run that, so the pre-mutation contents are parked in
`.mutation-salvage.json` first and any later run puts them back — if a test
suite starts failing or hanging for no apparent reason, run the tool once and it
will report what it restored.

## Benchmarks

```sh
make bench
```

Record results in [BENCH_TRACKER.md](BENCH_TRACKER.md), including what did not
move. The bar for an optimisation here is that it costs nothing in readability:
a real tool call makes an HTTPS request that dwarfs any of this, and the reason
to keep the handlers tight is that an editor holds the process open all day.
