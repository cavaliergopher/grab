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

// Get sends a file transfer request and returns a file transfer response
// context, following policy (e.g. redirects, cookies, auth) as configured on
// the client's HTTPClient.
//
// An error is returned if caused by client policy (such as CheckRedirect), or
// if there was an HTTP protocol error.
//
// Get blocks until a download request is completed or fails. For non-blocking
// operations which enable the monitoring of transfers in process, see GetAsync, GetBatch or use a Client.
//
// Get is a wrapper for DefaultClient.Do.
func Get(dst, urlStr string) (*Response, error) {
	req, err := NewRequest(dst, urlStr)
	if err != nil {
		return nil, err
	}

	resp := DefaultClient.Do(req)
	return resp, resp.Err() // resp.Err will block until complete
}

// GetBatch send multiple file transfer requests and returns a channel through
// which all Response transfer contexts will be returned.
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
