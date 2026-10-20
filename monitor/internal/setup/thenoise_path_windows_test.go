//go:build windows

package setup

import (
	"testing"

	"golang.org/x/sys/windows"
)

// Keep the vendor's real 87-character limit meaningful when the source runner
// supplies a long private TEMP. This names the same owned directory through its
// existing filesystem alias; it never changes the engine path validator.
func shortTheNoiseFixturePath(t *testing.T, path string) string {
	t.Helper()
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(pointer, &buffer[0], uint32(len(buffer)))
	if err != nil || n == 0 || n >= uint32(len(buffer)) {
		t.Fatalf("short name of owned TheNoise fixture %q: %v", path, err)
	}
	return windows.UTF16ToString(buffer[:n])
}
