package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/supervision"
)

// TestDeclarationProbeStartsThroughSupervision shows the Foundry status probe
// starts through supervision, so it runs inside its own job.
func TestDeclarationProbeStartsThroughSupervision(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	refused := errors.New("recorded")
	var got supervision.Options
	saved := declarationProbeStart
	declarationProbeStart = func(_ context.Context, o supervision.Options) (*supervision.Process, error) {
		got = o
		return nil, refused
	}
	t.Cleanup(func() { declarationProbeStart = saved })
	_, err = defaultDeclarationEnv(nil).Status(context.Background(), program, "service", "status")
	if !errors.Is(err, refused) {
		t.Fatalf("Status error = %v, want the supervision start's error", err)
	}
	if got.Executable != program || len(got.Args) != 2 || got.Args[0] != "service" || got.Stdout == nil || got.ForceTimeout <= 0 {
		t.Fatalf("supervision options = %+v", got)
	}
}

// A real status probe runs through supervision and returns its output; one
// past its deadline is stopped.
func TestDeclarationProbeRunsAndHonoursItsDeadline(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("supervision runs the probe on Windows only")
	}
	system := filepath.Join(os.Getenv("SystemRoot"), "System32")
	out, err := runDeclarationProbe(context.Background(), filepath.Join(system, "whoami.exe"), nil)
	if err != nil || len(out) == 0 {
		t.Fatalf("whoami probe: %q %v", out, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	began := time.Now()
	_, err = runDeclarationProbe(ctx, filepath.Join(system, "PING.EXE"), []string{"-n", "60", "127.0.0.1"})
	if err == nil || time.Since(began) > 15*time.Second {
		t.Fatalf("deadline probe: err=%v after %v", err, time.Since(began))
	}
}
