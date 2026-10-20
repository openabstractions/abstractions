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
	"github.com/openabstractions/abstraction-facade/go/client"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// responderView is what the responder helper process prints for one call.
type responderView struct {
	Outcome string `json:"outcome"`
	Effect  string `json:"effect,omitempty"`
	Target  string `json:"target,omitempty"`
	Record  string `json:"record,omitempty"`
	Records []struct {
		ID      string   `json:"id"`
		Options []string `json:"options"`
	} `json:"records,omitempty"`
}

// TestAsksResponderHelper is the body of a copied test executable: a program
// other than the in-process runtime that probes as itself ("probe") or calls
// responder@1 ("responder"). It does nothing in a normal test run.
func TestAsksResponderHelper(t *testing.T) {
	role := os.Getenv("OA_RESPONDER_HELPER")
	if role == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("OA_RESPONDER_ARGS")), &args); err != nil || len(args) < 3 {
		fmt.Fprintln(os.Stderr, "responder helper arguments:", err)
		os.Exit(2)
	}
	endpoint, runtimeProgram := args[0], args[1]
	if role == "probe" {
		exitOnFailure(probeCommand(append(args[2:], "--endpoint", endpoint, "--runtime-program", runtimeProgram, "--json"), os.Stdout, os.Stderr))
		os.Exit(0)
	}
	who, _, err := principal()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	machine := client.NewVerified(endpoint, listen.ServerExpectation{Principal: who, Program: runtimeProgram})
	responder, err := machine.ResolveAsksResponder(ctx, local)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var view responderView
	switch args[2] {
	case "list":
		page, err := responder.ListAnswerableContext(ctx, "", 16)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		view.Outcome = page.Outcome.String()
		for _, r := range page.Records {
			view.Records = append(view.Records, struct {
				ID      string   `json:"id"`
				Options []string `json:"options"`
			}{r.ID, r.Options})
		}
	case "answer":
		out, err := responder.AnswerContext(ctx, args[3], args[4])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		view.Outcome, view.Effect, view.Target = out.Outcome.String(), out.Effect.String(), out.TargetOutcome
		if out.Record != nil {
			view.Record = out.Record.ID
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(view); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

// runHelper runs a copied test executable in one role and decodes its output.
func runHelper(t *testing.T, program string, options runtimeFlags, role string, args ...string) []byte {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(append([]string{options.endpoint, filepath.Clean(exe)}, args...))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(program, "-test.run=^TestAsksResponderHelper$")
	command.Env = append(os.Environ(), "OA_RESPONDER_HELPER="+role, "OA_RESPONDER_ARGS="+string(encoded))
	var out, diagnostics bytes.Buffer
	command.Stdout, command.Stderr = &out, &diagnostics
	runErr := command.Run()
	if _, exited := runErr.(*exec.ExitError); runErr != nil && (!exited || role == "responder") {
		t.Fatalf("%s %v: %v\n%s", role, args, runErr, diagnostics.String())
	}
	return out.Bytes()
}

func respondAs(t *testing.T, program string, options runtimeFlags, args ...string) responderView {
	t.Helper()
	var view responderView
	out := runHelper(t, program, options, "responder", args...)
	if err := json.Unmarshal(out, &view); err != nil {
		t.Fatalf("responder %v printed %q: %v", args, out, err)
	}
	return view
}

func listsQuestion(view responderView, id string) bool {
	for _, r := range view.Records {
		if r.ID == id {
			return true
		}
	}
	return false
}

// delegate writes an exact, expiring question.answer permit for program.
func delegate(t *testing.T, options runtimeFlags, program, scope string, ttl time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	operator, err := client.NewVerified(options.endpoint, runtimeExpectation(t)).ResolveRightsOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := operator.ListPolicyContext(ctx, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	grant := rightsclient.PolicyRule{Subject: rightsclient.Subject{Account: probeSelf().Account, Program: program}, Action: asks.AnswerAction, Resource: scope, Permit: true}
	set, err := operator.SetRuleForContext(ctx, policy.Revision, grant, ttl, "delegated first-use answer under test")
	if err != nil || set.Outcome.String() != "applied" {
		t.Fatalf("delegation to %s %+v %v", program, set, err)
	}
}

func TestDelegatedFirstUseAnswerIsScopedAndReplayCannotRestoreRevokedRule(t *testing.T) {
	options := gatedRuntime(t)
	bin := probeBinary(t)
	responder := copyTestBinary(t, "oa-responder")
	if result, _ := probeAs(t, bin, options, "model", "resolve", "hf:org/model@not/a/revision"); result.Outcome != "forbidden" {
		t.Fatalf("first use %+v", result)
	}
	pending := pendingFirstUse(t, options)
	if len(pending) != 1 || pending[0].About != bin {
		t.Fatalf("pending %+v", pending)
	}
	if page := respondAs(t, responder, options, "list"); page.Outcome != "page" || len(page.Records) != 0 {
		t.Fatalf("ungranted page %+v", page)
	}
	if out := respondAs(t, responder, options, "answer", pending[0].ID, "allow"); out.Outcome != "forbidden" {
		t.Fatalf("ungranted answer %+v", out)
	}
	scope, err := asks.FirstUseAnswerResource(bin, host.ModelLookupAction, "hf", "allow")
	if err != nil {
		t.Fatal(err)
	}

	// The runtime asked this question; its own program never answers it,
	// whatever a delegation names (ASK-R3).
	delegate(t, options, probeSelf().Program, scope, 10*time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	machine := client.NewVerified(options.endpoint, runtimeExpectation(t))
	asker, err := machine.ResolveAsksResponder(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	if page, err := asker.ListAnswerableContext(ctx, "", 16); err != nil || len(page.Records) != 0 {
		t.Fatalf("the asking program was offered its own question %+v %v", page, err)
	}
	if out, err := asker.AnswerContext(ctx, pending[0].ID, "allow"); err != nil || out.Outcome.String() != "forbidden" {
		t.Fatalf("the asking program answered its own question %+v %v", out, err)
	}

	delegate(t, options, responder, scope, 10*time.Minute)
	page := respondAs(t, responder, options, "list")
	if page.Outcome != "page" || len(page.Records) != 1 || page.Records[0].ID != pending[0].ID || len(page.Records[0].Options) != 1 || page.Records[0].Options[0] != "allow" {
		t.Fatalf("scoped page %+v", page)
	}
	answer := respondAs(t, responder, options, "answer", pending[0].ID, "allow")
	if answer.Outcome != "answered" || answer.Effect != "applied" || answer.Target != "permitted" {
		t.Fatalf("answer %+v", answer)
	}
	operator, err := machine.ResolveRightsOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	target := rightsclient.Subject{Account: probeSelf().Account, Program: bin}
	read, err := operator.ReadRuleContext(ctx, target, host.ModelLookupAction, "hf")
	if err != nil || read.Outcome.String() != "found" || read.Record == nil || read.Record.Expires == "" {
		t.Fatalf("bounded target rule %+v %v", read, err)
	}
	expires, err := time.Parse(time.RFC3339Nano, read.Record.Expires)
	if err != nil || time.Until(expires) > 5*time.Minute || time.Until(expires) <= 0 {
		t.Fatalf("target expiry %q %v", read.Record.Expires, err)
	}
	// Undo uses the exact question identity even for an exact delegation.
	revoke, err := operator.RevokeAnswerPermitsContext(ctx, read.Revision,
		rightsclient.Subject{Account: probeSelf().Account, Program: responder}, target,
		[]string{host.ModelLookupAction}, "hf", pending[0].ID)
	if err != nil || revoke.Outcome.String() != "applied" {
		t.Fatalf("revoke %+v %v", revoke, err)
	}
	replay := respondAs(t, responder, options, "answer", pending[0].ID, "allow")
	if replay.Outcome != "answered" || replay.Effect != "conflict" || replay.Target != "not_granted" {
		t.Fatalf("replay after revoke %+v", replay)
	}
	read, err = operator.ReadRuleContext(ctx, target, host.ModelLookupAction, "hf")
	if err != nil || read.Outcome.String() != "unknown" {
		t.Fatalf("replay restored revoked target %+v %v", read, err)
	}
	refuseScope, err := asks.FirstUseAnswerResource(bin, host.ModelLookupAction, "hf", "refuse")
	if err != nil {
		t.Fatal(err)
	}
	delegate(t, options, responder, refuseScope, 10*time.Minute)
	if conflict := respondAs(t, responder, options, "answer", answer.Record, "refuse"); conflict.Outcome != "conflict" {
		t.Fatalf("different delegated option %+v", conflict)
	}
	if result, _ := probeAs(t, bin, options, "model", "resolve", "ollama:library/model@not/a/revision"); result.Outcome != "forbidden" {
		t.Fatalf("second first use %+v", result)
	}
	pending = pendingFirstUse(t, options)
	if len(pending) != 1 {
		t.Fatalf("second pending %+v", pending)
	}
	if out := respondAs(t, responder, options, "answer", pending[0].ID, "allow"); out.Outcome != "forbidden" {
		t.Fatalf("out-of-scope target %+v", out)
	}
	expiringScope, err := asks.FirstUseAnswerResource(bin, host.ModelLookupAction, "ollama", "allow")
	if err != nil {
		t.Fatal(err)
	}
	delegate(t, options, responder, expiringScope, time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	if out := respondAs(t, responder, options, "answer", pending[0].ID, "allow"); out.Outcome != "forbidden" {
		t.Fatalf("expired delegation %+v", out)
	}
	read, err = operator.ReadRuleContext(ctx, target, host.ModelLookupAction, "ollama")
	if err != nil || read.Outcome.String() != "unknown" {
		t.Fatalf("expired delegation wrote target %+v %v", read, err)
	}
}

// A hard link to the target's executable is the target under another path:
// delegated to answer the target's question, it reads forbidden and is never
// offered it (ASK-R3).
func TestAHardLinkOfTheTargetCannotAnswerItsQuestion(t *testing.T) {
	options := gatedRuntime(t)
	target := copyTestBinary(t, "oa-linked-target")
	link := filepath.Join(filepath.Dir(target), "oa-linked-helper"+filepath.Ext(target))
	if err := os.Link(target, link); err != nil {
		t.Skipf("hard links unavailable here: %v", err)
	}
	var result probeResult
	if out := runHelper(t, target, options, "probe", "model", "resolve", "hf:org/model@not/a/revision"); json.Unmarshal(out, &result) != nil || result.Outcome != "forbidden" {
		t.Fatalf("first use as the target printed %q", out)
	}
	pending := pendingFirstUse(t, options)
	if len(pending) != 1 || !samePrograms(pending[0].About, target) {
		t.Fatalf("pending %+v, wanted a question about %s", pending, target)
	}
	scope, err := asks.FirstUseAnswerResource(pending[0].About, host.ModelLookupAction, "hf", "allow")
	if err != nil {
		t.Fatal(err)
	}
	delegate(t, options, link, scope, 10*time.Minute)
	if page := respondAs(t, link, options, "list"); page.Outcome != "page" || listsQuestion(page, pending[0].ID) {
		t.Fatalf("a hard link of the target was offered its question %+v", page)
	}
	if out := respondAs(t, link, options, "answer", pending[0].ID, "allow"); out.Outcome != "forbidden" || out.Effect == "applied" {
		t.Fatalf("a hard link of the target answered its question %+v", out)
	}
}

// A program delegated to answer the first-use question about itself reads
// forbidden and is never offered it (ASK-R3); its target rule stays unwritten.
func TestAProgramCannotAnswerItsOwnFirstUseQuestion(t *testing.T) {
	options := gatedRuntime(t)
	self := copyTestBinary(t, "oa-self-approver")
	var result probeResult
	if out := runHelper(t, self, options, "probe", "model", "resolve", "hf:org/model@not/a/revision"); json.Unmarshal(out, &result) != nil || result.Outcome != "forbidden" {
		t.Fatalf("first use as the helper printed %q", out)
	}
	pending := pendingFirstUse(t, options)
	if len(pending) != 1 || !samePrograms(pending[0].About, self) {
		t.Fatalf("pending %+v, wanted a question about %s", pending, self)
	}
	scope, err := asks.FirstUseAnswerResource(pending[0].About, host.ModelLookupAction, "hf", "allow")
	if err != nil {
		t.Fatal(err)
	}
	delegate(t, options, pending[0].About, scope, 10*time.Minute)
	if page := respondAs(t, self, options, "list"); page.Outcome != "page" || listsQuestion(page, pending[0].ID) {
		t.Fatalf("the target was offered its own question %+v", page)
	}
	if out := respondAs(t, self, options, "answer", pending[0].ID, "allow"); out.Outcome != "forbidden" || out.Effect == "applied" {
		t.Fatalf("the target answered its own question %+v", out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	operator, err := client.NewVerified(options.endpoint, runtimeExpectation(t)).ResolveRightsOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	target := rightsclient.Subject{Account: probeSelf().Account, Program: pending[0].About}
	if read, err := operator.ReadRuleContext(ctx, target, host.ModelLookupAction, "hf"); err != nil || read.Outcome.String() != "unknown" {
		t.Fatalf("self-approval wrote the target rule %+v %v", read, err)
	}
	if still := pendingFirstUse(t, options); len(still) != 1 || still[0].ID != pending[0].ID {
		t.Fatalf("the question did not stay pending %+v", still)
	}
}
