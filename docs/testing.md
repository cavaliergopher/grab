# Testing and benchmarking

## Running things

```bash
make check      # what CI runs: fmt, vet, staticcheck, tidy, tests
make test       # just the tests
```

Tests run with `-race -count=1 -shuffle=on`. The race detector matters here:
split transfers write concurrently through `WriteAt`, share a byte counter and
a bandwidth gauge, and hand completed ranges to the checkpointer. Shuffling
matters because the tests share a local test server; a test that only passes in
a particular order is a broken test.

While iterating, run a subset:

```bash
cd v3 && go test -race -count=1 -run 'TestRange|TestCheckpoint' ./...
```

## The test server

`pkg/grabtest` is a configurable HTTP server. `WithTestServer` starts one,
hands your callback its URL, and shuts it down afterwards:

```go
grabtest.WithTestServer(t, func(url string) {
    req := mustNewRequest(filename, url)
    req.RangeSize = 4096
    resp := mustDo(req)
    testComplete(t, resp)
},
    grabtest.ContentLength(32768),
)
```

The body is deterministic: **byte *i* of the file is `byte(i)`**. That is what
makes range testing tractable — a range served from the wrong offset contains
visibly wrong bytes, so `assertFileContents` catches a misplaced write just as
readily as a missing one. `DefaultHandlerSHA256Checksum` and friends are the
checksums of that body at the default length.

Options worth knowing:

| Option | Use |
| --- | --- |
| `ContentLength(n)` | size of the served file |
| `AcceptRanges(false)` | server that does not support ranges |
| `HeaderBlacklist("Content-Length")` | unknown-length responses |
| `ETag(fn)` / `ETagStatic(s)` | change validators mid-test, to exercise a modified remote file |
| `RecordRanges(rec)` | record every range served, for `rec.Ranges()` |
| `TimeToFirstByte(d)` | delay each response |
| `StatusCode(fn)` | force particular status codes |
| `MethodWhitelist(...)` | servers that reject HEAD |

`grabtest.AssertRangesCover(t, rec, size)` asserts the recorded ranges tile the
file exactly once — no gaps, no overlaps, nothing past the end. It is the
strongest single assertion for a split transfer and worth reaching for first.

To inject failures, replace `Client.HTTPClient` with a wrapper implementing
`HTTPClient`. `range_test.go` has three worth copying: `rangeFailClient` (fail
requests after the *n*th), `bodyFailClient` (truncate a response body part way
through, to interrupt a range mid-write), and `rangeHeaderSpy` (record the
`Range` headers actually sent).

## Writing tests

**Use `t.TempDir()` for every file.** Tests run shuffled and in parallel with
each other's servers; a fixed filename in the working directory is a flake
waiting to happen.

**Check that the test bites.** A test that passes against broken code is worse
than no test, and range and checkpoint logic is unusually good at looking
correct while being wrong. Before trusting a new test, break the thing it is
meant to catch and confirm it fails:

```bash
# e.g. reintroduce the inclusive/exclusive Range end confusion
sed -i '' 's/r.End-1/r.End/' util.go
go test -count=1 -run TestRangeTransfer .   # must fail
git checkout util.go
```

This caught a real gap during development: a test named for checkpoint-based
completion was in fact passing through the ordinary file-length shortcut and
never exercised the checkpoint at all.

**Say what a test actually verifies.** If the mechanism you were aiming at
turns out not to be reachable from a test, rename the test for what it does
cover rather than leaving the name overclaiming.

## Benchmarks

Two families, and it matters which you are looking at. Both live in
`v3/bench_test.go`.

### `make bench` — what grab costs

`BenchmarkTransfer`, `BenchmarkRangeSet`, `BenchmarkCheckpointStore`. No
simulated network, so everything measured is work grab does. **These are the
ones to watch for regressions.**

Sub-benchmark names are `key=value` pairs so that `benchstat` reads them as
configuration axes:

```bash
go install golang.org/x/perf/cmd/benchstat@latest
git stash && make bench > /tmp/old.txt && git stash pop
make bench > /tmp/new.txt
benchstat /tmp/old.txt /tmp/new.txt
```

For a stable comparison, run more than once — `-count=6` and let benchstat do
the statistics — and keep the machine otherwise idle.

`BenchmarkTransfer` reports `MB/s`, `reqs/op` and `conns` alongside the usual
`ns/op` and allocations. `range=off` is the unsplit baseline; a split transfer
should land within a modest fraction of it, and the gap is the cost of the
extra requests. If that gap grows sharply, suspect something being done per
range or per write that should be done per transfer — that is exactly how the
redundant final checkpoint was found, which was costing split transfers four
fifths of their throughput.

The `durable=true` and `durable=false` pairs isolate what recording progress
costs, since only a durable transfer flushes. Compare them on the same
machine: the gap is a property of whatever device `b.TempDir()` lives on, so
it is worth watching for change but not worth comparing across hosts.

### `make bench-network` — what tuning is worth

`BenchmarkRangeSize` and `BenchmarkConcurrency`, against a server that sleeps a
round trip before each response and meters each TCP connection. **Read these,
do not track them** — they are mostly a measurement of the sleeps, so they say
little about a change to grab, but they show the shape of the trade-offs. See
[range-requests.md](range-requests.md) for what they mean.

To explore a different network, edit the `netProfile` constants in the
benchmark — `rtt`, `bps`, and whether the server offers HTTP/2 — and rerun.

### Writing benchmarks

The benchmark server is defined in `bench_test.go` rather than reusing
`grabtest`, whose handler writes the body one byte at a time. That is fine for
correctness and hopeless for a benchmark, where it would be most of what got
measured.

Two habits the existing benchmarks follow and new ones should:

- **Verify before timing.** Each downloads and checksums the file once outside
  the timer, so a harness that is fast because it is broken cannot be mistaken
  for a fast transfer.
- **Build a fresh connection pool per sub-benchmark.** The pass the testing
  package makes over a benchmark to discover its sub-benchmarks is enough to
  fill a shared pool, which hides the cost of connecting and makes the
  connection count meaningless.
