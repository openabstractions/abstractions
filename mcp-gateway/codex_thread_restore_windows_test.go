package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// The Restart Manager names the process holding a lock file, with the start
// time that tells a reused PID apart, without this reader opening the file.
func TestCodexLockHoldersNamesTheHoldingProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), restoreThreadA+".lock")
	held, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	self := rmUniqueProcess{PID: uint32(os.Getpid()), Start: created}
	holders, err := codexLockHolders(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range holders {
		found = found || h == self
	}
	if !found {
		t.Fatalf("holders %v lack this process %v", holders, self)
	}
	held.Close()
	if holders, err := codexLockHolders(path); err != nil || len(holders) != 0 {
		t.Fatalf("released file still held: %v %v", holders, err)
	}
}

// The test binary's parent holds no Codex locks, and may be outside the
// restore window: either way no thread is restored.
func TestCodexParentLoadedThreadsIgnoresOtherHolders(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "thread-writer-locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := os.OpenFile(filepath.Join(dir, restoreThreadA+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if threads, _ := codexParentLoadedThreads(home); len(threads) != 0 {
		t.Fatalf("restored %v held by this process, not the parent", threads)
	}
}
