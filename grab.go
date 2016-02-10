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
	"os"
)

// Get sends a file transfer request and returns a file transfer response
// context, following policy (e.g. redirects, cookies, auth) as configured on
// the client's HTTPClient.
//
// An error is returned if caused by client policy (such as CheckRedirect), or
// if there was an HTTP protocol error.
//
// Get is a synchronous, blocking operation which returns only once a download
// request is completed or fails. For non-blocking operations which enable the
// monitoring of transfers in process, see GetAsync, GetBatch or use a Client.
//
// Get is a wrapper for DefaultClient.Do.
func Get(dst, src string) (*Response, error) {
	// init client and request
	req, err := NewRequest(src)
	if err != nil {
		return nil, err
	}

	req.Filename = dst

	// execute with default client
	return DefaultClient.Do(req)
}

// GetAsync sends a file transfer request and returns a channel to receive the
// file transfer response context.
//
// The Response is sent via the returned channel and the channel closed as soon
// as the HTTP/1.1 GET request has been served; before the file transfer begins.
//
// The Response may then be used to monitor the progress of the file transfer
// while it is in process.
//
// Any error which occurs during the file transfer will be set in the returned
// Response.Error field as soon as the Response.IsComplete method returns true.
//
// GetAsync is a wrapper for DefaultClient.DoAsync.
func GetAsync(dst, src string) (<-chan *Response, error) {
	// init client and request
	req, err := NewRequest(src)
	if err != nil {
		return nil, err
	}

	req.Filename = dst

	// execute async with default client
	return DefaultClient.DoAsync(req), nil
}

// GetBatch executes multiple requests with the given number of workers and
// immediately returns a channel to receive the Responses as they become
// available. Excess requests are queued until a worker becomes available. The
// channel is closed once all responses have been sent.
//
// GetBatch requires that the destination path is an existing directory. If not,
// an error is returned which may be identified with IsBadDestination.
//
// If zero is given as the worker count, one worker will be created for each
// given request and all requests will start at the same time.
//
// Each response is sent through the channel once the request is initiated via
// HTTP GET or an error has occurred, but before the file transfer begins.
//
// Any error which occurs during any of the file transfers will be set in the
// associated Response.Error field as soon as the Response.IsComplete method
// returns true.
//
// GetBatch is a wrapper for DefaultClient.GetBatch.
func GetBatch(workers int, dst string, sources ...string) (<-chan *Response, error) {
	// default to current working directory
	if dst == "" {
		dst = "."
	}

	// check that dst is an existing directory
	fi, err := os.Stat(dst)
	if err != nil {
		return nil, err
	}

	if !fi.IsDir() {
		return nil, newGrabError(errBadDestination, "Destination path is not a directory")
	}

	// build slice of request
	reqs := make([]*Request, len(sources))
	for i := 0; i < len(sources); i++ {
		req, err := NewRequest(sources[i])
		if err != nil {
			return nil, err
		}

		req.Filename = dst

		reqs[i] = req
	}

	// execute batch with default client
	return DefaultClient.DoBatch(workers, reqs...), nil
}
