//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestLoadBaseDeniesWritesUntilDone holds a --base file as the CLI does:
// from the hash until done, opening it to write or deleting it fails with
// a sharing violation; after done both work.
func TestLoadBaseDeniesWritesUntilDone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "base.safetensors")
	if err := os.WriteFile(path, safetensors(stEntry{"w", "BF16", halfWeights(4096, 21, false)}), 0o600); err != nil {
		t.Fatal(err)
	}
	base, done, err := loadBase(path)
	if err != nil {
		t.Fatal(err)
	}
	if base.Length == 0 || len(base.Tensors) != 1 {
		t.Fatalf("loaded %d bytes, %d tensors", base.Length, len(base.Tensors))
	}
	if f, err := os.OpenFile(path, os.O_WRONLY, 0); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		if err == nil {
			//unchecked: closing the handle the test should not have got
			f.Close()
		}
		t.Fatalf("opening the held base to write: %v, want a sharing violation", err)
	}
	if err := os.Remove(path); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("deleting the held base: %v, want a sharing violation", err)
	}
	done()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("after done: %v", err)
	}
	//unchecked: a test handle opened only to prove the lock is gone
	f.Close()
	if err := os.Remove(path); err != nil {
		t.Fatalf("deleting after done: %v", err)
	}
}
