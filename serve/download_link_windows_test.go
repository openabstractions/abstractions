package main

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// linkCount is the number of names the file at path has.
func linkCount(t *testing.T, path string) uint32 {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		t.Fatal(err)
	}
	return info.NumberOfLinks
}
