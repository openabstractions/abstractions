//go:build !windows

package setup

import "testing"

func shortTheNoiseFixturePath(t *testing.T, path string) string {
	t.Helper()
	return path
}
