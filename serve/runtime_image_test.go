package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The watch stops the runtime at the first positive mismatch, and a failed
// or empty read of its image never stops it.
func TestRuntimeImageWatchStopsOnlyOnAMismatch(t *testing.T) {
	same := func(a, b string) bool { return a == b }
	cases := []struct {
		name    string
		current func() (string, error)
		stops   bool
	}{
		{"same file", func() (string, error) { return `C:\OA\tools\openabstractions.exe`, nil }, false},
		{"moved aside", func() (string, error) { return `C:\Config.Msi\64ae4f.rbf`, nil }, true},
		{"unreadable", func() (string, error) { return "", errors.New("access denied") }, false},
		{"empty", func() (string, error) { return "", nil }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := runtimeImageWatch{launched: `C:\OA\tools\openabstractions.exe`, current: c.current, same: same}
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			stopped := make(chan error, 1)
			w.run(ctx, 10*time.Millisecond, func(err error) { stopped <- err })
			select {
			case err := <-stopped:
				if !c.stops || !errors.Is(err, errRuntimeImageReplaced) {
					t.Fatalf("stopped with %v, want stops=%v", err, c.stops)
				}
			default:
				if c.stops {
					t.Fatal("a moved image did not stop the runtime")
				}
			}
		})
	}
	if (runtimeImageWatch{}).replaced() {
		t.Fatal("a watch without a launch path reported a mismatch")
	}
}
