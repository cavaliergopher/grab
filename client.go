package grab

import (
	"context"
	"net/http"
	"os"
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

// Do sends a file transfer request and returns a file transfer response,
// following policy (e.g. redirects, cookies, auth) as configured on the
// client's HTTPClient.
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
// start of the transfer for an already initiated connection - potentially
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

	// queue requests
	go func() {
		for _, req := range requests {
			reqch <- req
		}
		close(reqch)
		wg.Wait()
		close(respch)
	}()

	return respch
}

// do submits a HTTP request and returns a Response. It does not start
// downloading the response content. This should be performed in a separate
// goroutine by calling Response.copy.
func (c *Client) do(req *Request) (resp *Response) {
	// cancel will be called on all code-paths via resp.close
	ctx, cancel := context.WithCancel(req.Context())
	resp = &Response{
		Request:    req,
		Start:      time.Now(),
		Done:       make(chan struct{}, 0),
		Filename:   req.Filename,
		ctx:        ctx,
		cancel:     cancel,
		bufferSize: req.BufferSize,
		writeFlags: os.O_CREATE | os.O_WRONLY,
	}

	// get fileinfo for an existing file
	if err := resp.setFileInfo(); err != nil {
		resp.close(err)
		return
	}

	// check if existing file is complete
	if ok, err := resp.checkExisting(); ok || err != nil {
		resp.close(err)
		return
	}

	// check for resume support or find the name of an unknown file by sending
	// a HEAD request
	if !req.NoResume && (resp.fi != nil || resp.Filename == "") {
		hreq := new(http.Request)
		*hreq = *req.HTTPRequest
		hreq.Method = "HEAD"
		if ok, err := c.doHTTPRequest(hreq, resp); ok || err != nil {
			resp.close(err)
			return
		}
	}

	// send GET request
	if ok, err := c.doHTTPRequest(req.HTTPRequest, resp); ok || err != nil {
		resp.close(err)
		return
	}

	// open destination for writing
	if err := resp.openWriter(); err != nil {
		resp.close(err)
		return
	}

	return
}

// doHTTPRequest sends a HTTP Request, processes the response and checks for
// any existing file if the filename is now known.
//
// Returns true if the existing file is already completed.
func (c *Client) doHTTPRequest(hreq *http.Request, resp *Response) (bool, error) {
	if c.UserAgent != "" && hreq.Header.Get("User-Agent") == "" {
		hreq.Header.Set("User-Agent", c.UserAgent)
	}

	hresp, err := c.HTTPClient.Do(hreq)
	if err != nil {
		return false, err
	}

	if err := resp.readResponse(hresp); err != nil {
		return false, err
	}

	if !resp.DidResume {
		return resp.checkExisting()
	}

	return false, nil
}
