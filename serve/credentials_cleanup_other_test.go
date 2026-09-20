//go:build !windows

package main

import "testing"

// storeItems has nothing to list: off Windows the test runtime uses the file
// store inside its temporary state directory.
func storeItems(*testing.T, string) []string { return nil }

func removeStoreItems(*testing.T, string) {}
