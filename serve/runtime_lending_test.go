package main

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	lend "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
)

// newLendingTestProvider builds a runtimeProviders holding one on-demand
// lending declaration, without launching any real process: the supervisor's
// readiness is driven directly by the test through s.set, exactly as a real
// probe would drive it after a launch.
func newLendingTestProvider(t *testing.T, name string) (*runtimeProviders, *providerSupervisor) {
	t.Helper()
	p := &runtimeProviders{supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{}), report: func(error) {}}
	f := providerFile{Declaration: providerDeclaration{Name: name, Program: "unused", Endpoint: "oa-test-lend-" + name,
		Transport: transportNative, Contracts: []string{storageLending}, Activation: wire.ActivationOnDemand.String()}}
	s := newProviderSupervisor(p, f)
	p.supervisors[name] = s
	return p, s
}

// activated reports whether the supervisor's on-demand launch was asked for,
// without s.run ever having been started (these tests drive readiness
// directly, so activate's usual effect of starting supervision is unused).
func activated(s *providerSupervisor) bool {
	select {
	case <-s.wanted:
		return true
	default:
		return false
	}
}

// A provider already ready costs the first call nothing beyond the transport
// build: FAC-R8's "a provider that is ready costs a resolution nothing beyond
// the description read."
func TestLendingProviderFastReadySucceedsFirstCall(t *testing.T) {
	p, s := newLendingTestProvider(t, "fast")
	s.set(wire.DeclarationReadinessReady, "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, reason := p.lendingProvider(ctx)
	if client == nil || reason != "" {
		t.Fatalf("fast provider: client=%v reason=%q", client, reason)
	}
}

// FAC-R8: the first resolution that launches an on-demand provider waits for
// its readiness inside the caller's own deadline. A caller whose deadline
// passes first reads unavailable with reason activating:<name>, the launch
// continues, and the next call finds the provider ready.
func TestLendingProviderActivatingThenReadyOnLaterCall(t *testing.T) {
	p, s := newLendingTestProvider(t, "slow")

	// The provider becomes ready well after the first caller's short deadline,
	// simulating a slow-starting on-demand provider.
	go func() {
		time.Sleep(150 * time.Millisecond)
		s.set(wire.DeclarationReadinessReady, "")
	}()

	shortCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	client, reason := p.lendingProvider(shortCtx)
	if client != nil || reason != "activating:slow" {
		t.Fatalf("first call under a short deadline: client=%v reason=%q, want activating:slow", client, reason)
	}
	if !activated(s) {
		t.Fatalf("the launch was not activated")
	}

	// The launch keeps running in the background; a later call with room to
	// wait finds it ready.
	laterCtx, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	client, reason = p.lendingProvider(laterCtx)
	if client == nil || reason != "" {
		t.Fatalf("later call: client=%v reason=%q", client, reason)
	}
}

// FAC-R8: the wait never exceeds the runtime's launch budget, even with a
// caller context carrying no deadline of its own, and a provider that never
// becomes ready.
func TestLendingProviderLaunchBudgetBoundsANeverReadyProvider(t *testing.T) {
	previous := lendingLaunchBudget
	lendingLaunchBudget = 60 * time.Millisecond
	t.Cleanup(func() { lendingLaunchBudget = previous })

	p, s := newLendingTestProvider(t, "stuck")

	start := time.Now()
	client, reason := p.lendingProvider(context.Background())
	elapsed := time.Since(start)
	if client != nil || reason != "activating:stuck" {
		t.Fatalf("never-ready provider: client=%v reason=%q, want activating:stuck", client, reason)
	}
	if elapsed > lendingLaunchBudget+500*time.Millisecond {
		t.Fatalf("wait exceeded the launch budget: %v", elapsed)
	}
	if !activated(s) {
		t.Fatalf("the launch was not activated")
	}
}

// lendingQuestions lists the first-use questions book holds about lending.
func lendingQuestions(t *testing.T, book *asks.Book) []asks.Record {
	t.Helper()
	records, _, err := book.ApplicationHistory()
	if err != nil {
		t.Fatal(err)
	}
	var lending []asks.Record
	for _, q := range records {
		if _, action, _, ok := asks.FirstUse(q.Key, q.About, q.Text); ok && action == ActionLend {
			lending = append(lending, q)
		}
	}
	return lending
}

// TestLendNotGrantedAdmitsAFirstUseQuestion: a lend refused for want of a
// rule admits the runtime's first-use question about the engine (VISION
// 2026-09-22, research/model-bridge/DECISION.md rule 3); listing the ledger
// admits none. A lend that reaches the decision without the caller's peer
// records no publisher (ASK-A6).
func TestLendNotGrantedAdmitsAFirstUseQuestion(t *testing.T) {
	policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "rights.json"), firstUseActions)
	if err != nil {
		t.Fatal(err)
	}
	a, book := firstUseBook(t)
	r := &runtimeRights{policy: policy, owner: a.rights.owner, firstUse: a.admitFirstUse}
	a.rights = r
	l := &runtimeLending{rights: r}
	caller := inference.Subject{Account: r.owner, Program: filepath.Join(t.TempDir(), "lender.exe")}
	if page := l.Lends(context.Background(), caller, "", 16); len(lendingQuestions(t, book)) != 0 {
		t.Fatalf("listing admitted a question: %+v", page)
	}
	if result := l.Lend(context.Background(), caller, "hf", "object", "lmstudio", false); result.Outcome != lend.LendOutcomeNotPermitted {
		t.Fatalf("lend without a rule: %+v", result)
	}
	questions := lendingQuestions(t, book)
	if len(questions) != 1 || questions[0].About != caller.Program || !strings.HasSuffix(questions[0].Text, " on "+ResourceEngine("lmstudio")) {
		t.Fatalf("one lend question about %s expected: %+v", caller.Program, questions)
	}
	if q := questions[0]; q.Publisher != "" || q.PublisherTrusted {
		t.Fatalf("a lend decided without a peer recorded publisher %q trusted=%v", q.Publisher, q.PublisherTrusted)
	}
}

// TestLendFirstUseRecordsTheCallersPublisher: a signed program that opens the
// runtime's lending pipe itself and is refused a lend gets a first-use
// question naming its publisher, read from the code of the peer the pipe
// bound (ASK-A6).
func TestLendFirstUseRecordsTheCallersPublisher(t *testing.T) {
	pythonExe, pythonArgs, program, want := signedPythonPublisher(t)
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "rights.json"), firstUseActions)
	if err != nil {
		t.Fatal(err)
	}
	a, book := firstUseBook(t)
	r := &runtimeRights{policy: policy, owner: account.Uid, firstUse: a.admitFirstUse}
	endpoint := fmt.Sprintf(`\\.\pipe\oa-lend-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	h, err := listenLending(endpoint, &runtimeLending{rights: r}, r.owner, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- h.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-served })

	var capture frameCapture
	lend.NewLendingClient(&capture).Lend("hf", "object", "lmstudio", false)
	capture.reply = runPipePython(t, pythonExe, pythonArgs, endpoint, capture.frame)
	if result, err := lend.NewLendingClient(&capture).Lend("hf", "object", "lmstudio", false); err != nil || result.Outcome != lend.LendOutcomeNotPermitted {
		t.Fatalf("lend from python: %+v %v", result, err)
	}
	questions := lendingQuestions(t, book)
	if len(questions) != 1 || !samePrograms(questions[0].About, program) {
		t.Fatalf("one lend question about %s expected: %+v", program, questions)
	}
	if q := questions[0]; q.Publisher != want.Subject || q.PublisherTrusted != want.Trusted {
		t.Fatalf("the lend question must carry publisher %+v: %+v", want, q)
	}
}
