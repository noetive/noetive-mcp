# Contributing

## Getting set up

```sh
make hooks
make build test lint
cd installer && npm ci --ignore-scripts && npm test
```

`make hooks` points `core.hooksPath` at `.githooks`; `make build` and `make test` do it too, so a fresh clone gets the hooks from the first thing it runs. The pre-commit hook is the fast half — formatting, credentials, and the generated-file check — and pre-push runs `go test -race` and the linter. Both name anything they had to skip because a tool was missing.

Node 22 or newer for the installer: `npm test` runs `node --test "dist/test/*.test.js"`, and node only expands that glob itself from 21 onwards. The published wrapper's own floor is lower — `engines` says `>=20` — because that governs users, not contributors.

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

`packaging/claude-plugin`, `packaging/kiro-power`, `.claude-plugin/`, `skills/` and `.mcp.json` are emitted from `tools/manifest.yaml`. Hand-edit any of them and CI fails. Run `make emit`.

## Before opening a pull request

```sh
make test lint emit
cd installer && npm test
git status --porcelain -- packaging/ .claude-plugin/ .mcp.json skills/   # emit changed nothing
```

`git status`, not `git diff`: adding a tool emits a *new* skill file, and a diff does not see one at all. CI asserts the same thing without the pathspec, because its checkout has nothing else in it.

Integration tests hit production and need a key: `NOETIVE_KEY_SECRET=keyu_... integration/run.sh`. They skip without one.

## Cutting a release

```sh
node scripts/stamp-version.js 1.4.0   # every file that carries a version
make emit                             # push it into the plugin manifests
git add -A && git commit -m "chore: release 1.4.0"
git push
# Once CI is green:
git tag v1.4.0 && git push origin v1.4.0
```

`git add -A`, not `commit -am`: adding a tool emits a *new* skill file, and `-a`
does not stage what git has never seen.

The committed tree is what ships, and the tag only has to agree with it. Nine
files carry the version — `tools/manifest.yaml`, both installer manifests,
`server.json` and the three generated plugin manifests among them — so the bump
is one command rather than a checklist. Two guards keep them together: the
emitter's tests refuse a tree whose version files disagree with each other, and
the release workflow refuses a tag that disagrees with `tools/manifest.yaml`.
That refusal is the first step of the run, before a GitHub Release exists,
because a release cannot be unpublished cleanly and an npm version cannot be
reused at all.

The pipeline is tag → GitHub Release → npm → a smoke install on three operating
systems → the MCP registry. Each stage gates the next, so a broken npm publish
never reaches the registry entry that advertises it.

A prerelease tag (`v1.4.0-rc.1`) runs every stage except the last, and publishes
where nothing picks it up by default: npm gets the `next` dist-tag rather than
`latest`, and the GitHub Release is marked as a prerelease. Install one
explicitly with `npm i -g @noetive/mcp-server@next`.

The MCP registry is skipped for prereleases. It is the one publish that cannot
be undone, so it waits for a release tag; everything an RC is worth cutting for
happens before it.

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
