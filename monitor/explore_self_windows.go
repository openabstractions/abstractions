package main

import (
	"os"
	"path/filepath"
)

// exploreSelfProgram preserves the path spelling Windows binds to the Panel.
// Resolving an 8.3 alias would name a different exact rights subject from the
// program path observed on its native service connections.
func exploreSelfProgram(fallback string) string {
	exe, err := os.Executable()
	if err != nil {
		return fallback
	}
	return filepath.Clean(exe)
}
