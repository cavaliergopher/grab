# Developing grab

Notes for people and agents working on grab itself. For how to *use* the
package, see the [package documentation](https://pkg.go.dev/github.com/cavaliergopher/grab/v3)
and the top-level [README](../README.md).

- **[architecture.md](architecture.md)** — how a transfer is put together: the
  state machine, what each file is responsible for, and the invariants that
  must not be broken.
- **[range-requests.md](range-requests.md)** — how to reason about
  `Request.RangeSize`, `Request.Concurrency` and throughput, with the numbers
  behind the rules of thumb.
- **[testing.md](testing.md)** — how to run and write tests and benchmarks.

## Getting started

Everything runs through the Makefile from the repository root:

```bash
make check
```

That is the same suite the CI workflow runs: `gofmt`, `go vet`, `staticcheck`,
`go mod tidy`, and the tests with `-race -count=1 -shuffle=on`. Run it before
you push; it is cheap.

```bash
make bench           # what grab costs — watch this for regressions
make bench-network   # what RangeSize and Concurrency are worth — read this
```

## Things worth knowing before you change anything

**grab has no module dependencies, and should keep it that way.** The whole
package is standard library. `make tidy` fails the build if `go.mod` or
`go.sum` changes, so adding one is a deliberate act, not an accident.

**Every download is a series of byte ranges**, even the ones that are not
split. A plain download is a single range covering the whole file, requested
with no `Range` header at all. There is one transfer implementation and it has
no special cases for "simple" downloads — if you find yourself adding a second
path, that is a sign to stop and reconsider.

**`Client.Do` returns once the response headers have arrived.** It blocks
through the first request so that `HTTPResponse`, `Filename`, `Size` and
`CanResume` are all populated by the time the caller sees the `Response` — the
examples in the [README](../README.md) print them on the very next line. This
is not an error-handling decision: `Do` has no `error` return, and a failure is
always read from `Response.Err`. Blocking only means that `Err` does not block
in turn.

Those fields are written without a lock, which is safe only because the caller
is blocked while the state machine writes them. That is why the first range is
fetched synchronously by the state machine while the rest are fetched by
background workers. Do not move it.

**A checkpoint must never claim data that is not on disk.** See
[architecture.md](architecture.md#ordering). Getting
this backwards produces a corrupt file on the *next* run rather than this one,
which is about the worst failure mode available to us.

**Tests that share the local test server run shuffled.** Do not write a test
that depends on running before or after another one. Use `t.TempDir()` for
every file a test touches.
