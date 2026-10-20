//go:build !windows

package main

import (
	"os"
	"syscall"
	"testing"
)

// linkCount is the number of names the file at path has.
func linkCount(t *testing.T, path string) uint32 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return uint32(info.Sys().(*syscall.Stat_t).Nlink)
}
