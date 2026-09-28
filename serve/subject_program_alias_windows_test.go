//go:build windows

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func shortSubjectProgramAlias(t *testing.T, program string) (string, bool) {
	t.Helper()
	p, err := windows.UTF16PtrFromString(program)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || n >= uint32(len(buf)) {
		t.Fatalf("GetShortPathName(%q): n=%d err=%v", program, n, err)
	}
	short := filepath.Clean(windows.UTF16ToString(buf[:n]))
	return short, !strings.EqualFold(short, program)
}
