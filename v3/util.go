package grab

import (
	"fmt"
	"math"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// setRangeHeader sets the Range header of a request to the given byte range of
// a file of the given total size, which may be negative if it is not known.
//
// A range that covers the whole file is requested without a Range header at
// all, so that an unsplit transfer of a complete file remains the plain request
// grab has always made.
//
// The last byte position in a Range header is inclusive, so it is one less than
// the end of the half-open interval a byteRange describes.
func setRangeHeader(req *http.Request, r byteRange, size int64) {
	switch {
	case r.Start == 0 && (size < 0 || r.End >= size):
		req.Header.Del("Range")
	case r.End == math.MaxInt64:
		// the end of the file is as far as the range goes, but where that is
		// is not yet known
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", r.Start))
	default:
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", r.Start, r.End-1))
	}
}

// parseContentRange parses a Content-Range header of the form
// "bytes first-last/complete" and returns the range as the half-open interval
// [start, end), along with the complete size of the remote file.
//
// It returns ok as false if the header is missing, malformed, or does not
// specify the complete length of the file - as a server may respond with "*"
// when it does not know it.
func parseContentRange(s string) (start, end, size int64, ok bool) {
	spec, found := strings.CutPrefix(s, "bytes ")
	if !found {
		return 0, 0, 0, false
	}
	rng, complete, found := strings.Cut(spec, "/")
	if !found {
		return 0, 0, 0, false
	}
	first, last, found := strings.Cut(rng, "-")
	if !found {
		return 0, 0, 0, false
	}
	var err error
	if start, err = strconv.ParseInt(first, 10, 64); err != nil {
		return 0, 0, 0, false
	}
	if end, err = strconv.ParseInt(last, 10, 64); err != nil {
		return 0, 0, 0, false
	}
	if size, err = strconv.ParseInt(complete, 10, 64); err != nil {
		return 0, 0, 0, false
	}
	// the last byte position is inclusive
	if end++; start < 0 || end <= start || end > size {
		return 0, 0, 0, false
	}
	return start, end, size, true
}

// setLastModified sets the last modified timestamp of a local file according to
// the Last-Modified header returned by a remote server.
func setLastModified(resp *http.Response, filename string) error {
	// https://tools.ietf.org/html/rfc7232#section-2.2
	// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Last-Modified
	header := resp.Header.Get("Last-Modified")
	if header == "" {
		return nil
	}
	lastmod, err := time.Parse(http.TimeFormat, header)
	if err != nil {
		return nil
	}
	return os.Chtimes(filename, lastmod, lastmod)
}

// mkdirp creates all missing parent directories for the destination file path.
func mkdirp(path string) error {
	dir := filepath.Dir(path)
	if fi, err := os.Stat(dir); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("error checking destination directory: %w", err)
		}
		if err := os.MkdirAll(dir, 0777); err != nil {
			return fmt.Errorf("error creating destination directory: %w", err)
		}
	} else if !fi.IsDir() {
		panic("grab: developer error: destination path is not directory")
	}
	return nil
}

// guessFilename returns a filename for the given http.Response. If none can be
// determined ErrNoFilename is returned.
//
// TODO: NoStore operations should not require a filename
func guessFilename(resp *http.Response) (string, error) {
	filename := resp.Request.URL.Path
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if val, ok := params["filename"]; ok {
				filename = val
			} // else filename directive is missing.. fallback to URL.Path
		}
	}

	// sanitize
	if filename == "" || strings.HasSuffix(filename, "/") || strings.Contains(filename, "\x00") {
		return "", ErrNoFilename
	}

	// filepath.Base returns the path separator when the cleaned path is the
	// root, and that separator is platform specific: "/" on unix but "\" on
	// Windows. Both must be rejected, or a Content-Disposition filename such
	// as "." or "filename/.." yields a filename of "\" on Windows instead of
	// an error.
	filename = filepath.Base(path.Clean("/" + filename))
	if filename == "" || filename == "." || filename == "/" ||
		filename == string(filepath.Separator) {
		return "", ErrNoFilename
	}

	return filename, nil
}
