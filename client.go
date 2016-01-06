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
	HTTPClient *http.Client
	UserAgent  string
}

func NewClient(userAgent string) *Client {
	return &Client{
		UserAgent: userAgent,
		HTTPClient: &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			},
		},
	}
}

func (c *Client) Do(req *Request) error {
	// create a response
	resp := Response{
		Request: req,
	}

	// default to current working directory
	if req.Filename == "" {
		req.Filename = "."
	}

	// see if file is a directory
	needFilename := false
	if fi, err := os.Stat(req.Filename); err == nil {
		// file exists - is it a directory?
		if fi.IsDir() {
			// destination is a directory - compute a file name
			needFilename = true
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	if !needFilename {
		resp.Filename = req.Filename
	}

	// set user agent string
	if c.UserAgent != "" && req.HTTPRequest.Header.Get("User-Agent") == "" {
		req.HTTPRequest.Header.Set("User-Agent", c.UserAgent)
	}

	// switch the request to HEAD metho
	method := req.HTTPRequest.Method
	req.HTTPRequest.Method = "HEAD"

	// get file metadata
	canResume := false
	if hresp, err := c.HTTPClient.Do(req.HTTPRequest); err == nil && (hresp.StatusCode >= 200 && hresp.StatusCode < 300) {
		// is a known size set and a size returned in the HEAd request?
		if req.Size == 0 && hresp.ContentLength > 0 {
			resp.Size = uint64(hresp.ContentLength)
		} else if req.Size > 0 && hresp.ContentLength > 0 && req.Size != uint64(hresp.ContentLength) {
			return errorf(errBadLength, "Bad content length: %d, expected %d", hresp.ContentLength, req.Size)
		}

		// does server supports resuming downloads?
		if hresp.Header.Get("Accept-Ranges") == "bytes" {
			canResume = true
		}

		// TODO: get filename from Content-Disposition header
	}

	// reset request
	req.HTTPRequest.Method = method

	// compute filename from URL if still needed
	if needFilename {
		filename := path.Base(req.HTTPRequest.URL.Path)
		if filename == "" {
			return errorf(errNoFilename, "No filename could be determined")
		} else {
			// update filepath with filename from URL
			resp.Filename = filepath.Join(req.Filename, filename)
		}
	}

	// open destination for writing
	f, err := os.OpenFile(resp.Filename, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// seek to the start of the file
	resp.progress = 0
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
				resp.progress = uint64(fi.Size())

				// set byte range header in next request
				req.HTTPRequest.Header.Set("Range", fmt.Sprintf("bytes=%d-", fi.Size()))
			}
		}
	}

	// skip if already downloaded
	if resp.Size > 0 && resp.Size == resp.progress {
		return nil
	}

	// request content
	if hresp, err := c.HTTPClient.Do(req.HTTPRequest); err != nil {
		return err
	} else {

		// validate content length
		if resp.Size > 0 && resp.Size != (resp.progress+uint64(hresp.ContentLength)) {
			return errorf(errBadLength, "Bad content length: %d, expected %d", hresp.ContentLength, resp.Size-resp.progress)
		}

		// download and update progress
		var buffer [4096]byte
		for {
			// read HTTP stream
			n, err := hresp.Body.Read(buffer[:])
			if err != nil && err != io.EOF {
				return err
			}

			// increment progress
			atomic.AddUint64(&resp.progress, uint64(n))

			// write to file
			if _, werr := f.Write(buffer[:n]); werr != nil {
				return werr
			}

			// break when finished
			if err == io.EOF {
				break
			}
		}
	}

	return nil
}
