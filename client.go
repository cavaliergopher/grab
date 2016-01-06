package grab

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sync/atomic"
	"time"
)

// A Client is an HTTP file transfer client. Its zero value is a usable client
// that uses http.Client defaults.
//
// Clients are safe for concurrent use by multiple goroutines.
type Client struct {
	HTTPClient *http.Client
	UserAgent  string
}

// NewClient returns a new file transfer Client, using default transport
// configuration.
func NewClient() *Client {
	return &Client{
		UserAgent: "grab",
		HTTPClient: &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			},
		},
	}
}

// Do sends a file transfer request and returns a file transfer response
// context, following policy (e.g. redirects, cookies, auth) as configured on
// the client's HTTPClient.
//
// An error is returned if caused by client policy (such as CheckRedirect), or
// if there was an HTTP protocol error.
func (c *Client) Do(req *Request) (*Response, error) {
	// prepare request with HEAD request
	resp, err := c.prepare(req)
	if err != nil {
		return resp, err
	}

	// transfer content
	if err := c.transfer(req, resp); err != nil {
		return resp, err
	}

	return resp, nil
}

func (c *Client) DoAsync(req *Request) (*Response, error) {
	// prepare request with HEAD request
	resp, err := c.prepare(req)
	if err != nil {
		return resp, err
	}

	// transfer content
	go c.transfer(req, resp)

	return resp, nil
}

// prepare creates a Response context for the given request using a HTTP HEAD
// request to the remote server.
func (c *Client) prepare(req *Request) (*Response, error) {
	// create a response
	resp := &Response{
		Request: req,
	}

	// default to current working directory
	if req.Filename == "" {
		req.Filename = "."
	}

	// see if file is a directory
	needFilename := false
	if fi, err := os.Stat(req.Filename); err == nil {
		// file exists - is it a directory?
		if fi.IsDir() {
			// destination is a directory - compute a file name
			needFilename = true
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	if !needFilename {
		resp.Filename = req.Filename
	}

	// set user agent string
	if c.UserAgent != "" && req.HTTPRequest.Header.Get("User-Agent") == "" {
		req.HTTPRequest.Header.Set("User-Agent", c.UserAgent)
	}

	// switch the request to HEAD method
	method := req.HTTPRequest.Method
	req.HTTPRequest.Method = "HEAD"

	// get file metadata
	if hresp, err := c.HTTPClient.Do(req.HTTPRequest); err == nil && (hresp.StatusCode >= 200 && hresp.StatusCode < 300) {
		// is a known size set and a size returned in the HEAD request?
		if req.Size == 0 && hresp.ContentLength > 0 {
			resp.Size = uint64(hresp.ContentLength)
		} else if req.Size > 0 && hresp.ContentLength > 0 && req.Size != uint64(hresp.ContentLength) {
			return nil, errorf(errBadLength, "Bad content length: %d, expected %d", hresp.ContentLength, req.Size)
		}

		// does server supports resuming downloads?
		if hresp.Header.Get("Accept-Ranges") == "bytes" {
			resp.canResume = true
		}

		// TODO: get filename from Content-Disposition header
	}

	// reset request
	req.HTTPRequest.Method = method

	// compute filename from URL if still needed
	if needFilename {
		filename := path.Base(req.HTTPRequest.URL.Path)
		if filename == "" {
			return nil, errorf(errNoFilename, "No filename could be determined")
		} else {
			// update filepath with filename from URL
			resp.Filename = filepath.Join(req.Filename, filename)
		}
	}

	return resp, nil
}

// transfer initiates a file transfer for a prepared Response context.
func (c *Client) transfer(req *Request, resp *Response) error {
	// start timer - duration includes file seeking and HEAD request
	resp.Start = time.Now()

	// open destination for writing
	f, err := os.OpenFile(resp.Filename, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return resp.setError(err)
	}

	// seek to the start of the file
	resp.bytesTransferred = 0
	if _, err := f.Seek(0, 0); err != nil {
		return resp.setError(err)
	}

	// attempt to resume previous download (if any)
	if resp.canResume {
		if fi, err := f.Stat(); err != nil {
			resp.Error = err
			return resp.setError(err)
		} else if fi.Size() > 0 {
			// seek to end of file
			if _, err = f.Seek(0, os.SEEK_END); err != nil {
				return resp.setError(err)
			} else {
				atomic.AddUint64(&resp.bytesTransferred, uint64(fi.Size()))

				// set byte range header in next request
				req.HTTPRequest.Header.Set("Range", fmt.Sprintf("bytes=%d-", fi.Size()))
			}
		}
	}

	// skip if already downloaded
	if resp.Size > 0 && resp.Size == resp.bytesTransferred {
		return nil
	}

	// request content
	if hresp, err := c.HTTPClient.Do(req.HTTPRequest); err != nil {
		return resp.setError(err)
	} else {
		// TODO: Validate response code

		// validate content length
		if resp.Size > 0 && resp.Size != (resp.bytesTransferred+uint64(hresp.ContentLength)) {
			return resp.setError(errorf(errBadLength, "Bad content length: %d, expected %d", hresp.ContentLength, resp.Size-resp.bytesTransferred))
		}

		// download and update progress
		var buffer [4096]byte
		for {
			// read HTTP stream
			n, err := hresp.Body.Read(buffer[:])
			if err != nil && err != io.EOF {
				return resp.setError(err)
			}

			// increment progress
			atomic.AddUint64(&resp.bytesTransferred, uint64(n))

			// write to file
			if _, werr := f.Write(buffer[:n]); werr != nil {
				return resp.setError(werr)
			}

			// break when finished
			if err == io.EOF {
				break
			}
		}
	}

	resp.End = time.Now()

	return nil
}
