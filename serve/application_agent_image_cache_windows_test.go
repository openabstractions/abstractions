//go:build windows

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procAgentImageIOCounters = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessIoCounters")

func agentImageIO(t *testing.T) windows.IO_COUNTERS {
	t.Helper()
	var c windows.IO_COUNTERS
	if r, _, err := procAgentImageIOCounters.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&c))); r == 0 {
		t.Fatal(err)
	}
	return c
}

type agentImageCounts struct{ reads, stamps, lookups atomic.Int64 }

// countAgentImage wraps the real image reader and stamp and the fixture's
// gateway lookup. The verification cache starts empty.
func countAgentImage(run *selfRegisteredRun) *agentImageCounts {
	c := &agentImageCounts{}
	run.d.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
		c.reads.Add(1)
		return readApplicationAgentImage(ctx, program, binding)
	}
	run.d.agentStamp = func(program string) (applicationAgentStamp, error) {
		c.stamps.Add(1)
		return readApplicationAgentStamp(program)
	}
	excluded := run.d.agentExcluded
	run.d.agentExcluded = func(ctx context.Context, program, key string) (bool, error) {
		c.lookups.Add(1)
		return excluded(ctx, program, key)
	}
	run.d.agentImages.forget()
	return c
}

func TestMessagesNativeInboxWatchReusesVerifiedImage(t *testing.T) {
	run, _ := messages1608Fixture(t)
	c := countAgentImage(run)
	call, _ := messages1608Call(t)
	instance := messages1608Presence(t, run, call, "", "ctx")
	if got := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "ready"); got.outcome != "opened" {
		t.Fatal(got)
	}
	// Four enrollment passes, each counted by its stamp check.
	start := c.stamps.Load()
	until := time.Now().Add(10 * time.Second)
	for c.stamps.Load() < start+4 && time.Now().Before(until) {
		time.Sleep(20 * time.Millisecond)
	}
	if ticks := c.stamps.Load() - start; ticks < 4 {
		t.Fatal("watch ticks", ticks)
	}
	if messages1608Row(t, run.d) == nil {
		t.Fatal("unchanged image retired the inbox")
	}
	if reads, lookups := c.reads.Load(), c.lookups.Load(); reads != 1 || lookups != 1 {
		t.Fatalf("image reads %d, gateway lookups %d over %d checks; want 1 each", reads, lookups, c.stamps.Load())
	}
}

// The admission I/O per check drops by at least the verification's own
// operations, measured against the same capture with the cache dropped.
func TestApplicationAgentAdmissionIO(t *testing.T) {
	run, _ := messages1608Fixture(t)
	c := countAgentImage(run)
	call, _ := messages1608Call(t)
	p, err := call.Peer()
	if err != nil {
		t.Fatal(err)
	}
	const n = 40
	measure := func(fresh bool) (reads, other float64) {
		before := agentImageIO(t)
		for range n {
			if fresh {
				run.d.agentImages.forget()
			}
			if _, ok, err := run.d.captureApplicationAgent(run.ctx, p, "native-agent"); !ok || err != nil {
				t.Fatal(ok, err)
			}
		}
		after := agentImageIO(t)
		return float64(after.ReadOperationCount-before.ReadOperationCount) / n, float64(after.OtherOperationCount-before.OtherOperationCount) / n
	}
	freshReads, freshOther := measure(true)
	start := c.reads.Load()
	cachedReads, cachedOther := measure(false)
	run.d.mu.Lock()
	program := run.d.unconfirmed["native-agent"].Program
	run.d.mu.Unlock()
	before := agentImageIO(t)
	for range n {
		if _, err := readApplicationAgentImage(run.ctx, program, "binding"); err != nil {
			t.Fatal(err)
		}
	}
	after := agentImageIO(t)
	imageOther := float64(after.OtherOperationCount-before.OtherOperationCount) / n
	t.Logf("per check: verified %.1f read / %.1f other operations; cached %.1f / %.1f; image read alone %.1f other", freshReads, freshOther, cachedReads, cachedOther, imageOther)
	// The last verified check left its verification for the cached checks.
	if got := c.reads.Load() - start; got != 0 {
		t.Fatal("cached checks read the image", got)
	}
	if cachedReads > freshReads || cachedOther > freshOther-imageOther/2 {
		t.Fatal("cached admission did not drop the image verification's I/O")
	}
}

func copyAgentImage(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err = out.Close(); err != nil {
		t.Fatal(err)
	}
}

// A running agent image renamed away and replaced at its registered path is
// re-verified by the next check, which refuses it and retires the inbox.
func TestMessagesNativeInboxRetiresReplacedImage(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	program := filepath.Join(dir, "native-agent.exe")
	copyAgentImage(t, exe, program)
	run, _ := messages1608FixtureImage(t, program)
	c := countAgentImage(run)
	call, _ := messages1608CallImage(t, program)
	instance := messages1608Presence(t, run, call, "", "ctx")
	if got := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "ready"); got.outcome != "opened" {
		t.Fatal(got)
	}
	run.d.mu.Lock()
	mark := run.d.unconfirmed["native-agent"]
	run.d.mu.Unlock()
	verified := c.reads.Load()
	if verified != 1 {
		t.Fatal("image reads before replacement", verified)
	}
	copyAgentImage(t, exe, program+".new")
	if err = os.Rename(program, program+".old"); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(program+".new", program); err != nil {
		t.Fatal(err)
	}
	replaced := time.Now()
	image, err := run.d.admittedAgentImage(run.ctx, mark.Program, mark.Agent.Binding)
	if err != nil || image.Key == mark.ProgramKey || c.reads.Load() == verified {
		t.Fatal("next check kept the verified image", err, image.Key, mark.ProgramKey, c.reads.Load())
	}
	for messages1608Row(t, run.d) != nil && time.Since(replaced) < 3*time.Second {
		time.Sleep(5 * time.Millisecond)
	}
	if messages1608Row(t, run.d) != nil {
		t.Fatal("replaced image kept the inbox")
	}
	t.Logf("inbox retired %v after replacement", time.Since(replaced).Round(time.Millisecond))
}
