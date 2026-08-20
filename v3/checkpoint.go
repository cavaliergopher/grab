package grab

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
)

// checkpointVersion is the version of the checkpoint file format. A checkpoint
// written by a different version is discarded rather than interpreted.
const checkpointVersion = 1

// checkpointSuffix is appended to the destination file name to name its
// checkpoint file.
const checkpointSuffix = ".grab"

// A byteRange is the half-open interval [Start, End) of a file.
type byteRange struct {
	Start, End int64
}

func (r byteRange) len() int64 { return r.End - r.Start }

// A rangeSet is a set of byte ranges, held as a sorted list of disjoint,
// maximal intervals. Adjacent and overlapping ranges are coalesced as they are
// added, so a transfer that has completed contiguously from the start of the
// file is held as a single interval.
//
// The zero value is an empty set. A rangeSet is not safe for concurrent use.
type rangeSet struct {
	ranges []byteRange
}

// add records the half-open interval [start, end) as complete, coalescing it
// with any ranges it meets or overlaps.
func (s *rangeSet) add(start, end int64) {
	if end <= start {
		return
	}
	// Find the first range that ends at or after the new range starts. Every
	// range before it ends strictly before start, so none of them can be
	// coalesced with the new range and they are left alone.
	i := sort.Search(len(s.ranges), func(i int) bool {
		return s.ranges[i].End >= start
	})
	// Absorb every subsequent range that begins at or before the new range
	// ends. Testing for equality here is what coalesces ranges that merely
	// meet, as well as those that overlap.
	j := i
	for j < len(s.ranges) && s.ranges[j].Start <= end {
		if s.ranges[j].Start < start {
			start = s.ranges[j].Start
		}
		if s.ranges[j].End > end {
			end = s.ranges[j].End
		}
		j++
	}
	s.ranges = append(s.ranges[:i], append([]byteRange{{start, end}}, s.ranges[j:]...)...)
}

// clone returns a copy of the set that can be read while the original is still
// being added to.
func (s *rangeSet) clone() *rangeSet {
	return &rangeSet{ranges: append([]byteRange(nil), s.ranges...)}
}

// contiguousPrefix returns the length of the run of complete bytes at the start
// of the file.
func (s *rangeSet) contiguousPrefix() int64 {
	if len(s.ranges) == 0 || s.ranges[0].Start != 0 {
		return 0
	}
	return s.ranges[0].End
}

// completedBytes returns the total number of bytes recorded as complete.
func (s *rangeSet) completedBytes() (n int64) {
	for _, r := range s.ranges {
		n += r.len()
	}
	return
}

// missing returns the ranges of [0, size) that are not yet complete, split into
// ranges of at most rangeSize bytes and ordered from the start of the file, so
// that a transfer working through them in order fills the file front to back.
//
// A rangeSize of zero or less yields a single range per contiguous gap.
func (s *rangeSet) missing(size, rangeSize int64) []byteRange {
	var missing []byteRange
	gap := func(start, end int64) {
		if rangeSize < 1 {
			missing = append(missing, byteRange{start, end})
			return
		}
		for ; start < end; start += rangeSize {
			missing = append(missing, byteRange{start, min(start+rangeSize, end)})
		}
	}

	next := int64(0)
	for _, r := range s.ranges {
		if next >= size {
			return missing
		}
		if r.Start > next {
			gap(next, min(r.Start, size))
		}
		if r.End > next {
			next = r.End
		}
	}
	if next < size {
		gap(next, size)
	}
	return missing
}

// A checkpoint records which byte ranges of a split transfer have been written
// to the destination file in full, so that an interrupted transfer can be
// resumed without refetching them. It lives beside the destination file and is
// removed once the transfer completes.
//
// A checkpoint also records the validators of the remote file its ranges were
// read from. A checkpoint left behind by a transfer of a file that has since
// been modified is therefore discarded, rather than used to assemble a local
// copy out of two different remote files.
type checkpoint struct {
	Version      int        `json:"version"`
	URL          string     `json:"url"`
	Size         int64      `json:"size"`
	ETag         string     `json:"etag,omitempty"`
	LastModified string     `json:"lastModified,omitempty"`
	RangeSize    int64      `json:"rangeSize"`
	Complete     [][2]int64 `json:"complete"`

	// filename is the path of the checkpoint file itself, and is not part of
	// the file format.
	filename string
}

// checkpointFilename returns the path of the checkpoint file that accompanies
// the given destination file.
func checkpointFilename(filename string) string {
	return filename + checkpointSuffix
}

// newCheckpoint returns an empty checkpoint for a transfer of the given remote
// file to the given destination path.
func newCheckpoint(filename, url string, size, rangeSize int64, hdr http.Header) *checkpoint {
	return &checkpoint{
		Version:      checkpointVersion,
		URL:          url,
		Size:         size,
		ETag:         hdr.Get("ETag"),
		LastModified: hdr.Get("Last-Modified"),
		RangeSize:    rangeSize,
		filename:     checkpointFilename(filename),
	}
}

// loadCheckpoint reads the checkpoint file accompanying the given destination
// file and returns the ranges it records as complete.
//
// The checkpoint is only used if it describes the same transfer of the same
// remote file - see checkpoint.matches. Otherwise, or if no checkpoint file
// exists, a nil rangeSet is returned and the transfer starts over. An error is
// returned only if an existing checkpoint could not be read or removed.
func loadCheckpoint(filename, url string, size, rangeSize int64, hdr http.Header) (*rangeSet, error) {
	name := checkpointFilename(filename)
	b, err := os.ReadFile(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("error reading checkpoint file: %w", err)
	}

	var c checkpoint
	if err := json.Unmarshal(b, &c); err != nil {
		// A corrupt checkpoint tells us nothing about the destination file, so
		// there is nothing to do but start over.
		return nil, removeCheckpoint(name)
	}
	if !c.matches(url, size, rangeSize, hdr) {
		return nil, removeCheckpoint(name)
	}

	set := &rangeSet{}
	for _, r := range c.Complete {
		if r[0] < 0 || r[1] <= r[0] || r[1] > c.Size {
			// the checkpoint contradicts itself
			return nil, removeCheckpoint(name)
		}
		set.add(r[0], r[1])
	}
	return set, nil
}

// matches reports whether the checkpoint describes the same transfer of the
// same remote file as the given URL, size, range size and response headers.
func (c *checkpoint) matches(url string, size, rangeSize int64, hdr http.Header) bool {
	if c.Version != checkpointVersion ||
		c.URL != url ||
		c.Size != size ||
		c.RangeSize != rangeSize {
		return false
	}

	// Prefer the strong validator, and require that whichever validator the
	// server offers now is the one the checkpoint was written with. A server
	// that has stopped sending validators altogether leaves us unable to tell
	// whether the file changed, so the checkpoint is not used.
	if etag := hdr.Get("ETag"); c.ETag != "" || etag != "" {
		return c.ETag == etag
	}
	return c.LastModified != "" && c.LastModified == hdr.Get("Last-Modified")
}

// store writes the checkpoint to disk, recording the given ranges as complete.
//
// The destination file is flushed to stable storage before the checkpoint that
// describes it is renamed into place, so that a checkpoint can never claim data
// that the file system has not yet committed. A crash may lose a checkpoint,
// but cannot leave one that overstates what was written.
//
// The caller must pass a set that is no longer being added to - see
// rangeSet.clone - both because it is read here without synchronization, and
// because a range added after the flush below would be recorded as complete
// without its data having been flushed.
func (c *checkpoint) store(dst *os.File, complete *rangeSet) error {
	if err := dst.Sync(); err != nil {
		return fmt.Errorf("error flushing destination file: %w", err)
	}

	c.Complete = make([][2]int64, len(complete.ranges))
	for i, r := range complete.ranges {
		c.Complete[i] = [2]int64{r.Start, r.End}
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}

	f, err := os.CreateTemp(filepath.Dir(c.filename), filepath.Base(c.filename)+".*")
	if err != nil {
		return fmt.Errorf("error creating checkpoint file: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once the rename below has succeeded

	if _, err := f.Write(b); err != nil {
		f.Close()
		return fmt.Errorf("error writing checkpoint file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("error flushing checkpoint file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("error closing checkpoint file: %w", err)
	}
	if err := os.Rename(tmp, c.filename); err != nil {
		return fmt.Errorf("error renaming checkpoint file: %w", err)
	}
	return nil
}

// remove deletes the checkpoint file.
func (c *checkpoint) remove() error {
	return removeCheckpoint(c.filename)
}

func removeCheckpoint(name string) error {
	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("error removing checkpoint file: %w", err)
	}
	return nil
}
