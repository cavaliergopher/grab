package grab

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cavaliergopher/grab/v3/pkg/bps"
)

// A transferWriter is the destination of a file transfer.
//
// Ranges are written at their offset in the file and may be written out of
// order, so the destination is addressed by offset rather than written
// sequentially.
type transferWriter interface {
	io.WriterAt
	io.Closer

	// Truncate discards everything beyond the given size.
	Truncate(size int64) error

	// Sync flushes everything written so far to stable storage.
	Sync() error
}

// transfer copies a remote file to a local destination as a series of byte
// ranges, reporting progress and tracking the transfer rate.
//
// Every transfer is a series of ranges, even when it is a single range covering
// the whole file requested without a Range header. Ranges are handed to a fixed
// number of workers in ascending order, so that the destination file is filled
// from the start.
type transfer struct {
	n     int64 // must be 64bit aligned on 386
	ctx   context.Context
	gauge bps.Gauge
	lim   RateLimiter
	w     transferWriter

	// client and req are used to request every range but the first.
	client *Client
	req    *http.Request

	// first is the body of the response the Client already received for
	// ranges[0]. Requesting the first range before the transfer starts is what
	// allows Client.Do to validate the response headers before it returns.
	first io.ReadCloser

	ranges  []byteRange
	workers int
	bufSize int

	// size is the total size of the remote file, or -1 if it is not known.
	size int64

	// ckpt records completed ranges so that an interrupted transfer can be
	// resumed. It is nil if the transfer is not split across several ranges.
	ckpt *checkpointer

	// flush keeps the destination file on stable storage for a transfer that
	// is durable but not split, and so has no checkpointer to flush it. It is
	// nil for every other transfer.
	flush *flusher
}

func newTransfer(resp *Response, client *Client, first io.ReadCloser, ckpt *checkpointer) *transfer {
	workers := resp.Request.Concurrency
	if workers < 1 {
		workers = 1
	}
	if n := len(resp.ranges); n > 0 && workers > n {
		workers = n
	}
	bufSize := resp.bufferSize
	if bufSize < 1 {
		bufSize = 32 * 1024
	}
	return &transfer{
		ctx:     resp.Request.Context(),
		gauge:   bps.NewSMA(6), // five second moving average sampling every second
		lim:     resp.Request.RateLimiter,
		w:       resp.writer,
		client:  client,
		req:     resp.Request.HTTPRequest,
		first:   first,
		ranges:  resp.ranges,
		workers: workers,
		bufSize: bufSize,
		size:    resp.Size(),
		ckpt:    ckpt,
	}
}

// copy transfers every range of the file, reporting progress in a thread-safe
// manner and tracking the transfer rate. It checks for cancelation of the
// transfer's Context throughout.
func (c *transfer) copy() (written int64, err error) {
	// maintain a bps gauge in another goroutine
	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()
	go bps.Watch(ctx, c.gauge, c.N, time.Second)

	if c.ckpt != nil {
		stop := c.ckpt.start()
		defer func() {
			// Record what was written if the transfer is being abandoned: that
			// is exactly what a later transfer can skip. A transfer that
			// finished needs no checkpoint, since the caller deletes it next -
			// though its bytes must still reach the disk before this returns.
			if cerr := stop(err != nil); err == nil {
				err = cerr
			}
		}()
	}
	if c.flush != nil {
		stop := c.flush.start()
		defer func() {
			if ferr := stop(); err == nil {
				err = ferr
			}
		}()
	}

	// Hand ranges to the workers in ascending order. A worker that fails
	// cancels ctx, which releases both the workers and this loop.
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		queue    = make(chan int)
	)
	fail := func(e error) {
		// Record the error before canceling, so that the failure which stopped
		// the transfer is reported rather than the cancelation it causes in
		// every other worker.
		mu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		mu.Unlock()
		cancel()
	}
	for i := 0; i < c.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, c.bufSize)
			for i := range queue {
				if err := c.copyRange(ctx, i, buf); err != nil {
					fail(err)
					return
				}
			}
		}()
	}
dispatch:
	for i := range c.ranges {
		select {
		case queue <- i:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(queue)
	wg.Wait()

	mu.Lock()
	err = firstErr
	mu.Unlock()
	if err == nil {
		// no worker failed, so a canceled context is the caller's doing
		err = c.ctx.Err()
	}
	return c.N(), err
}

// copyRange transfers a single range of the file to its offset in the
// destination.
func (c *transfer) copyRange(ctx context.Context, i int, buf []byte) error {
	r := c.ranges[i]
	offset := r.Start
	if c.ckpt != nil {
		// Record what was written however this range ends. The bytes before a
		// failure are on disk and correct, and there is no reason for a later
		// transfer to fetch them again.
		defer func() { c.ckpt.add(byteRange{Start: r.Start, End: offset}) }()
	}

	body := c.first
	if i > 0 || body == nil {
		var err error
		if body, err = c.requestRange(ctx, r); err != nil {
			return err
		}
	}
	defer body.Close()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			// keep working
		}
		nr, er := body.Read(buf)
		if nr > 0 {
			if offset+int64(nr) > r.End {
				// the server sent more than the range it was asked for
				return ErrBadLength
			}
			nw, ew := c.w.WriteAt(buf[0:nr], offset)
			if nw > 0 {
				if c.ckpt != nil {
					c.ckpt.add(byteRange{Start: r.Start, End: offset + int64(nw)})
				}
				offset += int64(nw)
				atomic.AddInt64(&c.n, int64(nw))
			}
			if ew != nil {
				return ew
			}
			if nr != nw {
				return io.ErrShortWrite
			}
			// wait for rate limiter
			if c.lim != nil {
				if err := c.lim.WaitN(ctx, nr); err != nil {
					return err
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

	if r.End != math.MaxInt64 && offset != r.End {
		// the server sent less than the range it was asked for
		return ErrBadLength
	}
	return nil
}

// requestRange sends a Range request for the given range and returns the body
// of the response.
func (c *transfer) requestRange(ctx context.Context, r byteRange) (io.ReadCloser, error) {
	req := c.req.Clone(ctx)
	setRangeHeader(req, r, c.size)
	resp, err := c.client.doHTTPRequest(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		// The server answered a Range request for part of a file with
		// something other than that part of the file. Continuing would write
		// the wrong bytes at this offset.
		return nil, fmt.Errorf(
			"expected %d for range request: %w",
			http.StatusPartialContent, StatusCodeError(resp.StatusCode))
	}
	if _, _, size, ok := parseContentRange(resp.Header.Get("Content-Range")); ok && size != c.size {
		resp.Body.Close()
		// The remote file changed size part way through the transfer, so the
		// ranges already written came from a different file.
		return nil, ErrBadLength
	}
	return resp.Body, nil
}

// N returns the number of bytes transferred.
func (c *transfer) N() (n int64) {
	if c == nil {
		return 0
	}
	n = atomic.LoadInt64(&c.n)
	return
}

// BPS returns the current bytes per second transfer rate using a simple moving
// average.
func (c *transfer) BPS() (bps float64) {
	if c == nil || c.gauge == nil {
		return 0
	}
	return c.gauge.BPS()
}

// checkpointInterval is how often a transfer in progress records what it has
// written. It is the bound on how much of an interrupted transfer is lost: not
// how much has been downloaded since the last range completed, but how much has
// been downloaded in the last checkpointInterval.
//
// It is deliberately a duration rather than a number of bytes or ranges. Ranges
// are sized for throughput, which the round trip to request each one makes a
// question about the bandwidth-delay product of the connection; how much
// progress an interruption may cost is an unrelated question, and answering
// both with one number would force one of them to be answered badly.
const checkpointInterval = time.Second

// A checkpointer owns the record of which parts of a transfer are complete, and
// the checkpoint file that record is written to.
//
// Writing a checkpoint flushes the destination file to stable storage, which is
// far too slow to do while holding up a worker. Workers therefore only record
// what they have written, which costs no more than appending to an interval
// set, and a goroutine writes the result out once per checkpointInterval. That
// bounds the cost to a single flush per interval however many ranges are in
// flight, and bounds what an interruption loses to a single interval's worth of
// transfer however large those ranges are.
type checkpointer struct {
	ckpt *checkpoint
	dst  *os.File

	// durable reports whether progress is recorded as the transfer makes it.
	// When it is not, the checkpoint is written once and left alone: it marks
	// the destination file as written out of order without ever claiming that
	// any part of it is complete.
	durable bool

	mu       sync.Mutex
	complete rangeSet
	dirty    bool
	err      error

	done chan struct{}
}

func newCheckpointer(ckpt *checkpoint, dst *os.File, complete *rangeSet, durable bool) *checkpointer {
	p := &checkpointer{
		ckpt:    ckpt,
		dst:     dst,
		durable: durable,
		// The first store must write even though nothing has completed yet, so
		// that the checkpoint marks the file from the moment writing starts.
		dirty: true,
		done:  make(chan struct{}),
	}
	if complete != nil {
		p.complete = *complete
	}
	return p
}

// add records the given range of the file as written.
//
// Workers call this as they go, not only when a range is finished, so that an
// interrupted transfer does not discard the part of a range it had written.
// Writes within a range are sequential, so the range a worker reports is always
// a prefix of the range it was given.
func (p *checkpointer) add(r byteRange) {
	if !p.durable || r.End <= r.Start {
		return
	}
	p.mu.Lock()
	p.complete.add(r.Start, r.End)
	p.dirty = true
	p.mu.Unlock()
}

// start runs the checkpointer until the returned function is called, which
// stops it and returns the first error it met, if any.
//
// Pass record to have the progress made since the last checkpoint written
// before it stops. A transfer that is being abandoned wants that, so that as
// little as possible is lost. A transfer that finished does not: its checkpoint
// is about to be deleted, and writing one first only pays to flush the file
// twice. Either way a durable transfer flushes the destination one last time,
// as that is the promise Request.Durable makes, and a transfer that is not
// durable made no promise and flushes nothing.
func (p *checkpointer) start() (stop func(record bool) error) {
	quit := make(chan struct{})
	var final atomic.Bool
	// Write the checkpoint before any of the file is, so that there is no
	// window in which the destination has been written out of order but
	// nothing beside it says so.
	p.store()
	go func() {
		defer close(p.done)
		if !p.durable {
			// No progress is recorded, so there is never anything more to
			// write than the marker already written above.
			<-quit
			return
		}
		t := time.NewTicker(checkpointInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				p.store()
			case <-quit:
				if final.Load() {
					p.store()
				} else {
					p.sync()
				}
				return
			}
		}
	}()
	return func(record bool) error {
		final.Store(record)
		close(quit)
		<-p.done
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.err
	}
}

func (p *checkpointer) store() {
	// Snapshot the completed ranges before flushing the destination file, not
	// after. Everything in the snapshot was written before the flush and is
	// therefore covered by it; anything written after simply waits for the next
	// checkpoint. Taking the snapshot afterwards would record ranges whose data
	// had not been flushed.
	p.mu.Lock()
	if !p.dirty || p.err != nil {
		p.mu.Unlock()
		return
	}
	complete := p.complete.clone()
	p.dirty = false
	p.mu.Unlock()

	if err := p.ckpt.store(p.dst, complete); err != nil {
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
	}
}

// sync flushes the destination without writing a checkpoint, for a transfer
// that finished and whose checkpoint is about to be deleted. The record is of
// no further use, but the bytes it would have described still have to reach
// the disk before the caller is told the transfer succeeded.
func (p *checkpointer) sync() {
	p.mu.Lock()
	failed := p.err != nil
	p.mu.Unlock()
	if failed {
		return
	}
	if err := p.dst.Sync(); err != nil {
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
	}
}

// A flusher keeps the destination file of a durable transfer on stable storage
// as the transfer runs, for transfers that are not split and so have no
// checkpointer to do it for them. There is nothing to record - a transfer that
// is not split writes its file in order, so its length is its progress - only
// the flush itself, and the error it can report.
//
// Flushing throughout rather than once at the end is what keeps a durable
// transfer's reported rate honest. A single flush on completion would let the
// transfer run at the speed of memory and then stall, at 100% complete, for as
// long as the device needed to catch up.
type flusher struct {
	dst syncer

	mu   sync.Mutex
	err  error
	done chan struct{}
}

// A syncer flushes what has been written to it to stable storage. It is the
// part of transferWriter a flusher needs, and *os.File is what implements it.
type syncer interface {
	Sync() error
}

func newFlusher(dst syncer) *flusher {
	return &flusher{dst: dst, done: make(chan struct{})}
}

// start flushes the destination until the returned function is called, which
// flushes it once more and returns the first error met, if any.
func (p *flusher) start() (stop func() error) {
	quit := make(chan struct{})
	go func() {
		defer close(p.done)
		t := time.NewTicker(checkpointInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				p.sync()
			case <-quit:
				// However the transfer ended, what it wrote belongs on the
				// disk before anyone is told about it.
				p.sync()
				return
			}
		}
	}()
	return func() error {
		close(quit)
		<-p.done
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.err
	}
}

func (p *flusher) sync() {
	p.mu.Lock()
	failed := p.err != nil
	p.mu.Unlock()
	if failed {
		return
	}
	if err := p.dst.Sync(); err != nil {
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
	}
}

// copyContext behaves like io.CopyBuffer, except that it stops early if the
// given Context is canceled.
func copyContext(ctx context.Context, dst io.Writer, src io.Reader, buf []byte) (written int64, err error) {
	if buf == nil {
		buf = make([]byte, 32*1024)
	}
	for {
		select {
		case <-ctx.Done():
			return written, ctx.Err()
		default:
			// keep working
		}
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if ew != nil {
				return written, ew
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			return written, err
		}
	}
}
