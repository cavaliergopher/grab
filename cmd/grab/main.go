package main

import (
	"fmt"
	"os"
	"time"

	"github.com/cavaliercoder/grab"
)

func main() {
	// validate command args
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s [url]...\n", os.Args[0])
		os.Exit(1)
	}
	urls := os.Args[1:]

	// start file downloads, 3 at a time
	fmt.Printf("Downloading %d files...\n", len(urls))
	respch, err := grab.GetBatch(3, ".", urls...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	// monitor downloads
	responses := make([]*grab.Response, 0, len(urls))
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()

Loop:
	for {
		select {
		case resp := <-respch:
			if resp != nil {
				// a new response has been received and has started downloading
				responses = append(responses, resp)
			} else {
				// channel is closed - all downloads are complete
				updateUI(responses)
				break Loop
			}

		case <-t.C:
			// update UI every 200ms
			updateUI(responses)
		}
	}

	fmt.Printf("%d files downloaded.\n", len(urls))
}

// inProgress tracks the number of incomplete downloads currently printed to the
// terminal
var inProgress = 0

// updateUI prints the progress of all downloads to the terminal
func updateUI(responses []*grab.Response) {
	// clear lines for incomplete downloads
	if inProgress > 0 {
		fmt.Printf("\033[%dA\033[K", inProgress)
	}

	// print newly completed downloads
	for i, resp := range responses {
		if resp != nil && resp.IsComplete() {
			if resp.Err() != nil {
				fmt.Fprintf(os.Stderr, "Error downloading %s: %v\n",
					resp.Request.URL(),
					resp.Err())
			} else {
				fmt.Printf("Finished %s %d / %d bytes (%d%%)\n",
					resp.Filename,
					resp.BytesComplete(),
					resp.Size,
					int(100*resp.Progress()))
			}
			responses[i] = nil
		}
	}

	// print progress for incomplete downloads
	inProgress = 0
	for _, resp := range responses {
		if resp != nil {
			fmt.Printf("Downloading %s %d / %d bytes (%d%%) - %.02fKBp/s ETA: %ds \033[K\n",
				resp.Filename,
				resp.BytesComplete(),
				resp.Size,
				int(100*resp.Progress()),
				resp.BytesPerSecond()/1024,
				int64(resp.ETA().Sub(time.Now()).Seconds()))
			inProgress++
		}
	}
}
