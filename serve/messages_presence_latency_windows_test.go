//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

// presenceLatencyHelpers opens n native inboxes, one per helper process, as
// OpenCode's kept helpers do for its recent chats, and returns the renewal of
// each: the gateway's Announce followed by its InboxOpen.
func presenceLatencyHelpers(t *testing.T, run *selfRegisteredRun, n int) []func() time.Duration {
	t.Helper()
	renew := make([]func() time.Duration, n)
	for i := range n {
		call, _ := messages1608Call(t)
		contextName := fmt.Sprintf("ctx-%d", i)
		instance := messages1608Presence(t, run, call, "", contextName)
		if got := run.d.openNativeInbox(run.ctx, call, instance, contextName, "ready"); got.outcome != "opened" {
			t.Fatal(i, got)
		}
		pin, err := call.RetainProcess()
		if err != nil {
			t.Fatal(err)
		}
		_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
		pin.Close()
		if err != nil {
			t.Fatal(err)
		}
		renew[i] = func() time.Duration {
			start := time.Now()
			r := run.d.announceInSession(run.ctx, subject, session, wire.ApplicationPresence{Application: "native-agent", Instance: instance, LeaseMs: 60000, Interfaces: []wire.ApplicationInterface{}, Contexts: []wire.ApplicationContext{{Name: contextName}}})
			if r.Outcome != wire.ApplicationOutcomeApplied {
				t.Error("announce", i, r)
			}
			if got := run.d.openNativeInbox(run.ctx, call, instance, contextName, "ready"); got.outcome != "opened" {
				t.Error("inbox open", i, got)
			}
			return time.Since(start)
		}
	}
	return renew
}

// presenceLatencyRun renews every helper concurrently, each on its own
// cadence, and returns the sorted renewal latencies.
func presenceLatencyRun(renew []func() time.Duration, cadence, total time.Duration) []time.Duration {
	var mu sync.Mutex
	var all []time.Duration
	var wg sync.WaitGroup
	until := time.Now().Add(total)
	for i, f := range renew {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(i) * cadence / time.Duration(len(renew)))
			for time.Now().Before(until) {
				d := f()
				mu.Lock()
				all = append(all, d)
				mu.Unlock()
				time.Sleep(cadence)
			}
		}()
	}
	wg.Wait()
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	return all
}

// presenceLatencyRegistrations publishes n further applications@3
// registrations, so each registration read costs what an installed runtime
// with several registered applications pays.
func presenceLatencyRegistrations(t *testing.T, run *selfRegisteredRun, n int) {
	t.Helper()
	if run.d.v3 == nil {
		if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
			t.Fatal(err)
		}
	}
	for i := range n {
		name := fmt.Sprintf("latency-app%d", i)
		registration1459Publish(t, run.d, name, registration1459Program(name+".exe"), "Latency app", "operator", "")
	}
}

// Owner's host.log, 2026-11-09: with OpenCode keeping a helper for each of its
// eight recent chats, helpers' presence calls outlived the gateway's 2-second
// limit. The native inbox watch rechecked every open inbox's enrollment ten
// times a second, each check two registration reads under the directory lock
// that every renewal also takes. Twelve helpers, each renewing every second
// (ten times the gateway's cadence) against a store with ten registered
// applications, must each finish inside that limit.
// TestMessagesNativeInboxWatchChecksEachProgramOnce holds the watch's own share.
func TestMessagesPresenceRenewalLatencyManyHelpers(t *testing.T) {
	run, _ := messages1608Fixture(t)
	presenceLatencyRegistrations(t, run, 10)
	renew := presenceLatencyHelpers(t, run, 12)
	all := presenceLatencyRun(renew, time.Second, 6*time.Second)
	if len(all) == 0 {
		t.Fatal("no renewals")
	}
	worst := all[len(all)-1]
	t.Logf("helpers=12 renewals=%d p50=%v p95=%v max=%v", len(all), all[len(all)/2], all[len(all)*95/100], worst)
	if worst >= 2*time.Second {
		t.Fatalf("slowest presence renewal %v; the gateway gives up at 2 s", worst)
	}
}

// The watch checks enrollment once per application and program per pass,
// however many chats that program keeps open.
func TestMessagesNativeInboxWatchChecksEachProgramOnce(t *testing.T) {
	run, _ := messages1608Fixture(t)
	var stamps atomic.Int64
	run.d.agentStamp = func(program string) (applicationAgentStamp, error) {
		stamps.Add(1)
		return readApplicationAgentStamp(program)
	}
	presenceLatencyHelpers(t, run, 8)
	start := stamps.Load()
	time.Sleep(3 * time.Second)
	got := stamps.Load() - start
	if got < 1 || got > 5 {
		t.Fatalf("watch checked enrollment %d times in 3 s for one program with 8 open chats; want one check a second", got)
	}
	for i := range 8 {
		run.d.mu.Lock()
		row := run.d.nativeInboxes.rows[fmt.Sprintf("native-agent\x00ctx-%d", i)]
		run.d.mu.Unlock()
		if row == nil {
			t.Fatal("watch retired an enrolled chat", i)
		}
	}
}

// Revoking one application's enrollment retires only its open inboxes, inside
// the watch's one-second pass, and leaves another application's inboxes open.
func TestMessagesNativeInboxWatchRevokesOnlyThatApplication(t *testing.T) {
	run, revision := messages1608Fixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	otherImage := filepath.Join(t.TempDir(), "other-agent.exe")
	if err := os.WriteFile(otherImage, raw, 0700); err != nil {
		t.Fatal(err)
	}
	agent1478Register(t, run, "other-agent", identity.NormalizeSubjectProgram(otherImage))
	agent1478Set(t, run, "other-agent", "", true)
	answer(t, run.f, agent1478Question(t, run, "other-agent").ID, "keep")
	agent1478Wait(t, run, "other-agent", "enrolled")
	open := func(call *listen.FramedCall, application, contextName string) {
		t.Helper()
		pin, err := call.RetainProcess()
		if err != nil {
			t.Fatal(err)
		}
		_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
		pin.Close()
		if err != nil {
			t.Fatal(err)
		}
		r := run.d.announceInSession(run.ctx, subject, session, wire.ApplicationPresence{Application: application, LeaseMs: 60000, Interfaces: []wire.ApplicationInterface{}, Contexts: []wire.ApplicationContext{{Name: contextName}}})
		if r.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatal(application, r)
		}
		if got := run.d.openNativeInbox(run.ctx, call, r.Instance, contextName, "ready"); got.outcome != "opened" {
			t.Fatal(application, contextName, got)
		}
	}
	for i := range 2 {
		call, _ := messages1608Call(t)
		open(call, "native-agent", fmt.Sprintf("ctx-%d", i))
		otherCall, _ := messages1608CallImage(t, otherImage)
		open(otherCall, "other-agent", fmt.Sprintf("ctx-%d", i))
	}
	row := func(key string) *nativeInbox {
		run.d.mu.Lock()
		defer run.d.mu.Unlock()
		return run.d.nativeInboxes.rows[key]
	}
	agent1478Set(t, run, "native-agent", revision, false)
	revoked := time.Now()
	for (row("native-agent\x00ctx-0") != nil || row("native-agent\x00ctx-1") != nil) && time.Since(revoked) < 1500*time.Millisecond {
		time.Sleep(10 * time.Millisecond)
	}
	for i := range 2 {
		if row(fmt.Sprintf("native-agent\x00ctx-%d", i)) != nil {
			t.Fatal("revoked application's inbox outlived 1.5 s", i)
		}
		if row(fmt.Sprintf("other-agent\x00ctx-%d", i)) == nil {
			t.Fatal("other application's inbox retired", i)
		}
	}
}
