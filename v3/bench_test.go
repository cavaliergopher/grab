package grab

// The benchmarks in this file answer two different questions, and it is worth
// keeping straight which is which.
//
// BenchmarkTransfer, BenchmarkRangeSet and BenchmarkCheckpointStore measure
// what grab itself costs, against a server with no simulated network at all.
// They are the ones to watch for regressions, because everything they measure
// is work grab does.
//
//	make bench
//
// BenchmarkRangeSize and BenchmarkConcurrency measure the shape of the
// trade-off that Request.RangeSize and Request.Concurrency control, against a
// server that simulates a round trip and a per connection bandwidth limit.
// They exist to be read rather than tracked: they are dominated by simulated
// latency, so they say little about a change to grab, but they show what
// tuning those two fields is worth and what the limits are.
//
//	make bench-network
//
// To compare a change against main, install benchstat and give it both:
//
//	go install golang.org/x/perf/cmd/benchstat@latest
//	git stash && make bench > /tmp/old.txt && git stash pop
//	make bench > /tmp/new.txt
//	benchstat /tmp/old.txt /tmp/new.txt
//
// Sub-benchmark names are key=value pairs so that benchstat reads them as
// configuration axes, and will table the results across them.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A netProfile describes the network path a benchmark server sits behind.
//
// The zero value is a server on the same machine with nothing in the way,
// which is what the overhead benchmarks want. The trade-off benchmarks set rtt
// and bps to put a plausible network between the client and the file.
type netProfile struct {
	// rtt is slept before each response, standing in for the round trip a
	// request makes before its first byte comes back. It is the cost that
	// makes range size matter.
	rtt time.Duration

	// bps limits each TCP connection to this many bytes per second. Limiting
	// each connection rather than the server as a whole is what makes
	// concurrency worth anything, and is the usual shape of a real limit: a
	// per connection cap upstream, or simply one congestion window per
	// connection.
	//
	// Zero means unlimited.
	bps int

	// tls serves over TLS, and http2 additionally offers HTTP/2. Over HTTP/2
	// every range is multiplexed onto one connection and so shares one bps
	// budget, which is the whole point of comparing the two.
	tls   bool
	http2 bool
}

// pacer meters bytes at a fixed rate. Deadlines accumulate rather than being
// measured from each sleep, so that the coarse granularity of a short sleep
// does not compound into drift over a whole transfer.
type pacer struct {
	mu   sync.Mutex
	bps  float64
	next time.Time
}

func (p *pacer) take(n int) {
	p.mu.Lock()
	now := time.Now()
	if p.next.Before(now) {
		p.next = now
	}
	at := p.next
	p.next = p.next.Add(time.Duration(float64(n) / p.bps * float64(time.Second)))
	p.mu.Unlock()
	if d := time.Until(at); d > 0 {
		time.Sleep(d)
	}
}

type pacerKey struct{}

// benchServer serves a file of a known size over a netProfile, honouring Range
// requests. It counts the requests it answers and the connections it accepts,
// both of which a benchmark can report.
type benchServer struct {
	*httptest.Server
	profile  netProfile
	body     []byte
	sum      []byte
	requests int64
	conns    int64
}

func newBenchServer(tb testing.TB, size int, p netProfile) *benchServer {
	tb.Helper()
	body := make([]byte, size)
	for i := range body {
		body[i] = byte(i)
	}
	sum := sha256.Sum256(body)
	s := &benchServer{profile: p, body: body, sum: sum[:]}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	srv.EnableHTTP2 = p.http2
	if p.bps > 0 {
		srv.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
			// one budget per connection, shared by every stream on it
			return context.WithValue(ctx, pacerKey{}, &pacer{bps: float64(p.bps)})
		}
	}
	srv.Listener = &countingListener{Listener: srv.Listener, n: &s.conns}
	if p.tls || p.http2 {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	s.Server = srv
	tb.Cleanup(srv.Close)
	return s
}

type countingListener struct {
	net.Listener
	n *int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		atomic.AddInt64(l.n, 1)
	}
	return c, err
}

func (s *benchServer) serve(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&s.requests, 1)

	total := int64(len(s.body))
	start, end, partial := int64(0), total, false
	if spec, ok := strings.CutPrefix(r.Header.Get("Range"), "bytes="); ok {
		first, last, _ := strings.Cut(spec, "-")
		if v, err := strconv.ParseInt(first, 10, 64); err == nil {
			start = v
		}
		if v, err := strconv.ParseInt(last, 10, 64); err == nil && v+1 < end {
			end = v + 1
		}
		partial = true
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end-1, total))
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", `"bench"`)
	w.Header().Set("Content-Length", strconv.FormatInt(end-start, 10))

	if s.profile.rtt > 0 {
		time.Sleep(s.profile.rtt)
	}
	if partial {
		w.WriteHeader(http.StatusPartialContent)
	}
	if r.Method == http.MethodHead {
		return
	}

	p, _ := r.Context().Value(pacerKey{}).(*pacer)
	const slice = 32 << 10
	for off := start; off < end; off += slice {
		to := min(off+slice, end)
		if p != nil {
			p.take(int(to - off))
		}
		if _, err := w.Write(s.body[off:to]); err != nil {
			return
		}
		if p != nil {
			// send it now rather than at the end, so that the pacing the
			// client sees is the pacing that was applied
			w.(http.Flusher).Flush()
		}
	}
}

// client returns a grab Client configured to talk to this server.
//
// Each call builds its own connection pool. Sharing one would let connections
// opened by an earlier benchmark carry over into a later one, which both hides
// the cost of establishing them and makes the connection count meaningless -
// the pass the testing package makes over a benchmark to discover its
// sub-benchmarks is enough to fill a shared pool.
func (s *benchServer) client() *Client {
	c := NewClient()
	if s.profile.tls || s.profile.http2 {
		// clone the client httptest built, which trusts the server's
		// certificate and negotiates the protocol the server offers
		t := s.Server.Client().Transport.(*http.Transport).Clone()
		// match what NewClient does, so the pool is not the variable under test
		t.MaxIdleConnsPerHost = t.MaxIdleConns
		c.HTTPClient = &http.Client{Transport: t}
	}
	return c
}

// benchConfig is the part of a Request that the transfer benchmarks vary.
type benchConfig struct {
	rangeSize int64
	conc      int
	durable   bool
}

// download fetches the whole file into dir and returns how long it took.
func (s *benchServer) download(dir string, c *Client, cfg benchConfig) (time.Duration, error) {
	dst := filepath.Join(dir, "payload.bin")
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	if err := os.Remove(checkpointFilename(dst)); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	req, err := NewRequest(dst, s.URL)
	if err != nil {
		return 0, err
	}
	req.RangeSize = cfg.rangeSize
	req.Concurrency = cfg.conc
	req.Durable = cfg.durable
	start := time.Now()
	if err := c.Do(req).Err(); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// verify downloads the file once and checks it arrived intact. Every benchmark
// runs it before starting the clock, so that a harness which is fast because
// it is broken cannot be mistaken for a fast transfer.
func (s *benchServer) verify(tb testing.TB, dir string, c *Client, cfg benchConfig) {
	tb.Helper()
	if _, err := s.download(dir, c, cfg); err != nil {
		tb.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "payload.bin"))
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		tb.Fatal(err)
	}
	if got := h.Sum(nil); !bytes.Equal(got, s.sum) {
		tb.Fatalf("benchmark server served the wrong bytes: %x, expected %x", got, s.sum)
	}
}

// benchDownload is the body shared by every transfer benchmark.
func benchDownload(b *testing.B, s *benchServer, cfg benchConfig) {
	dir := b.TempDir()
	c := s.client()

	// Count connections from before the untimed run, since they are pooled and
	// reused: what the metric is worth showing is how many distinct
	// connections the transfer used, not how many it opened in any one
	// iteration.
	atomic.StoreInt64(&s.conns, 0)
	s.verify(b, dir, c, cfg)
	atomic.StoreInt64(&s.requests, 0)

	b.SetBytes(int64(len(s.body)))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := s.download(dir, c, cfg); err != nil {
			b.Fatal(err)
		}
	}

	b.StopTimer()
	b.ReportMetric(float64(atomic.LoadInt64(&s.requests))/float64(b.N), "reqs/op")
	b.ReportMetric(float64(atomic.LoadInt64(&s.conns)), "conns")
}

func rangeLabel(n int64) string {
	if n == 0 {
		return "off"
	}
	return byteLabel(n)
}

func byteLabel(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%dMiB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%dKiB", n>>10)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// BenchmarkTransfer measures what grab costs to move a file, with no network
// in the way. Everything it measures is work grab does: planning the ranges,
// dispatching them to workers, writing them at their offsets, and recording
// progress in the checkpoint.
//
// range=off is the unsplit transfer, and is the baseline the rest should be
// compared against. durable=true adds the flush and the checkpoint write that
// recording progress costs; the gap between the two is the price of Durable on
// whatever device b.TempDir() lives on.
func BenchmarkTransfer(b *testing.B) {
	const size = 8 << 20
	s := newBenchServer(b, size, netProfile{})

	for _, rangeSize := range []int64{0, 4 << 20, 1 << 20, 256 << 10, 64 << 10} {
		for _, conc := range []int{1, 4, 16} {
			if rangeSize == 0 && conc != 1 {
				continue // concurrency does nothing without ranges
			}
			for _, durable := range []bool{false, true} {
				if rangeSize == 0 && durable {
					continue // an unsplit transfer records nothing
				}
				name := fmt.Sprintf("range=%s/conc=%d/durable=%v",
					rangeLabel(rangeSize), conc, durable)
				b.Run(name, func(b *testing.B) {
					benchDownload(b, s, benchConfig{rangeSize, conc, durable})
				})
			}
		}
	}
}

// BenchmarkRangeSize shows what Request.RangeSize costs and buys.
//
// Each range costs a round trip before its first byte arrives, so throughput
// falls away as ranges approach the bandwidth-delay product of a connection -
// here 8 MiB/s x 20ms, or 160 KiB. Expect roughly 1/(1 + BDP/RangeSize) of the
// throughput of an arbitrarily large range: little loss at 1 MiB, about half
// at 160 KiB, and worse than not splitting at all by 32 KiB.
func BenchmarkRangeSize(b *testing.B) {
	const (
		size = 4 << 20
		rtt  = 20 * time.Millisecond
		bps  = 16 << 20
	)
	s := newBenchServer(b, size, netProfile{rtt: rtt, bps: bps})
	b.Logf("bandwidth-delay product per connection: %s",
		byteLabel(int64(float64(bps)*rtt.Seconds())))

	for _, rangeSize := range []int64{0, 2 << 20, 1 << 20, 256 << 10, 64 << 10, 32 << 10} {
		b.Run(fmt.Sprintf("range=%s", rangeLabel(rangeSize)), func(b *testing.B) {
			benchDownload(b, s, benchConfig{rangeSize: rangeSize, conc: 4})
		})
	}
}

// BenchmarkConcurrency shows what Request.Concurrency buys, and that the
// answer depends on the protocol.
//
// The server meters each TCP connection. Over HTTP/1.1 each range in flight
// has its own connection and its own budget, so throughput scales with
// Concurrency. Over HTTP/2 every range is multiplexed onto one connection and
// shares one budget, so throughput converges on that budget; the gain from one
// to two is a range waiting on its round trip being overlapped by another
// transferring, and there is nothing left to overlap after that.
//
// The conns metric is the giveaway: it tracks Concurrency over HTTP/1.1 and
// stays at one over HTTP/2.
func BenchmarkConcurrency(b *testing.B) {
	const (
		size = 4 << 20
		rtt  = 20 * time.Millisecond
		bps  = 16 << 20
	)
	for _, proto := range []struct {
		name  string
		http2 bool
	}{{"HTTP1.1", false}, {"HTTP2", true}} {
		s := newBenchServer(b, size, netProfile{
			rtt: rtt, bps: bps, tls: true, http2: proto.http2,
		})
		for _, conc := range []int{1, 2, 4, 8} {
			b.Run(fmt.Sprintf("proto=%s/conc=%d", proto.name, conc), func(b *testing.B) {
				benchDownload(b, s, benchConfig{rangeSize: 512 << 10, conc: conc})
			})
		}
	}
}

// BenchmarkRangeSet measures the interval set that records what a transfer has
// written. Workers add to it after every write, so it is on the hot path of
// every split transfer and its cost is paid per buffer rather than per range.
//
// sequential is the shape a transfer actually produces: each worker extends
// its own range, and the set collapses to one interval as they meet.
// fragmented is the worst case, a set that never coalesces.
func BenchmarkRangeSet(b *testing.B) {
	const writes = 1024

	b.Run("shape=sequential/workers=8", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			s := &rangeSet{}
			for w := int64(0); w < 8; w++ {
				start := w * writes
				for n := int64(1); n <= writes/8; n++ {
					s.add(start, start+n)
				}
			}
		}
	})

	b.Run("shape=fragmented", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			s := &rangeSet{}
			for n := int64(0); n < writes; n++ {
				s.add(n*2, n*2+1) // every interval leaves a gap
			}
		}
	})

	b.Run("op=missing", func(b *testing.B) {
		s := &rangeSet{}
		for n := int64(0); n < writes; n++ {
			s.add(n*2, n*2+1)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = s.missing(writes*2, 64)
		}
	})
}

// BenchmarkCheckpointStore measures writing a checkpoint, which flushes the
// destination file and renames a new checkpoint over the old one. It is the
// durability cost of a split transfer, and is paid once per
// checkpointInterval, not once per range.
func BenchmarkCheckpointStore(b *testing.B) {
	for _, n := range []int{1, 64} {
		b.Run(fmt.Sprintf("intervals=%d", n), func(b *testing.B) {
			dir := b.TempDir()
			filename := filepath.Join(dir, "payload.bin")
			dst, err := os.Create(filename)
			if err != nil {
				b.Fatal(err)
			}
			defer dst.Close()
			if _, err := dst.Write(make([]byte, 1<<20)); err != nil {
				b.Fatal(err)
			}

			complete := &rangeSet{}
			for i := int64(0); i < int64(n); i++ {
				complete.add(i*2048, i*2048+1024)
			}
			c := newCheckpoint(filename, "http://example.com/payload.bin",
				1<<20, 1<<16, testHeader(`"bench"`, ""))

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := c.store(dst, complete); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
