package grab

import (
	"hash"
	"net/http"
	"net/url"
	"sync/atomic"
)

// Download defines a single file download operation with its source URL,
// destination file path, progress and checksum information.
type Download struct {
	url      *url.URL
	req      *http.Request
	filepath string
	size     uint64
	progress uint64
	algo     hash.Hash
	checksum []byte
}

// Downloads is a slice of Downloads interfaces.
type Downloads []*Download

// download is a private implementation of the Download interface.

func NewDownload(dst, src string, size uint64, algo hash.Hash, checksum []byte) (*Download, error) {
	// create http request
	req, err := http.NewRequest("GET", src, nil)
	if err != nil {
		return nil, err
	}

	return &Download{
		url:      req.URL,
		req:      req,
		filepath: dst,
		size:     size,
		algo:     algo,
		checksum: checksum,
	}, nil
}

func (c *Download) URL() *url.URL {
	return c.url
}

// FilePath returns the local file path where the download will be stored.
func (c *Download) FilePath() string {
	return c.filepath
}

// Size returns the total number of bytes to be downloaded.
func (c *Download) Size() uint64 {
	return c.size
}

// Progress returns the number of bytes which have already been downloaded.
func (c *Download) Progress() uint64 {
	atomic.LoadUint64(&c.progress)
	return c.progress
}

// ProgressRatio returns the ratio of bytes which have already been downloaded
// over the total content length.
func (c *Download) ProgressRatio() float64 {
	if c.size == 0 {
		return 0
	}

	return float64(atomic.LoadUint64(&c.progress)) / float64(c.size)
}
