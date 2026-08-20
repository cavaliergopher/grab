package grabtest

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	DefaultHandlerContentLength       = 1 << 20
	DefaultHandlerMD5Checksum         = "c35cc7d8d91728a0cb052831bc4ef372"
	DefaultHandlerMD5ChecksumBytes    = MustHexDecodeString(DefaultHandlerMD5Checksum)
	DefaultHandlerSHA256Checksum      = "fbbab289f7f94b25736c58be46a994c441fd02552cc6022352e3d86d2fab7c83"
	DefaultHandlerSHA256ChecksumBytes = MustHexDecodeString(DefaultHandlerSHA256Checksum)
)

type StatusCodeFunc func(req *http.Request) int

// ETagFunc returns the ETag to send for the given request. Returning an empty
// string omits the header. It may be called concurrently.
type ETagFunc func(req *http.Request) string

type handler struct {
	statusCodeFunc     StatusCodeFunc
	etagFunc           ETagFunc
	methodWhitelist    []string
	headerBlacklist    []string
	contentLength      int
	acceptRanges       bool
	attachmentFilename string
	lastModified       time.Time
	ttfb               time.Duration
	rateLimiter        *time.Ticker
	recorder           *RangeRecorder
}

func NewHandler(options ...HandlerOption) (http.Handler, error) {
	h := &handler{
		methodWhitelist: []string{"GET", "HEAD"},
		contentLength:   DefaultHandlerContentLength,
		acceptRanges:    true,
	}
	for _, option := range options {
		if err := option(h); err != nil {
			return nil, err
		}
	}
	return h, nil
}

func WithTestServer(t *testing.T, f func(url string), options ...HandlerOption) {
	h, err := NewHandler(options...)
	if err != nil {
		t.Fatalf("unable to create test server handler: %v", err)
		return
	}
	s := httptest.NewServer(h)
	defer func() {
		h.(*handler).close()
		s.Close()
	}()
	f(s.URL)
}

func (h *handler) close() {
	if h.rateLimiter != nil {
		h.rateLimiter.Stop()
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// delay response
	if h.ttfb > 0 {
		time.Sleep(h.ttfb)
	}

	// validate request method
	allowed := false
	for _, m := range h.methodWhitelist {
		if r.Method == m {
			allowed = true
			break
		}
	}
	if !allowed {
		httpError(w, http.StatusMethodNotAllowed)
		return
	}

	// set server options
	if h.acceptRanges {
		w.Header().Set("Accept-Ranges", "bytes")
	}

	// set attachment filename
	if h.attachmentFilename != "" {
		w.Header().Set(
			"Content-Disposition",
			fmt.Sprintf("attachment;filename=\"%s\"", h.attachmentFilename),
		)
	}

	// set last modified timestamp
	lastMod := time.Now()
	if !h.lastModified.IsZero() {
		lastMod = h.lastModified
	}
	w.Header().Set("Last-Modified", lastMod.Format(http.TimeFormat))

	// set entity tag
	if etag := h.etag(r); etag != "" {
		w.Header().Set("ETag", etag)
	}

	// resolve the requested byte range as the half-open interval [start, end)
	total := int64(h.contentLength)
	start, end := int64(0), total
	partial := false
	if h.acceptRanges {
		if reqRange := r.Header.Get("Range"); reqRange != "" {
			var ok bool
			start, end, ok = parseByteRange(reqRange, total)
			if !ok {
				httpError(w, http.StatusBadRequest)
				return
			}
			if start >= total {
				httpError(w, http.StatusRequestedRangeNotSatisfiable)
				return
			}
			partial = true
			w.Header().Set(
				"Content-Range",
				fmt.Sprintf("bytes %d-%d/%d", start, end-1, total),
			)
		}
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start))
	h.recorder.record(r.Method, start, end)

	// apply header blacklist
	for _, key := range h.headerBlacklist {
		w.Header().Del(key)
	}

	// send header and status code
	w.WriteHeader(h.statusCode(r, partial))

	// send body
	if r.Method == "GET" {
		// use buffered io to reduce overhead on the reader
		bw := bufio.NewWriterSize(w, 4096)
		for i := start; !isRequestClosed(r) && i < end; i++ {
			bw.Write([]byte{byte(i)})
			if h.rateLimiter != nil {
				bw.Flush()
				w.(http.Flusher).Flush() // force the server to send the data to the client
				select {
				case <-h.rateLimiter.C:
				case <-r.Context().Done():
				}
			}
		}
		if !isRequestClosed(r) {
			bw.Flush()
		}
	}
}

// statusCode returns the status code to send for the given request. A request
// that is being answered with a byte range defaults to 206 Partial Content,
// unless the StatusCode option overrode it.
func (h *handler) statusCode(r *http.Request, partial bool) int {
	if h.statusCodeFunc != nil {
		return h.statusCodeFunc(r)
	}
	if partial {
		return http.StatusPartialContent
	}
	return http.StatusOK
}

// etag returns the entity tag to send for the given request. By default it is
// derived from the content length, so that it is stable for the life of a test
// server but differs between servers of differing size.
func (h *handler) etag(r *http.Request) string {
	if h.etagFunc != nil {
		return h.etagFunc(r)
	}
	return fmt.Sprintf(`"%x"`, h.contentLength)
}

// parseByteRange parses a Range header for a single byte range and returns it
// as the half-open interval [start, end), clamped to total.
//
// Only the "bytes=first-last" and "bytes=first-" forms are understood. The
// suffix form "bytes=-last" and multiple ranges are rejected, as grab never
// sends them and accepting them would hide a regression that did.
func parseByteRange(s string, total int64) (start, end int64, ok bool) {
	spec, found := strings.CutPrefix(s, "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false
	}
	first, last, found := strings.Cut(spec, "-")
	if !found || first == "" {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	end = total
	if last != "" {
		// the last byte position is inclusive
		lastPos, err := strconv.ParseInt(last, 10, 64)
		if err != nil || lastPos < start {
			return 0, 0, false
		}
		if end = lastPos + 1; end > total {
			end = total
		}
	}
	return start, end, true
}

// A RangeRecorder records the byte ranges served by a test server, so that a
// test can assert which parts of a file were transferred. It is safe for
// concurrent use. See the RecordRanges handler option.
type RangeRecorder struct {
	mu     sync.Mutex
	ranges [][2]int64
}

// Ranges returns the half-open intervals served so far, in the order they were
// requested. HEAD requests are not recorded.
func (rec *RangeRecorder) Ranges() [][2]int64 {
	if rec == nil {
		return nil
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([][2]int64(nil), rec.ranges...)
}

// Reset discards all recorded ranges.
func (rec *RangeRecorder) Reset() {
	if rec == nil {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.ranges = nil
}

func (rec *RangeRecorder) record(method string, start, end int64) {
	if rec == nil || method != "GET" {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.ranges = append(rec.ranges, [2]int64{start, end})
}

// isRequestClosed returns true if the client request has been canceled.
func isRequestClosed(r *http.Request) bool {
	return r.Context().Err() != nil
}

func httpError(w http.ResponseWriter, code int) {
	http.Error(w, http.StatusText(code), code)
}
