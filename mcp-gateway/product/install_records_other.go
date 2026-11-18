//go:build !windows

package product

// RegisteredToolFolders has no installer database to read outside Windows.
func RegisteredToolFolders() ([]string, error) { return nil, nil }
