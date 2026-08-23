package grabtest

import (
	"errors"
	"net/http"
	"time"
)

type HandlerOption func(*handler) error

func StatusCodeStatic(code int) HandlerOption {
	return func(h *handler) error {
		return StatusCode(func(req *http.Request) int {
			return code
		})(h)
	}
}

func StatusCode(f StatusCodeFunc) HandlerOption {
	return func(h *handler) error {
		if f == nil {
			return errors.New("status code function cannot be nil")
		}
		h.statusCodeFunc = f
		return nil
	}
}

// ETagStatic sets the ETag sent with every response. An empty string omits the
// header.
func ETagStatic(etag string) HandlerOption {
	return func(h *handler) error {
		return ETag(func(req *http.Request) string {
			return etag
		})(h)
	}
}

// ETag sets a function that returns the ETag to send for each request. It
// allows a test to change the entity tag part way through, to exercise a client
// noticing that the remote file was modified.
//
// The function may be called concurrently by requests in flight at the same
// time, so it must be safe for concurrent use.
func ETag(f ETagFunc) HandlerOption {
	return func(h *handler) error {
		if f == nil {
			return errors.New("etag function cannot be nil")
		}
		h.etagFunc = f
		return nil
	}
}

// RecordRanges records the byte range served for every GET request in the given
// recorder, so that a test can assert which parts of a file were transferred.
func RecordRanges(rec *RangeRecorder) HandlerOption {
	return func(h *handler) error {
		if rec == nil {
			return errors.New("range recorder cannot be nil")
		}
		h.recorder = rec
		return nil
	}
}

func MethodWhitelist(methods ...string) HandlerOption {
	return func(h *handler) error {
		h.methodWhitelist = methods
		return nil
	}
}

func HeaderBlacklist(headers ...string) HandlerOption {
	return func(h *handler) error {
		h.headerBlacklist = headers
		return nil
	}
}

func ContentLength(n int) HandlerOption {
	return func(h *handler) error {
		if n < 0 {
			return errors.New("content length must be zero or greater")
		}
		h.contentLength = n
		return nil
	}
}

func AcceptRanges(enabled bool) HandlerOption {
	return func(h *handler) error {
		h.acceptRanges = enabled
		return nil
	}
}

func LastModified(t time.Time) HandlerOption {
	return func(h *handler) error {
		h.lastModified = t.UTC()
		return nil
	}
}

func TimeToFirstByte(d time.Duration) HandlerOption {
	return func(h *handler) error {
		if d < 1 {
			return errors.New("time to first byte must be greater than zero")
		}
		h.ttfb = d
		return nil
	}
}

func RateLimiter(bps int) HandlerOption {
	return func(h *handler) error {
		if bps < 1 {
			return errors.New("bytes per second must be greater than zero")
		}
		h.rateLimiter = time.NewTicker(time.Second / time.Duration(bps))
		return nil
	}
}

func AttachmentFilename(filename string) HandlerOption {
	return func(h *handler) error {
		h.attachmentFilename = filename
		return nil
	}
}
