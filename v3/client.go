package grab

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultUserAgent is the User-Agent string sent by clients returned from
// NewClient, including DefaultClient.
//
// It is a product token followed by a version, as required by RFC 9110
// §10.1.5. Some servers - notably GitHub - reject requests whose User-Agent
// does not take this form.
const DefaultUserAgent = "grab/3"

// HTTPClient provides an interface allowing us to perform HTTP requests.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// A Client is a file download client.
//
// Clients are safe for concurrent use by multiple goroutines.
type Client struct {
	// HTTPClient specifies the http.Client which will be used for communicating
	// with the remote server during the file transfer.
	HTTPClient HTTPClient

	// UserAgent specifies the User-Agent string which will be set in the
	// headers of all requests made by this client.
	//
	// The user agent string may be overridden in the headers of each request.
	UserAgent string

	// BufferSize specifies the size in bytes of the buffer that is used for
	// transferring all requested files. Larger buffers may result in faster
	// throughput but will use more memory and result in less frequent updates
	// to the transfer progress statistics. The BufferSize of each request can
	// be overridden on each Request object. Default: 32KB.
	BufferSize int
}

// NewClient returns a new file download Client, using default configuration.
//
// The client is built on a copy of http.DefaultTransport, so it inherits the
// standard library's connection, TLS handshake and idle connection timeouts.
// Without them a request to a host that accepts a connection and then goes
// silent hangs until the operating system gives up, which takes minutes.
func NewClient() *Client {
	t := http.DefaultTransport.(*http.Transport).Clone()

	// Request.Concurrency puts several requests to the same host in flight at
	// once, and each of them releases its connection when its range is done.
	// A released connection is handed straight to another request that is
	// already waiting for one, but if none is waiting it goes to the idle pool
	// instead - and at the default of two per host, the pool overflows and the
	// connection is closed. The next range then has to pay for a new
	// connection, and a TLS handshake, to replace one that was perfectly good.
	//
	// Idle connections remain bounded in total by MaxIdleConns, and are still
	// reaped after IdleConnTimeout.
	t.MaxIdleConnsPerHost = t.MaxIdleConns

	return &Client{
		UserAgent:  DefaultUserAgent,
		HTTPClient: &http.Client{Transport: t},
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
	// cancel will be called on all code-paths via closeResponse
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	resp := &Response{
		Request:    req,
		Start:      time.Now(),
		Done:       make(chan struct{}),
		Filename:   req.Filename,
		ctx:        ctx,
		cancel:     cancel,
		bufferSize: req.BufferSize,
	}
	if resp.bufferSize == 0 {
		// default to Client.BufferSize
		resp.bufferSize = c.BufferSize
	}

	// Run state-machine while caller is blocked to initialize the file transfer.
	// Must never transition to the copyFile state - this happens next in another
	// goroutine.
	c.run(resp, c.statFileInfo)

	// Run copyFile in a new goroutine. copyFile will no-op if the transfer is
	// already complete or failed.
	go c.run(resp, c.copyFile)
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

// An stateFunc is an action that mutates the state of a Response and returns
// the next stateFunc to be called.
type stateFunc func(*Response) stateFunc

// run calls the given stateFunc function and all subsequent returned stateFuncs
// until a stateFunc returns nil or the Response.ctx is canceled. Each stateFunc
// should mutate the state of the given Response until it has completed
// downloading or failed.
func (c *Client) run(resp *Response, f stateFunc) {
	for {
		select {
		case <-resp.ctx.Done():
			if resp.IsComplete() {
				return
			}
			resp.err = resp.ctx.Err()
			f = c.closeResponse

		default:
			// keep working
		}
		if f = f(resp); f == nil {
			return
		}
	}
}

// statFileInfo retrieves FileInfo for any local file matching
// Response.Filename.
//
// If the file does not exist, is a directory, or its name is unknown the next
// stateFunc is headRequest.
//
// If the file exists, Response.fi is set and the next stateFunc is
// validateLocal.
//
// If an error occurs, the next stateFunc is closeResponse.
func (c *Client) statFileInfo(resp *Response) stateFunc {
	if resp.Request.NoStore || resp.Filename == "" {
		return c.headRequest
	}
	fi, err := os.Stat(resp.Filename)
	if err != nil {
		if os.IsNotExist(err) {
			return c.headRequest
		}
		resp.err = err
		return c.closeResponse
	}
	if fi.IsDir() {
		resp.Filename = ""
		return c.headRequest
	}
	resp.fi = fi
	return c.validateLocal
}

// validateLocal compares a local copy of the downloaded file to the remote
// file.
//
// An error is returned if the local file is larger than the remote file, or
// Request.SkipExisting is true.
//
// If the existing file matches the length of the remote file, the next
// stateFunc is checksumFile.
//
// If the local file is smaller than the remote file and the remote server is
// known to support ranged requests, the next stateFunc is getRequest.
func (c *Client) validateLocal(resp *Response) stateFunc {
	if resp.Request.SkipExisting {
		resp.err = ErrFileExists
		return c.closeResponse
	}

	// determine target file size
	expectedSize := resp.Request.Size
	if expectedSize == 0 && resp.HTTPResponse != nil {
		expectedSize = resp.HTTPResponse.ContentLength
	}

	if expectedSize == 0 {
		// size is either actually 0 or unknown
		// if unknown, we ask the remote server
		// if known to be 0, we proceed with a GET
		return c.headRequest
	}

	if resp.optionsKnown && hasCheckpoint(resp.Filename) {
		// A split transfer writes ranges at their offset in the file, so the
		// length of a partial file says nothing about which of its bytes are
		// valid - a file whose last range completed is already full length.
		// Its checkpoint is the authority on what has been downloaded,
		// including on whether the transfer is already complete. This holds
		// however the transfer that finds the file is configured, as it is the
		// transfer that wrote the file which decided the order.
		return c.planTransfer
	}

	if expectedSize == resp.fi.Size() {
		// local file matches remote file size - wrap it up
		resp.DidResume = true
		resp.bytesResumed = resp.fi.Size()
		return c.checksumFile
	}

	if resp.Request.NoResume {
		// local file should be overwritten
		return c.planTransfer
	}

	if expectedSize >= 0 && expectedSize < resp.fi.Size() {
		// remote size is known, is smaller than local size and we want to resume
		resp.err = ErrBadLength
		return c.closeResponse
	}

	if resp.CanResume {
		return c.planTransfer
	}
	return c.headRequest
}

// planTransfer decides which byte ranges of the remote file this transfer must
// download, and prepares the request for the first of them.
//
// Every transfer is a series of ranges. A transfer that is not split is the
// single range covering everything that remains to be downloaded, which for a
// fresh download of a file of unknown length is the whole file requested
// without a Range header - exactly the request grab has always made.
func (c *Client) planTransfer(resp *Response) stateFunc {
	if resp.canSplit() {
		return c.planSplitTransfer(resp)
	}

	// A file left behind by a split transfer may have holes in it, so its
	// length says nothing about which of its bytes are valid and it cannot be
	// resumed by an unsplit transfer. A checkpoint beside the file is what
	// marks it as written out of order.
	start := int64(0)
	if hasCheckpoint(resp.Filename) {
		// This transfer records nothing, so it starts over rather than trust a
		// record it will not maintain, and discards that record so it cannot
		// outlive the contents it describes.
		if resp.err = removeCheckpoint(checkpointFilename(resp.Filename)); resp.err != nil {
			return c.closeResponse
		}
	} else if resp.fi != nil && !resp.Request.NoResume && resp.CanResume &&
		(resp.Size() < 0 || resp.fi.Size() < resp.Size()) {
		// the remote file is either larger than the local copy, or of a size
		// the server declined to tell us
		start = resp.fi.Size()
		resp.DidResume = true
		resp.bytesResumed = start
	}
	// The range runs to the end of the file, wherever that turns out to be.
	// Asking for everything from an offset rather than up to the size the
	// server last reported is both what grab has always done and the more
	// robust request: if the remote file has grown in the meantime, the
	// mismatch is caught rather than silently truncating the download.
	resp.ranges = []byteRange{{Start: start, End: math.MaxInt64}}
	setRangeHeader(resp.Request.HTTPRequest, resp.ranges[0], resp.Size())
	return c.getRequest
}

// planSplitTransfer plans a transfer of Request.RangeSize sized ranges,
// resuming from a checkpoint if one describes this same transfer.
func (c *Client) planSplitTransfer(resp *Response) stateFunc {
	url := resp.Request.URL().String()
	size, rangeSize := resp.Size(), resp.Request.RangeSize

	if resp.Request.NoResume {
		if resp.err = removeCheckpoint(checkpointFilename(resp.Filename)); resp.err != nil {
			return c.closeResponse
		}
	} else if resp.fi != nil {
		resp.complete, resp.err = loadCheckpoint(
			resp.Filename, url, size, rangeSize, resp.HTTPResponse.Header)
		if resp.err != nil {
			return c.closeResponse
		}
	}
	if resp.complete == nil {
		// Either there is no checkpoint or it describes a different transfer.
		// Without one, nothing can be assumed about the contents of any
		// existing file, so the transfer starts over.
		resp.complete = &rangeSet{}
	}

	resp.ranges = resp.complete.missing(size, rangeSize)
	if n := resp.complete.completedBytes(); n > 0 {
		resp.DidResume = true
		resp.bytesResumed = n
	}
	if len(resp.ranges) == 0 {
		// the checkpoint accounts for every byte of the file
		return c.checksumFile
	}

	resp.checkpoint = newCheckpoint(
		resp.Filename, url, size, rangeSize, resp.HTTPResponse.Header)
	setRangeHeader(resp.Request.HTTPRequest, resp.ranges[0], resp.Size())
	return c.getRequest
}

func (c *Client) checksumFile(resp *Response) stateFunc {
	if resp.Request.hash == nil {
		return c.closeResponse
	}
	if resp.Filename == "" {
		panic("grab: developer error: filename not set")
	}
	if resp.Size() < 0 {
		panic("grab: developer error: size unknown")
	}
	req := resp.Request

	// compute checksum
	var sum []byte
	sum, resp.err = resp.checksumUnsafe()
	if resp.err != nil {
		return c.closeResponse
	}

	// compare checksum
	if !bytes.Equal(sum, req.checksum) {
		resp.err = ErrBadChecksum
		if !resp.Request.NoStore && req.deleteOnError {
			if err := os.Remove(resp.Filename); err != nil {
				// err should be os.PathError and include file path
				resp.err = fmt.Errorf(
					"cannot remove downloaded file with checksum mismatch: %w",
					err)
			}
		}
	}
	return c.closeResponse
}

// doHTTPRequest sends a HTTP Request and returns the response
func (c *Client) doHTTPRequest(req *http.Request) (*http.Response, error) {
	if c.UserAgent != "" && req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	return c.HTTPClient.Do(req)
}

func (c *Client) headRequest(resp *Response) stateFunc {
	if resp.optionsKnown {
		return c.planTransfer
	}
	resp.optionsKnown = true

	// A transfer that may be split cannot plan its ranges until it knows the
	// size of the remote file and whether the server supports Range requests,
	// so it always asks. Otherwise the HEAD request is only an optimisation
	// and is skipped whenever it would tell us nothing we need.
	if resp.Request.RangeSize <= 0 || resp.Request.NoStore {
		if resp.Request.NoResume {
			return c.planTransfer
		}

		if resp.Filename != "" && resp.fi == nil {
			// destination path is already known and does not exist
			return c.planTransfer
		}
	}

	hreq := new(http.Request)
	*hreq = *resp.Request.HTTPRequest
	hreq.Method = "HEAD"

	resp.HTTPResponse, resp.err = c.doHTTPRequest(hreq)
	if resp.err != nil {
		// Some servers mishandle HEAD and close the connection or otherwise
		// fail, even though they serve GET for the same resource correctly.
		// The HEAD request is only an optimisation - it tells us the size and
		// whether ranged requests are supported - so a failure here is not
		// fatal and we fall through to the GET request.
		//
		// A cancelled context is not a server fault and must not be retried.
		if resp.ctx.Err() != nil {
			return c.closeResponse
		}
		resp.err = nil
		resp.HTTPResponse = nil
		return c.planTransfer
	}
	resp.HTTPResponse.Body.Close()

	if resp.HTTPResponse.StatusCode != http.StatusOK {
		return c.planTransfer
	}

	// In case of redirects during HEAD, record the final URL and use it
	// instead of the original URL when sending future requests.
	// This way we avoid sending potentially unsupported requests to
	// the original URL, e.g. "Range", since it was the final URL
	// that advertised its support.
	resp.Request.HTTPRequest.URL = resp.HTTPResponse.Request.URL
	resp.Request.HTTPRequest.Host = resp.HTTPResponse.Request.Host

	return c.readResponse
}

func (c *Client) getRequest(resp *Response) stateFunc {
	resp.HTTPResponse, resp.err = c.doHTTPRequest(resp.Request.HTTPRequest)
	if resp.err != nil {
		return c.closeResponse
	}

	// check status code
	if !resp.Request.IgnoreBadStatusCodes {
		if resp.HTTPResponse.StatusCode < 200 || resp.HTTPResponse.StatusCode > 299 {
			resp.err = StatusCodeError(resp.HTTPResponse.StatusCode)
			return c.closeResponse
		}
	}

	if resp.HTTPResponse.StatusCode != http.StatusPartialContent && resp.isPartialRequest() {
		// The server answered a Range request with the whole file, so the
		// response begins at the start of the file rather than where the
		// request asked it to. Nothing that was downloaded before can be kept,
		// and the transfer cannot be split.
		if resp.err = resp.restartFromStart(); resp.err != nil {
			return c.closeResponse
		}
	}

	return c.readResponse
}

func (c *Client) readResponse(resp *Response) stateFunc {
	if resp.HTTPResponse == nil {
		panic("grab: developer error: Response.HTTPResponse is nil")
	}

	// Determine the total size of the remote file. A partial response reports
	// it directly, which is both simpler and more reliable than adding the
	// length of the response to the offset it started at.
	if _, _, size, ok := parseContentRange(resp.HTTPResponse.Header.Get("Content-Range")); ok {
		resp.sizeUnsafe = size
	} else {
		resp.sizeUnsafe = resp.HTTPResponse.ContentLength
		if resp.sizeUnsafe >= 0 && len(resp.ranges) > 0 {
			// remote size is known, and is relative to where this response
			// begins in the file
			resp.sizeUnsafe += resp.ranges[0].Start
		}
	}
	if resp.sizeUnsafe >= 0 && resp.Request.Size > 0 && resp.Request.Size != resp.sizeUnsafe {
		resp.err = ErrBadLength
		return c.closeResponse
	}

	// check filename
	if resp.Filename == "" {
		filename, err := guessFilename(resp.HTTPResponse)
		if err != nil {
			resp.err = err
			return c.closeResponse
		}
		// Request.Filename will be empty or a directory
		resp.Filename = filepath.Join(resp.Request.Filename, filename)
	}

	if resp.requestMethod() == "HEAD" {
		if resp.HTTPResponse.Header.Get("Accept-Ranges") == "bytes" {
			resp.CanResume = true
		}
		return c.statFileInfo
	}
	return c.openWriter
}

// openWriter opens the destination the file transfer will be written to.
//
// Requires that Response.Filename, Response.ranges and Response.DidResume are
// already set.
func (c *Client) openWriter(resp *Response) stateFunc {
	if !resp.Request.NoStore && !resp.Request.NoCreateDirectories {
		resp.err = mkdirp(resp.Filename)
		if resp.err != nil {
			return c.closeResponse
		}
	}

	// file is the destination file, if the transfer is being stored, and is
	// needed to flush it before each checkpoint.
	var file *os.File
	if resp.Request.NoStore {
		resp.writer = &resp.storeBuffer
	} else {
		// Ranges are written at their offset in the file rather than
		// sequentially, so the file is never opened for appending: that would
		// force every write to the end of the file, whatever offset it was
		// given. An existing file is truncated later in copyFile, if the
		// BeforeCopy hook does not cancel the transfer first.
		f, err := os.OpenFile(resp.Filename, os.O_CREATE|os.O_WRONLY, 0666)
		if err != nil {
			resp.err = err
			return c.closeResponse
		}
		resp.writer, file = f, f
	}

	// init transfer
	if resp.bufferSize < 1 {
		resp.bufferSize = 32 * 1024
	}
	var ckpt *checkpointer
	if resp.checkpoint != nil && file != nil {
		ckpt = newCheckpointer(resp.checkpoint, file, resp.complete)
	}
	resp.transfer = newTransfer(resp, c, resp.HTTPResponse.Body, ckpt)

	// next step is copyFile, but this will be called later in another goroutine
	return nil
}

// copy transfers content for a HTTP connection established via Client.do()
func (c *Client) copyFile(resp *Response) stateFunc {
	if resp.IsComplete() {
		return nil
	}

	// run BeforeCopy hook
	if f := resp.Request.BeforeCopy; f != nil {
		resp.err = f(resp)
		if resp.err != nil {
			return c.closeResponse
		}
	}

	var bytesCopied int64
	if resp.transfer == nil {
		panic("grab: developer error: Response.transfer is nil")
	}

	// We waited to truncate the file in openWriter() to make sure
	// the BeforeCopy didn't cancel the copy. If this was an existing
	// file that is not going to be resumed, truncate the contents.
	if resp.fi != nil && !resp.DidResume {
		if resp.err = resp.writer.Truncate(0); resp.err != nil {
			return c.closeResponse
		}
	}

	bytesCopied, resp.err = resp.transfer.copy()
	if resp.err != nil {
		return c.closeResponse
	}
	if resp.err = closeWriter(resp); resp.err != nil {
		return c.closeResponse
	}

	// The transfer is complete, so the record of which of its ranges were
	// complete is no longer of any use.
	if resp.checkpoint != nil {
		if resp.err = resp.checkpoint.remove(); resp.err != nil {
			return c.closeResponse
		}
	}

	// set file timestamp
	if !resp.Request.NoStore && !resp.Request.IgnoreRemoteTime {
		resp.err = setLastModified(resp.HTTPResponse, resp.Filename)
		if resp.err != nil {
			return c.closeResponse
		}
	}

	// update transfer size if previously unknown
	if resp.Size() < 0 {
		discoveredSize := resp.bytesResumed + bytesCopied
		atomic.StoreInt64(&resp.sizeUnsafe, discoveredSize)
		if resp.Request.Size > 0 && resp.Request.Size != discoveredSize {
			resp.err = ErrBadLength
			return c.closeResponse
		}
	}

	// run AfterCopy hook
	if f := resp.Request.AfterCopy; f != nil {
		resp.err = f(resp)
		if resp.err != nil {
			return c.closeResponse
		}
	}

	return c.checksumFile
}

// closeWriter closes the destination and returns any error from doing so.
//
// A file system is allowed to defer a write error until the file is closed,
// and some - NFS in particular - routinely do. Discarding that error reports a
// download as successful when none of it may have reached the disk.
func closeWriter(resp *Response) error {
	var err error
	if resp.writer != nil {
		err = resp.writer.Close()
	}
	resp.writer = nil
	return err
}

// hasCheckpoint reports whether a checkpoint file accompanies the given
// destination file.
func hasCheckpoint(filename string) bool {
	_, err := os.Stat(checkpointFilename(filename))
	return err == nil
}

// close finalizes the Response
func (c *Client) closeResponse(resp *Response) stateFunc {
	if resp.IsComplete() {
		panic("grab: developer error: response already closed")
	}

	resp.fi = nil
	// Any close error here belongs to a transfer that already failed, and
	// would only mask the error that caused it.
	_ = closeWriter(resp)
	resp.closeResponseBody()

	resp.End = time.Now()
	close(resp.Done)
	if resp.cancel != nil {
		resp.cancel()
	}

	return nil
}
