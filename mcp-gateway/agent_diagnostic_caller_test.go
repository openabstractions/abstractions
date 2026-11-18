package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

func diagnosticCallerFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := privatedir.Ensure(dir); err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return dir, program
}

// Delay an actual native identity walk, after its locality check. The ordinary
// host budget remains a refusal; a live test caller owns the longer fixture.
func TestAgentDiagnosticDelayedIdentityWalkUsesCreatingCaller(t *testing.T) {
	for _, withCaller := range []bool{false, true} {
		name := "ordinary-budget"
		if withCaller {
			name = "creating-caller"
		}
		t.Run(name, func(t *testing.T) {
			dir, program := diagnosticCallerFixture(t)
			lifetime := testCaller(t)
			caller := context.Background()
			if withCaller {
				caller = lifetime
			}
			var once sync.Once
			var observed atomic.Bool
			stop, ok := fileid.ObserveOpens(func(path string) {
				if filepath.Clean(path) == filepath.Clean(dir) {
					once.Do(func() {
						observed.Store(true)
						timer := time.NewTimer(2 * agentDiagnosticAttempt)
						defer timer.Stop()
						select {
						case <-timer.C:
						case <-lifetime.Done():
						}
					})
				}
			})
			if !ok {
				t.Fatal("identity observer already in use")
			}
			defer stop()
			d := newAgentDiagnostic(caller, dir, program)
			if d != nil {
				defer d.close()
			}
			if !observed.Load() {
				t.Fatal("actual state identity walk was not delayed")
			}
			if !withCaller {
				if d != nil {
					t.Fatal("ordinary construction accepted its expired identity budget")
				}
				if _, err := os.Stat(filepath.Join(dir, agentDiagnosticName)); !os.IsNotExist(err) {
					t.Fatal("refused construction published a lifecycle record", err)
				}
				return
			}
			if d == nil {
				t.Fatal("live creating caller did not retain its identity walk")
			}
			rows := readAgentDiagnostics(lifetime, dir)
			if len(rows) != 1 || rows[0].PID != os.Getpid() || rows[0].Phase != "started" || rows[0].Cause != "unproved" || len(rows[0].ImageSHA256) != 64 {
				t.Fatal("actual lifecycle snapshot unavailable", rows)
			}
		})
	}
}
