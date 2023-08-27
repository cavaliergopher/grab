package grab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/cavaliergopher/grab/v3/pkg/bps"
)

type transferer interface {
	// Copy performs the bytes copy from the reader to the writer,
	// reports the progress, and transfer rate.
	// Returns bytes written and error, using same behaviour as io.Copy.Buffer
	Copy() (written int64, err error)
	// N returns the number of bytes transferred.
	N() int64
	// BPS returns the current bytes per second transfer rate using a simple moving average.
	BPS() float64
}

func newGauge() bps.Gauge {
	// five second moving average sampling every second
	return bps.NewSMA(6)

}

type transfer struct {
	n       int64 // must be 64bit aligned on 386
	ctx     context.Context
	gauge   bps.Gauge
	lim     RateLimiter
	w       io.Writer
	r       io.Reader
	bufSize int
}

func newTransfer(ctx context.Context, lim RateLimiter, dst io.Writer, src io.Reader, bufSize int) *transfer {
	return &transfer{
		ctx:     ctx,
		gauge:   newGauge(),
		lim:     lim,
		w:       dst,
		r:       src,
		bufSize: bufSize,
	}
}

// Copy behaves similarly to io.CopyBuffer except that it checks for cancelation
// of the given context.Context, reports progress in a thread-safe manner and
// tracks the transfer rate.
func (c *transfer) Copy() (written int64, err error) {
	if c == nil {
		return 0, errors.New("nil *transfer instance")
	}

	// maintain a bps gauge in another goroutine
	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()
	go bps.Watch(ctx, c.gauge, c.N, time.Second)

	// start the transfer
	bufSize := c.bufSize
	if bufSize < 1 {
		bufSize = 32 * 1024
	}
	buf := make([]byte, bufSize)

	for {
		select {
		case <-c.ctx.Done():
			err = c.ctx.Err()
			return
		default:
			// keep working
		}
		nr, er := c.r.Read(buf)
		if nr > 0 {
			nw, ew := c.w.Write(buf[0:nr])
			if nw > 0 {
				written += int64(nw)
				atomic.StoreInt64(&c.n, written)
			}
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
			// wait for rate limiter
			if c.lim != nil {
				err = c.lim.WaitN(c.ctx, nr)
				if err != nil {
					return
				}
			}
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return written, err
}

// N returns the number of bytes transferred.
func (c *transfer) N() int64 {
	if c == nil {
		return 0
	}
	return atomic.LoadInt64(&c.n)
}

// BPS returns the current bytes per second transfer rate using a simple moving
// average.
func (c *transfer) BPS() float64 {
	if c == nil || c.gauge == nil {
		return 0
	}
	return c.gauge.BPS()
}

// transferRangesErr wraps a http error with extra information
// about the offset ranges that were successfully written
type transferRangesErr struct {
	// wrapped error
	err error
	// the end byte offset of the last successfully written range
	LastOffsetEnd int64
}

func (e transferRangesErr) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return "transferRangesErr: nil"
}

func (e transferRangesErr) Unwrap() error {
	return e.err
}

type transferRanges struct {
	n       int64 // must be 64bit aligned on 386
	ctx     context.Context
	client  HTTPClient
	gauge   bps.Gauge
	lim     RateLimiter
	w       io.WriterAt
	r       *http.Request
	length  int64
	offset  int64
	workers int
	bufSize int
}

func newTransferRanges(client HTTPClient, headResp *Response, dst io.WriterAt) *transferRanges {
	return &transferRanges{
		ctx:     headResp.Request.Context(),
		client:  client,
		gauge:   newGauge(),
		lim:     headResp.Request.RateLimiter,
		w:       dst,
		r:       headResp.Request.HTTPRequest,
		length:  headResp.HTTPResponse.ContentLength,
		offset:  headResp.bytesResumed,
		workers: headResp.Request.RangeRequestMax,
		bufSize: headResp.bufferSize,
	}
}

// Copy performs concurrent http Range requests to transfer chunks and write them at
// offsets to the WriterAt instance.
// Checks for cancelation of the given context.Context, reports progress in a
// thread-safe manner and tracks the transfer rate.
func (c *transferRanges) Copy() (written int64, err error) {
	if c == nil {
		return 0, errors.New("nil *transferRanges instance")
	}

	if c.length == 0 {
		err = errors.New("cannot transfer ranges: ContentLength is 0")
		return
	}

	if c.workers < 1 {
		c.workers = 1
	}

	if c.bufSize < 1 {
		c.bufSize = 32 * 1024
	}

	c.n = 0

	// maintain a bps gauge in another goroutine
	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()
	go bps.Watch(ctx, c.gauge, c.N, time.Second)

	wg, ctx := errgroup.WithContext(ctx)

	chunkSize := (c.length - c.offset) / int64(c.workers)
	var start, end int64
	start += c.offset
	completed := make([]int64, c.workers)
	for i := 1; i <= c.workers; i++ {
		if i == c.workers {
			end = c.offset + c.length
		} else {
			end = start + chunkSize
		}
		if end > c.length {
			end = c.length
		}
		id := i - 1
		offset := start
		limit := end
		wg.Go(func() error {
			e := c.requestChunk(ctx, offset, limit)
			if e == nil {
				// when a chunk succeeds, record the ending offset
				completed[id] = limit
			}
			return e
		})
		start = end
	}

	if err = wg.Wait(); err != nil {
		rangeErr := transferRangesErr{err: err}
		// find the last successful end offset before an error
		for _, offset := range completed {
			if offset == 0 {
				break
			}
			rangeErr.LastOffsetEnd = offset
		}
		err = rangeErr
	}

	return c.N(), err
}

func (c *transferRanges) requestChunk(ctx context.Context, offset, limit int64) error {
	req := c.r.Clone(ctx)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, limit))

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("server responded with %d status code, expected %d for range request",
			resp.StatusCode, http.StatusPartialContent)
	}

	defer resp.Body.Close()

	// start the transfer
	buf := make([]byte, c.bufSize)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			// keep working
		}
		nr, er := resp.Body.Read(buf)
		if nr > 0 {
			nw, ew := c.w.WriteAt(buf[0:nr], offset)
			if nw > 0 {
				atomic.AddInt64(&c.n, int64(nw))
			}
			if ew != nil {
				return ew
			}
			if nr != nw {
				return io.ErrShortWrite
			}
			offset += int64(nw)
			// wait for rate limiter
			if c.lim != nil {
				if er = c.lim.WaitN(ctx, nr); er != nil {
					return er
				}
			}
		}
		if er != nil {
			if er != io.EOF {
				return er
			}
			break
		}
	}

	return nil
}

// N returns the total number of bytes transferred across all concurrent chunks.
func (c *transferRanges) N() int64 {
	if c == nil {
		return 0
	}
	return atomic.LoadInt64(&c.n)
}

// BPS returns the current bytes per second transfer rate using a simple moving
// average, across all concurrent chunks.
func (c *transferRanges) BPS() float64 {
	if c == nil || c.gauge == nil {
		return 0
	}
	return c.gauge.BPS()
}
