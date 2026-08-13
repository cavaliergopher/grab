package grab

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestIsStatusCodeError asserts that StatusCodeErrors are recognised even when
// they have been wrapped by a caller.
func TestIsStatusCodeError(t *testing.T) {
	tests := []struct {
		Name   string
		Err    error
		Expect bool
	}{
		{"Nil", nil, false},
		{"Bare", StatusCodeError(404), true},
		{"Wrapped", fmt.Errorf("download failed: %w", StatusCodeError(404)), true},
		{"Doubly wrapped", fmt.Errorf("a: %w", fmt.Errorf("b: %w", StatusCodeError(500))), true},
		{"Other error", ErrBadLength, false},
		{"Other wrapped error", fmt.Errorf("nope: %w", ErrBadLength), false},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			if got := IsStatusCodeError(test.Err); got != test.Expect {
				t.Errorf("expected: %v, got: %v, for: %v", test.Expect, got, test.Err)
			}
		})
	}
}

// TestGetBatchNotADirectory asserts that GetBatch reports a comparable error
// when the destination is not a directory.
func TestGetBatchNotADirectory(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "testNotADirectory")
	if err := os.WriteFile(filename, []byte("not a directory"), 0666); err != nil {
		t.Fatal(err)
	}

	_, err := GetBatch(1, filename, "http://localhost/example")
	if !errors.Is(err, ErrNotADirectory) {
		t.Errorf("expected: %v, got: %v", ErrNotADirectory, err)
	}
}

// TestMkdirpErrorWrapping asserts that errors from the filesystem are wrapped
// rather than flattened into a string, so that callers can inspect them.
func TestMkdirpErrorWrapping(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test is meaningless as root")
	}
	parent := filepath.Join(t.TempDir(), "testMkdirpDenied")
	if err := os.Mkdir(parent, 0500); err != nil {
		t.Fatal(err)
	}

	// restore write permission so that the temporary directory can be removed
	defer os.Chmod(parent, 0700)

	err := mkdirp(filepath.Join(parent, "child", "file"))
	if err == nil {
		t.Skip("destination directory was created despite read-only parent")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("expected error wrapping %v, got: %v", os.ErrPermission, err)
	}
}
