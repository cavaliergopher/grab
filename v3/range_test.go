package grab

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cavaliergopher/grab/v3/pkg/grabtest"
)

// assertFileContents asserts that the given file holds the body the test server
// serves for a file of the given size. Each byte of that body is addressed by
// its offset, so this catches a range that was written at the wrong offset just
// as readily as one that was never written at all.
func assertFileContents(t *testing.T, filename string, size int) {
	t.Helper()
	b, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != size {
		t.Fatalf("expected %d bytes in %s, got: %d", size, filename, len(b))
	}
	for i, got := range b {
		if want := byte(i); got != want {
			t.Fatalf("byte %d of %s: expected %d, got: %d", i, filename, want, got)
		}
	}
}

func assertNoCheckpoint(t *testing.T, filename string) {
	t.Helper()
	if _, err := os.Stat(checkpointFilename(filename)); !os.IsNotExist(err) {
		t.Errorf("expected no checkpoint file beside %s, got: %v", filename, err)
	}
}

func readCheckpoint(t *testing.T, filename string) *checkpoint {
	t.Helper()
	b, err := os.ReadFile(checkpointFilename(filename))
	if err != nil {
		t.Fatal(err)
	}
	c := &checkpoint{}
	if err := json.Unmarshal(b, c); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestRangeTransfer tests downloading a file as a series of Range requests.
func TestRangeTransfer(t *testing.T) {
	tests := []struct {
		Name        string
		Size        int
		RangeSize   int64
		Concurrency int
		// the number of GET requests the transfer should make
		ExpectRequests int
	}{
		// a file that divides evenly into ranges, and one that leaves a short
		// final range - the latter is what catches an off by one in the Range
		// header, as the last range would run past the end of the file
		{"Exact multiple", 4096, 1024, 4, 4},
		{"Short final range", 4000, 1024, 4, 4},
		{"One byte over", 1025, 1024, 4, 2},

		{"Sequential", 4096, 1024, 1, 4},
		{"More workers than ranges", 4096, 1024, 16, 4},
		{"Concurrency unset", 4096, 1024, 0, 4},
		{"Single byte ranges", 16, 1, 4, 16},

		// a range size that cannot split the file leaves the transfer as the
		// single request grab has always made
		{"Range size equal to the file", 4096, 4096, 4, 1},
		{"Range size larger than the file", 4096, 8192, 4, 1},
		{"Range size unset", 4096, 0, 4, 1},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "testRangeTransfer")
			rec := &grabtest.RangeRecorder{}
			grabtest.WithTestServer(t, func(url string) {
				req := mustNewRequest(filename, url)
				req.RangeSize = test.RangeSize
				req.Concurrency = test.Concurrency
				resp := mustDo(req)

				if resp.Size() != int64(test.Size) {
					t.Errorf("expected size %d, got: %d", test.Size, resp.Size())
				}
				if n := len(rec.Ranges()); n != test.ExpectRequests {
					t.Errorf("expected %d requests, got: %d, %v", test.ExpectRequests, n, rec.Ranges())
				}
				// every byte of the file was requested, none of them twice,
				// and nothing beyond the end of the file
				grabtest.AssertRangesCover(t, rec, int64(test.Size))
				testComplete(t, resp)
			},
				grabtest.ContentLength(test.Size),
				grabtest.RecordRanges(rec),
			)

			assertFileContents(t, filename, test.Size)
			// a completed transfer leaves nothing behind
			assertNoCheckpoint(t, filename)
		})
	}
}

// rangeHeaderSpy records the Range header of every GET request.
type rangeHeaderSpy struct {
	client HTTPClient
	mu     sync.Mutex
	seen   []string
}

func (c *rangeHeaderSpy) Do(req *http.Request) (*http.Response, error) {
	if req.Method == "GET" {
		c.mu.Lock()
		c.seen = append(c.seen, req.Header.Get("Range"))
		c.mu.Unlock()
	}
	return c.client.Do(req)
}

func (c *rangeHeaderSpy) headers() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

// TestRangeTransferRequestHeaders pins the Range headers grab puts on the wire.
//
// Building every transfer out of ranges must not change the requests made by
// the transfers that are not split: a download still asks for the whole file
// with no Range header at all, and a resume still asks for everything from an
// offset rather than up to the size the server last reported.
func TestRangeTransferRequestHeaders(t *testing.T) {
	const size = 2048
	tests := []struct {
		Name string
		// bytes of an existing partial file, if any
		Partial   int
		RangeSize int64
		// Filename is resolved from the URL when Anonymous is set, which is
		// what forces the HEAD request for a file that does not exist yet
		Anonymous bool
		Expect    []string
	}{
		{Name: "Fresh download", Expect: []string{""}},
		{Name: "Fresh download after a HEAD", Anonymous: true, Expect: []string{""}},
		{Name: "Resume", Partial: 512, Expect: []string{"bytes=512-"}},
		{
			Name:      "Split",
			RangeSize: 512,
			Expect:    []string{"bytes=0-511", "bytes=512-1023", "bytes=1024-1535", "bytes=1536-2047"},
		},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			dir := t.TempDir()
			filename := filepath.Join(dir, "testRangeHeaders")
			dst := filename
			if test.Anonymous {
				dst = dir
			}
			if test.Partial > 0 {
				if err := os.WriteFile(filename, make([]byte, test.Partial), 0666); err != nil {
					t.Fatal(err)
				}
			}

			spy := &rangeHeaderSpy{client: DefaultClient.HTTPClient}
			client := NewClient()
			client.HTTPClient = spy
			grabtest.WithTestServer(t, func(url string) {
				req := mustNewRequest(dst, url+"/testRangeHeaders")
				req.RangeSize = test.RangeSize
				req.Concurrency = 1 // so that the ranges are requested in order
				if err := client.Do(req).Err(); err != nil {
					t.Fatal(err)
				}
			},
				grabtest.ContentLength(size),
			)

			if got := spy.headers(); fmt.Sprint(got) != fmt.Sprint(test.Expect) {
				t.Errorf("expected Range headers %q, got: %q", test.Expect, got)
			}
		})
	}
}

// TestRangeTransferFallsBack tests that a transfer which cannot be split is
// still downloaded correctly, with a single request.
func TestRangeTransferFallsBack(t *testing.T) {
	size := 4096
	tests := []struct {
		Name    string
		Options []grabtest.HandlerOption
	}{
		{"Server does not accept ranges", []grabtest.HandlerOption{grabtest.AcceptRanges(false)}},
		{"Size unknown", []grabtest.HandlerOption{grabtest.HeaderBlacklist("Content-Length")}},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "testRangeFallback")
			rec := &grabtest.RangeRecorder{}
			opts := append([]grabtest.HandlerOption{
				grabtest.ContentLength(size),
				grabtest.RecordRanges(rec),
			}, test.Options...)

			grabtest.WithTestServer(t, func(url string) {
				req := mustNewRequest(filename, url)
				req.RangeSize = 1024
				req.Concurrency = 4
				resp := mustDo(req)

				if n := len(rec.Ranges()); n != 1 {
					t.Errorf("expected a single request, got: %d, %v", n, rec.Ranges())
				}
				if resp.DidResume {
					t.Error("expected Response.DidResume to be false")
				}
				testComplete(t, resp)
			}, opts...)

			assertFileContents(t, filename, size)
			assertNoCheckpoint(t, filename)
		})
	}
}

// TestRangeTransferNoStore tests that a transfer held in memory is downloaded
// correctly, and writes no checkpoint since there is no file to checkpoint.
func TestRangeTransferNoStore(t *testing.T) {
	size := 4096
	grabtest.WithTestServer(t, func(url string) {
		req := mustNewRequest("", url)
		req.NoStore = true
		req.RangeSize = 1024
		req.Concurrency = 4
		resp := mustDo(req)

		b, err := resp.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		if len(b) != size {
			t.Fatalf("expected %d bytes, got: %d", size, len(b))
		}
		for i, got := range b {
			if want := byte(i); got != want {
				t.Fatalf("byte %d: expected %d, got: %d", i, want, got)
			}
		}
		testComplete(t, resp)
	},
		grabtest.ContentLength(size),
	)
}

// TestRangeTransferChecksum tests that a split transfer assembles a file whose
// checksum matches the whole, using the checksums published for the test
// server's body.
func TestRangeTransferChecksum(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "testRangeChecksum")
	grabtest.WithTestServer(t, func(url string) {
		req := mustNewRequest(filename, url)
		req.RangeSize = 64 * 1024
		req.Concurrency = 4
		req.SetChecksum(sha256.New(), grabtest.DefaultHandlerSHA256ChecksumBytes, true)
		resp := mustDo(req)
		testComplete(t, resp)
	})
	assertNoCheckpoint(t, filename)
}

// rangeFailClient fails range requests once the given number of them have been
// served. If once is set, only the first request past that point fails and the
// rest are served normally.
type rangeFailClient struct {
	client HTTPClient
	after  int64
	once   bool
	served int64
	failed int64
	err    error
}

func (c *rangeFailClient) Do(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Range") != "" && atomic.AddInt64(&c.served, 1) > c.after {
		if !c.once || atomic.AddInt64(&c.failed, 1) == 1 {
			return nil, c.err
		}
	}
	return c.client.Do(req)
}

// TestRangeTransferReportsTheFailure tests that when one range of a concurrent
// transfer fails, the transfer shuts its workers down and reports the
// underlying failure, rather than the cancelation that failure causes in every
// other worker.
func TestRangeTransferReportsTheFailure(t *testing.T) {
	const (
		size      = 32768
		rangeSize = 1024
	)
	failure := errors.New("TEST: range failed")
	filename := filepath.Join(t.TempDir(), "testRangeFirstError")
	grabtest.WithTestServer(t, func(url string) {
		// exactly one range fails, so every other worker can only fail with
		// the cancelation that failure causes
		failing := &rangeFailClient{
			client: DefaultClient.HTTPClient,
			after:  4,
			once:   true,
			err:    failure,
		}
		client := NewClient()
		client.HTTPClient = failing

		req := mustNewRequest(filename, url)
		req.RangeSize = rangeSize
		req.Concurrency = 8
		resp := client.Do(req)
		if err := resp.Err(); !errors.Is(err, failure) {
			t.Fatalf("expected the transfer to fail with %v, got: %v", failure, err)
		}
		testComplete(t, resp)
	},
		grabtest.ContentLength(size),
		// a slow server keeps the other workers reading when the failure
		// happens, so that they are cancelled part way through a range
		grabtest.RateLimiter(2048),
	)
}

// TestRangeTransferResume tests that a split transfer which is interrupted
// records what it completed, and that a later transfer resumes from that record
// rather than downloading the file again.
func TestRangeTransferResume(t *testing.T) {
	const (
		size      = 32768
		rangeSize = 4096
		ranges    = size / rangeSize
	)
	filename := filepath.Join(t.TempDir(), "testRangeResume")
	failure := errors.New("TEST: range failed")

	rec := &grabtest.RangeRecorder{}
	grabtest.WithTestServer(t, func(url string) {
		// Fail part way through, having transferred some of the ranges. One
		// worker keeps the ranges that succeed contiguous, so exactly which
		// ranges completed is predictable.
		failing := &rangeFailClient{client: DefaultClient.HTTPClient, after: 2, err: failure}
		client := NewClient()
		client.HTTPClient = failing

		req := mustNewRequest(filename, url)
		req.RangeSize = rangeSize
		req.Durable = true
		req.Concurrency = 1
		resp := client.Do(req)
		if err := resp.Err(); !errors.Is(err, failure) {
			t.Fatalf("expected the transfer to fail with %v, got: %v", failure, err)
		}
		testComplete(t, resp)

		// the interrupted transfer recorded the ranges it completed
		c := readCheckpoint(t, filename)
		expect := [][2]int64{{0, 2 * rangeSize}}
		if fmt.Sprint(c.Complete) != fmt.Sprint(expect) {
			t.Fatalf("expected checkpoint ranges %v, got: %v", expect, c.Complete)
		}
		if c.Size != size || c.RangeSize != rangeSize {
			t.Fatalf("unexpected checkpoint: %+v", c)
		}
		if resp.BytesComplete() != 2*rangeSize {
			t.Errorf("expected %d bytes complete, got: %d", 2*rangeSize, resp.BytesComplete())
		}

		// now let the transfer finish
		rec.Reset()
		req = mustNewRequest(filename, url)
		req.RangeSize = rangeSize
		req.Durable = true
		req.Concurrency = 4
		req.SetChecksum(sha256.New(), sha256OfTestBody(t, size), false)
		resp = mustDo(req)

		if !resp.DidResume {
			t.Error("expected Response.DidResume to be true")
		}
		if resp.BytesComplete() != size {
			t.Errorf("expected %d bytes complete, got: %d", size, resp.BytesComplete())
		}
		// only the ranges that were missing were requested again
		got := rec.Ranges()
		if len(got) != ranges-2 {
			t.Errorf("expected %d requests to finish the transfer, got: %d, %v",
				ranges-2, len(got), got)
		}
		for _, r := range got {
			if r[0] < 2*rangeSize {
				t.Errorf("range %v was already complete and should not have been requested", r)
			}
		}
		testComplete(t, resp)
	},
		grabtest.ContentLength(size),
		grabtest.RecordRanges(rec),
	)

	assertFileContents(t, filename, size)
	assertNoCheckpoint(t, filename)
}

// truncatedBody fails part way through reading a response body, so that a
// range is interrupted after some of it has been written to the destination.
type truncatedBody struct {
	rc    io.ReadCloser
	limit int64
	n     int64
	err   error
}

func (b *truncatedBody) Read(p []byte) (int, error) {
	if b.n >= b.limit {
		return 0, b.err
	}
	if int64(len(p)) > b.limit-b.n {
		p = p[:b.limit-b.n]
	}
	n, err := b.rc.Read(p)
	b.n += int64(n)
	return n, err
}

func (b *truncatedBody) Close() error { return b.rc.Close() }

// bodyFailClient truncates the body of the nth range response.
type bodyFailClient struct {
	client HTTPClient
	nth    int64
	limit  int64
	err    error
	seen   int64
}

func (c *bodyFailClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.client.Do(req)
	if err != nil || req.Header.Get("Range") == "" {
		return resp, err
	}
	if atomic.AddInt64(&c.seen, 1) == c.nth {
		resp.Body = &truncatedBody{rc: resp.Body, limit: c.limit, err: c.err}
	}
	return resp, nil
}

// TestRangeTransferCheckpointsPartialRanges tests that a transfer interrupted
// part way through a range keeps the part of it that was written.
//
// Recording only whole ranges would tie how much an interruption costs to
// RangeSize, which is chosen for throughput and can be far larger than anyone
// would want to lose.
func TestRangeTransferCheckpointsPartialRanges(t *testing.T) {
	const (
		size      = 32768
		rangeSize = 8192
		bufSize   = 1024
		// the second range fails once this much of it has been written, which
		// is not a multiple of rangeSize
		partial = 5 * bufSize
		expect  = rangeSize + partial
	)
	filename := filepath.Join(t.TempDir(), "testRangePartial")
	failure := errors.New("TEST: body truncated")

	rec := &grabtest.RangeRecorder{}
	grabtest.WithTestServer(t, func(url string) {
		client := NewClient()
		client.HTTPClient = &bodyFailClient{
			client: DefaultClient.HTTPClient,
			nth:    2,
			limit:  partial,
			err:    failure,
		}

		req := mustNewRequest(filename, url)
		req.RangeSize = rangeSize
		req.Durable = true
		req.Concurrency = 1 // so the ranges are attempted in order
		req.BufferSize = bufSize
		resp := client.Do(req)
		if err := resp.Err(); !errors.Is(err, failure) {
			t.Fatalf("expected the transfer to fail with %v, got: %v", failure, err)
		}

		// the checkpoint records the whole first range plus the part of the
		// second that was written, which is not a range boundary
		c := readCheckpoint(t, filename)
		want := [][2]int64{{0, expect}}
		if fmt.Sprint(c.Complete) != fmt.Sprint(want) {
			t.Fatalf("expected checkpoint ranges %v, got: %v", want, c.Complete)
		}
		if resp.BytesComplete() != expect {
			t.Errorf("expected %d bytes complete, got: %d", expect, resp.BytesComplete())
		}

		// resuming picks up mid range, and asks for the remainder of it first
		rec.Reset()
		req = mustNewRequest(filename, url)
		req.RangeSize = rangeSize
		req.Durable = true
		req.Concurrency = 1
		req.SetChecksum(sha256.New(), sha256OfTestBody(t, size), false)
		resp = mustDo(req)

		if !resp.DidResume {
			t.Error("expected Response.DidResume to be true")
		}
		got := rec.Ranges()
		if len(got) == 0 || got[0][0] != expect {
			t.Fatalf("expected the first request to resume at %d, got: %v", expect, got)
		}
		// and nothing already written is fetched again
		for _, r := range got {
			if r[0] < expect {
				t.Errorf("range %v was already written and should not have been requested", r)
			}
		}
		testComplete(t, resp)
	},
		grabtest.ContentLength(size),
		grabtest.RecordRanges(rec),
	)

	assertFileContents(t, filename, size)
	assertNoCheckpoint(t, filename)
}

// TestRangeTransferResumeDiscardsStaleCheckpoint tests that a checkpoint left
// behind by a transfer of a file that has since been modified is not used to
// assemble a local copy out of two different remote files.
func TestRangeTransferResumeDiscardsStaleCheckpoint(t *testing.T) {
	const (
		size      = 32768
		rangeSize = 4096
	)
	filename := filepath.Join(t.TempDir(), "testRangeStaleCheckpoint")
	failure := errors.New("TEST: range failed")

	var version int64
	rec := &grabtest.RangeRecorder{}
	grabtest.WithTestServer(t, func(url string) {
		failing := &rangeFailClient{client: DefaultClient.HTTPClient, after: 2, err: failure}
		client := NewClient()
		client.HTTPClient = failing

		req := mustNewRequest(filename, url)
		req.RangeSize = rangeSize
		req.Durable = true
		req.Concurrency = 1
		if err := client.Do(req).Err(); !errors.Is(err, failure) {
			t.Fatalf("expected the transfer to fail with %v, got: %v", failure, err)
		}
		readCheckpoint(t, filename) // it exists

		// the remote file changes before the transfer is resumed
		atomic.AddInt64(&version, 1)

		rec.Reset()
		req = mustNewRequest(filename, url)
		req.RangeSize = rangeSize
		req.Durable = true
		req.Concurrency = 4
		req.SetChecksum(sha256.New(), sha256OfTestBody(t, size), false)
		resp := mustDo(req)

		if resp.DidResume {
			t.Error("expected Response.DidResume to be false for a modified remote file")
		}
		grabtest.AssertRangesCover(t, rec, size)
		testComplete(t, resp)
	},
		grabtest.ContentLength(size),
		grabtest.RecordRanges(rec),
		grabtest.ETag(func(req *http.Request) string {
			return fmt.Sprintf(`"v%d"`, atomic.LoadInt64(&version))
		}),
	)

	assertFileContents(t, filename, size)
	assertNoCheckpoint(t, filename)
}

// TestRangeTransferNoResume tests that Request.NoResume discards a checkpoint
// and downloads the file again.
func TestRangeTransferNoResume(t *testing.T) {
	const (
		size      = 32768
		rangeSize = 4096
	)
	filename := filepath.Join(t.TempDir(), "testRangeNoResume")
	failure := errors.New("TEST: range failed")

	rec := &grabtest.RangeRecorder{}
	grabtest.WithTestServer(t, func(url string) {
		failing := &rangeFailClient{client: DefaultClient.HTTPClient, after: 2, err: failure}
		client := NewClient()
		client.HTTPClient = failing

		req := mustNewRequest(filename, url)
		req.RangeSize = rangeSize
		req.Durable = true
		req.Concurrency = 1
		if err := client.Do(req).Err(); !errors.Is(err, failure) {
			t.Fatalf("expected the transfer to fail with %v, got: %v", failure, err)
		}

		rec.Reset()
		req = mustNewRequest(filename, url)
		req.RangeSize = rangeSize
		req.Durable = true
		req.Concurrency = 4
		req.NoResume = true
		resp := mustDo(req)

		if resp.DidResume {
			t.Error("expected Response.DidResume to be false")
		}
		grabtest.AssertRangesCover(t, rec, size)
		testComplete(t, resp)
	},
		grabtest.ContentLength(size),
		grabtest.RecordRanges(rec),
	)

	assertFileContents(t, filename, size)
	assertNoCheckpoint(t, filename)
}

// TestRangeTransferHoleInFullLengthFile tests that a split transfer trusts its
// checkpoint over the length of the destination file.
//
// A split transfer writes each range at its offset, so one whose final range
// completed leaves a file of the full length with a hole in the middle of it.
// Taking that length to mean the file is complete would leave the hole there
// for good.
func TestRangeTransferHoleInFullLengthFile(t *testing.T) {
	const (
		size      = 32768
		rangeSize = 4096
		holeStart = 2 * rangeSize
		holeEnd   = 3 * rangeSize
	)
	filename := filepath.Join(t.TempDir(), "testRangeHole")

	rec := &grabtest.RangeRecorder{}
	grabtest.WithTestServer(t, func(url string) {
		writeHoledFile(t, filename, url, size, rangeSize, holeStart, holeEnd)

		req := mustNewRequest(filename, url)
		req.RangeSize = rangeSize
		req.Durable = true
		req.Concurrency = 4
		req.SetChecksum(sha256.New(), sha256OfTestBody(t, size), false)
		resp := mustDo(req)

		if !resp.DidResume {
			t.Error("expected Response.DidResume to be true")
		}
		// only the hole was requested
		expect := [][2]int64{{holeStart, holeEnd}}
		if got := rec.Ranges(); fmt.Sprint(got) != fmt.Sprint(expect) {
			t.Errorf("expected ranges %v to be requested, got: %v", expect, got)
		}
		testComplete(t, resp)
	},
		grabtest.ContentLength(size),
		grabtest.RecordRanges(rec),
	)

	// the hole was filled with the right bytes
	assertFileContents(t, filename, size)
	assertNoCheckpoint(t, filename)
}

// TestRangeTransferAlreadyComplete tests that downloading a file that is
// already present in full transfers nothing.
func TestRangeTransferAlreadyComplete(t *testing.T) {
	const (
		size      = 32768
		rangeSize = 4096
	)
	filename := filepath.Join(t.TempDir(), "testRangeCheckpointComplete")

	rec := &grabtest.RangeRecorder{}
	grabtest.WithTestServer(t, func(url string) {
		req := mustNewRequest(filename, url)
		req.RangeSize = rangeSize
		req.Durable = true
		req.Concurrency = 4
		mustDo(req)

		// downloading again requests nothing, as the file is already complete
		rec.Reset()
		req = mustNewRequest(filename, url)
		req.RangeSize = rangeSize
		req.Durable = true
		req.Concurrency = 4
		req.SetChecksum(sha256.New(), sha256OfTestBody(t, size), false)
		resp := mustDo(req)

		if n := len(rec.Ranges()); n != 0 {
			t.Errorf("expected no requests for a complete file, got: %d, %v", n, rec.Ranges())
		}
		if !resp.DidResume {
			t.Error("expected Response.DidResume to be true")
		}
		testComplete(t, resp)
	},
		grabtest.ContentLength(size),
		grabtest.RecordRanges(rec),
	)
	assertFileContents(t, filename, size)
}

// sha256OfTestBody returns the SHA-256 checksum of the body the test server
// serves for a file of the given size.
func sha256OfTestBody(t *testing.T, size int) []byte {
	t.Helper()
	h := sha256.New()
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(i)
	}
	h.Write(b)
	return h.Sum(nil)
}

// writeHoledFile builds the state an interrupted split transfer leaves behind:
// a file of the full length whose every range but one was written, and a
// checkpoint that records exactly those ranges.
func writeHoledFile(t *testing.T, filename, url string, size, rangeSize, holeStart, holeEnd int64) {
	t.Helper()
	body := make([]byte, size)
	for i := range body {
		body[i] = byte(i)
	}
	for i := holeStart; i < holeEnd; i++ {
		body[i] = 0xff // never written, and not what the server would send
	}
	if err := os.WriteFile(filename, body, 0666); err != nil {
		t.Fatal(err)
	}

	head := grabtest.MustHTTPDoWithClose(grabtest.MustHTTPNewRequest("HEAD", url, nil))
	complete := newRangeSet([2]int64{0, holeStart}, [2]int64{holeEnd, size})
	c := newCheckpoint(filename, url, size, rangeSize, head.Header)
	f, err := os.OpenFile(filename, os.O_WRONLY, 0666)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store(f, complete); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// TestUnsplitTransferDiscardsHoledFile tests that a transfer which is not split
// refuses to resume from the length of a file that a split transfer wrote out
// of order. Such a file can already be full length while parts of it were
// never written, so its length says nothing about which of its bytes are
// valid, and only the checkpoint beside it marks it as written out of order.
func TestUnsplitTransferDiscardsHoledFile(t *testing.T) {
	const (
		size      = 32768
		rangeSize = 4096
		holeStart = 2 * rangeSize
		holeEnd   = 3 * rangeSize
	)
	filename := filepath.Join(t.TempDir(), "testUnsplitHole")

	grabtest.WithTestServer(t, func(url string) {
		writeHoledFile(t, filename, url, size, rangeSize, holeStart, holeEnd)

		// RangeSize is unset, so this transfer would not have split the file,
		// and no checksum is set to catch a resume that leaves the hole.
		resp := mustDo(mustNewRequest(filename, url))
		if resp.DidResume {
			t.Error("expected Response.DidResume to be false")
		}
		testComplete(t, resp)
	},
		grabtest.ContentLength(size),
	)

	assertFileContents(t, filename, size)
	assertNoCheckpoint(t, filename)
}

// TestRangeTransferWithoutDurable tests that a split transfer which is not
// durable records no progress, but still marks the destination file as written
// out of order, so that what it leaves behind is never mistaken for a file
// that can be resumed.
func TestRangeTransferWithoutDurable(t *testing.T) {
	const (
		size      = 32768
		rangeSize = 8192
	)
	filename := filepath.Join(t.TempDir(), "testRangeNotDurable")
	failure := errors.New("TEST: range failed")

	rec := &grabtest.RangeRecorder{}
	grabtest.WithTestServer(t, func(url string) {
		client := NewClient()
		client.HTTPClient = &rangeFailClient{
			client: DefaultClient.HTTPClient,
			after:  2,
			err:    failure,
		}

		req := mustNewRequest(filename, url)
		req.RangeSize = rangeSize
		req.Concurrency = 1
		if err := client.Do(req).Err(); !errors.Is(err, failure) {
			t.Fatalf("expected the transfer to fail with %v, got: %v", failure, err)
		}

		// the checkpoint accompanies the file, but claims nothing of it
		if c := readCheckpoint(t, filename); len(c.Complete) != 0 {
			t.Errorf("expected the checkpoint to record no progress, got: %v", c.Complete)
		}

		// so the transfer starts over rather than resuming what was written
		rec.Reset()
		req = mustNewRequest(filename, url)
		req.RangeSize = rangeSize
		req.Concurrency = 4
		req.SetChecksum(sha256.New(), sha256OfTestBody(t, size), false)
		resp := mustDo(req)

		if resp.DidResume {
			t.Error("expected Response.DidResume to be false")
		}
		grabtest.AssertRangesCover(t, rec, size)
		testComplete(t, resp)
	},
		grabtest.ContentLength(size),
		grabtest.RecordRanges(rec),
	)

	assertFileContents(t, filename, size)
	assertNoCheckpoint(t, filename)
}

// TestDurableTransferReportsSyncError tests that a durable transfer fails if
// the destination cannot be flushed, rather than reporting a success whose
// bytes may never have reached the disk.
func TestDurableTransferReportsSyncError(t *testing.T) {
	expect := errors.New("TEST: sync failed")
	w := &errTransferWriter{syncErr: expect}
	body := "hello world"
	resp := &Response{
		Request: &Request{NoStore: true, Durable: true},
		writer:  w,
		ranges:  []byteRange{{Start: 0, End: int64(len(body))}},
	}
	resp.transfer = newTransfer(resp, nil, io.NopCloser(strings.NewReader(body)), nil)
	resp.transfer.flush = newFlusher(w)

	if _, err := resp.transfer.copy(); !errors.Is(err, expect) {
		t.Errorf("expected error: %v, got: %v", expect, err)
	}
}

// TestDurableUnsplitTransfer tests that Request.Durable applies to a transfer
// that is not split, which has no checkpoint to record and so nothing to do
// but flush.
func TestDurableUnsplitTransfer(t *testing.T) {
	const size = 32768
	filename := filepath.Join(t.TempDir(), "testDurableUnsplit")

	rec := &grabtest.RangeRecorder{}
	grabtest.WithTestServer(t, func(url string) {
		req := mustNewRequest(filename, url)
		req.Durable = true
		req.SetChecksum(sha256.New(), sha256OfTestBody(t, size), false)
		resp := mustDo(req)

		// one request for the whole file, as without Durable
		if got := rec.Ranges(); len(got) != 1 {
			t.Errorf("expected a single request, got: %v", got)
		}
		// but flushed as it went, which is all Durable means here
		if resp.transfer.flush == nil {
			t.Error("expected the transfer to flush the destination")
		}
		testComplete(t, resp)
	},
		grabtest.ContentLength(size),
		grabtest.RecordRanges(rec),
	)

	assertFileContents(t, filename, size)
	assertNoCheckpoint(t, filename)
}
