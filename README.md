# grab

[![GoDoc](https://godoc.org/github.com/cavaliercoder/grab?status.svg)](https://godoc.org/github.com/cavaliercoder/grab) [![Build Status](https://travis-ci.org/cavaliercoder/grab.svg)](https://travis-ci.org/cavaliercoder/grab) [![Go Report Card](https://goreportcard.com/badge/github.com/cavaliercoder/grab)](https://goreportcard.com/report/github.com/cavaliercoder/grab)

*Downloading the internet, one go routine at a time!*

	$ go get github.com/cavaliercoder/grab

Grab is a Go package for downloading files from the internet with the following
rad features:

* Monitor download progress asynchronously
* Auto-resume incomplete downloads
* Guess filename from content header or URL path
* Safely cancel downloads
* Validate downloads using checksums
* Download batches of files asynchronously

For a full walkthrough, see:
http://cavaliercoder.com/blog/downloading-large-files-in-go.html

Requires Go v1.4+


## Example

The following code can be used to create a cut-down 'wget'-like binary which
simply downloads each URL given on the command line to the current working
directory.

Files are downloaded three at a time with progress updates printed periodically.

```go
package main

import (
	"fmt"
	"github.com/cavaliercoder/grab"
	"os"
	"time"
)

func main() {
	// validate command args
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s url [url]...\n", os.Args[0])
		os.Exit(1)
	}

	// create a custom client
	client := grab.NewClient()
	client.UserAgent = "Grab example"

	// create request for each URL given on the command line
	reqs := make([]*grab.Request, 0)
	for _, url := range os.Args[1:] {
		req, err := grab.NewRequest(url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}

		reqs = append(reqs, req)
	}

	// start file downloads, 3 at a time
	fmt.Printf("Downloading %d files...\n", len(reqs))
	respch := client.DoBatch(3, reqs...)

	// start a ticker to update progress every 200ms
	t := time.NewTicker(200 * time.Millisecond)

	// monitor downloads
	completed := 0
	inProgress := 0
	responses := make([]*grab.Response, 0)
	for completed < len(reqs) {
		select {
		case resp := <-respch:
			// a new response has been received and has started downloading
			// (nil is received once, when the channel is closed by grab)
			if resp != nil {
				responses = append(responses, resp)
			}

		case <-t.C:
			// clear lines
			if inProgress > 0 {
				fmt.Printf("\033[%dA\033[K", inProgress)
			}

			// update completed downloads
			for i, resp := range responses {
				if resp != nil && resp.IsComplete() {
					// print final result
					if resp.Error != nil {
						fmt.Fprintf(os.Stderr, "Error downloading %s: %v\n", resp.Request.URL(), resp.Error)
					} else {
						fmt.Printf("Finished %s %d / %d bytes (%d%%)\n", resp.Filename, resp.BytesTransferred(), resp.Size, int(100*resp.Progress()))
					}

					// mark completed
					responses[i] = nil
					completed++
				}
			}

			// update downloads in progress
			inProgress = 0
			for _, resp := range responses {
				if resp != nil {
					inProgress++
					fmt.Printf("Downloading %s %d / %d bytes (%d%%)\033[K\n", resp.Filename, resp.BytesTransferred(), resp.Size, int(100*resp.Progress()))
				}
			}
		}
	}

	t.Stop()

	fmt.Printf("%d files successfully downloaded.\n", len(reqs))
}

```


## License

Copyright (c) 2015 Ryan Armstrong

Permission is hereby granted, free of charge, to any person obtaining a copy of
this software and associated documentation files (the "Software"), to deal in
the Software without restriction, including without limitation the rights to
use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
the Software, and to permit persons to whom the Software is furnished to do so,
subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
