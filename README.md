# grab

[![Go Reference](https://pkg.go.dev/badge/github.com/cavaliergopher/grab/v3.svg)](https://pkg.go.dev/github.com/cavaliergopher/grab/v3) [![Build Status](https://github.com/cavaliergopher/grab/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/cavaliergopher/grab/actions/workflows/test.yml)

*Downloading the internet, one goroutine at a time!*

To use the package in your own program:

	$ go get github.com/cavaliergopher/grab/v3

To install the `grab` command line downloader:

	$ go install github.com/cavaliergopher/grab/v3/cmd/grab@latest

Grab is a Go package for downloading files from the internet with the following
rad features:

* Monitor download progress concurrently
* Auto-resume incomplete downloads
* Guess filename from content header or URL path
* Safely cancel downloads using context.Context
* Validate downloads using checksums
* Download batches of files concurrently
* Apply rate limiters

Requires Go v1.23+

## Example

The following example downloads a PDF copy of the free eBook, "An Introduction
to Programming in Go" into the current working directory.

```go
resp, err := grab.Get(".", "http://www.golang-book.com/public/pdf/gobook.pdf")
if err != nil {
	log.Fatal(err)
}

fmt.Println("Download saved to", resp.Filename)
```

The following, more complete example allows for more granular control and
periodically prints the download progress until it is complete.

The second time you run the example, it will auto-resume the previous download
and exit sooner.

```go
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/cavaliergopher/grab/v3"
)

func main() {
	// create client
	client := grab.NewClient()
	req, _ := grab.NewRequest(".", "http://www.golang-book.com/public/pdf/gobook.pdf")

	// start download
	fmt.Printf("Downloading %v...\n", req.URL())
	resp := client.Do(req)
	fmt.Printf("  %v\n", resp.HTTPResponse.Status)

	// start UI loop
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()

Loop:
	for {
		select {
		case <-t.C:
			fmt.Printf("  transferred %v / %v bytes (%.2f%%)\n",
				resp.BytesComplete(),
				resp.Size(),
				100*resp.Progress())

		case <-resp.Done:
			// download is complete
			break Loop
		}
	}

	// check for errors
	if err := resp.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Download failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Download saved to ./%v \n", resp.Filename)

	// Output:
	// Downloading http://www.golang-book.com/public/pdf/gobook.pdf...
	//   200 OK
	//   transferred 42970 / 2893557 bytes (1.49%)
	//   transferred 1207474 / 2893557 bytes (41.73%)
	//   transferred 2758210 / 2893557 bytes (95.32%)
	// Download saved to ./gobook.pdf
}
```

## Design trade-offs

The primary use case for Grab is to concurrently downloading thousands of large
files from remote file repositories where the remote files are immutable.
Examples include operating system package repositories or ISO libraries.

Grab aims to provide robust, sane defaults. These are usually determined using
the HTTP specifications, or by mimicking the behavior of common web clients like
cURL, wget and common web browsers.

Grab aims to be stateless. The only state that exists is the remote files you
wish to download and the local copy which may be completed, partially completed
or not yet created. The advantage to this is that the local file system is not
cluttered unnecessarily with addition state files (like a `.crdownload` file).
The disadvantage of this approach is that grab must make assumptions about the
local and remote state; specifically, that they have not been modified by
another program.

If the local or remote file are modified outside of grab, and you download the
file again with resuming enabled, the local file will likely become corrupted.
In this case, you might consider making remote files immutable, or disabling
resume.

There is one deliberate exception. Setting `Request.RangeSize` splits a download
into ranges that are fetched separately, and `Request.Concurrency` fetches
several of them at once. Because those ranges are written at their offset in the
file rather than in order, the length of a partial file no longer says which of
its bytes are valid. Such a transfer therefore writes a `.grab` file beside the
destination and removes it once the download finishes. Its presence is what
marks the file as written out of order, so that a later download neither resumes
from its length nor mistakes a file that reached full length with holes in it
for a finished one. Downloads that are not split write no such file.

Setting `Request.Durable` fills that file in. The transfer then records what it
has written about once a second, including the part of a range that has been
written but not yet finished, so an interruption costs roughly a second of
transfer regardless of how large the ranges are. Ranges can therefore be sized
for throughput without trading away progress. Recording progress means waiting
for it: everything transferred so far is flushed before each record is written,
so a durable download runs no faster than the destination accepts it. Without
`Durable` the file claims nothing and an interrupted download starts over.

`Request.Durable` is not only about splitting. It flushes any download as it
proceeds, so one that reports success has reached the disk rather than been
handed to the operating system to write out later. That costs throughput - the
download runs no faster than the device accepts data - and it is what allows a
failure to write to be reported at all.

That record also carries the `ETag` and `Last-Modified` of the remote file it
read those ranges from, and is discarded unless they still match. A split
download resumed against a file that has changed in the meantime starts over
rather than assembling a local copy out of two different remote files - which is
a stronger guarantee than an unsplit resume can make, as that has no choice but
to assume the remote file is unchanged.

Grab aims to enable best-in-class functionality for more complex features
through extensible interfaces, rather than reimplementation. For example,
you can provide your own Hash algorithm to compute file checksums, or your
own rate limiter implementation (with all the associated trade-offs) to rate
limit downloads.

## Development

Notes for working on grab itself are in [docs/](docs/):

* [Architecture](docs/architecture.md) — the transfer state machine, and the
  invariants that must not be broken
* [Range requests and throughput](docs/range-requests.md) — how to reason about
  `RangeSize` and `Concurrency`, with the measurements behind the rules of thumb
* [Testing and benchmarking](docs/testing.md) — the test server, and what the
  benchmarks are for

```bash
make check           # what CI runs
make bench           # what grab costs; watch for regressions
make bench-network   # what RangeSize and Concurrency are worth
```
