# Range requests and throughput

`Request.RangeSize` splits a download into ranges fetched with separate
requests; `Request.Concurrency` fetches several at once.

Modelled numbers come from `make bench-network`; measured ones are marked as
such under [Testing](#testing). All were taken on one machine — reproduce them
before relying on them.

## Ranges cost a round trip each

A worker cannot ask for its next range until it has finished the last one, so
every range costs one round trip before its first byte arrives. Over a
connection carrying `B` bytes per second with a round trip of `RTT`, a transfer
reaches about

```
1 / (1 + BDP/RangeSize)      where BDP = B × RTT
```

of the throughput it would reach with arbitrarily large ranges. `BDP` is the
bandwidth-delay product — how many bytes are in flight on the wire at once.

Measured over a 4 MiB file, 20 ms round trip, 16 MiB/s per connection,
`Concurrency: 4`. The bandwidth-delay product is 320 KiB:

| `RangeSize` | requests | throughput | |
| --- | --- | --- | --- |
| unset (unsplit) | 1 | 15.5 MB/s | one connection, so one budget |
| 2 MiB | 3 | 22.3 MB/s | |
| 1 MiB | 5 | **33.4 MB/s** | best: about 3× the BDP |
| 256 KiB | 17 | 22.6 MB/s | below the BDP, losing half |
| 64 KiB | 65 | 10.0 MB/s | **worse than not splitting** |
| 32 KiB | 129 | 5.7 MB/s | | 

Rule of thumb: **keep `RangeSize` well above the bandwidth-delay product.** Ten
times it costs about a tenth of the throughput; matching it costs half. Some
products, to calibrate:

| Path | Per-connection rate | RTT | BDP |
| --- | --- | --- | --- |
| Same datacenter | 100 MB/s | 1 ms | 100 KiB |
| Across a continent | 4 MiB/s | 50 ms | 200 KiB |
| Intercontinental | 4 MiB/s | 200 ms | 800 KiB |
| Fast link, high latency | 100 MB/s | 100 ms | 10 MiB |

## Concurrency depends on where the bottleneck is

Measured over the same file with 512 KiB ranges, comparing protocols against a
server that meters **each TCP connection**:

| `Concurrency` | HTTP/1.1 | connections | HTTP/2 | connections |
| --- | --- | --- | --- | --- |
| 1 | 9.8 MB/s | 1 | 9.8 MB/s | 1 |
| 2 | 17.2 MB/s | 2 | 14.3 MB/s | 1 |
| 4 | 28.1 MB/s | 15 | 14.4 MB/s | 1 |
| 8 | 42.0 MB/s | 38 | 14.3 MB/s | 1 |

Over HTTP/1.1 each range in flight has its own connection and its own budget,
so throughput scales with `Concurrency`. Over HTTP/2 every range is multiplexed
onto one connection and shares one congestion window, so throughput converges
on that connection's limit. The connection counts show it directly.

HTTP/2 still gains from `Concurrency: 2`, since a range waiting on its round
trip is overlapped by another transferring, but there is nothing left to
overlap beyond that.

Concurrency is not useless over HTTP/2. Some servers and CDNs meter each
*stream* rather than each connection, and against those it multiplies as
HTTP/1.1 does. A client cannot tell the two apart, so grab does not cap
`Concurrency` when it negotiates HTTP/2.

## Durability

Every split transfer writes a checkpoint file beside the destination. Its
presence marks the file as written out of order: ranges land at their offset,
so a partial file's length says nothing about which of its bytes are valid, and
one can reach full length with holes in it. A later transfer that finds the
checkpoint starts over rather than resuming from that length.

`Durable` decides whether the checkpoint also records progress. With it,
workers report what they have written as they go, not only when a range
finishes; once per second the destination is flushed and the record rewritten.
An interruption then costs about a second of transfer, whatever `RangeSize` and
`Concurrency` are set to. Smaller ranges do not make it cheaper, and cost
throughput — see the table above.

Without it the checkpoint is written once and left alone, claiming nothing. The
transfer never flushes, and an interruption costs everything it had written.

The checkpoint is removed on completion and left in place on failure. Format
and ordering rules are in [architecture.md](architecture.md#the-checkpoint).

`Durable` is not only about splitting. A transfer that is not split has no
progress to record — it writes its file in order, so its length is its
progress — but it is still flushed on the same interval, so `Response.Err`
waits for the data and can report a failure to write it.

A checksum, if one is set, is computed by re-reading the destination after the
transfer rather than from the bytes as they passed through. Under `Durable`
those bytes have already been flushed, so a write that failed is caught before
the checksum rather than after it. The read itself may still be served from the
page cache, so a matching checksum tells you the right bytes were written, not
that the medium was read back.

## Testing

For the test suite and the benchmarks, see [testing.md](testing.md). This
section is about the other kind of testing: whether any of the above survives
contact with a real server.

Everything above was measured against a simulated network. To check it, eight
downloads of `ubuntu-24.04.3-live-server-amd64.iso` (3,303,444,480 bytes) were
run through the `grab` CLI against two public mirrors, each verified against the
SHA-256 Ubuntu publishes. Three configurations — unsplit, 16 MiB ranges with
four workers, and 1 MiB ranges with sixteen — over both HTTP versions.

The two mirrors were chosen to sit on opposite sides of the only question that
decides whether concurrency can help: where the bottleneck is.

| mirror | regime | one connection |
| --- | --- | --- |
| `mirror.arizona.edu`, RTT ~51 ms | meters each connection | 43 MB/s |
| `mirror.us.leaseweb.net`, RTT ~11 ms | outruns the client link | 109 MB/s |

Against the per-connection limited mirror:

| RangeSize | Concurrency | requests | HTTP/1.1 | HTTP/2 |
| --- | --- | --- | --- | --- |
| unset | 1 | 1 | 42.6 MB/s | 43.3 MB/s |
| 16 MiB | 4 | 198 | **62.8 MB/s** | 41.3 MB/s |
| 1 MiB | 16 | 3,152 | **62.6 MB/s** | 41.7 MB/s |

Four things came out of it.

- All eight downloads produced a byte-identical file, including the one that
  reassembled 3,152 ranges arriving out of order. None left a checkpoint
  behind.
- Four workers over HTTP/1.1 open four connections and run 1.48x faster. The
  same settings over HTTP/2 are four streams on one connection and match a
  single request; sixteen workers change nothing.
- At equal concurrency, 1 MiB ranges matched 16 MiB ranges to within a percent
  despite sixteen times the requests, and despite 1 MiB sitting below that
  mirror's ~2.2 MB bandwidth-delay product. The BDP bounds what *one* worker
  loses; enough workers hide it.
- Against the fast mirror one connection already reached 108.8 MB/s, and
  sixteen workers with 3,152 requests made it slower, at 97.1 MB/s. With no
  headroom to win, the extra requests are pure cost.

### Again, from EC2

The home run left one question open: when the per-connection mirror stopped
improving at ~62 MB/s, was that the mirror or the client link? Repeating the
matrix from an EC2 instance in `us-east-1` answered it — the ceiling was the
client link.

| Concurrency | from a gigabit home link | from EC2 |
| --- | --- | --- |
| 1 | 42.6 MB/s | 43.5 MB/s |
| 4 | 62.8 MB/s | 73.6 MB/s |
| 16 | 62.6 MB/s | 101.0 MB/s |
| 64 | not run | 179.0 MB/s |

Over HTTP/2 the same sweep ran 49.0, 47.2, 38.9, 37.5 MB/s — getting worse as
concurrency rises, since more ranges on one connection means more round trips
with nothing to overlap them. At 64 workers the faster mirror
stopped serving and returned `503 Service Unavailable` on both protocols.
There is a point past which concurrency is all downside and a little bit rude.

### Durability costs disk bandwidth

These runs predate `Durable`, which did not exist when they were measured;
every split transfer flushed. Read the split rows as durable ones, and the
unsplit rows as any transfer that does not flush — which is now also a split
transfer with `Durable` unset.

Against the fast mirror every split configuration landed within 0.1% of the
same rate — 131.07, 130.94 and 131.01 MB/s across two protocols and two
concurrency levels — while an unsplit download of the same file ran at
268 MB/s. Serving the same payload from `localhost` reproduces it with no
network involved:

| configuration | to an EBS volume | to tmpfs |
| --- | --- | --- |
| unsplit | 163–418 MB/s | 1554–1613 MB/s |
| 16 MiB ranges, 4 workers | 132–201 MB/s | 2504–2543 MB/s |
| 1 MiB ranges, 16 workers | 134 MB/s | 2434–2501 MB/s |

The difference is the flush. A durable transfer flushes the destination once
per `checkpointInterval` so the record cannot claim data the file system has
not committed — see [architecture.md](architecture.md#ordering). Every other
transfer never flushes.

The two columns measure different work. 163 and 418 MB/s are one configuration
measured twice, and 418 MB/s is four times what the volume can absorb: the
3.3 GB file fits in the host's 16 GB of memory, so it landed in page cache and
the kernel wrote it out after the measurement stopped. The unsplit figure is
the rate into memory with the disk still owed the data; the checkpointed figure
is the rate with the data on disk. 131 MB/s also beats this volume's
synchronous write rate of 105 MB/s (`dd conv=fdatasync`), because grab overlaps
flushing with transferring.

Provisioning the volume confirms it. The same localhost matrix against gp3 at
1000 MB/s instead of the default 125:

| | volume, `dd conv=fdatasync` | grab, checkpointed |
| --- | --- | --- |
| gp3 at 125 MB/s | 135 MB/s | 132–177 MB/s |
| gp3 at 1000 MB/s | 378 MB/s | 347–399 MB/s |

Checkpointed throughput tracks the volume's synchronous write rate about one
for one, so there is no overhead to remove. Nor does the flushing cadence add
any: grab flushing once a second matches `dd` with a single flush at the end
(132–177 against 135 MB/s, and 347–399 against 378), and halving the flushes
would only double the bytes in each.

What separates the two columns is what the transfer waits for, not how much
disk work it causes. Both write the same bytes to the same device at the same
rate. A transfer that does not flush returns once the last byte reaches the
page cache and lets the kernel write it out afterwards; a durable one waits.
Leaving `Durable` unset therefore does not make the file durable any sooner —
it stops `Response.Err` from waiting for it, and gives up knowing when the data
is safe.

The illusion also has a limit. It holds only while the file fits in memory;
beyond that the kernel's dirty page limits throttle the writer and an unsplit
transfer converges on the same rate.

So the only lever that shortens time-to-durable is faster storage. For scale,
grab sustained 2.5 GB/s to tmpfs while issuing 3,151 range requests.

Two cautions if you repeat these measurements. Mirror throughput drifts — the
same mirror served between 25 and 43 MB/s on one connection within a single
session — so run the configurations you mean to compare back to back. And
check that a distributor's published checksum describes the file it serves:
Rocky Linux's `CHECKSUM` claims 1,480,048,640 bytes for an ISO it serves at
2,755,067,904.

## Choosing values

- **Leave both unset** unless you know the transfer is large and the path is
  worth parallelising. An unsplit transfer is one request and no checkpoint
  file, and that is the right default.
- **`RangeSize`**: at least ten times the bandwidth-delay product of one
  connection. A megabyte is a reasonable starting point for most internet
  paths; raise it on fast, high-latency links.
- **`Durable`**: set it when losing the transfer would matter more than
  finishing it quickly. A durable transfer runs at the rate the destination
  accepts data rather than the rate the network delivers it — the difference
  between the two columns above.
- **`Concurrency`**: as high as the server tolerates if the limit is per
  connection, 2–4 if you are talking to an HTTP/2 origin that meters per
  connection. Remember `Request.BufferSize` is allocated per range in flight,
  and that `Client.DoBatch` workers multiply with it.
- **Measure.** `make bench-network` is a model, not your network.

## Why this shape

Decisions that are easy to want to revisit:

- **`RangeSize` splits unconditionally.** It is a size, not a hint. A transfer
  either splits into ranges of that size or, if it cannot, is a single range.
- **The checkpoint is written even without `Durable`.** It costs one write at
  the start of the transfer, and it is the only thing marking the destination
  as written out of order. Without it nothing distinguishes a file abandoned
  mid-split from a resumable or a finished one, and a later download resumes
  from its length onto holes. What `Durable` buys is the progress recorded in
  it, and the flush that has to precede each update.
- **The default is one range, not one range per worker.** Fixed-size ranges
  fill the file front to back at a predictable rate, and let the number of
  requests scale with the file rather than with a worker count that has nothing
  to do with it.
- **grab does not special-case HTTP/2.** See above.
- **Checkpointing is on an interval, not per range.** A checkpoint costs two
  flushes, about 9ms (`BenchmarkCheckpointStore`). Doing it per range would
  make a fast link with small ranges pay it hundreds of times a second, which
  is the opposite of what either throughput or durability wants.
