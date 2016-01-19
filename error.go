package grab

import (
	"fmt"
)

// error codes
const (
	errBadLength = iota
	errNoFilename
	errChecksumMismatch
	errBadDestination
)

// grabError is a custom error type
type grabError struct {
	err  string
	code int
}

// Error returns the error string of a grabError.
func (c *grabError) Error() string {
	return c.err
}

// newGrabError creates a new grabError instance with the given code and
// message.
func newGrabError(code int, format string, a ...interface{}) error {
	return &grabError{
		err:  fmt.Sprintf(format, a...),
		code: code,
	}
}

// isErrorType returns true if the given error is a grabError with the specified
// error code.
func isErrorType(err error, code int) bool {
	if gerr, ok := err.(*grabError); ok {
		return gerr.code == code
	}

	return false
}

// IsContentLengthMismatch returns a boolean indicating whether the error is
// known to report that a HTTP response indicated that the requested file is not
// the expected length.
func IsContentLengthMismatch(err error) bool {
	return isErrorType(err, errBadLength)
}

// IsNoFilename returns a boolean indicating whether the error is known to
// report that a destination filename could not be determined from the
// Content-Disposition headers of a HTTP response or the requested URL path.
func IsNoFilename(err error) bool {
	return isErrorType(err, errNoFilename)
}

// IsChecksumMismatch returns a boolean indicating whether the error is known to
// report that the downloaded file did not match the expected checksum value.
func IsChecksumMismatch(err error) bool {
	return isErrorType(err, errChecksumMismatch)
}

// IsBadDestination returns a boolean indicating whether the error is known to
// report that the given destination path is not valid for the requested
// operation.
func IsBadDestination(err error) bool {
	return isErrorType(err, errBadDestination)
}
