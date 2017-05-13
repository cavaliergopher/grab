/*
Package grab provides a HTTP client implementation specifically geared for
downloading large files with progress feedback, pause and resume and checksum
validation features.

For a full walkthrough, see:
http://cavaliercoder.com/blog/downloading-large-files-in-go.html

Please log any issues at:
https://github.com/cavaliercoder/grab/issues

If the given destination path for a transfer request is a directory, the file
transfer will be stored in that directory and the file's name will be determined
using Content-Disposition headers in the server's response or from the last
segment of the path of the URL.

An empty destination string or "." means the transfer will be stored in the
current working directory.

If a destination file already exists, grab will assume it is a complete or
partially complete download of the requested file. If the remote server supports
resuming interrupted downloads, grab will resume downloading from the end of the
partial file. If the server does not support resumed downloads, the file will be
retransferred in its entirety. If the file is already complete, grab will return
successfully.
*/
package grab

import (
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
