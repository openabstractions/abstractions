//go:build !windows && !unix

package main

import "os"

// mapFile reads path into memory where the platform has no mapping here.
func mapFile(path string) ([]byte, func(), error) {
	data, err := os.ReadFile(path)
	return data, func() {}, err
}

// mapLocked is mapFile: the copy in memory is the caller's own.
func mapLocked(path string) ([]byte, func(), error) { return mapFile(path) }
