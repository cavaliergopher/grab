//go:build !windows

package grab

import "os"

// syncDir flushes a directory's own contents to stable storage.
//
// Renaming a file into place is atomic, but the directory entry that names it
// is not durable until the directory itself is flushed. Without this, a crash
// can leave a file whose contents reached the disk but whose name did not.
func syncDir(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
