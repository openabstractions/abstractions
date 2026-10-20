package main

import (
	"context"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/client"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The 0.4.0 rights slice end to end over the runtime's IPC (ASK-R5,
// RIGHTS-R16): the operator enrolls a program, the program answers a
// routine first-use question through responder@1, the record names it as
// the answerer, a script host cannot be enrolled, and Stop with cleanup takes the permit
// it gave.
func TestAnEnrolledProgramAnswersOverIPCAndStopTakesItsPermit(t *testing.T) {
	options := gatedRuntime(t)
	bin := probeBinary(t)
	answererProgram := copyTestBinary(t, "oa-assistant")
	python := copyTestBinary(t, "python")
	if result, _ := probeAs(t, bin, options, "model", "resolve", "hf:org/model@not/a/revision"); result.Outcome != "forbidden" {
		t.Fatalf("first use %+v", result)
	}
	pending := pendingFirstUse(t, options)
	if len(pending) != 1 {
		t.Fatalf("pending %+v", pending)
	}
	if page := respondAs(t, answererProgram, options, "list"); page.Outcome != "page" || listsQuestion(page, pending[0].ID) {
		t.Fatalf("a program never enrolled was offered %+v", page)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	machine := client.NewVerified(options.endpoint, runtimeExpectation(t))
	operator, err := machine.ResolveRightsOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	enroll := func(program string) string {
		t.Helper()
		policy, err := operator.ListPolicyContext(ctx, "", 1)
		if err != nil {
			t.Fatal(err)
		}
		rule := rightsclient.PolicyRule{Subject: rightsclient.Subject{Account: probeSelf().Account, Program: program}, Action: asks.AnswerAction, Resource: rights.MetaScopeApps, Permit: true}
		set, err := operator.SetRuleForContext(ctx, policy.Revision, rule, rights.MetaRuleLapse, "Let the assistant answer OpenAbstractions questions for you")
		if err != nil {
			t.Fatal(err)
		}
		return set.Outcome.String()
	}
	if got := enroll(python); got != "invalid" {
		t.Fatalf("enrolling a script host: %s", got)
	}
	if got := enroll(answererProgram); got != "applied" {
		t.Fatalf("enrolling the assistant: %s", got)
	}
	page := respondAs(t, answererProgram, options, "list")
	if page.Outcome != "page" || !listsQuestion(page, pending[0].ID) {
		t.Fatalf("the enrolled program was not offered the routine question %+v", page)
	}
	answer := respondAs(t, answererProgram, options, "answer", pending[0].ID, "allow")
	if answer.Outcome != "answered" || answer.Effect != "applied" || answer.Target != "permitted" {
		t.Fatalf("answer %+v", answer)
	}
	asksOperator, err := machine.ResolveAsksOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	read, err := asksOperator.ReadAttributedQuestionContext(ctx, pending[0].ID)
	if err != nil || read.Question.Outcome != askclient.OperatorQuestionOutcomeFound || read.Question.AnsweredBy == nil || read.Question.AnsweredBy.String() != "responder" || !samePrograms(read.Answerer, answererProgram) {
		t.Fatalf("the record does not carry the answerer: %+v %v", read, err)
	}
	target := rightsclient.Subject{Account: probeSelf().Account, Program: bin}
	rule, err := operator.ReadRuleContext(ctx, target, host.ModelLookupAction, "hf")
	if err != nil || rule.Outcome.String() != "found" || rule.Record == nil || !samePrograms(rule.Record.SetBy.Program, answererProgram) {
		t.Fatalf("the permit %+v %v", rule, err)
	}
	if expires, err := time.Parse(time.RFC3339Nano, rule.Record.Expires); err != nil || time.Until(expires) < rights.MetaRuleLapse-time.Hour {
		t.Fatalf("the permit does not last until the enrollment ends: %q %v", rule.Record.Expires, err)
	}
	policy, err := operator.ListPolicyContext(ctx, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := operator.StopAnswererContext(ctx, policy.Revision, rightsclient.Subject{Account: probeSelf().Account, Program: answererProgram}, true)
	if err != nil || stop.Outcome.String() != "applied" {
		t.Fatalf("stop %+v %v", stop, err)
	}
	if after, err := operator.ReadRuleContext(ctx, target, host.ModelLookupAction, "hf"); err != nil || after.Outcome.String() != "unknown" {
		t.Fatalf("the permit outlived Stop %+v %v", after, err)
	}
}
