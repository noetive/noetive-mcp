# Benchmark tracker

Allocation counts for the MCP tool handlers, measured with a stub broker so the
numbers are the handler's own cost rather than the network's.

Reproduce:

```sh
go test -run "^$" -bench=. -benchmem -benchtime=20000x -memprofile=mem.out ./internal/broker/
go tool pprof -top -alloc_objects mem.out
```

## What is worth optimising here

A real tool call makes an HTTPS request to `semantik.noetive.io`, which costs
orders of magnitude more than anything below. These handlers are not where a
user's latency comes from, and no amount of work here changes that.

They are still worth keeping tight for one reason: an editor holds this process
open all day, and every allocation is garbage the collector walks on behalf of a
program that is otherwise idle. The bar for a change here is that it costs
nothing in readability. Anything that trades clarity for a few bytes is not
worth taking.

## 2026-08-19 — removing `fmt` from the result-rendering paths

Apple M4 Pro, darwin/arm64, Go 1.25.2.

| Benchmark | Before | After | Change |
|---|---|---|---|
| `Search/hits=1` | 11 allocs, 537 B, 420 ns | 10 allocs, 512 B, 356 ns | −1 alloc, −15% time |
| `Search/hits=10` | 11 allocs, 1178 B, 515 ns | 10 allocs, 1152 B, 453 ns | −1 alloc, −12% time |
| `Search/hits=50` | 11 allocs, 3930 B, 835 ns | 10 allocs, 3904 B, 774 ns | −1 alloc, −7% time |
| `Publish` | 14 allocs, 922 B, 608 ns | 12 allocs, 896 B, 484 ns | −2 allocs, −20% time |
| `TargetingResolution` | 10 allocs, 474 B, 389 ns | 9 allocs, 448 B, 330 ns | −1 alloc, −15% time |
| `ErrorShaping` | 16 allocs, 850 B, 611 ns | 9 allocs, 584 B, 372 ns | **−44% allocs, −31% bytes, −39% time** |
| `Lint` | 12 allocs, 650 B, 673 ns | 9 allocs, 576 B, 291 ns | −25% allocs, −57% time |

`ErrorShaping` moved most because it started worst. `broker.failure` accounted
for 22.6% of all allocations in the profile — it built its message with four
`fmt.Fprintf` calls into an unsized `strings.Builder`, so it paid for the
formatter's reflection and for the buffer to double as it grew.

Failure is not a cold path. A transient 503 from the broker is routine, and the
error result is what the agent reads to decide whether to retry, so it runs
about as often as anything else here.

The change everywhere was the same: direct `WriteString` calls into a builder
sized once with `Grow`, and `strconv` in place of `%d`. No behaviour moved, and
the messages are byte-identical — the tests asserting their content are what
says so.

## What the profile shows now

```
26%  mcp.NewToolResultStructured   library, allocating the result
23%  context.WithDeadlineCause     the per-call timeout
17%  SearchTool.func1              the results slice, scaling with hits
11%  time.newTimer                 the per-call timeout
 5%  strings.Builder.grow          one presized allocation per message
```

Nothing left is worth removing:

- The **library allocations** belong to mcp-go and are the result object itself.
- The **timeout** is 34% of allocations across two entries, and it is the reason
  a hung broker cannot hang the editor. It could be skipped when the parent
  context already carries an earlier deadline, but that adds a branch to every
  handler to save two allocations on a path that then makes a network call.
- The **results slice** is preallocated to the response length already, which is
  why the allocation count is flat at 10 whether the server returns 1 hit or 50.
- The **builders** are one allocation each, which is the floor for returning a
  string.
