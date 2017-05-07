package grab

import "errors"

var (
	// ErrBadLength indicates that the server response or an existing file does
	// not match the expected content length.
	ErrBadLength = errors.New("bad content length")

	// ErrBadChecksum indicates that a downloaded file failed to pass checksum
	// validation.
	ErrBadChecksum = errors.New("checksum mismatch")

	// ErrNoFilename indicates that a reasonable filename could not be
	// automatically determined using the URL or response headers from a server.
	ErrNoFilename = errors.New("no filename could be determined")

	// ErrFileExists indicates that the destination path already exists.
	ErrFileExists = errors.New("file exists")
)
