package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
)

// slowCommandFixture is the native applications host with a stand-in
// connector whose note.read answers after delay, the way the ComfyUI
// connector answers a command while ComfyUI is still starting. Its "open"
// verb answers at once.
func slowCommandFixture(t *testing.T, delay time.Duration) *applicationV3GatewayFixture {
	t.Helper()
	f := newApplicationV3GatewayFixture(t)
	f.manifest.Verbs = []registrationwire.ActivationVerb{{Name: "open", Class: appmodel.CommandClassReveal, InputSchema: `{"type":"object","properties":{},"additionalProperties":false}`}}
	f.mu.Lock()
	f.answer = func(name string, _ json.RawMessage) []byte {
		switch name {
		case "note.read":
			time.Sleep(delay)
		case "open":
			return applications.EncodeHostActivationResult(&applications.HostActivationResult{Outcome: applications.HostActivationOutcomeApplied})
		}
		return nil
	}
	f.mu.Unlock()
	// Every other call keeps a short budget; a command call follows its caller.
	saved := applicationsCallBudget
	applicationsCallBudget = 300 * time.Millisecond
	t.Cleanup(func() { applicationsCallBudget = saved })
	return f
}

func sessionOpen(f *applicationV3GatewayFixture) bool {
	select {
	case <-f.s.done:
		return false
	default:
		return true
	}
}

// Owner R31, 2026-11-12: a ComfyUI command sent while ComfyUI was still
// starting outlived the host's fixed call budget. The host cut the call and
// retired the session, and the person's ComfyUI lost its connection. A
// command now answers within its caller's wait, and a caller that leaves
// first leaves the session working.
func TestApplicationV3CommandAnswersWithinTheCallersWait(t *testing.T) {
	f := slowCommandFixture(t, 900*time.Millisecond)
	// The caller waits 3 s: the answer comes past the ordinary 300 ms budget.
	started := time.Now()
	result, _ := f.call(t, 2)
	if result.Outcome != applications.OutcomeApplied || result.Answer == nil || result.Answer.Result == nil || result.Answer.Result.Outcome != appmodel.CommandOutcomeApplied {
		t.Fatalf("a command slower than the ordinary budget: %+v", result)
	}
	if took := time.Since(started); took < 900*time.Millisecond {
		t.Fatalf("the answer came before the application's: %v", took)
	}
	// This caller waits 400 ms and leaves before the application answers.
	call := appmodel.GatewayCall{Application: "fixture", Instance: f.instance, Tool: "note.read", Arguments: `{}`, IdempotencyKey: "abandoned-command"}
	key := receiptKey(3, time.Now())
	if _, err := f.client(400*time.Millisecond).Call(key, call); err == nil {
		t.Fatal("a caller that left got an answer")
	}
	time.Sleep(100 * time.Millisecond)
	if !sessionOpen(f) {
		t.Fatal("a caller that stopped waiting retired the application's session")
	}
	// The next command is sent once the earlier answer arrived, and applies.
	again, _ := f.call(t, 4)
	if again.Outcome != applications.OutcomeApplied || again.Answer == nil || again.Answer.Result == nil || again.Answer.Result.Outcome != appmodel.CommandOutcomeApplied {
		t.Fatalf("the command after an abandoned one: %+v", again)
	}
	replayed, err := f.c.Call(key, call)
	if err != nil || replayed.Outcome != applications.OutcomeUnknown || replayed.Reason != "receipt" || replayed.Answer != nil {
		t.Fatalf("same-key replay of the abandoned command: %+v %v", replayed, err)
	}
	if f.effects.Load() != 3 || !sessionOpen(f) {
		t.Fatalf("effects=%d open=%v", f.effects.Load(), sessionOpen(f))
	}
	f.mu.Lock()
	reasons := []string{}
	for _, record := range f.records {
		reasons = append(reasons, record.Reason)
	}
	f.mu.Unlock()
	found := false
	for _, reason := range reasons {
		found = found || reason == "answer:wait_ended"
	}
	if !found {
		t.Fatalf("the abandoned command's record: %q", reasons)
	}
}

// Owner R31, 2026-11-12: after the session was retired, an agent's second
// open tried to start ComfyUI again and failed with "delivery uncertain;
// session retired". An open of the running instance, after a command its
// caller abandoned, answers that instance and starts nothing.
func TestApplicationV3DuplicateStartKeepsTheRunningSession(t *testing.T) {
	f := slowCommandFixture(t, 900*time.Millisecond)
	var starts atomic.Int32
	f.g.mu.Lock()
	f.g.start = func(ctx context.Context, _ ownedApplicationImage, _ ownedApplicationBinding, _ []string, _ func(context.Context, ownedApplicationBinding) error) (*ownedApplicationSession, error) {
		starts.Add(1)
		return nil, errors.Join(errOwnedApplicationUncertain, errors.New("a second host"))
	}
	f.g.mu.Unlock()
	call := appmodel.GatewayCall{Application: "fixture", Instance: f.instance, Tool: "note.read", Arguments: `{}`}
	if _, err := f.client(300*time.Millisecond).Call(receiptKey(2, time.Now()), call); err == nil {
		t.Fatal("a caller that left got an answer")
	}
	for i, key := range []int{3, 4} {
		opened, err := f.c.Activate(receiptKey(key, time.Now()), openRequest)
		if err != nil || opened.Outcome != applications.ActivationOutcomeReady || opened.Instance != f.instance || opened.Started {
			t.Fatalf("open %d of the running instance: %+v %v", i+1, opened, err)
		}
	}
	if starts.Load() != 0 || !sessionOpen(f) {
		t.Fatalf("starts=%d open=%v", starts.Load(), sessionOpen(f))
	}
	f.g.mu.Lock()
	kept := f.g.owned["fixture"] == f.s
	f.g.mu.Unlock()
	if !kept {
		t.Fatal("the running session left the gateway")
	}
	result, _ := f.call(t, 5)
	if result.Answer == nil || result.Answer.Result == nil || result.Answer.Result.Outcome != appmodel.CommandOutcomeApplied {
		t.Fatalf("a command after the duplicate opens: %+v", result)
	}
}
