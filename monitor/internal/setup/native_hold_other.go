//go:build !windows

package setup

import "os"

// openWriteExcluded opens path for reading. The native package is Windows
// only. On other platforms the receipt check just reads the file.
func openWriteExcluded(path string) (*os.File, error) {
	return os.Open(path)
}
