package grab

import (
	"fmt"
)

const (
	errBadLength = iota
	errNoFilename
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
// known to report that a HTTP request response indicated that the requested
// file is not the expected length.
func IsContentLengthMismatch(err error) bool {
	return isErrorType(err, errBadLength)
}

// IsNoFilename returns a boolean indicating whether the error is known to
// report that a destination filename could not be determined from the
// Content-Disposition headers of a HTTP response or the request URL's path.
func IsNoFilename(err error) bool {
	return isErrorType(err, errNoFilename)
}
