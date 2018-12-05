package grab

import (
	"context"
	"fmt"
	"os"
)

// Get sends a HTTP request and downloads the content of the requested URL to
// the given destination file path. The caller is blocked until the download is
// completed, successfully or otherwise.
//
// An error is returned if caused by client policy (such as CheckRedirect), or
// if there was an HTTP protocol or IO error.
//
// For non-blocking calls or control over HTTP client headers, redirect policy,
// and other settings, create a Client instead.
func Get(dst, urlStr string) (*Response, error) {
	req, err := NewRequest(dst, urlStr)
	if err != nil {
		return nil, err
	}

	resp := DefaultClient.Do(req)
	return resp, resp.Err()
}

// GetBatch sends multiple HTTP requests and downloads the content of the
// requested URLs to the given destination directory using the given number of
// concurrent worker goroutines.
//
// The Response for each requested URL is sent through the returned Response
// channel, as soon as a worker receives a response from the remote server. The
// Response can then be used to track the progress of the download while it is
// in progress.
//
// The returned Response channel will be closed by Grab, only once all downloads
// have completed or failed.
//
// If an error occurs during any download, it will be available via call to the
// associated Response.Err.
//
// For control over HTTP client headers, redirect policy, and other settings,
// create a Client instead.
func GetBatch(workers int, dst string, urlStrs ...string) (<-chan *Response, error) {
	fi, err := os.Stat(dst)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("destination is not a directory")
	}

	reqs := make([]*Request, len(urlStrs))
	for i := 0; i < len(urlStrs); i++ {
		req, err := NewRequest(dst, urlStrs[i])
		if err != nil {
			return nil, err
		}
		reqs[i] = req
	}

	ch := DefaultClient.DoBatch(workers, reqs...)
	return ch, nil
}

// GetParallel is used to download large files in multiple chunks, where each chunk
// is downloaded in parallel through multiple HTTP requests.
func GetParallel(dst, urlStr string, chunkSize int64, workers int) (<-chan *Response, int, error) {
	req, err := NewRequest(dst, urlStr)
	if err != nil {
		return nil, 0, err
	}
	// cancel will be called on all code-paths via closeResponse
	ctx, cancel := context.WithCancel(req.Context())
	resp := &Response{
		Request: req,
		Done:    make(chan struct{}, 0),
		ctx:     ctx,
		cancel:  cancel,
	}
	// Get the size of the file with a HEAD request
	client := DefaultClient
	client.run(resp, client.headRequest)

	// The number of chunks the download file is being split into
	chunks := (resp.Size / chunkSize) + 1
	reqs := make([]*Request, chunks)

	// startByte and endByte determines the positions of the chunk that should be downloaded
	var startByte = int64(0)
	var endByte = chunkSize - 1

	var count = 0
	for startByte < resp.Size {
		req, err := NewRequest(dst, urlStr)
		if err != nil {
			return nil, 0, err
		}
		if endByte >= resp.Size {
			endByte = resp.Size - 1
		}
		rangeHeader := fmt.Sprintf("bytes=%d-%d", startByte, endByte)
		req.HTTPRequest.Header.Add("Range", rangeHeader)
		reqs[count] = req

		startByte = endByte + 1
		endByte += chunkSize
		count++
	}

	ch := client.DoBatch(workers, reqs...)
	return ch, count, nil
}
