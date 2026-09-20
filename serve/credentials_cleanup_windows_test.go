//go:build windows

package main

import (
	"errors"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	testAdvapi32      = windows.NewLazySystemDLL("advapi32.dll")
	testCredEnumerate = testAdvapi32.NewProc("CredEnumerateW")
	testCredDelete    = testAdvapi32.NewProc("CredDeleteW")
	testCredFree      = testAdvapi32.NewProc("CredFree")
)

// testCredential mirrors the leading fields of CREDENTIALW.
type testCredential struct {
	Flags      uint32
	Type       uint32
	TargetName *uint16
}

// storeItems lists the generic credential target names under prefix.
func storeItems(t *testing.T, prefix string) []string {
	t.Helper()
	filter, err := windows.UTF16PtrFromString(prefix + "*")
	if err != nil {
		t.Fatal(err)
	}
	var count uint32
	var list **testCredential
	if r, _, e := testCredEnumerate.Call(uintptr(unsafe.Pointer(filter)), 0, uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&list))); r == 0 {
		if errors.Is(e, windows.ERROR_NOT_FOUND) {
			return nil
		}
		t.Fatalf("CredEnumerateW: %v", e)
	}
	defer testCredFree.Call(uintptr(unsafe.Pointer(list)))
	var names []string
	for _, c := range unsafe.Slice(list, count) {
		names = append(names, windows.UTF16PtrToString(c.TargetName))
	}
	return names
}

// removeStoreItems deletes every generic credential under the test namespace.
func removeStoreItems(t *testing.T, namespace string) {
	t.Helper()
	for _, target := range storeItems(t, namespace+"/") {
		name, err := windows.UTF16PtrFromString(target)
		if err != nil {
			t.Error(err)
			continue
		}
		if r, _, e := testCredDelete.Call(uintptr(unsafe.Pointer(name)), 1, 0); r == 0 && !errors.Is(e, windows.ERROR_NOT_FOUND) {
			t.Errorf("delete %s: %v", target, e)
		}
	}
	if left := storeItems(t, namespace+"/"); len(left) != 0 {
		t.Errorf("test credentials left under %s: %d", namespace, len(left))
	}
}
