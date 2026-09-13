package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Public charter exports maintained inputs through split.manifest; private
// development reads the same source files directly. Neither layout may skip.
func productionFile(t *testing.T, relative string) string {
	t.Helper()
	for _, root := range []string{"../test/production", "../../openabstractions-flat"} {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	t.Fatalf("missing maintained production fixture %s (public idl/test/production or private flat source)", relative)
	return ""
}
