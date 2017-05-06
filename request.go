package grab

import (
	"context"
	"hash"
	"net/http"
	"net/url"
)

// A Request represents an HTTP file transfer request to be sent by a Client.
type Request struct {
	// Label is an arbitrary string which may used to label a Request with a
	// user friendly name.
	Label string

	// Tag is an arbitrary interface which may be used to relate a Request to
	// other data.
	Tag interface{}

	// HTTPRequest specifies the http.Request to be sent to the remote server to
	// initiate a file transfer. It includes request configuration such as URL,
	// protocol version, HTTP method, request headers and authentication.
	HTTPRequest *http.Request

	// Filename specifies the path where the file transfer will be stored in
	// local storage.
	//
	// An empty string means the transfer will be stored in the current working
	// directory.
	Filename string

	// CreateMissing specifies that any missing directories in the Filename path
	// should be automatically created.
	CreateMissing bool

	// SkipExisting specifies that any files at the given Filename path, that
	// already exist will be naively skipped; without checking file size or
	// checksum.
	SkipExisting bool

	// Size specifies the expected size of the file transfer if known. If the
	// server response size does not match, the transfer is cancelled and an
	// error returned.
	Size int64

	// BufferSize specifies the size in bytes of the buffer that is used for
	// transferring the requested file. Larger buffers may result in faster
	// throughput but will use more memory and result in less frequent updates
	// to the transfer progress statistics. Default: 32KB.
	BufferSize int

	// hash, checksum and deleteOnError are set via SetChecksum.
	hash          hash.Hash
	checksum      []byte
	deleteOnError bool

	// Context for cancellation and timeout
	ctx context.Context
}

// NewRequest returns a new file transfer Request suitable for use with
// Client.Do.
func NewRequest(dst, urlStr string) (*Request, error) {
	// create http request
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
// The returned context is always non-nil; it defaults to the
// background context.
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

	// propagate to HTTPRequest
	r2.HTTPRequest = r2.HTTPRequest.WithContext(ctx)
	return r2
}

// URL returns the URL to be requested from the remote server.
func (r *Request) URL() *url.URL {
	return r.HTTPRequest.URL
}

// SetChecksum sets the desired checksum and hashing algorithm for a file
// transfer request. Once the transfer is complete, the given hashing algorithm
// will be used to compute the checksum of the transferred file. The hash will
// then be compared with the given hash. If the hashes do not match, an error
// will be returned by the associated Response.Err method.
//
// If deleteOnError is true, the transferred file will be deleted automatically
// if it fails checksum validation.
//
// The given hash must be unique to this request to prevent corruption from
// other requests in other goroutines. E.g. Sha256.New.
//
// To disable checksum validation (default), call SetChecksum with a nil hash.
func (r *Request) SetChecksum(h hash.Hash, sum []byte, deleteOnError bool) {
	if h == nil {
		h, sum, deleteOnError = nil, nil, false
	}

	r.hash = h
	r.checksum = sum
	r.deleteOnError = deleteOnError
}
