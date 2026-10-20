//go:build windows

package main

import "golang.org/x/sys/windows"

// restrictLibrarySearch limits every later library load of this elevated
// process to the system directory, so no file beside it or on PATH is
// loaded as a library (FAC-H1). main calls it before anything else.
func restrictLibrarySearch() error {
	return windows.SetDefaultDllDirectories(windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
}
