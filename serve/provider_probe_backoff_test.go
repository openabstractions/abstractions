package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
)

// An attached provider that is down is probed with a doubling wait capped at
// probeReady, is read ready within one probeReady of coming up, and after a
// ready reading is probed at probeWaiting again when it goes down. The real
// supervisor runs from a declaration file through runtimeProviders.Serve and
// probes an endpoint served by this process: down closes each connection
// unanswered, up answers Describe through the generated chat dispatcher.
func TestAnAttachedProviderThatIsDownIsProbedWithBackoff(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	newInstallationDir(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	self = identity.CanonicalProgramPath(filepath.Clean(self))

	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, providersDir), 0o700); err != nil {
		t.Fatal(err)
	}
	const name = "backoff"
	f := providerFile{Version: providerFileVersion, DeclaredBy: "test", DeclaredAt: time.Now().UnixMilli(),
		Declaration: providerDeclaration{Name: name, Program: self, Arguments: []string{}, Endpoint: name, Transport: transportNative,
			Contracts: []string{inference.Contract}, Activation: "attach"}}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, providersDir, name+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := openFixtureProviders(t, state, false, func(err error) { t.Log(err) })
	if err != nil {
		t.Fatal(err)
	}
	const waiting, ready = 20 * time.Millisecond, 320 * time.Millisecond
	p.timing = providerTiming{probeWaiting: waiting, probeReady: ready, backoffFirst: time.Hour, backoffMax: time.Hour,
		stable: time.Hour, probeBudget: 2 * time.Second, inventoryBudget: 2 * time.Second}
	s := p.lookup(name)
	if s == nil || s.endpointErr != nil {
		t.Fatalf("supervisor %v", s)
	}

	l, err := listen.Listen(s.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	var up atomic.Bool
	var mu sync.Mutex
	var probes []time.Time // connections the provider closed unanswered
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			if !up.Load() {
				mu.Lock()
				probes = append(probes, time.Now())
				mu.Unlock()
				conn.Close()
				continue
			}
			go func() {
				defer conn.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 1<<20)
				if call != nil {
					defer call.Close()
				}
				if err != nil {
					return
				}
				reply, err := (&iwire.ChatDispatcher{Handler: fixtureChat{calls: filepath.Join(state, "calls")}}).ExchangeFrame(call.Frame)
				if err == nil {
					call.Reply(reply)
				}
			}()
		}
	}()
	downSince := func(since time.Time) []time.Time {
		mu.Lock()
		defer mu.Unlock()
		var out []time.Time
		for _, at := range probes {
			if !at.Before(since) {
				out = append(out, at)
			}
		}
		return out
	}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	start := time.Now()
	go func() { served <- p.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Error(err)
		}
	})

	// Down for 1.5 s: the schedule 0, 20, 60, 140, 300, 620, 940, 1260 ms
	// allows at most 8 probes; a fixed 20 ms wait would make about 75.
	const down = 1500 * time.Millisecond
	time.Sleep(down)
	seen := downSince(start)
	if len(seen) < 4 || len(seen) > 8 {
		t.Fatalf("%d probes in %v while down, want 4 to 8: %v", len(seen), down, seen)
	}
	if last := seen[len(seen)-1].Sub(seen[len(seen)-2]); last < ready*3/4 {
		t.Fatalf("the wait between the last two probes was %v, want about %v", last, ready)
	}
	if s.ready() {
		t.Fatalf("read ready while down: %+v", s.state())
	}

	// Up: read ready within one probeReady plus one probe.
	upAt := time.Now()
	up.Store(true)
	for !s.ready() {
		if time.Since(upAt) > 2*ready+time.Second {
			t.Fatalf("not ready %v after coming up: %+v", time.Since(upAt), s.state())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if latency := time.Since(upAt); latency > ready+250*time.Millisecond {
		t.Fatalf("read ready %v after coming up, want within %v", latency, ready)
	}

	// Down again: the ready reading reset the wait, so the failed probe at
	// most one probeReady later is followed by probes 20, 60 and 140 ms on.
	downAt := time.Now()
	up.Store(false)
	time.Sleep(ready + 400*time.Millisecond)
	if again := downSince(downAt); len(again) < 3 {
		t.Fatalf("%d probes in %v after going down again, want the wait reset to %v: %v", len(again), ready+400*time.Millisecond, waiting, again)
	}
}
