package grab

import (
	"fmt"
	"testing"
)

// TestErrors validates the categorization of different error types
func TestErrors(t *testing.T) {
	msg := "error message"

	errs := []error{
		fmt.Errorf(msg),       // test non-grab errors
		newGrabError(-1, msg), // test grab error
	}

	for _, err := range errs {
		if err.Error() != msg {
			t.Errorf("Expected error message '%s', got '%s'", msg, err.Error())
		}

		if IsBadDestination(err) {
			t.Errorf("Error is not a bad destination error")
		}

		if IsChecksumMismatch(err) {
			t.Errorf("Error is not a checksum mismatch error")
		}

		if IsNoFilename(err) {
			t.Errorf("Error is not a filename error")
		}

		if IsContentLengthMismatch(err) {
			t.Errorf("Error is not a content length mismatch")
		}
	}

	if err := newGrabError(errBadDestination, msg); !IsBadDestination(err) {
		t.Errorf("Error should identify as a bad destination error")
	}

	if err := newGrabError(errBadLength, msg); !IsContentLengthMismatch(err) {
		t.Errorf("Error should identify as a content length mismatch")
	}

	if err := newGrabError(errChecksumMismatch, msg); !IsChecksumMismatch(err) {
		t.Errorf("Error should identify as a checksum mismatch")
	}

	if err := newGrabError(errNoFilename, msg); !IsNoFilename(err) {
		t.Errorf("Error should identify as a missing filename error")
	}
}
