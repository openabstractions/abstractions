package main

import (
	"context"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/client"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// reaskFixture is one gated runtime with a probe program whose model lookup
// on hf is the first-use question under test.
type reaskFixture struct {
	t        *testing.T
	options  runtimeFlags
	bin      string
	ctx      context.Context
	machine  *client.Machine
	operator *rightsclient.Operator
	target   rightsclient.Subject
}

func newReaskFixture(t *testing.T) *reaskFixture {
	t.Helper()
	options := gatedRuntime(t)
	bin := probeBinary(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*runtimeWait+time.Minute)
	t.Cleanup(cancel)
	machine := client.NewVerified(options.endpoint, runtimeExpectation(t))
	operator, err := machine.ResolveRightsOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	return &reaskFixture{t: t, options: options, bin: bin, ctx: ctx, machine: machine, operator: operator,
		target: rightsclient.Subject{Account: probeSelf().Account, Program: bin}}
}

// refused runs the lookup, which must be refused, and returns the pending
// first-use questions after it.
func (f *reaskFixture) refused(stage string) []string {
	f.t.Helper()
	if result, _ := probeAs(f.t, f.bin, f.options, "model", "resolve", "hf:org/model@not/a/revision"); result.Outcome != "forbidden" {
		f.t.Fatalf("%s: lookup %+v", stage, result)
	}
	var ids []string
	for _, r := range pendingFirstUse(f.t, f.options) {
		ids = append(ids, r.ID)
	}
	return ids
}

// asked is refused and requires exactly one new pending question.
func (f *reaskFixture) asked(stage, previous string) string {
	f.t.Helper()
	ids := f.refused(stage)
	if len(ids) != 1 || ids[0] == previous {
		f.t.Fatalf("%s: pending %v, wanted one new question", stage, ids)
	}
	return ids[0]
}

func (f *reaskFixture) decision() string {
	f.t.Helper()
	d, _ := probeAs(f.t, f.bin, f.options, "rights", "decide", host.ModelLookupAction, "hf")
	return d.Outcome
}

func (f *reaskFixture) removeRule(stage string) {
	f.t.Helper()
	read, err := f.operator.ReadRuleContext(f.ctx, f.target, host.ModelLookupAction, "hf")
	if err != nil || read.Outcome.String() != "found" {
		f.t.Fatalf("%s: rule %+v %v", stage, read, err)
	}
	if revoke, err := f.operator.RevokeRuleContext(f.ctx, read.Revision, f.target, host.ModelLookupAction, "hf"); err != nil || revoke.Outcome.String() != "applied" {
		f.t.Fatalf("%s: revoke %+v %v", stage, revoke, err)
	}
}

// answered reads the recorded option of one retained question.
func (f *reaskFixture) answered(id string) string {
	f.t.Helper()
	questions, err := f.machine.ResolveAsksOperator(f.ctx, local)
	if err != nil {
		f.t.Fatal(err)
	}
	attributedRead, err := questions.ReadAttributedQuestionContext(f.ctx, id)
	read := attributedRead.Question
	if err != nil || read.Record == nil {
		return ""
	}
	return read.Record.Option
}

// ASK-R2: a delegated allow whose effect applied its target permit is asked
// again once that permit no longer holds, after a revoke and after expiry.
// Walkthrough 2026-10-02 D4.
func TestFirstUseAsksAgainOnceADelegatedAllowLapses(t *testing.T) {
	f := newReaskFixture(t)
	responder := copyTestBinary(t, "oa-reask-responder")
	scope, err := asks.FirstUseAnswerResource(f.bin, host.ModelLookupAction, "hf", "allow")
	if err != nil {
		t.Fatal(err)
	}

	first := f.asked("first use", "")
	delegate(t, f.options, responder, scope, 10*time.Minute)
	if out := respondAs(t, responder, f.options, "answer", first, "allow"); out.Outcome != "answered" || out.Effect != "applied" {
		t.Fatalf("delegated allow %+v", out)
	}
	if got := f.decision(); got != "permitted" {
		t.Fatalf("decision after the delegated allow %s", got)
	}
	f.removeRule("revoke")
	second := f.asked("after revoke", first)

	// The delegation ends in a few seconds, so the target permit it writes
	// ends a second earlier (ASK-R4).
	delegate(t, f.options, responder, scope, 6*time.Second)
	if out := respondAs(t, responder, f.options, "answer", second, "allow"); out.Outcome != "answered" || out.Effect != "applied" {
		t.Fatalf("short delegated allow %+v", out)
	}
	for deadline := time.Now().Add(15 * time.Second); f.decision() != "not_granted"; time.Sleep(250 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the short target permit did not expire")
		}
	}
	f.asked("after expiry", second)
}

// ASK-R2: an allow whose rule did not land keeps its answered record, so the
// Panel's Retry rule still finds it; the refusal asks nothing new.
func TestFirstUseKeepsAnAllowWhoseRuleDidNotLand(t *testing.T) {
	f := newReaskFixture(t)
	first := f.asked("first use", "")
	questions, err := f.machine.ResolveAsksOperator(f.ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	// The answer is recorded and its rule write never happens, as when the
	// Panel's write conflicted or failed.
	if out, err := questions.AnswerQuestionIfPendingContext(f.ctx, first, "allow"); err != nil || out.Outcome.String() != "answered" {
		t.Fatalf("answer without a rule %+v %v", out, err)
	}
	if ids := f.refused("rule not written"); len(ids) != 0 {
		t.Fatalf("an allow whose rule never landed was asked again %v", ids)
	}
	if option := f.answered(first); option != "allow" {
		t.Fatalf("the answered allow was not kept for its retry: option %q", option)
	}
}

// ASK-R2: refuse means "not this time": the next refusal asks again once
// the interval has passed, and not before. never stays a silent deny, even
// after its deny rule is removed and the interval passes.
func TestFirstUseAsksAgainAfterARefuseOnlyAfterTheInterval(t *testing.T) {
	previous := firstUseRefuseInterval
	firstUseRefuseInterval = 3 * time.Second
	t.Cleanup(func() { firstUseRefuseInterval = previous })
	f := newReaskFixture(t)

	first := f.asked("first use", "")
	answerFirstUse(t, f.options, first, "refuse")
	answeredAt := time.Now()
	if ids := f.refused("inside the interval"); len(ids) != 0 {
		if time.Since(answeredAt) < firstUseRefuseInterval {
			t.Fatalf("a refuse was asked again inside the interval %v", ids)
		}
		t.Skip("the lookup outlasted the refuse interval on this machine")
	}
	time.Sleep(time.Until(answeredAt.Add(firstUseRefuseInterval + 500*time.Millisecond)))
	second := f.asked("after the interval", first)

	answerFirstUse(t, f.options, second, "never")
	if got := f.decision(); got != "denied" {
		t.Fatalf("decision after never %s", got)
	}
	f.removeRule("remove the deny")
	time.Sleep(firstUseRefuseInterval + 500*time.Millisecond)
	if ids := f.refused("after never"); len(ids) != 0 {
		t.Fatalf("a standing never was asked again %v", ids)
	}
}
