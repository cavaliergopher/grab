package grab

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sync/atomic"
)

type Client struct {
	client *http.Client

	userAgent string
}

func NewClient(userAgent string) *Client {
	return &Client{
		userAgent: userAgent,
		client: &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			},
		},
	}
}

func (c *Client) SetHTTPClient(client *http.Client) {
	c.client = client
}

func (c *Client) Do(d *Download) error {

	// default to current working directory
	if d.filepath == "" {
		d.filepath = "."
	}

	// see if file is a directory
	needFilename := false
	if fi, err := os.Stat(d.filepath); err != nil {
		return err
	} else {
		if fi.IsDir() {
			// destination is a directory - compute a file name
			needFilename = true
		}
	}

	// configure client request
	if c.userAgent != "" {
		d.req.Header.Set("User-Agent", c.userAgent)
	}

	// switch the request to HEAD metho
	method := d.req.Method
	d.req.Method = "HEAD"

	// get file metadata
	canResume := false
	if resp, err := c.client.Do(d.req); err == nil && (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		// update or validate content length
		if d.size == 0 && resp.ContentLength > 0 {
			d.size = uint64(resp.ContentLength)
		} else if d.size > 0 && resp.ContentLength > 0 && d.size != uint64(resp.ContentLength) {
			return errorf(errBadLength, "Bad content length: %d, expected %d", resp.ContentLength, d.size)
		}

		// does server supports resuming downloads?
		if resp.Header.Get("Accept-Ranges") == "bytes" {
			canResume = true
		}

		// TODO: get filename from Content-Disposition header
	}

	// compute filename from URL if still needed
	if needFilename {
		filename := path.Base(d.url.Path)
		if filename == "" {
			return errorf(errNoFilename, "No filename could be determined")
		} else {
			// update filepath with filename from URL
			d.filepath = filepath.Join(d.filepath, filename)
		}
	}

	// open destination for writing
	f, err := os.OpenFile(d.filepath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// seek to the start of the file
	d.progress = 0
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}

	// attempt to resume previous download (if any)
	if canResume {
		if fi, err := f.Stat(); err != nil {
			return err
		} else if fi.Size() > 0 {
			// seek to end of file
			if _, err = f.Seek(0, os.SEEK_END); err != nil {
				return err
			} else {
				d.progress = uint64(fi.Size())

				// set byte range header in next request
				d.req.Header.Set("Range", fmt.Sprintf("bytes=%d-", fi.Size()))
			}
		}
	}

	// skip if already downloaded
	if d.size > 0 && d.size == d.progress {
		return nil
	}

	// reset request and get file content
	d.req.Method = method
	resp, err := c.client.Do(d.req)
	if err != nil {
		return err
	}

	// validate content length
	if d.size > 0 && d.size != (d.progress+uint64(resp.ContentLength)) {
		return errorf(errBadLength, "Bad content length: %d, expected %d", resp.ContentLength, d.size-d.progress)
	}

	// download and update progress
	var buffer [4096]byte
	for {
		// read HTTP stream
		n, err := resp.Body.Read(buffer[:])
		if err != nil && err != io.EOF {
			return err
		}

		// increment progress
		atomic.AddUint64(&d.progress, uint64(n))

		// write to file
		if _, werr := f.Write(buffer[:n]); werr != nil {
			return werr
		}

		// break when finished
		if err == io.EOF {
			break
		}
	}

	return nil
}
