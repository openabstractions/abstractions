//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	asks "github.com/openabstractions/abstraction-asks/go"
	askwire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/grants"
	facaderuntime "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// The source fixture uses the real admitted gateway and native responder.
// Its person decisions are isolated Book/operator fixture actions.
func TestMessagesAsksNativeMCPPersonEnrollmentAndRoutineAnswer(t *testing.T) {
	run, _ := messages1608Fixture(t)
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "oa-codex-integration.exe")
	build := exec.Command("go", "-C", filepath.Join(root, "mcp-gateway"), "test", "-c", "-o", helper, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("gateway fixture build: %v\n%s", err, out)
	}
	descriptor := wire.ApplicationDescriptor{Name: "oa-codex-integration", Title: "Native Asks fixture", Program: identity.NormalizeSubjectProgram(helper)}
	if got := run.d.register(run.ctx, run.f.panel, descriptor); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	agent1478Set(t, run, descriptor.Name, "", true)
	keep := agent1478Question(t, run, descriptor.Name)
	answer(t, run.f, keep.ID, "keep")
	agent1478Wait(t, run, descriptor.Name, "enrolled")
	a, policy := run.f.a, run.f.a.rights
	policy.applications = run.d
	run.d.roleDecode = func([]byte, string) (bool, error) { return false, nil }
	if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	if _, err := policy.policy.PrepareMetaHolderContext(run.ctx); err != nil {
		t.Fatal("native fixture eligibility preparation", err)
	}
	appEndpoint := fullEndpoint(t, "asks1876-applications")
	apps, err := listenApplications(appEndpoint, run.d, policy.owner, func(err error) { t.Log(err) })
	if err != nil {
		t.Fatal(err)
	}
	apps.messages, err = openMessageStore(run.state, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	apps.messageRights = policy
	a.endpoint = fullEndpoint(t, "asks1876-questions")
	opts := facaderuntime.Options{Endpoint: fullEndpoint(t, "asks1876-runtime"), LogEndpoint: fullEndpoint(t, "asks1876-log"), ConfigEndpoint: fullEndpoint(t, "asks1876-config"), ConfigWithoutMachine: true, Sink: remoteTestSink(func(logging.Record) error { return nil }), Applications: apps, ApplicationsEndpoint: appEndpoint}
	a.configure(&opts)
	opts.RightsPolicy, opts.RightsEndpoint = policy.policy, fullEndpoint(t, "asks1876-rights")
	host, err := facaderuntime.Listen(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(run.ctx, 40*time.Second)
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		host.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	config, err := json.Marshal(struct {
		Endpoint, State, Application, Context string
		Server                                listen.ServerExpectation
	}{opts.Endpoint, t.TempDir(), descriptor.Name, "asks-fixture", runtimeExpectation(t)})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, helper, "-test.run=^TestAgentStartGatewayFixture$", "-test.count=1")
	cmd.Env = append(os.Environ(), "OA_RECEIVER_START_GATEWAY_FIXTURE="+string(config))
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	session, err := mcp.NewClient(&mcp.Implementation{Name: "untrusted-vendor-label", Version: "fixture"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal("native gateway connection", err, diagnostics.String())
	}
	defer session.Close()
	inventory, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range inventory.Tools {
		names[tool.Name] = true
	}
	for _, name := range []string{"oa_asks_request_answering", "oa_asks_list_answerable", "oa_asks_list_choices", "oa_asks_answer", "oa_asks_answer_choice"} {
		if !names[name] {
			t.Fatal("missing native Asks tool", name)
		}
	}
	call := func(name string, args any) []byte {
		t.Helper()
		out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || out.IsError || len(out.Content) != 1 {
			t.Fatal(name, out, err)
		}
		text, ok := out.Content[0].(*mcp.TextContent)
		if !ok || text.Meta["org.openabstractions/untrusted"] != true {
			t.Fatal("Asks trust metadata", out)
		}
		if strings.HasPrefix(text.Text, "{") {
			return []byte(text.Text)
		}
		_, raw, ok := strings.Cut(text.Text, "\n")
		if !ok {
			t.Fatal("Asks result boundary", text.Text)
		}
		return []byte(raw)
	}
	// ASK-R14: the chat's own Messages watch carries the Asks wake key.
	asksWake := func() string {
		t.Helper()
		out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "oa_messages_watch", Arguments: map[string]any{"wait_ms": 0}})
		if err != nil || out.IsError || len(out.Content) != 1 {
			t.Fatal("oa_messages_watch", out, err)
		}
		var page struct {
			Outcome  string `json:"outcome"`
			AsksWake string `json:"asks_wake"`
		}
		if err := json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &page); err != nil || page.Outcome != "page" {
			t.Fatal("Messages watch page", out.Content[0], err)
		}
		return page.AsksWake
	}
	decision, err := rights.DecodeDecision(call("oa_asks_request_answering", map[string]any{}))
	if err != nil || decision.Outcome != rights.DecisionOutcomeNotGranted {
		t.Fatal(decision, err)
	}
	// Registration and Keep have granted no answering rights. Only this fixture
	// person answers the native enrollment occurrence and commits its effect.
	var enrollment asks.Record
	entries, _, err := a.book.ApplicationHistoryScoped()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		permission, ok := asks.PermissionForRecord(policy.owner, entry.Record)
		if !ok || len(permission.Targets) != 1 {
			continue
		}
		target := permission.Targets[0]
		if target.Action == asks.AnswerAction && samePrograms(target.Program, helper) && entry.Record.Pending() {
			enrollment = entry.Record
		}
	}
	if enrollment.ID == "" {
		t.Fatal("native person enrollment missing")
	}
	if got := asksWake(); got != "" {
		t.Fatal("an unenrolled program's chat received an Asks wake", got)
	}
	protectedAnswer, err := askwire.DecodeResponderDecision(call("oa_asks_answer", map[string]any{"id": enrollment.ID, "option": "allow"}))
	if err != nil || protectedAnswer.Outcome != askwire.OperatorDecisionOutcomeForbidden {
		t.Fatal("agent answered own person permission", protectedAnswer, err)
	}
	if _, outcome, err := a.book.AnswerApplication(enrollment.ID, "allow"); err != nil || outcome != "answered" {
		t.Fatal(outcome, err)
	}
	snapshot, err := policy.policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := a.applyOperatorAllow(ctx, run.f.panel, snapshot.Revision, enrollment.ID, func() error { return nil }); err != nil || out.Outcome != rights.PolicyEditOutcomeApplied {
		t.Fatal(out, err)
	}
	before := asksWake()
	routine := run.f.ask(t, run.f.comfy, grants.ModelLookupAction, "hf")
	woken := asksWake()
	if woken == "" || woken <= before || strings.Contains(woken, routine.ID) {
		t.Fatal("a routine question gave no content-free Asks wake", before, woken)
	}
	if again := asksWake(); again != woken {
		t.Fatal("the same question moved the Asks wake", woken, again)
	}
	var beforeHidden gateway.AsksWatchResult
	if err := json.Unmarshal(call("oa_asks_watch", map[string]any{"wait_ms": 0}), &beforeHidden); err != nil || beforeHidden.Outcome != "page" || len(beforeHidden.Items) != 1 {
		t.Fatal("eligible Asks watch", beforeHidden, err)
	}
	protected := run.f.ask(t, run.f.lemonade, grants.ModelLookupAction, "unknown-registry")
	if got := asksWake(); got != woken {
		t.Fatal("a person-only question woke the answerer's chat", woken, got)
	}
	var afterHidden gateway.AsksWatchResult
	if err := json.Unmarshal(call("oa_asks_watch", map[string]any{"wait_ms": 0, "revision": beforeHidden.Revision}), &afterHidden); err != nil || afterHidden.Revision != beforeHidden.Revision {
		t.Fatal("protected Asks churn leaked", beforeHidden, afterHidden, err)
	}
	page, err := askwire.DecodeOperatorPage(call("oa_asks_list_answerable", map[string]any{"limit": 64}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range page.Records {
		found = found || record.ID == routine.ID
		if record.ID == protected.ID || record.ID == enrollment.ID {
			t.Fatal("protected question disclosed", record.ID)
		}
	}
	if !found {
		t.Fatal("routine question absent", page)
	}
	refused, err := askwire.DecodeResponderDecision(call("oa_asks_answer", map[string]any{"id": protected.ID, "option": "allow"}))
	if err != nil || refused.Outcome != askwire.OperatorDecisionOutcomeForbidden {
		t.Fatal("protected permission answered", refused, err)
	}
	answered, err := askwire.DecodeResponderDecision(call("oa_asks_answer", map[string]any{"id": routine.ID, "option": "allow"}))
	if err != nil || answered.Outcome != askwire.OperatorDecisionOutcomeAnswered || answered.Effect != askwire.ResponderEffectOutcomeApplied {
		t.Fatal(answered, err)
	}
	if got := asksWake(); got != "" {
		t.Fatal("an answered question kept the Asks wake", got)
	}
	observed, found, err := a.book.ApplicationRecord(routine.ID)
	if err != nil || !found || observed.Pending() || observed.AnsweredBy != asks.AnsweredByResponder || !samePrograms(observed.Answerer, helper) {
		t.Fatal("origin did not observe attributed answer", observed, err)
	}
	if got := policy.policy.Decide(run.f.comfy, grants.ModelLookupAction, "hf"); got.Outcome != rights.DecisionOutcomePermitted {
		t.Fatal("origin effect not committed", got)
	}
	snapshot, err = policy.policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := policy.policy.StopAnswerer(snapshot.Revision, rights.Subject{Account: policy.owner, Program: descriptor.Program}, true, func() error { return nil }); err != nil || out.Outcome != rights.PolicyEditOutcomeApplied {
		t.Fatal(out, err)
	}
	after, err := askwire.DecodeOperatorPage(call("oa_asks_list_answerable", map[string]any{"limit": 64}))
	if err != nil || len(after.Records) != 0 {
		t.Fatal("revoked responder retained visible asks", after, err)
	}
	if got := policy.policy.Decide(run.f.comfy, grants.ModelLookupAction, "hf"); got.Outcome != rights.DecisionOutcomeNotGranted {
		t.Fatal("Stop retained responder effect", got)
	}
}
