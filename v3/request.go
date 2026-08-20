package grab

import (
	"context"
	"hash"
	"net/http"
	"net/url"
)

// A Hook is a user provided callback function that can be called by grab at
// various stages of a requests lifecycle. If a hook returns an error, the
// associated request is canceled and the same error is returned on the Response
// object.
//
// Hook functions are called synchronously and should never block unnecessarily.
// Response methods that block until a download is complete, such as
// Response.Err, Response.Cancel or Response.Wait will deadlock. To cancel a
// download from a callback, simply return a non-nil error.
type Hook func(*Response) error

// A Request represents an HTTP file transfer request to be sent by a Client.
type Request struct {
	// Label is an arbitrary string which may used to label a Request with a
	// user friendly name.
	Label string

	// Tag is an arbitrary interface which may be used to relate a Request to
	// other data.
	Tag any

	// HTTPRequest specifies the http.Request to be sent to the remote server to
	// initiate a file transfer. It includes request configuration such as URL,
	// protocol version, HTTP method, request headers and authentication.
	HTTPRequest *http.Request

	// Filename specifies the path where the file transfer will be stored in
	// local storage. If Filename is empty or a directory, the true Filename will
	// be resolved using Content-Disposition headers or the request URL.
	//
	// An empty string means the transfer will be stored in the current working
	// directory.
	Filename string

	// SkipExisting specifies that ErrFileExists should be returned if the
	// destination path already exists. The existing file will not be checked for
	// completeness.
	SkipExisting bool

	// NoResume specifies that a partially completed download will be restarted
	// without attempting to resume any existing file. If the download is already
	// completed in full, it will not be restarted.
	NoResume bool

	// NoStore specifies that grab should not write to the local file system.
	// Instead, the download will be stored in memory and accessible only via
	// Response.Open or Response.Bytes.
	NoStore bool

	// NoCreateDirectories specifies that any missing directories in the given
	// Filename path should not be created automatically, if they do not already
	// exist.
	NoCreateDirectories bool

	// IgnoreBadStatusCodes specifies that grab should accept any status code in
	// the response from the remote server. Otherwise, grab expects the response
	// status code to be within the 2XX range (after following redirects).
	IgnoreBadStatusCodes bool

	// IgnoreRemoteTime specifies that grab should not attempt to set the
	// timestamp of the local file to match the remote file.
	IgnoreRemoteTime bool

	// Size specifies the expected size of the file transfer if known. If the
	// server response size does not match, the transfer is cancelled and
	// ErrBadLength returned.
	Size int64

	// BufferSize specifies the size in bytes of the buffer that is used for
	// transferring the requested file. Larger buffers may result in faster
	// throughput but will use more memory and result in less frequent updates
	// to the transfer progress statistics. If a RateLimiter is configured,
	// BufferSize should be much lower than the rate limit. Default: 32KB.
	BufferSize int

	// RateLimiter allows the transfer rate of a download to be limited. The given
	// Request.BufferSize determines how frequently the RateLimiter will be
	// polled.
	RateLimiter RateLimiter

	// BeforeCopy is a user provided callback that is called immediately before
	// a request starts downloading. If BeforeCopy returns an error, the request
	// is cancelled and the same error is returned on the Response object.
	BeforeCopy Hook

	// AfterCopy is a user provided callback that is called immediately after a
	// request has finished downloading, before checksum validation and closure.
	// This hook is only called if the transfer was successful. If AfterCopy
	// returns an error, the request is canceled and the same error is returned on
	// the Response object.
	AfterCopy Hook

	// RangeSize specifies the size in bytes of each Range request used to
	// download the file. If RangeSize is zero, or the remote server does not
	// support Range requests, the file is downloaded as a single range with a
	// single request.
	//
	// Size ranges for throughput. Each one costs a round trip to the server
	// before its first byte arrives, so a transfer reaches roughly
	//
	//	1 / (1 + BDP/RangeSize)
	//
	// of the throughput it would reach with arbitrarily large ranges, where
	// BDP is the bandwidth-delay product of a single connection. Ten times the
	// bandwidth-delay product therefore costs around a tenth of the
	// throughput, and matching it costs half. A connection carrying 4 MiB/s
	// over a 50ms round trip has a bandwidth-delay product of about 200 KiB,
	// so ranges below a megabyte start to cost real throughput, and ranges of
	// 64 KiB are slower than not splitting the transfer at all.
	//
	// Sizing ranges for throughput does not cost progress. When a transfer is
	// split into more than one range, what it has written is recorded in a
	// checkpoint file alongside the destination file, so that an interrupted
	// transfer resumes without downloading it again. That record includes the
	// part of a range that was written before the interruption, so what is
	// lost is bounded by the interval between checkpoints - about a second of
	// transfer - rather than by RangeSize or Concurrency. The checkpoint is
	// removed once the transfer completes. See Request.Filename.
	//
	// A split transfer writes to the destination file out of order, so an
	// existing file that has no valid checkpoint beside it cannot be resumed
	// and is downloaded again in full.
	RangeSize int64

	// Concurrency specifies the maximum number of ranges that may be
	// downloaded at the same time. Concurrency has no effect unless RangeSize
	// is set, as a transfer that is not split is a single range. Default: 1.
	//
	// What concurrency is worth depends on where the bottleneck is. Over
	// HTTP/1.1 each range in flight has its own connection, so against a
	// server or a path that limits each connection rather than each client,
	// throughput scales with Concurrency.
	//
	// Over HTTP/2 the ranges are multiplexed onto a single connection and
	// share its congestion window, so concurrency does not multiply bandwidth.
	// It is still worth setting above one, as one range waiting on its round
	// trip overlaps another transferring, but there is little to gain beyond
	// two. Servers that meter each stream rather than each connection are the
	// exception, and behave as HTTP/1.1 does.
	//
	// Note that Request.BufferSize is allocated for each range in flight,
	// rather than once for the transfer.
	Concurrency int

	// hash, checksum and deleteOnError - set via SetChecksum.
	hash          hash.Hash
	checksum      []byte
	deleteOnError bool

	// Context for cancellation and timeout - set via WithContext
	ctx context.Context
}

// NewRequest returns a new file transfer Request suitable for use with
// Client.Do.
func NewRequest(dst, urlStr string) (*Request, error) {
	if dst == "" {
		dst = "."
	}
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, err
	}
	return &Request{
		HTTPRequest: req,
		Filename:    dst,
	}, nil
}

// Context returns the request's context. To change the context, use
// WithContext.
//
// The returned context is always non-nil; it defaults to the background
// context.
//
// The context controls cancelation.
func (r *Request) Context() context.Context {
	if r.ctx != nil {
		return r.ctx
	}

	return context.Background()
}

// WithContext returns a shallow copy of r with its context changed
// to ctx. The provided ctx must be non-nil.
func (r *Request) WithContext(ctx context.Context) *Request {
	if ctx == nil {
		panic("nil context")
	}
	r2 := new(Request)
	*r2 = *r
	r2.ctx = ctx
	r2.HTTPRequest = r2.HTTPRequest.WithContext(ctx)
	return r2
}

// URL returns the URL to be downloaded.
func (r *Request) URL() *url.URL {
	return r.HTTPRequest.URL
}

// SetChecksum sets the desired hashing algorithm and checksum value to validate
// a downloaded file. Once the download is complete, the given hashing algorithm
// will be used to compute the actual checksum of the downloaded file. If the
// checksums do not match, an error will be returned by the associated
// Response.Err method.
//
// If deleteOnError is true, the downloaded file will be deleted automatically
// if it fails checksum validation.
//
// To prevent corruption of the computed checksum, the given hash must not be
// used by any other request or goroutines.
//
// To disable checksum validation, call SetChecksum with a nil hash.
func (r *Request) SetChecksum(h hash.Hash, sum []byte, deleteOnError bool) {
	r.hash = h
	r.checksum = sum
	r.deleteOnError = deleteOnError
}
