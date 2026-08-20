package grab

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// newRangeSet returns a rangeSet holding the given [start, end) pairs.
func newRangeSet(ranges ...[2]int64) *rangeSet {
	s := &rangeSet{}
	for _, r := range ranges {
		s.add(r[0], r[1])
	}
	return s
}

func (s *rangeSet) pairs() [][2]int64 {
	pairs := make([][2]int64, len(s.ranges))
	for i, r := range s.ranges {
		pairs[i] = [2]int64{r.Start, r.End}
	}
	return pairs
}

func TestRangeSetAdd(t *testing.T) {
	tests := []struct {
		Name   string
		Add    [][2]int64
		Expect [][2]int64
	}{
		{"Empty", nil, [][2]int64{}},
		{"Single", [][2]int64{{0, 10}}, [][2]int64{{0, 10}}},
		{"In order", [][2]int64{{0, 10}, {20, 30}}, [][2]int64{{0, 10}, {20, 30}}},
		{"Out of order", [][2]int64{{20, 30}, {0, 10}}, [][2]int64{{0, 10}, {20, 30}}},

		// ranges that meet exactly are coalesced, which is the common case for
		// a transfer completing its ranges in order
		{"Meeting", [][2]int64{{0, 10}, {10, 20}}, [][2]int64{{0, 20}}},
		{"Meeting backwards", [][2]int64{{10, 20}, {0, 10}}, [][2]int64{{0, 20}}},
		{"Closing a gap", [][2]int64{{0, 10}, {20, 30}, {10, 20}}, [][2]int64{{0, 30}}},
		{"Closing many gaps", [][2]int64{{0, 10}, {20, 30}, {40, 50}, {10, 40}}, [][2]int64{{0, 50}}},

		{"Overlapping", [][2]int64{{0, 20}, {10, 30}}, [][2]int64{{0, 30}}},
		{"Enclosed", [][2]int64{{0, 30}, {10, 20}}, [][2]int64{{0, 30}}},
		{"Enclosing", [][2]int64{{10, 20}, {0, 30}}, [][2]int64{{0, 30}}},
		{"Duplicate", [][2]int64{{0, 10}, {0, 10}}, [][2]int64{{0, 10}}},
		{"Spanning several", [][2]int64{{0, 10}, {20, 30}, {40, 50}, {5, 45}}, [][2]int64{{0, 50}}},

		// a range that adds nothing is ignored rather than recorded
		{"Empty range", [][2]int64{{0, 10}, {10, 10}}, [][2]int64{{0, 10}}},
		{"Inverted range", [][2]int64{{0, 10}, {30, 20}}, [][2]int64{{0, 10}}},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			s := newRangeSet(test.Add...)
			if got := s.pairs(); !reflect.DeepEqual(got, test.Expect) && len(got)+len(test.Expect) > 0 {
				t.Errorf("expected: %v, got: %v", test.Expect, got)
			}
		})
	}
}

func TestRangeSetContiguousPrefix(t *testing.T) {
	tests := []struct {
		Name   string
		Set    [][2]int64
		Expect int64
	}{
		{"Empty", nil, 0},
		{"From the start", [][2]int64{{0, 10}}, 10},
		{"Not from the start", [][2]int64{{10, 20}}, 0},
		{"With a gap", [][2]int64{{0, 10}, {20, 30}}, 10},
		{"Coalesced", [][2]int64{{0, 10}, {10, 20}}, 20},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			if got := newRangeSet(test.Set...).contiguousPrefix(); got != test.Expect {
				t.Errorf("expected: %d, got: %d", test.Expect, got)
			}
		})
	}
}

func TestRangeSetCompletedBytes(t *testing.T) {
	tests := []struct {
		Name   string
		Set    [][2]int64
		Expect int64
	}{
		{"Empty", nil, 0},
		{"Single", [][2]int64{{0, 10}}, 10},
		{"Disjoint", [][2]int64{{0, 10}, {20, 35}}, 25},
		{"Overlapping counted once", [][2]int64{{0, 20}, {10, 30}}, 30},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			if got := newRangeSet(test.Set...).completedBytes(); got != test.Expect {
				t.Errorf("expected: %d, got: %d", test.Expect, got)
			}
		})
	}
}

func TestRangeSetMissing(t *testing.T) {
	tests := []struct {
		Name      string
		Set       [][2]int64
		Size      int64
		RangeSize int64
		Expect    [][2]int64
	}{
		{
			"Nothing complete", nil, 100, 25,
			[][2]int64{{0, 25}, {25, 50}, {50, 75}, {75, 100}},
		},
		{
			"Short final range", nil, 90, 25,
			[][2]int64{{0, 25}, {25, 50}, {50, 75}, {75, 90}},
		},
		{
			"Range size larger than the file", nil, 10, 25,
			[][2]int64{{0, 10}},
		},
		{
			"Unsplit", nil, 100, 0,
			[][2]int64{{0, 100}},
		},
		{
			"Everything complete", [][2]int64{{0, 100}}, 100, 25,
			nil,
		},
		{
			"Resuming a prefix", [][2]int64{{0, 50}}, 100, 25,
			[][2]int64{{50, 75}, {75, 100}},
		},
		{
			"Resuming with a hole", [][2]int64{{0, 25}, {50, 100}}, 100, 25,
			[][2]int64{{25, 50}},
		},
		{
			"Resuming with several holes", [][2]int64{{25, 50}, {75, 100}}, 100, 25,
			[][2]int64{{0, 25}, {50, 75}},
		},
		{
			"Hole larger than the range size", [][2]int64{{0, 25}, {75, 100}}, 100, 10,
			[][2]int64{{25, 35}, {35, 45}, {45, 55}, {55, 65}, {65, 75}},
		},
		{
			// a checkpoint that records more than the file now holds must not
			// produce ranges past the end of it
			"Complete past the end", [][2]int64{{0, 200}}, 100, 25,
			nil,
		},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			got := newRangeSet(test.Set...).missing(test.Size, test.RangeSize)
			expect := make([]byteRange, len(test.Expect))
			for i, r := range test.Expect {
				expect[i] = byteRange{r[0], r[1]}
			}
			if len(got)+len(expect) > 0 && !reflect.DeepEqual(got, expect) {
				t.Errorf("expected: %v, got: %v", expect, got)
			}
		})
	}
}

// TestRangeSetMissingCoversTheFile checks the invariant that matters most: for
// any set of complete ranges, the missing ranges plus the complete ones tile
// the file exactly, with no gaps and no overlaps.
func TestRangeSetMissingCoversTheFile(t *testing.T) {
	const size = 1000
	sets := [][][2]int64{
		nil,
		{{0, 100}},
		{{100, 200}},
		{{0, 100}, {200, 300}},
		{{0, 1}, {999, 1000}},
		{{500, 501}},
	}
	for _, rangeSize := range []int64{1, 7, 100, 999, 1000, 1001} {
		for _, ranges := range sets {
			set := newRangeSet(ranges...)
			all := newRangeSet(ranges...)
			for _, r := range set.missing(size, rangeSize) {
				if r.len() > rangeSize {
					t.Errorf("range %v exceeds the range size %d", r, rangeSize)
				}
				if r.Start < 0 || r.End > size {
					t.Errorf("range %v falls outside the file", r)
				}
				all.add(r.Start, r.End)
			}
			expect := [][2]int64{{0, size}}
			if got := all.pairs(); !reflect.DeepEqual(got, expect) {
				t.Errorf(
					"complete and missing ranges do not tile the file: %v, for range size %d over %v",
					got, rangeSize, ranges,
				)
			}
		}
	}
}

func testHeader(etag, lastModified string) http.Header {
	h := http.Header{}
	if etag != "" {
		h.Set("ETag", etag)
	}
	if lastModified != "" {
		h.Set("Last-Modified", lastModified)
	}
	return h
}

func TestCheckpointStoreAndLoad(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "testCheckpoint")
	url := "http://example.com/testCheckpoint"
	hdr := testHeader(`"abc"`, "")

	dst, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()

	c := newCheckpoint(filename, url, 1000, 100, hdr)
	if err := c.store(dst, newRangeSet([2]int64{0, 100}, [2]int64{200, 300})); err != nil {
		t.Fatal(err)
	}

	set, err := loadCheckpoint(filename, url, 1000, 100, hdr)
	if err != nil {
		t.Fatal(err)
	}
	if set == nil {
		t.Fatal("expected the checkpoint to be loaded")
	}
	expect := [][2]int64{{0, 100}, {200, 300}}
	if got := set.pairs(); !reflect.DeepEqual(got, expect) {
		t.Errorf("expected: %v, got: %v", expect, got)
	}

	// storing again replaces the file rather than accumulating temporary files
	// beside it
	if err := c.store(dst, newRangeSet([2]int64{0, 300})); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("expected only the destination and its checkpoint, got: %v", names)
	}

	if err := c.remove(); err != nil {
		t.Fatal(err)
	}
	if set, err := loadCheckpoint(filename, url, 1000, 100, hdr); err != nil || set != nil {
		t.Errorf("expected no checkpoint after remove, got: %v, %v", set, err)
	}
	// removing a checkpoint that is already gone is not an error
	if err := c.remove(); err != nil {
		t.Errorf("expected removing a missing checkpoint to succeed, got: %v", err)
	}
}

func TestCheckpointIsDiscarded(t *testing.T) {
	const (
		url  = "http://example.com/testCheckpointDiscarded"
		size = 1000
		rs   = 100
	)
	etag := testHeader(`"abc"`, "")
	lastmod := testHeader("", "Thu, 29 Nov 1973 21:33:09 GMT")

	tests := []struct {
		Name string
		// mutate the checkpoint on disk before loading it
		Corrupt func(c *checkpoint)
		// load it with these arguments
		URL       string
		Size      int64
		RangeSize int64
		Header    http.Header
	}{
		{Name: "Different URL", URL: "http://example.com/other", Size: size, RangeSize: rs, Header: etag},
		{Name: "Different size", URL: url, Size: size + 1, RangeSize: rs, Header: etag},
		{Name: "Different range size", URL: url, Size: size, RangeSize: rs + 1, Header: etag},
		{Name: "Different ETag", URL: url, Size: size, RangeSize: rs, Header: testHeader(`"xyz"`, "")},
		{
			Name:      "ETag no longer offered",
			URL:       url,
			Size:      size,
			RangeSize: rs,
			Header:    http.Header{},
		},
		{
			Name:      "ETag newly offered",
			Corrupt:   func(c *checkpoint) { c.ETag = "" },
			URL:       url,
			Size:      size,
			RangeSize: rs,
			Header:    etag,
		},
		{
			Name:      "Future version",
			Corrupt:   func(c *checkpoint) { c.Version = checkpointVersion + 1 },
			URL:       url,
			Size:      size,
			RangeSize: rs,
			Header:    etag,
		},
		{
			Name:      "Range past the end of the file",
			Corrupt:   func(c *checkpoint) { c.Complete = [][2]int64{{0, size + 1}} },
			URL:       url,
			Size:      size,
			RangeSize: rs,
			Header:    etag,
		},
		{
			Name:      "Inverted range",
			Corrupt:   func(c *checkpoint) { c.Complete = [][2]int64{{100, 0}} },
			URL:       url,
			Size:      size,
			RangeSize: rs,
			Header:    etag,
		},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "testCheckpointDiscarded")
			dst, err := os.Create(filename)
			if err != nil {
				t.Fatal(err)
			}
			defer dst.Close()

			c := newCheckpoint(filename, url, size, rs, etag)
			if err := c.store(dst, newRangeSet([2]int64{0, 100})); err != nil {
				t.Fatal(err)
			}
			if test.Corrupt != nil {
				test.Corrupt(c)
				b, err := json.Marshal(c)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(c.filename, b, 0666); err != nil {
					t.Fatal(err)
				}
			}

			set, err := loadCheckpoint(filename, test.URL, test.Size, test.RangeSize, test.Header)
			if err != nil {
				t.Fatal(err)
			}
			if set != nil {
				t.Errorf("expected the checkpoint to be discarded, got: %v", set.pairs())
			}
			// a discarded checkpoint is deleted, so that it cannot be
			// reconsidered by a later transfer
			if _, err := os.Stat(c.filename); !os.IsNotExist(err) {
				t.Errorf("expected the checkpoint file to be removed, got: %v", err)
			}
		})
	}

	t.Run("Last-Modified is used without an ETag", func(t *testing.T) {
		filename := filepath.Join(t.TempDir(), "testCheckpointLastModified")
		dst, err := os.Create(filename)
		if err != nil {
			t.Fatal(err)
		}
		defer dst.Close()

		c := newCheckpoint(filename, url, size, rs, lastmod)
		if err := c.store(dst, newRangeSet([2]int64{0, 100})); err != nil {
			t.Fatal(err)
		}
		set, err := loadCheckpoint(filename, url, size, rs, lastmod)
		if err != nil {
			t.Fatal(err)
		}
		if set == nil {
			t.Fatal("expected the checkpoint to be loaded")
		}

		// but a different timestamp means the remote file was modified
		other := testHeader("", "Fri, 30 Nov 1973 21:33:09 GMT")
		if set, err := loadCheckpoint(filename, url, size, rs, other); err != nil || set != nil {
			t.Errorf("expected the checkpoint to be discarded, got: %v, %v", set, err)
		}
	})

	t.Run("Corrupt file", func(t *testing.T) {
		filename := filepath.Join(t.TempDir(), "testCheckpointCorrupt")
		name := checkpointFilename(filename)
		if err := os.WriteFile(name, []byte("{not json"), 0666); err != nil {
			t.Fatal(err)
		}
		set, err := loadCheckpoint(filename, url, size, rs, etag)
		if err != nil {
			t.Fatal(err)
		}
		if set != nil {
			t.Error("expected a corrupt checkpoint to be discarded")
		}
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Errorf("expected the checkpoint file to be removed, got: %v", err)
		}
	})

	t.Run("No checkpoint", func(t *testing.T) {
		filename := filepath.Join(t.TempDir(), "testCheckpointMissing")
		set, err := loadCheckpoint(filename, url, size, rs, etag)
		if err != nil {
			t.Fatal(err)
		}
		if set != nil {
			t.Error("expected no range set when no checkpoint exists")
		}
	})
}
