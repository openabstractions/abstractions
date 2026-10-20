//go:build !windows

package main

// restrictLibrarySearch has nothing to restrict outside Windows, where the
// helper refuses every operation.
func restrictLibrarySearch() error { return nil }
