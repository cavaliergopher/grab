package grab

import (
	"net/http"
	"sync/atomic"
)

// Response represents the response from an HTTP transfer request.
type Response struct {
	// The Request that was sent to obtain this Response.
	Request *Request

	// HTTPResponse specifies the HTTP response received from the remote server.
	// The response's Body is nil (having already been consumed).
	HTTPResponse *http.Response

	// Filename specifies the path where the file transfer is stored in local
	// storage.
	Filename string

	// Size specifies the total size of the file transfer.
	Size uint64

	// Error specifies any error that may have occurred during the file transfer
	// that created this response.
	Error error

	// progress specifies the number of bytes which have already been
	// transferred and should only be accessed atomically.
	progress uint64

	// canResume specifies whether the server support ranged transfers for
	// resuming previous transfers.
	canResume bool
}

// Progress returns the number of bytes which have already been downloaded.
func (c *Response) Progress() uint64 {
	return atomic.LoadUint64(&c.progress)
}

// ProgressRatio returns the ratio of bytes which have already been downloaded
// over the total content length.
func (c *Response) ProgressRatio() float64 {
	if c.Size == 0 {
		return 0
	}

	return float64(atomic.LoadUint64(&c.progress)) / float64(c.Size)
}

// setError sets the response context error if any was encountered during
// transfer.
func (c *Response) setError(err error) error {
	c.Error = err
	return err
}
