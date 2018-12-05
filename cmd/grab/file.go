package main

import (
	"fmt"
	"os"
	"time"

	"github.com/cavaliercoder/grab"
)

func main() {
	// get URL to download from command args
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s url\n", os.Args[0])
		os.Exit(1)
	}

	url := os.Args[1]
	var chunkSize = 1024 * 1024 * 10 //10MB

	// download file
	fmt.Printf("Downloading %s...\n", url)

	start := time.Now()
	respch, chunks, err := grab.GetParallel(".", url, int64(chunkSize), 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error downloading %s: %v\n", url, err)
		os.Exit(1)
	}

	responses := make([]*grab.Response, 0, chunks)
	t := time.NewTicker(500 * time.Millisecond)
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
				updateConsole(responses)
				break Loop
			}

		case <-t.C:
			// update UI every 200ms
			updateConsole(responses)
		}
	}

	fmt.Println("Download finished")
	elapsed := time.Since(start)
	fmt.Printf("Download took %s", elapsed)
}

// updateConsole prints the progress of the download to the terminal
func updateConsole(responses []*grab.Response) {
	// print progress for incomplete downloads
	var downloadedBytes int64 = 0
	var size int64 = 0
	for _, resp := range responses {
		downloadedBytes += resp.BytesComplete()
		size += resp.Size
	}

	if size != 0 {
		fmt.Printf("Downloading %d/%d bytes\n", downloadedBytes, size)
	}
}
