package grab

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// A Client is a file download client.
//
// Clients are safe for concurrent use by multiple goroutines.
type Client struct {
	// HTTPClient specifies the http.Client which will be used for communicating
	// with the remote server during the file transfer.
	HTTPClient *http.Client

	// UserAgent specifies the User-Agent string which will be set in the
	// headers of all requests made by this client.
	//
	// The user agent string may be overridden in the headers of each request.
	UserAgent string
}

// NewClient returns a new file download Client, using default configuration.
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

// DefaultClient is the default client and is used by all Get convenience
// functions.
var DefaultClient = NewClient()

// CancelRequest cancels an in-flight request by closing its connection.
func (c *Client) CancelRequest(req *Request) {
	if t, ok := c.HTTPClient.Transport.(*http.Transport); ok {
		t.CancelRequest(req.HTTPRequest)
	}
}

// Do sends a file transfer request and returns a file transfer response
// context, following policy (e.g. redirects, cookies, auth) as configured on
// the client's HTTPClient.
//
// An error is returned if caused by client policy (such as CheckRedirect), or
// if there was an HTTP protocol error.
//
// Do is a synchronous, blocking operation which returns only once a download
// request is completed or fails. For non-blocking operations which enable the
// monitoring of transfers in process, see DoAsync and DoBatch.
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
// The Response may then be used to monitor the progress of the file transfer
// while it is in process.
//
// Any error which occurs during the file transfer will be set in the returned
// Response.Error field as soon as the Response.IsComplete method returns true.
func (c *Client) DoAsync(req *Request) <-chan *Response {
	r := make(chan *Response, 1)
	go func() {
		// prepare request with HEAD request
		resp, err := c.do(req)
		if err == nil && !resp.IsComplete() {
			// transfer data in new goroutine
			go resp.copy()
		}

		r <- resp
		close(r)
	}()

	return r
}

// DoBatch executes multiple requests with the given number of workers and
// immediately returns a channel to receive the Responses as they become
// available. Excess requests are queued until a worker becomes available. The
// channel is closed once all responses have been sent.
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
func (c *Client) DoBatch(workers int, reqs ...*Request) <-chan *Response {
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

	// default one worker per request
	if workers == 0 {
		workers = len(reqs)
	}

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
		Request:    req,
		Start:      time.Now(),
		bufferSize: req.BufferSize,
	}

	// default to current working directory
	if req.Filename == "" {
		req.Filename = "."
	} else if req.SkipExisting {
		// skip existing files
		fi, err := os.Stat(req.Filename)
		if !os.IsNotExist(err) {
			if err != nil {
				return resp, resp.close(err)
			}

			if !fi.IsDir() {
				resp.Filename = req.Filename
				resp.DidResume = true
				resp.Size = uint64(fi.Size())
				resp.bytesResumed = uint64(fi.Size())
				return resp, resp.close(nil)
			}
		}
	}

	// default write flags
	wflags := os.O_CREATE | os.O_WRONLY

	// flag to resume previous download
	doResume := false

	// set user agent string
	if c.UserAgent != "" && req.HTTPRequest.Header.Get("User-Agent") == "" {
		req.HTTPRequest.Header.Set("User-Agent", c.UserAgent)
	}

	// request HEAD first
	methods := []string{"HEAD", req.HTTPRequest.Method}
	for _, method := range methods {
		// set request method
		req.HTTPRequest.Method = method

		// get response headers
		if hresp, err := c.HTTPClient.Do(req.HTTPRequest); err != nil {
			return resp, resp.close(err)

		} else if method != "HEAD" || (hresp.StatusCode >= 200 && hresp.StatusCode < 300) {
			// ignore non 2XX results for HEAD requests.
			// dont make assumptions about non 2XX statuses for GET requests -
			// let the caller make such decisions.

			// allow caller to see response if error occurs before final transfer
			resp.HTTPResponse = hresp

			// set response size
			if hresp.ContentLength > 0 {
				// If this is a GET request which resumes a previous transfer,
				// ContentLength will likely only be the size of the requested
				// byte range, not the full file size.
				resp.Size = resp.BytesTransferred() + uint64(hresp.ContentLength)
			}

			// check content length matches expected length
			if req.Size > 0 && hresp.ContentLength > 0 && req.Size != resp.Size {
				return resp, resp.close(newGrabError(errBadLength, "Bad content length in %s response: %d, expected %d", method, resp.Size, req.Size))
			}

			// compute destination filename
			if err := computeFilename(req, resp); err != nil {
				return resp, resp.close(err)
			}

			// TODO: skip FileInfo check if completed in HEAD

			// get fileinfo for destination
			if fi, err := os.Stat(resp.Filename); err == nil {
				// check if file transfer already complete
				if resp.Size > 0 && uint64(fi.Size()) == resp.Size {
					// update response
					resp.DidResume = true
					resp.bytesResumed = uint64(fi.Size())
					resp.bytesTransferred = uint64(fi.Size())

					// validate checksum
					if err := resp.checksum(); err != nil {
						return resp, resp.close(err)
					}

					return resp, resp.close(nil)
				}

				// check if existing file is larger than expected
				if uint64(fi.Size()) > resp.Size {
					return resp, resp.close(newGrabError(errBadLength, "Existing file (%d bytes) is larger than remote (%d bytes)", fi.Size(), resp.Size))
				}

				// check if resume is supported
				if method == "HEAD" && hresp.Header.Get("Accept-Ranges") == "bytes" {
					// resume previous download
					doResume = true

					// allow writer to append to existing file
					wflags = os.O_APPEND | os.O_WRONLY

					// update progress
					resp.bytesTransferred = uint64(fi.Size())

					// set byte range in next request
					if fi.Size() > 0 {
						resp.bytesResumed = uint64(fi.Size())
						req.HTTPRequest.Header.Set("Range", fmt.Sprintf("bytes=%d-", fi.Size()))
					}
				}
			} else if !os.IsNotExist(err) {
				// error in os.Stat
				return resp, resp.close(err)
			}
		}
	}

	// create destination directory
	if req.CreateMissing {
		dir := filepath.Dir(req.Filename)
		if dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return resp, resp.close(err)
			}
		}
	}

	// open destination for writing
	f, err := os.OpenFile(resp.Filename, wflags, 0644)
	if err != nil {
		return resp, resp.close(err)
	}
	resp.writer = f

	// seek to the start of the file
	if _, err := f.Seek(0, 0); err != nil {
		return resp, resp.close(err)
	}

	// seek to end if resuming previous download
	if doResume {
		if _, err = f.Seek(0, os.SEEK_END); err != nil {
			return resp, resp.close(err)
		}

		resp.DidResume = true
	}

	// response context is ready to start transfer with resp.copy()
	return resp, nil
}

// computeFilename determines the destination filename for a request and sets
// resp.Filename. If no name is determined, an error is returned which can be
// identified with IsNoFilename.
func computeFilename(req *Request, resp *Response) error {
	// return if destination path already set
	if resp.Filename != "" {
		return nil
	}

	// see if requested destination is a directory
	dir := ""
	if fi, err := os.Stat(req.Filename); err == nil {
		// file exists - is it a directory?
		if fi.IsDir() {
			// destination is a directory - compute a file name later
			dir = req.Filename
		} else {
			// destination is an existing file
			resp.Filename = req.Filename
		}
	} else if os.IsNotExist(err) {
		// file doesn't exist
		resp.Filename = req.Filename
	} else {
		// an error occurred
		return err
	}

	// get filename from Content-Disposition header
	if resp.Filename == "" {
		if cd := resp.HTTPResponse.Header.Get("Content-Disposition"); cd != "" {
			if _, params, err := mime.ParseMediaType(cd); err == nil {
				if filename, ok := params["filename"]; ok {
					resp.Filename = path.Join(dir, filename)
				}
			}
		}
	}

	// get filename from url if still needed
	if resp.Filename == "" {
		if req.HTTPRequest.URL.Path != "" && !strings.HasSuffix(req.HTTPRequest.URL.Path, "/") {
			// update filepath with filename from URL
			resp.Filename = filepath.Join(dir, path.Base(req.HTTPRequest.URL.Path))
		}
	}

	// too bad if no filename found yet
	if resp.Filename == "" {
		return newGrabError(errNoFilename, "No filename could be determined")
	}

	return nil
}
