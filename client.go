package grab

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	// cancel will be called in resp.close
	ctx, cancel := context.WithCancel(req.Context())

	// create a response
	resp = &Response{
		Request:    req,
		Start:      time.Now(),
		Done:       make(chan struct{}, 0),
		ctx:        ctx,
		cancel:     cancel,
		bufferSize: req.BufferSize,
		writeFlags: os.O_CREATE | os.O_WRONLY,
	}

	// check for existing file
	if err := c.getFileInfo(req, resp); err != nil {
		resp.close(err)
		return
	}

	if ok, err := c.checkComplete(req, resp); ok || err != nil {
		resp.close(err)
		return
	}

	// try resume
	if resp.fi != nil || resp.Filename == "" {
		// send HEAD request
		hreq := new(http.Request)
		*hreq = *req.HTTPRequest
		hreq.Method = "HEAD"
		hresp, err := c.HTTPClient.Do(hreq)
		if err != nil {
			resp.close(err)
			return
		}
		resp.HTTPResponse = hresp

		if err := c.processResponse(req, resp); err != nil {
			resp.close(err)
			return
		}

		if ok, err := c.checkComplete(req, resp); ok || err != nil {
			resp.close(err)
			return
		}

		if resp.CanResume && resp.fi != nil {
			req.HTTPRequest.Header.Set("Range", fmt.Sprintf("bytes=%d-", resp.fi.Size()))
			resp.DidResume = true
			resp.bytesResumed = resp.fi.Size()
			resp.bytesTransferred = resp.fi.Size()
			resp.writeFlags = os.O_APPEND | os.O_WRONLY
		}
	}

	// send transfer request
	hresp, err := c.HTTPClient.Do(req.HTTPRequest)
	if err != nil {
		resp.close(err)
		return
	}

	resp.HTTPResponse = hresp
	if err := c.processResponse(req, resp); err != nil {
		resp.close(err)
		return
	}

	if !resp.DidResume {
		if ok, err := c.checkComplete(req, resp); ok || err != nil {
			resp.close(err)
			return
		}
	}

	// open destination for writing
	if err := resp.openWriter(); err != nil {
		resp.close(err)
		return
	}

	return resp
}

func (c *Client) getFileInfo(req *Request, resp *Response) error {
	if resp.Filename == "" {
		resp.Filename = req.Filename
	}

	fi, err := os.Stat(resp.Filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return err
	}

	if fi.IsDir() {
		resp.Filename = ""
		return nil
	}

	resp.fi = fi

	return nil
}

// checkComplete returns true if the requested download is already completed,
// before starting transfer.
//
// The size of an existing file is checked, first against Request.Size, if
// given and then against the Content-Length returned by the remote server.
//
// If the file is complete, and a checksum has been requested, it will also be
// checked.
//
// TODO: check timestamps and/or E-Tags
func (c *Client) checkComplete(req *Request, resp *Response) (bool, error) {
	if resp.fi == nil {
		return false, nil
	}

	// skip existing files
	if req.SkipExisting {
		return true, ErrFileExists
	}

	size := req.Size
	if size == 0 && resp.HTTPResponse != nil {
		// This assumes that the HTTPResponse is for a HEAD or non-ranged GET
		// request. Ranged requests will not return the full file size; just the
		// size of the requested range.
		size = resp.HTTPResponse.ContentLength
	}

	if size == 0 {
		return false, nil
	}

	if size < resp.fi.Size() {
		return false, ErrBadLength
	}

	if size == resp.fi.Size() {
		resp.DidResume = true
		resp.bytesResumed = resp.fi.Size()
		resp.bytesTransferred = resp.fi.Size()

		if err := resp.checksum(); err != nil {
			return false, err
		}

		return true, nil
	}

	return false, nil
}

func (c *Client) processResponse(req *Request, resp *Response) error {
	if resp.HTTPResponse.ContentLength > 0 {
		resp.Size = resp.bytesTransferred + resp.HTTPResponse.ContentLength

		// check expected file size
		if req.Size > 0 && req.Size != resp.Size {
			return ErrBadLength
		}

		// check existing file size
		if resp.fi != nil && resp.fi.Size() > resp.Size {
			return ErrBadLength
		}
	}

	// check can resume
	if resp.HTTPResponse.Header.Get("Accept-Ranges") == "bytes" {
		resp.CanResume = true
	}

	// check filename
	if resp.Filename == "" {
		filename, err := guessFilename(resp.HTTPResponse)
		if err != nil {
			return err
		}
		resp.Filename = filepath.Join(req.Filename, filename)

		if err := c.getFileInfo(req, resp); err != nil {
			return err
		}
	}

	return nil
}
