package grab

import (
	"mime"
	"net/http"
	"path"
	"strings"
)

// guessFilename returns a filename for the given http.Response. If none can be
// determined ErrNoFilename is returned.
func guessFilename(resp *http.Response) (string, error) {
	// extract filename from Content-Disposition header
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if filename, ok := params["filename"]; ok {
				return filename, nil
			}
		}
	}

	// extract filename from URL
	urlPath := resp.Request.URL.Path
	if urlPath != "" && !strings.HasSuffix(urlPath, "/") {
		return path.Base(urlPath), nil
	}

	return "", ErrNoFilename
}
