# Architecture

## Where things live

| File | Responsibility |
| --- | --- |
| `client.go` | The state machine that drives a transfer from request to closed response |
| `request.go` | `Request` — everything the caller configures |
| `response.go` | `Response` — everything the caller observes, plus the in-memory `NoStore` destination |
| `transfer.go` | Moving bytes: the worker pool, the per-range copy loop, and the checkpointer |
| `checkpoint.go` | The interval set and the on-disk checkpoint format |
| `util.go` | HTTP header helpers: `Range`, `Content-Range`, `Last-Modified`, filename guessing |
| `pkg/grabtest` | The test server: byte ranges, validators, failure injection, assertions |

## The state machine

`Client.Do` runs the machine synchronously until the transfer is ready to move
bytes, then hands the rest to a goroutine. Each state is a `stateFunc` that
mutates the `Response` and returns the next one, or nil to stop.

```
statFileInfo ──> validateLocal ──> planTransfer ──> getRequest ──> readResponse ──> openWriter
     │                 │                ▲                              │              │
     │                 │                │                              │              │ (returns nil;
     └──> headRequest ─┴────────────────┘                              │              │  Do returns here)
              │                                                        │              ▼
              └────────────> readResponse ─(HEAD)─> statFileInfo <──────┘          copyFile
                                                                                      │
                                                                                      ▼
                                                                              checksumFile ──> closeResponse
```

- **`statFileInfo`** looks for an existing destination file.
- **`validateLocal`** decides whether it is already complete, too large, or
  worth resuming. When the transfer could be split and a checkpoint exists, it
  defers to `planTransfer` — the length of a partial file means nothing for a
  split transfer, since a transfer whose last range finished leaves a file of
  the full length with a hole in it.
- **`headRequest`** is normally an optimisation and is skipped when it would
  tell us nothing. A transfer that may be split always sends it, because it
  cannot plan ranges without the size of the file and the server's
  `Accept-Ranges`.
- **`planTransfer`** decides the ranges. This is the only place that decides
  what gets downloaded.
- **`getRequest`** issues the request for the *first* range, synchronously.
- **`readResponse`** establishes the size, resolves the filename, and after a
  HEAD loops back to `statFileInfo`.
- **`openWriter`** opens the destination and builds the `transfer`. It returns
  nil, which is where `Client.Do` returns to the caller.
- **`copyFile`** runs in a goroutine and does the actual work.

## Planning ranges

`planTransfer` produces `Response.ranges`, and everything downstream just
executes them.

A transfer is split only if `Response.canSplit()`: `RangeSize > 0`, not
`NoStore`, the server advertised `Accept-Ranges: bytes`, and the file is larger
than one range. Otherwise the plan is a single range covering whatever remains,
which reproduces exactly what grab did before ranges existed:

| Situation | Plan | `Range` header |
| --- | --- | --- |
| Fresh download | `[0, ∞)` | none |
| Resuming an unsplit download | `[n, ∞)` | `bytes=n-` |
| Split, fresh | `RangeSize` sized ranges from 0 | `bytes=0-…` per range |
| Split, resuming | the ranges the checkpoint does not cover | per range |

The open-ended `∞` is deliberate: asking for everything from an offset rather
than up to the size the server last reported means a file that has grown is
caught by the length check rather than silently truncated.

Ranges are dispatched to workers **in ascending order**, so the destination
fills from the front. That is what keeps the checkpoint's completed set small —
usually a single interval — and what makes a partially downloaded file useful.

## Moving bytes

`transfer.copy` starts `Concurrency` workers reading range indexes off a
channel. A worker that fails records its error *before* cancelling the shared
context, so the failure that stopped the transfer is what gets reported rather
than the cancellation it causes in every other worker.

Each range is written with `WriteAt` at its offset. The destination is
therefore never opened `O_APPEND` — on some systems that forces every write to
the end of the file whatever offset it was given, which would silently corrupt
every split transfer.

`Request.NoStore` writes to `bufferAt`, an in-memory `io.WriterAt`, so it takes
the same path as everything else rather than being a special case.

## The checkpoint

A split transfer records what it has written in `<filename>.grab`, so an
interrupted one can resume. It is removed when the transfer completes, and
deliberately left behind when it fails.

```json
{
  "version": 1,
  "url": "https://example.com/file.iso",
  "size": 1073741824,
  "etag": "\"a1b2c3\"",
  "lastModified": "Mon, 02 Jan 2006 15:04:05 GMT",
  "rangeSize": 1048576,
  "complete": [[0, 8388608], [10485760, 11534336]]
}
```

On resume the checkpoint is used only if `version`, `url`, `size`, `rangeSize`
and the validators all still match; otherwise it is deleted and the transfer
starts over. This makes a resumed split transfer *safer* than a resumed unsplit
one, which has no choice but to assume the remote file is unchanged.

Workers report what they have written as they go, not only when a range
finishes, and the checkpointer writes once per `checkpointInterval` (one
second). That decouples how much an interruption costs from `RangeSize`, which
is chosen for throughput — see [range-requests.md](range-requests.md).

### Ordering

The destination file is flushed before the checkpoint describing it is renamed
into place, and the completed set is snapshotted before that flush.

A checkpoint must never name data the filesystem has not committed. One that
understates costs a refetch. One that overstates makes a resumed transfer skip
ranges that were never written, producing a corrupt file that passes unnoticed
unless the caller set a checksum.

Snapshotting before the flush keeps the record within what the flush covers.
Anything written after it waits for the next checkpoint. `checkpoint.store`
therefore takes a `*rangeSet` the caller has already cloned, and
`checkpointer.store` clones under the mutex before calling it.

The checkpoint itself is written to a sibling temp file and renamed into place.
Readers see either the new checkpoint or the previous one, never a partial
write, and the directory entry is replaced in a single operation for
filesystems that need the parent inode updated atomically.

A transfer that *finished* skips the final checkpoint entirely: `copyFile`
deletes the checkpoint on the next line, and writing one first costs two
flushes — around 9ms, per `BenchmarkCheckpointStore` — for nothing.
