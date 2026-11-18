package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-provider-kit/go/modelplace"
)

// A real runtime collects the partial file of a failed download once the
// schedule's clock is past its age, logs the removal, and keeps a completed
// download's result even a year later.
func TestRuntimeCollectsFailedDownloadPartialAndKeepsCompletedResult(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	priorFirst, priorInterval, priorClock := runtimeJobTmpFirstPass, runtimeJobTmpInterval, runtimeJobTmpClock
	runtimeJobTmpFirstPass, runtimeJobTmpInterval = 200*time.Millisecond, 200*time.Millisecond
	runtimeJobTmpClock = func() time.Time { return time.Now().Add(365 * 24 * time.Hour) }
	t.Cleanup(func() {
		runtimeJobTmpFirstPass, runtimeJobTmpInterval, runtimeJobTmpClock = priorFirst, priorInterval, priorClock
	})

	options, _ := isolatedRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("startup: %v", err)
	case <-time.After(runtimeWait):
		cancel()
		t.Fatalf("runtime not ready within %v", runtimeWait)
	}
	t.Cleanup(func() {
		cancel()
		if err := awaitStopped(t, done, "runtime"); err != nil {
			t.Error(err)
		}
	})
	endpoint := options.endpoint
	jobs := modelplace.JobStoreIn(options.stateDir)

	// A completed download: its result stays.
	body := []byte("bytes a completed download keeps")
	completed := runJSON(t, "download", func(out, diag io.Writer) error {
		return downloadCommand([]string{served(t, body), "--endpoint", endpoint, "--out", t.TempDir(), "--json", "--timeout", "90s"}, out, diag)
	})
	if completed.Receipt.OperationID == "" || completed.Snapshot == nil || completed.Snapshot.State != "complete" {
		t.Fatalf("completed download: %+v", completed)
	}
	result := filepath.Join(jobs, "results", completed.Receipt.OperationID)

	// A failed download: a wrong digest ends it permanently.
	wanted := sha256.Sum256([]byte("other bytes"))
	var out, diagnostics bytes.Buffer
	assertExit(t, downloadCommand([]string{served(t, []byte("held bytes")), "--endpoint", endpoint, "--out", t.TempDir(), "--sha256", hex.EncodeToString(wanted[:]), "--json", "--timeout", "90s"}, &out, &diagnostics), exitEnded, "download with a wrong digest")
	listed := runJSON(t, "jobs list", func(out, diag io.Writer) error {
		return jobsCommand([]string{"list", "--endpoint", endpoint, "--json"}, out, diag)
	})
	failedID := ""
	for _, s := range listed.Snapshots {
		if s.State == "failed" {
			failedID = s.Receipt.OperationID
		}
	}
	if failedID == "" {
		t.Fatalf("no failed operation: %+v", listed.Snapshots)
	}
	// The failed work's allocated partial name holds bytes, as an
	// interrupted transfer leaves them.
	partial := filepath.Join(jobs, "work", failedID)
	if err := os.WriteFile(partial, []byte("abandoned partial bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := os.Lstat(partial)
		if os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed download's partial still present: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Lstat(result); err != nil {
		t.Fatalf("completed result removed by partial collection: %v", err)
	}

	// One removal record names the failed operation and its partial; none
	// names the completed operation.
	removals := func() []string {
		var lines []string
		_ = filepath.WalkDir(options.out, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			for _, line := range strings.Split(string(data), "\n") {
				if strings.Contains(line, "abstraction.storage/retention.removed") {
					lines = append(lines, line)
				}
			}
			return nil
		})
		return lines
	}
	var lines []string
	for time.Now().Before(deadline) {
		if lines = removals(); len(lines) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], failedID) || !strings.Contains(lines[0], "work/"+failedID) || strings.Contains(lines[0], completed.Receipt.OperationID) {
		t.Fatalf("removal records: %q", lines)
	}
}
