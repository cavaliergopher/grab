//go:build windows

package grab

// syncDir does nothing on Windows, where FlushFileBuffers rejects the handle
// that opening a directory yields. There is no portable equivalent, so the
// durability of a rename is left to the file system.
func syncDir(name string) error { return nil }
