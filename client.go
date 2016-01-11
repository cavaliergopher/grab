package grab

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// A Client is an HTTP file transfer client. Its zero value is a usable client
// that uses http.Client defaults.
//
// Clients are safe for concurrent use by multiple goroutines.
type Client struct {
	// HTTPClient specifies the http.Client which will be used for communicating
	// with the remote server during the file transfer.
	HTTPClient *http.Client

	// UserAgent specifies the User-Agent string which will be set in the
	// headers of all requests made by this client.
	UserAgent string
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

// DefaultClient is the default client and is used by Get.
var DefaultClient = NewClient()

// Do sends a file transfer request and returns a file transfer response
// context, following policy (e.g. redirects, cookies, auth) as configured on
// the client's HTTPClient.
//
// An error is returned if caused by client policy (such as CheckRedirect), or
// if there was an HTTP protocol error.
func (c *Client) Do(req *Request) (*Response, error) {
	// prepare request with HEAD request
	resp, err := c.do(req)
	if err != nil {
		return resp, err
	}

	// transfer content
	if !resp.IsComplete() {
		if err := resp.copy(); err != nil {
			return resp, err
		}
	}

	return resp, nil
}

// DoAsync sends a file transfer request and returns a channel to receive the
// file transfer response context.
//
// The Response is sent via the returned channel and the channel closed as soon
// as the HTTP/1.1 GET request has been served; before the file transfer begins.
//
// The Response may then be used to gauge the progress of the file transfer
// while it is in process.
//
// Any error which occurs during the file transfer will be set in the returned
// Response.Error field at the time which it occurs.
func (c *Client) DoAsync(req *Request) <-chan *Response {
	r := make(chan *Response, 0)
	go func() {
		// prepare request with HEAD request
		resp, err := c.do(req)
		if err == nil && !resp.IsComplete() {
			// transfer data in new goroutine
			go func() {
				resp.copy()
			}()
		}

		r <- resp
		close(r)
	}()

	return r
}

// DoBatch executes multiple requests with the given number of workers and
// returns a channel to receive the file transfer response contexts. The channel
// is closed once all responses have been received.
//
// Each response is sent through the channel once the request is initiated via
// HTTP GET or an error has occurred but before the file transfer begins.
//
// Any error which occurs during any of the file transfers will be set in the
// associated Response.Error field.
func (c *Client) DoBatch(reqs Requests, workers int) <-chan *Response {
	// TODO: enable cancelling of batch jobs

	responses := make(chan *Response, workers)
	workerDone := make(chan bool, workers)

	// start work queue
	producer := make(chan *Request, 0)
	go func() {
		// feed queue
		for i := 0; i < len(reqs); i++ {
			producer <- reqs[i]
		}
		close(producer)

		// close channel when all workers are finished
		for i := 0; i < workers; i++ {
			<-workerDone
		}
		close(responses)
	}()

	// start workers
	for i := 0; i < workers; i++ {
		go func(i int) {
			// work until producer is dried up
			for req := range producer {
				// set up notifier
				req.notifyOnCloseInternal = make(chan *Response, 1)

				// start request
				resp := <-c.DoAsync(req)

				// ship state to caller
				responses <- resp

				// wait for async op to finish before moving to next request
				<-req.notifyOnCloseInternal
			}

			// signal worker is done
			workerDone <- true
		}(i)
	}

	return responses
}

// do creates a Response context for the given request using a HTTP HEAD
// request to the remote server.
func (c *Client) do(req *Request) (*Response, error) {
	// create a response
	resp := &Response{
		Request: req,
		Start:   time.Now(),
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
		return resp, resp.close(err)
	}

	if !needFilename {
		resp.Filename = req.Filename
	}

	// default write flags
	wflags := os.O_CREATE | os.O_WRONLY

	// set user agent string
	if c.UserAgent != "" && req.HTTPRequest.Header.Get("User-Agent") == "" {
		req.HTTPRequest.Header.Set("User-Agent", c.UserAgent)
	}

	// switch the request to HEAD method
	method := req.HTTPRequest.Method
	req.HTTPRequest.Method = "HEAD"

	// get file metadata
	if hresp, err := c.HTTPClient.Do(req.HTTPRequest); err == nil && (hresp.StatusCode >= 200 && hresp.StatusCode < 300) {
		// allow caller to see response if error occurs before final transfer
		resp.HTTPResponse = hresp

		// is a known size set and a size returned in the HEAD request?
		if req.Size == 0 && hresp.ContentLength > 0 {
			resp.Size = uint64(hresp.ContentLength)
		} else if req.Size > 0 && hresp.ContentLength > 0 && req.Size != uint64(hresp.ContentLength) {
			return resp, resp.close(newGrabError(errBadLength, "Bad content length in HEAD response: %d, expected %d", hresp.ContentLength, req.Size))
		}

		// does server supports resuming downloads?
		if hresp.Header.Get("Accept-Ranges") == "bytes" {
			resp.canResume = true
			wflags |= os.O_APPEND
		}

		// get filename from Content-Disposition header
		if needFilename {
			if cd := hresp.Header.Get("Content-Disposition"); cd != "" {
				if _, params, err := mime.ParseMediaType(cd); err == nil {
					if filename, ok := params["filename"]; ok {
						resp.Filename = filename
						needFilename = false
					}
				}
			}
		}
	}

	// reset request
	req.HTTPRequest.Method = method

	// compute filename from URL if still needed
	if needFilename {
		if req.HTTPRequest.URL.Path == "" || strings.HasSuffix(req.HTTPRequest.URL.Path, "/") {
			return resp, resp.close(newGrabError(errNoFilename, "No filename could be determined"))
		} else {
			// update filepath with filename from URL
			resp.Filename = filepath.Join(req.Filename, path.Base(req.HTTPRequest.URL.Path))
			needFilename = false
		}
	}

	// open destination for writing
	f, err := os.OpenFile(resp.Filename, wflags, 0644)
	if err != nil {
		return resp, resp.close(err)
	}
	resp.writer = f

	// seek to the start of the file
	resp.bytesTransferred = 0
	if _, err := f.Seek(0, 0); err != nil {
		return resp, resp.close(err)
	}

	// attempt to resume previous download (if any)
	if resp.canResume {
		if fi, err := f.Stat(); err != nil {
			resp.Error = err
			return resp, resp.close(err)

		} else if uint64(fi.Size()) > resp.Size {
			return resp, resp.close(newGrabError(errBadLength, "Exising file is larger than remote"))

		} else if fi.Size() > 0 {
			// seek to end of file
			if _, err = f.Seek(0, os.SEEK_END); err != nil {
				return resp, resp.close(err)
			} else {
				atomic.AddUint64(&resp.bytesTransferred, uint64(fi.Size()))

				// set byte range header in next request
				req.HTTPRequest.Header.Set("Range", fmt.Sprintf("bytes=%d-", fi.Size()))
			}
		}
	}

	// skip if already downloaded
	if resp.Size > 0 && resp.Size == resp.bytesTransferred {
		return resp, resp.close(nil)
	}

	// request content
	if hresp, err := c.HTTPClient.Do(req.HTTPRequest); err != nil {
		return resp, resp.close(err)
	} else {
		// set HTTP response in transfer context
		resp.HTTPResponse = hresp

		// TODO: Validate HTTP response codes

		// validate content length
		if resp.Size > 0 && resp.Size != (resp.bytesTransferred+uint64(hresp.ContentLength)) {
			return resp, resp.close(newGrabError(errBadLength, "Bad content length in GET response: %d, expected %d", hresp.ContentLength, resp.Size-resp.bytesTransferred))
		}
	}

	// response context is ready to start transfer with resp.copy()
	return resp, nil
}
