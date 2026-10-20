package setup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An install that resumed after an interruption leaves the earlier
// process's .partial-* copy; the sweep after the install removes it. A
// partial this process wrote stays even when idle for minutes, since
// another component's slow download may still be writing it.
func TestSweepStalePartialsRemovesOnlyEarlierProcessCopies(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	start := now.Add(-10 * time.Minute)
	write := func(name string, at time.Time) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, make([]byte, 10), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		return path
	}
	old := write(".partial-1", start.Add(-time.Hour))
	idle := write(".partial-2", now.Add(-3*time.Minute))
	staged := write("abc.zip", start.Add(-time.Hour))
	sweepStalePartials(dir, start)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("partial from before start kept: %v", err)
	}
	for _, kept := range []string{idle, staged} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("%s removed: %v", kept, err)
		}
	}
}
