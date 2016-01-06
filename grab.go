/*
Package grab provides a HTTP client implementation specifically geared for
downloading large files with progress feedback, pause and resume and checksum
validation features.

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

If any HTTP response is one of the following redirect codes, grab follows the
redirect, up to a maximum of 10 redirects:

	301 (Moved Permanently)
	302 (Found)
	303 (See Other)
	307 (Temporary Redirect)

An error is returned if there were too many redirects or if there was an HTTP
protocol error.
*/
package grab

// Get tranfers a file from the specified source URL to the given destination
// path and returns the completed Response context.
//
// To make a request with custom headers, use NewRequest and DefaultClient.Do.
func Get(dst, src string) (*Response, error) {
	// init client and request
	req, err := NewRequest(src)
	if err != nil {
		return nil, err
	}

	req.Filename = dst

	return DefaultClient.Do(req)
}

// GetAsync tranfers a file from the specified source URL to the given
// destination path and returns the Response context.
//
// The Response is returned as soon as a HTTP/1.1 HEAD request has completed to
// determine the size of the requested file and supported server features.
//
// The Response may then be used to gauge the progress of the file transfer
// while it is in process.
//
// If an error occurs while initializing the request, it will be returned
// immediately. Any error which occurs during the file transfer will instead be
// set on the returned Response at the time which it occurs.
//
// To make a request with custom headers, use NewRequest and DefaultClient.Do.
func GetAsync(dst, src string) (*Response, error) {
	// init client and request
	req, err := NewRequest(src)
	if err != nil {
		return nil, err
	}

	req.Filename = dst

	return DefaultClient.DoAsync(req)
}
