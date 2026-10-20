package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// choiceView is what the shared-choice helper process prints for one call.
type choiceView struct {
	Outcome string   `json:"outcome"`
	ID      string   `json:"id,omitempty"`
	IDs     []string `json:"ids,omitempty"`
	Options []string `json:"options,omitempty"`
}

// TestSharedChoiceHelper is the body of a copied test executable: an
// application that shares a choice ("share"), or a program that lists or
// answers shared choices through responder@1 ("list", "answer"). It does
// nothing in a normal test run.
func TestSharedChoiceHelper(t *testing.T) {
	if os.Getenv("OA_CHOICE_HELPER") == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("OA_CHOICE_ARGS")), &args); err != nil || len(args) < 3 {
		fmt.Fprintln(os.Stderr, "choice helper arguments:", err)
		os.Exit(2)
	}
	who, _, err := principal()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	machine := client.NewVerified(args[0], listen.ServerExpectation{Principal: who, Program: args[1]})
	var view choiceView
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	switch args[2] {
	case "share":
		choices, err := machine.ResolveAsksChoices(ctx, local)
		if err != nil {
			fail(err)
		}
		now := time.Now()
		q := askswire.ChoiceRequest{RequestRef: askswire.RequestRef{Key: args[3], ReplayUntilMs: now.Add(time.Hour).UnixMilli()}, Title: "The output already exists",
			Options: []askswire.ChoiceOption{{ID: "replace", Label: "Replace"}, {ID: "keep", Label: "Keep"}, {ID: "cancel", Label: "Cancel"}}, PendingUntilMs: now.Add(10 * time.Minute).UnixMilli()}
		out, err := choices.ShareContext(ctx, q, []string{"keep", "cancel"})
		if err != nil {
			fail(err)
		}
		view.Outcome, view.ID = out.Outcome.String(), out.ID
	case "list", "answer":
		responder, err := machine.ResolveAsksResponder(ctx, local)
		if err != nil {
			fail(err)
		}
		if args[2] == "list" {
			page, err := responder.ListChoicesContext(ctx, "", 16)
			if err != nil {
				fail(err)
			}
			view.Outcome = page.Outcome.String()
			for _, c := range page.Choices {
				view.IDs = append(view.IDs, c.ID)
				for _, o := range c.Options {
					view.Options = append(view.Options, o.ID)
				}
			}
		} else {
			out, err := responder.AnswerChoiceContext(ctx, args[3], args[4])
			if err != nil {
				fail(err)
			}
			view.Outcome = out.Outcome.String()
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(view); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func choiceAs(t *testing.T, program string, options runtimeFlags, args ...string) choiceView {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(append([]string{options.endpoint, filepath.Clean(exe)}, args...))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(program, "-test.run=^TestSharedChoiceHelper$")
	command.Env = append(os.Environ(), "OA_CHOICE_HELPER=1", "OA_CHOICE_ARGS="+string(encoded))
	var out, diagnostics bytes.Buffer
	command.Stdout, command.Stderr = &out, &diagnostics
	if err := command.Run(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, diagnostics.String())
	}
	var view choiceView
	if err := json.Unmarshal(out.Bytes(), &view); err != nil {
		t.Fatalf("%v printed %q: %v", args, out.String(), err)
	}
	return view
}

// The ordinary-choice answerer over the runtime's IPC (asks ASK-R9): an
// application shares a choice; a program never enrolled sees nothing; the
// requester itself, enrolled, cannot answer its own choice; an enrolled
// assistant sees only the shared options, answers one, and its answer renews
// the enrollment and is audited; after Stop it sees nothing.
func TestAnEnrolledProgramAnswersASharedChoiceOverIPC(t *testing.T) {
	options := gatedRuntime(t)
	requester := copyTestBinary(t, "oa-app")
	assistant := copyTestBinary(t, "oa-assistant")
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	machine := client.NewVerified(options.endpoint, runtimeExpectation(t))
	operator, err := machine.ResolveRightsOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	set := func(program, action, resource string, ttl time.Duration) {
		t.Helper()
		policy, err := operator.ListPolicyContext(ctx, "", 1)
		if err != nil {
			t.Fatal(err)
		}
		rule := rightsclient.PolicyRule{Subject: rightsclient.Subject{Account: probeSelf().Account, Program: program}, Action: action, Resource: resource, Permit: true}
		if edit, err := operator.SetRuleForContext(ctx, policy.Revision, rule, ttl, "shared choice test"); err != nil || edit.Outcome.String() != "applied" {
			t.Fatalf("set %s %s: %+v %v", program, action, edit, err)
		}
	}
	set(requester, "abstraction.asks/question.ask", "account", time.Hour)
	shared := choiceAs(t, requester, options, "share", "output")
	if shared.Outcome != "pending" || shared.ID == "" {
		t.Fatalf("share %+v", shared)
	}
	if view := choiceAs(t, assistant, options, "list"); view.Outcome != "page" || len(view.IDs) != 0 {
		t.Fatalf("a program never enrolled was offered %+v", view)
	}
	set(requester, asks.AnswerAction, rights.MetaScopeApps, rights.MetaRuleLapse)
	if view := choiceAs(t, requester, options, "list"); view.Outcome != "page" || len(view.IDs) != 0 {
		t.Fatalf("the requester was offered its own choice %+v", view)
	}
	if view := choiceAs(t, requester, options, "answer", shared.ID, "keep"); view.Outcome != "forbidden" {
		t.Fatalf("the requester answered its own choice %+v", view)
	}
	set(assistant, asks.AnswerAction, rights.MetaScopeApps, time.Hour)
	enrollment := func() time.Time {
		t.Helper()
		read, err := operator.ReadRuleContext(ctx, rightsclient.Subject{Account: probeSelf().Account, Program: assistant}, asks.AnswerAction, rights.MetaScopeApps)
		if err != nil || read.Record == nil {
			t.Fatalf("enrollment %+v %v", read, err)
		}
		expires, err := time.Parse(time.RFC3339Nano, read.Record.Expires)
		if err != nil {
			t.Fatal(err)
		}
		return expires
	}
	before := enrollment()
	view := choiceAs(t, assistant, options, "list")
	if view.Outcome != "page" || len(view.IDs) != 1 || view.IDs[0] != shared.ID || len(view.Options) != 2 || view.Options[0] != "keep" || view.Options[1] != "cancel" {
		t.Fatalf("the enrolled assistant's page %+v", view)
	}
	if denied := choiceAs(t, assistant, options, "answer", shared.ID, "replace"); denied.Outcome != "forbidden" {
		t.Fatalf("a reserved option was answered %+v", denied)
	}
	if answered := choiceAs(t, assistant, options, "answer", shared.ID, "keep"); answered.Outcome != "answered" {
		t.Fatalf("answer %+v", answered)
	}
	if after := enrollment(); !after.After(before.Add(24 * time.Hour)) {
		t.Fatalf("the answer did not renew the enrollment: %v then %v", before, after)
	}
	asksOperator, err := machine.ResolveAsksOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	details, err := asksOperator.ReadChoiceDetailsContext(ctx, shared.ID)
	if err != nil || details.AnsweredVia == nil || *details.AnsweredVia != askclient.ChoiceRouteResponder || !samePrograms(details.Choice.Record.Answer.AnsweredBy, assistant) || !samePrograms(details.AskerProgram, requester) {
		t.Fatalf("details %+v %v", details, err)
	}
	audit, err := asksOperator.ListAnswersContext(ctx, "", 16)
	if err != nil || len(audit.Answers) != 1 || audit.Answers[0].Label != "Keep" || !samePrograms(audit.Answers[0].Answerer, assistant) {
		t.Fatalf("audit %+v %v", audit, err)
	}
	policy, err := operator.ListPolicyContext(ctx, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if stop, err := operator.StopAnswererContext(ctx, policy.Revision, rightsclient.Subject{Account: probeSelf().Account, Program: assistant}, false); err != nil || stop.Outcome.String() != "applied" {
		t.Fatalf("stop %+v %v", stop, err)
	}
	next := choiceAs(t, requester, options, "share", "after-stop")
	if view := choiceAs(t, assistant, options, "list"); view.Outcome != "page" || len(view.IDs) != 0 {
		t.Fatalf("a stopped assistant was offered %+v", view)
	}
	if denied := choiceAs(t, assistant, options, "answer", next.ID, "keep"); denied.Outcome != "forbidden" {
		t.Fatalf("a stopped assistant answered %+v", denied)
	}
}
