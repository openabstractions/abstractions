//go:build !windows

package setup

// displayAdapters reports no adapters: every graphics-specific engine
// package in the catalog is a Windows package.
func displayAdapters() ([]string, error) {
	return nil, nil
}
