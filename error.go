package grab

import (
	"fmt"
)

const (
	errBadLength = iota
	errNoFilename
)

type grabError struct {
	err  string
	code int
}

func (c *grabError) Error() string {
	return c.err
}

func errorf(code int, format string, a ...interface{}) error {
	return &grabError{
		err:  fmt.Sprintf(format, a...),
		code: code,
	}
}

// IsContentLengthMismatch returns a boolean indicating whether the error is
// known to report that a HTTP request response indicated that the requested
// file is not the expected length.
func IsContentLengthMismatch(err error) bool {
	if gerr, ok := err.(*grabError); ok {
		return gerr.code == errBadLength
	}

	return false
}

func IsNoFilename(err error) bool {
	if gerr, ok := err.(*grabError); ok {
		return gerr.code == errNoFilename
	}

	return false
}
