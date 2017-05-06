package grab

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
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
	// TODO: http.Transport.CancelRequest is deprecated
	if t, ok := c.HTTPClient.Transport.(*http.Transport); ok {
		t.CancelRequest(req.HTTPRequest)
	}
}

// Do sends a file transfer request and returns a file transfer response
// context, following policy (e.g. redirects, cookies, auth) as configured on
// the client's HTTPClient.
//
// Like http.Get, Do blocks while the transfer is initiated, but returns as soon
// as the transfer has started transferring in a background goroutine, or if it
// failed early.
//
// An error is returned via Response.Err if caused by client policy (such as
// CheckRedirect), or if there was an HTTP protocol or IO error. Response.Err
// will block the caller until the transfer is completed, successfully or
// otherwise.
func (c *Client) Do(req *Request) *Response {
	resp := c.do(req)
	go resp.copy()
	return resp
}

// DoChannel executes all requests sent through the given Request channel, one
// at a time, until it is closed by another goroutine. The caller is blocked
// until the Request channel is closed and all transfers have completed. All
// responses are sent through the given Response channel as soon as they are
// received from the remote servers and can be used to track the progress of
// each download.
//
// Slow Response receivers will cause a worker to block and therefore delay the
// start of the transfer for an already initiated connection, potentially
// causing a server timeout. It is the caller's responsibility to ensure a
// sufficient buffer size is used for the Response channel to prevent this.
//
// If an error occurs during any of the file transfers it will be accessible via
// the associated Response.Err function.
func (c *Client) DoChannel(reqch <-chan *Request, respch chan<- *Response) {
	// TODO: enable cancelling of batch jobs
	for req := range reqch {
		resp := c.Do(req)
		respch <- resp
		<-resp.Done
	}
}

// DoBatch executes all the given requests using the given number of concurrent
// workers. Control is passed back to the caller as soon as the workers are
// initiated.
//
// If the requested number of workers is less than one, a worker will be created
// for every request. I.e. all requests will be executed concurrently.
//
// If an error occurs during any of the file transfers it will be accessible via
// call to the associated Response.Err.
//
// The returned Response channel is closed only after all of the given Requests
// have completed, successfully or otherwise.
func (c *Client) DoBatch(workers int, requests ...*Request) <-chan *Response {
	if workers < 1 {
		workers = len(requests)
	}

	// start workers
	reqch := make(chan *Request, len(requests))
	respch := make(chan *Response, len(requests))
	wg := sync.WaitGroup{}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			c.DoChannel(reqch, respch)
			wg.Done()
		}()
	}

	go func() {
		// send requests
		for _, req := range requests {
			reqch <- req
		}
		close(reqch)

		// wait for transfers to complete
		wg.Wait()
		close(respch)
	}()

	return respch
}

// do creates a Response context for the given request using a HTTP HEAD
// request to the remote server. It does not transfer any body content as this
// may be preferable in a separate goroutine.
func (c *Client) do(req *Request) (resp *Response) {
	// create a response
	resp = &Response{
		Request:    req,
		Start:      time.Now(),
		Done:       make(chan struct{}, 0),
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
				resp.close(err)
				return
			}

			if !fi.IsDir() {
				resp.Filename = req.Filename
				resp.DidResume = true
				resp.Size = uint64(fi.Size())
				resp.bytesResumed = uint64(fi.Size())
				resp.close(nil)
				return
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
		// check for context cancellation
		select {
		case <-req.Context().Done():
			resp.close(req.Context().Err())
			return

		default:
			// continue
		}

		// set request method
		req.HTTPRequest.Method = method

		// get response headers
		if hresp, err := c.HTTPClient.Do(req.HTTPRequest); err != nil {
			resp.close(err)
			return

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
				resp.close(ErrBadLength)
				return
			}

			// compute destination filename
			if err := computeFilename(req, resp); err != nil {
				resp.close(err)
				return
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
					err := resp.checksum()
					resp.close(err)
					return
				}

				// check if existing file is larger than expected
				if uint64(fi.Size()) > resp.Size {
					resp.close(ErrBadLength)
					return
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
				resp.close(err)
				return
			}
		}
	}

	// create destination directory
	if req.CreateMissing {
		dir := filepath.Dir(req.Filename)
		if dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				resp.close(err)
				return
			}
		}
	}

	// open destination for writing
	f, err := os.OpenFile(resp.Filename, wflags, 0644)
	if err != nil {
		resp.close(err)
		return
	}
	resp.writer = f

	// seek to the start of the file
	if _, err := f.Seek(0, 0); err != nil {
		resp.close(err)
		return
	}

	// seek to end if resuming previous download
	if doResume {
		if _, err = f.Seek(0, os.SEEK_END); err != nil {
			resp.close(err)
			return
		}

		resp.DidResume = true
	}

	return
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
		// unknown error occurred
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
		return ErrNoFilename
	}

	return nil
}
