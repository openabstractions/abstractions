//go:build windows

package cacheproducer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCopyProducerHeldSourceAndReceiptRefuseWithoutEffects(t *testing.T) {
	for _, held := range []string{"source", "receipt"} {
		t.Run(held, func(t *testing.T) {
			config, _, _, vendor := installedCopyFixture(t, false)
			copies := discoverFixture(t, config, vendor)
			path := copies[0].Receipt
			if held == "source" {
				path = filepath.Join(vendor.v.root, "plugin", "hooks", "register.js")
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			name, err := windows.UTF16PtrFromString(path)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if handle != windows.InvalidHandle {
					if err := windows.CloseHandle(handle); err != nil {
						t.Error(err)
					}
				}
			}()
			if held == "source" {
				if _, err := copies[0].Observe(context.Background()); err == nil {
					t.Fatal("held source was readable")
				}
				if err := copies[0].Reconcile(context.Background()); err == nil {
					t.Fatal("held source accepted for repair")
				}
			} else {
				rows, err := copiesInConfig(context.Background(), config, vendor.run, nil)
				if err != nil || len(rows) != 1 || rows[0].CanReconcile() {
					t.Fatalf("held receipt: %+v, %v", rows, err)
				}
				if reading, err := rows[0].Observe(context.Background()); err != nil || reading.State != CopyUnreadable {
					t.Fatalf("held receipt result: %+v, %v", reading, err)
				}
			}
			if err := windows.CloseHandle(handle); err != nil {
				t.Fatal(err)
			}
			// The deferred close now has no live handle to affect.
			handle = windows.InvalidHandle
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatal("held copy changed", err)
			}
		})
	}
}
