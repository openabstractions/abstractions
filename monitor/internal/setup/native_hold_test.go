package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Setup re-pins only the receipt's own bytes: the held program matches the
// pinned size and SHA-256, and while it is held no process writes it. A file
// with other bytes is refused and released.
func TestHoldPinnedProgramChecksTheReceiptAndExcludesWriters(t *testing.T) {
	program := filepath.Join(t.TempDir(), "launch-helper.exe")
	body := []byte("the receipt's launch helper")
	if err := os.WriteFile(program, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	pin := sevenZipPin{Path: program, Bytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
	held, err := holdPinnedProgram(context.Background(), pin)
	if err != nil || held.Path != program {
		t.Fatalf("the receipt's program was not held: %+v %v", held, err)
	}
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(program, []byte("another helper"), 0o600); err == nil {
			t.Fatal("the held program was rewritten")
		}
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("another helper!!!!!!!!!!!!!"), 0o600); err != nil {
		t.Fatal("the released program stays locked", err)
	}
	if _, err := holdPinnedProgram(context.Background(), pin); !errors.Is(err, errPinnedDigest) {
		t.Fatal("a program off its receipt was held:", err)
	}
	if err := os.Remove(program); err != nil {
		t.Fatal("a refused hold kept the file open", err)
	}
}
