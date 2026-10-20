package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The notification reference is created once and every later run reads the
// same one: its replay deadline is never refreshed (ASK-W5, ASK-N3).
func TestNotificationReferenceIsRetained(t *testing.T) {
	dir := t.TempDir()
	key := "pretend-lemonade-0123456789abcdef0123456789abcdef"
	path := notificationRefFile(dir, key)
	if _, err := retainedRef(path, key, false); err == nil {
		t.Fatal("reading without a posted reference must fail")
	}
	first, err := retainedRef(path, key, true)
	if err != nil || first.Key == "" || first.ReplayUntilMs == 0 {
		t.Fatal(first, err)
	}
	again, err := retainedRef(path, key, true)
	if err != nil || again != first {
		t.Fatal("a later run must reuse the same reference", again, first, err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := retainedRef(path, key, true); err == nil {
		t.Fatal("a damaged reference file must not be replaced silently")
	}
	if filepath.Dir(path) != dir || downloadName("http://127.0.0.1:1/slow.bin?x=1") != "slow.bin" || downloadName("http://h/") != "download" {
		t.Fatal("names")
	}
}
