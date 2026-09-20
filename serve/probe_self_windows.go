package main

import (
	"os"
	"path/filepath"
)

// selfProgramPath preserves the path spelling Windows binds to this process.
// Resolving an 8.3 alias here would produce a different exact rights subject
// from QueryFullProcessImageName on the accepted pipe connection.
func selfProgramPath(fallback string) string {
	exe, err := os.Executable()
	if err != nil {
		return fallback
	}
	return filepath.Clean(exe)
}
