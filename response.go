package grab

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

// Response represents the response to a completed or in-process download
// request.
//
// A response may be returned as soon a HTTP response is received from a remote
// server, but before the body content has started transferring.
//
// All Response method calls are thread-safe.
type Response struct {
	// The Request that was sent to obtain this Response.
	Request *Request

	// HTTPResponse specifies the HTTP response received from the remote server.
	//
	// The response Body should not be used as it will be consumed and closed by
	// grab.
	HTTPResponse *http.Response

	// Filename specifies the path where the file transfer is stored in local
	// storage.
	Filename string

	// Size specifies the total expected size of the file transfer.
	Size uint64

	// Start specifies the time at which the file transfer started.
	Start time.Time

	// End specifies the time at which the file transfer completed.
	//
	// This should not be read until IsComplete returns true.
	End time.Time

	// DidResume specifies that the file transfer resumed a previously
	// incomplete transfer.
	DidResume bool

	// Done is closed once the transfer is finalized, either successfully or with
	// errors.
	Done chan struct{}

	// writer is the file handle used to write the downloaded file to local
	// storage
	writer io.WriteCloser

	// bytesCompleted specifies the number of bytes which were already
	// transferred before this transfer began.
	bytesResumed uint64

	// bytesTransferred specifies the number of bytes which have already been
	// transferred and should only be accessed atomically.
	bytesTransferred uint64

	// bufferSize specifies the site in bytes of the transfer buffer.
	bufferSize uint

	// Error specifies any error that may have occurred during the file
	// transfer.
	//
	// This should not be read until IsComplete returns true.
	err error
}

// Wait blocks until the underlying file transfer is completed. If the transfer
// is already completed, Wait returns immediately.
func (c *Response) Wait() {
	<-c.Done
}

// Err blocks the calling goroutine until the underlying file transfer is
// completed and returns any error that may have occurred or nil. If the
// transfer is already completed, Err returns immediately.
func (c *Response) Err() error {
	<-c.Done
	return c.err
}

// IsComplete indicates whether the Response transfer context has completed with
// either a success or failure. If the transfer was unsuccessful, Response.Error
// will be non-nil.
func (c *Response) IsComplete() bool {
	select {
	case <-c.Done:
		return true
	default:
		return false
	}
}

// BytesTransferred returns the number of bytes which have already been
// downloaded, including any data used to resume a previous download.
func (c *Response) BytesTransferred() uint64 {
	return atomic.LoadUint64(&c.bytesTransferred)
}

// Progress returns the ratio of bytes which have already been downloaded over
// the total file size as a fraction of 1.00.
//
// Multiply the returned value by 100 to return the percentage completed.
func (c *Response) Progress() float64 {
	if c.Size == 0 {
		return 0
	}

	return float64(c.BytesTransferred()) / float64(c.Size)
}

// Duration returns the duration of a file transfer. If the transfer is in
// process, the duration will be between now and the start of the transfer. If
// the transfer is complete, the duration will be between the start and end of
// the completed transfer process.
func (c *Response) Duration() time.Duration {
	if c.IsComplete() {
		return c.End.Sub(c.Start)
	}

	return time.Now().Sub(c.Start)
}

// ETA returns the estimated time at which the the download will complete. If
// the transfer has already complete, the actual end time will be returned.
func (c *Response) ETA() time.Time {
	if c.IsComplete() {
		return c.End
	}

	// total progress through transfer
	transferred := c.BytesTransferred()
	if transferred == 0 {
		return time.Time{}
	}

	// bytes remaining
	remainder := c.Size - transferred

	// time elapsed
	duration := time.Now().Sub(c.Start)

	// average bytes per second for transfer
	bps := float64(transferred-c.bytesResumed) / duration.Seconds()

	// estimated seconds remaining
	secs := float64(remainder) / bps

	return time.Now().Add(time.Duration(secs) * time.Second)
}

// AverageBytesPerSecond returns the average bytes transferred per second over
// the duration of the file transfer.
func (c *Response) AverageBytesPerSecond() float64 {
	return float64(c.BytesTransferred()-c.bytesResumed) / c.Duration().Seconds()
}

// copy transfers content for a HTTP connection established via Client.do()
func (c *Response) copy() error {
	if c.IsComplete() {
		return c.err
	}

	if c.bufferSize < 1 {
		c.bufferSize = 32 * 1024
	}

	// download and update progress
	buffer := make([]byte, c.bufferSize)
	for {
		// check for cancellation
		select {
		case <-c.Request.Context().Done():
			return c.close(c.Request.Context().Err())

		default:
			// continue
		}

		// read HTTP stream
		n, err := c.HTTPResponse.Body.Read(buffer)
		if err != nil && err != io.EOF {
			return c.close(err)
		}

		// write output stream
		// TODO: fix buffer underwrites
		if _, werr := c.writer.Write(buffer[:n]); werr != nil {
			return c.close(werr)
		}
		atomic.AddUint64(&c.bytesTransferred, uint64(n))

		// break when finished
		if err == io.EOF {
			c.HTTPResponse.Body.Close()
			c.writer.Close()
			break
		}
	}

	// validate checksum
	if err := c.checksum(); err != nil {
		return c.close(err)
	}

	return c.close(nil)
}

// checksum validates a completed file transfer.
func (c *Response) checksum() error {
	if c.Request.hash == nil {
		return nil
	}

	// open downloaded file
	f, err := os.Open(c.Filename)
	if err != nil {
		return err
	}
	defer f.Close()

	// hash file
	if _, err := io.Copy(c.Request.hash, f); err != nil {
		return err
	}

	// compare checksum
	sum := c.Request.hash.Sum(nil)
	if !bytes.Equal(sum, c.Request.checksum) {
		if c.Request.deleteOnError {
			os.Remove(c.Filename)
		}

		return ErrBadChecksum
	}

	return nil
}

// close finalizes the Response
func (c *Response) close(err error) error {
	if c.IsComplete() {
		panic("Response already closed")
	}

	if c.writer != nil {
		c.writer.Close()
		c.writer = nil
	}

	if c.HTTPResponse != nil && c.HTTPResponse.Body != nil {
		c.HTTPResponse.Body.Close()
	}

	c.err = err
	c.End = time.Now()
	close(c.Done)

	return err
}
