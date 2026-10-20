package main

import (
	"encoding/json"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
)

// commandCallApp is a ComfyUI-shaped application behind the native gateway
// fixture: one command per class the simple call shape serves, two contexts,
// and an answer that records every Invocation the gateway sends (FAC-M32).
type commandCallApp struct {
	f        *applicationV3GatewayFixture
	mu       sync.Mutex
	epoch    string
	stale    int
	received []receivedInvocation
}

type receivedInvocation struct {
	tool       string
	invocation appmodel.Invocation
}

const (
	commandCallListSchema   = `{"type":"object","properties":{"search":{"type":"string"},"source":{"type":"string","enum":["saved","template"]}},"additionalProperties":false}`
	commandCallRunSchema    = `{"type":"object","properties":{"workflow":{"type":"string","minLength":1},"inputs":{"type":"object","additionalProperties":{"type":"integer"}}},"required":["workflow"],"additionalProperties":false}`
	commandCallWidgetSchema = `{"type":"object","properties":{"node":{"type":"string"},"value":{}},"required":["node","value"],"additionalProperties":false}`
)

func newCommandCallApp(t *testing.T) *commandCallApp {
	t.Helper()
	f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, true)
	for _, class := range []string{"read", "edit", "commit", "propose"} {
		if err := permission.a.rights.policy.Set(f.subject, "abstraction.facade/command."+class, "app:fixture", true); err != nil {
			t.Fatal(err)
		}
	}
	f.manifest.Offers.Commands = []appmodel.DeclaredCommand{
		{Name: "workflow.list", Class: appmodel.CommandClassRead},
		{Name: "workflow.check", Class: appmodel.CommandClassPropose},
		{Name: "node.set_widget", Class: appmodel.CommandClassEdit},
		{Name: "workflow.run", Class: appmodel.CommandClassCommit},
	}
	spec := func(name string, class appmodel.CommandClass, schema string) appmodel.CommandSpec {
		return appmodel.CommandSpec{Name: name, Class: class, Title: name, InputSchema: schema, OutputSchema: `{}`, Targets: []string{}}
	}
	widget := spec("node.set_widget", appmodel.CommandClassEdit, commandCallWidgetSchema)
	widget.Proposable = true
	f.description = &appmodel.Description{Version: "appmodel@1",
		Kinds: []appmodel.Kind{{Name: "workflow", Schema: `{}`, Fields: []appmodel.KindField{}}, {Name: "node", Schema: `{}`, Fields: []appmodel.KindField{}}},
		Commands: []appmodel.CommandSpec{
			spec("workflow.list", appmodel.CommandClassRead, commandCallListSchema),
			spec("workflow.check", appmodel.CommandClassPropose, `{}`),
			widget,
			spec("workflow.run", appmodel.CommandClassCommit, commandCallRunSchema),
		},
		Extensions: []string{"proposal"},
		Bounds:     appmodel.ContextBounds{SnapshotBytes: 4096, ChangeBytes: 1024, RetainedRevisions: 16, PendingProposals: 1, CallsInFlight: 1}}
	a := &commandCallApp{f: f, epoch: "e1"}
	f.mu.Lock()
	f.answer = a.answer
	f.mu.Unlock()
	return a
}

func (a *commandCallApp) answer(tool string, arguments json.RawMessage) []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	if tool == "app_describe" {
		return nil
	}
	if tool == "app_contexts" {
		raw, _ := appmodel.ToJSON(&appmodel.ContextPage{Outcome: appmodel.ReadingOutcomePage, Contexts: []appmodel.ContextEntry{
			{Context: appmodel.ContextRef{Instance: a.f.instance, Context: "view", Epoch: a.epoch}, Title: "Current", Revision: "r1"},
			{Context: appmodel.ContextRef{Instance: a.f.instance, Context: "other", Epoch: "e2"}, Title: "Other", Revision: "r5"},
		}})
		return raw
	}
	invocation, err := appmodel.DecodeInvocation(arguments)
	if err != nil {
		raw, _ := appmodel.ToJSON(&appmodel.CommandResult{Outcome: appmodel.CommandOutcomeInvalid, Reason: "app:" + err.Error()})
		return raw
	}
	a.received = append(a.received, receivedInvocation{tool, *invocation})
	if invocation.Context.Epoch != a.epoch && invocation.Context.Context == "view" || a.stale > 0 {
		if a.stale > 0 {
			a.stale--
		}
		// A stale answer applied nothing; the context was reopened.
		a.epoch = "e3"
		raw, _ := appmodel.ToJSON(&appmodel.CommandResult{Outcome: appmodel.CommandOutcomeStale, Reason: "epoch"})
		return raw
	}
	if invocation.Mode == appmodel.InvokeModePropose {
		raw, _ := appmodel.ToJSON(&appmodel.CommandResult{Outcome: appmodel.CommandOutcomeProposed, Revision: invocation.ExpectedRevision, Proposal: "p1", Summary: "Would change it"})
		return raw
	}
	raw, _ := appmodel.ToJSON(&appmodel.CommandResult{Outcome: appmodel.CommandOutcomeApplied, Revision: "r2", Effects: `{}`})
	return raw
}

func (a *commandCallApp) sent() []receivedInvocation {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]receivedInvocation(nil), a.received...)
}

func (a *commandCallApp) call(t *testing.T, key string, call appmodel.GatewayCall) *appmodel.CommandResult {
	t.Helper()
	call.Application, call.Instance = "fixture", a.f.instance
	result, err := a.f.c.Call(key, call)
	if err != nil || result.Outcome != applications.OutcomeApplied || result.Answer == nil || result.Answer.Result == nil {
		t.Fatalf("%s: %+v %v", call.Tool, result, err)
	}
	return result.Answer.Result
}

var generatedKey = regexp.MustCompile(`^[0-9a-f]{32}$`)

// [FAC-M32] owner R23, 2026-11-10: an agent calls a command by passing only
// its input_schema object. The gateway reads the context, its epoch and
// revision from the application's own app_contexts and builds the Invocation,
// for a read, an edit and a commit command alike.
func TestApplicationV3GatewayCommandCallSimpleShape(t *testing.T) {
	a := newCommandCallApp(t)
	n := 300
	next := func() string { n++; return receiptKey(n, time.Now()) }

	got := a.call(t, next(), appmodel.GatewayCall{Tool: "workflow.list", Arguments: `{"search":"z image"}`})
	sent := a.sent()
	if got.Outcome != appmodel.CommandOutcomeApplied || len(sent) != 1 {
		t.Fatalf("workflow.list: %+v, sent %+v", got, sent)
	}
	inv := sent[0].invocation
	if inv.Context != (appmodel.ContextRef{Instance: a.f.instance, Context: "view", Epoch: "e1"}) || inv.Mode != appmodel.InvokeModeApply || inv.ExpectedRevision != "r1" || inv.Arguments != `{"search":"z image"}` {
		t.Fatalf("built invocation: %+v", inv)
	}
	if !generatedKey.MatchString(inv.IdempotencyKey) || got.IdempotencyKey != inv.IdempotencyKey {
		t.Fatalf("generated key %q not echoed: %q", inv.IdempotencyKey, got.IdempotencyKey)
	}

	got = a.call(t, next(), appmodel.GatewayCall{Tool: "node.set_widget", Arguments: `{"node":"5","value":30}`, IdempotencyKey: "edit-1"})
	sent = a.sent()
	inv = sent[len(sent)-1].invocation
	if got.Outcome != appmodel.CommandOutcomeApplied || got.IdempotencyKey != "edit-1" || inv.IdempotencyKey != "edit-1" || inv.ExpectedRevision != "r1" || inv.Mode != appmodel.InvokeModeApply {
		t.Fatalf("edit: %+v sent %+v", got, inv)
	}

	got = a.call(t, next(), appmodel.GatewayCall{Tool: "workflow.run", Arguments: `{"workflow":"w","inputs":{"seed":7}}`, Context: "other", ExpectedRevision: "r4"})
	sent = a.sent()
	inv = sent[len(sent)-1].invocation
	if got.Outcome != appmodel.CommandOutcomeApplied || inv.Context.Context != "other" || inv.Context.Epoch != "e2" || inv.ExpectedRevision != "r4" || !generatedKey.MatchString(got.IdempotencyKey) {
		t.Fatalf("commit in a named context: %+v sent %+v", got, inv)
	}

	got = a.call(t, next(), appmodel.GatewayCall{Tool: "workflow.run", Arguments: `{"workflow":"w"}`, Context: "gone"})
	if got.Outcome != appmodel.CommandOutcomeNotFound || got.Reason != "context:gone" {
		t.Fatalf("unknown context: %+v", got)
	}
	a.f.mu.Lock()
	records := append([]appmodel.CallRecord(nil), a.f.records...)
	a.f.mu.Unlock()
	last := records[2]
	if last.Command != "workflow.run" || last.Context == nil || last.Context.Epoch != "e2" || last.RevisionBefore != "r4" || last.IdempotencyKey == "" || last.Class == nil || *last.Class != appmodel.CommandClassCommit {
		t.Fatalf("audit of the built call: %+v", last)
	}
}

// [FAC-M32] The gateway checks arguments against input_schema before it sends
// the call, and a refusal names the field.
func TestApplicationV3GatewayCommandCallNamesTheField(t *testing.T) {
	a := newCommandCallApp(t)
	for i, c := range []struct{ tool, arguments, reason string }{
		{"workflow.run", `{}`, "field:workflow:missing"},
		{"workflow.list", `{"serch":"z image"}`, "field:serch:unknown"},
		{"workflow.run", `{"workflow":5}`, "field:workflow:type"},
		{"workflow.list", `{"source":"web"}`, "field:source:bound"},
		{"workflow.run", `{"workflow":""}`, "field:workflow:bound"},
		{"workflow.run", `{"workflow":"w","inputs":{"seed":"x"}}`, "field:inputs.seed:type"},
		{"workflow.list", `[]`, "field:$:type"},
	} {
		got := a.call(t, receiptKey(400+i, time.Now()), appmodel.GatewayCall{Tool: c.tool, Arguments: appmodel.Raw(c.arguments), IdempotencyKey: "k"})
		if got.Outcome != appmodel.CommandOutcomeInvalid || got.Reason != c.reason || got.IdempotencyKey != "k" {
			t.Errorf("%s %s: %+v, want invalid %s", c.tool, c.arguments, got, c.reason)
		}
	}
	if sent := a.sent(); len(sent) != 0 {
		t.Fatalf("refused calls reached the application: %+v", sent)
	}
}

// [FAC-M32] A stale answer applied nothing: the gateway reads the context's
// new epoch once and resends with the same key; a second stale answer stands.
func TestApplicationV3GatewayCommandCallRefreshesStaleOnce(t *testing.T) {
	a := newCommandCallApp(t)
	a.mu.Lock()
	a.stale = 1
	a.mu.Unlock()
	got := a.call(t, receiptKey(500, time.Now()), appmodel.GatewayCall{Tool: "workflow.list", Arguments: `{}`})
	sent := a.sent()
	if got.Outcome != appmodel.CommandOutcomeApplied || len(sent) != 2 || sent[0].invocation.Context.Epoch != "e1" || sent[1].invocation.Context.Epoch != "e3" || sent[0].invocation.IdempotencyKey != sent[1].invocation.IdempotencyKey || got.IdempotencyKey != sent[0].invocation.IdempotencyKey {
		t.Fatalf("stale refresh: %+v sent %+v", got, sent)
	}
	a.mu.Lock()
	a.stale = 2
	a.mu.Unlock()
	got = a.call(t, receiptKey(501, time.Now()), appmodel.GatewayCall{Tool: "workflow.list", Arguments: `{}`, IdempotencyKey: "list-2"})
	if sent = a.sent(); got.Outcome != appmodel.CommandOutcomeStale || len(sent) != 4 || got.IdempotencyKey != "list-2" {
		t.Fatalf("second stale answer: %+v sent %d", got, len(sent))
	}
}

// [FAC-M32] A repeated call under the same outer key replays its answer,
// including the key the gateway generated, and sends nothing again.
func TestApplicationV3GatewayCommandCallKeysReplay(t *testing.T) {
	a := newCommandCallApp(t)
	outer := receiptKey(600, time.Now())
	first := a.call(t, outer, appmodel.GatewayCall{Tool: "node.set_widget", Arguments: `{"node":"5","value":1}`, IdempotencyKey: "edit-key"})
	again := a.call(t, outer, appmodel.GatewayCall{Tool: "node.set_widget", Arguments: `{"node":"5","value":1}`, IdempotencyKey: "edit-key"})
	if first.Outcome != appmodel.CommandOutcomeApplied || again.IdempotencyKey != "edit-key" || len(a.sent()) != 1 {
		t.Fatalf("supplied key replay: %+v %+v sent %d", first, again, len(a.sent()))
	}
	outer = receiptKey(601, time.Now())
	first = a.call(t, outer, appmodel.GatewayCall{Tool: "workflow.run", Arguments: `{"workflow":"w"}`})
	again = a.call(t, outer, appmodel.GatewayCall{Tool: "workflow.run", Arguments: `{"workflow":"w"}`})
	if !generatedKey.MatchString(first.IdempotencyKey) || again.IdempotencyKey != first.IdempotencyKey || len(a.sent()) != 2 {
		t.Fatalf("generated key replay: %q %q sent %d", first.IdempotencyKey, again.IdempotencyKey, len(a.sent()))
	}
	// Two identical calls under different outer keys are two calls.
	other := a.call(t, receiptKey(602, time.Now()), appmodel.GatewayCall{Tool: "workflow.run", Arguments: `{"workflow":"w"}`})
	if other.IdempotencyKey == first.IdempotencyKey || len(a.sent()) != 3 {
		t.Fatalf("identical arguments shared a key: %q", other.IdempotencyKey)
	}
}

// [FAC-M32] mode follows the command's class: a propose command evaluates in
// mode propose, an apply-class command in apply unless the caller asks to
// propose, and the call is decided as command.propose.
func TestApplicationV3GatewayCommandCallProposeFollowsTheClass(t *testing.T) {
	a := newCommandCallApp(t)
	apply := appmodel.InvokeModeApply
	got := a.call(t, receiptKey(700, time.Now()), appmodel.GatewayCall{Tool: "workflow.check", Arguments: `{}`, Mode: &apply})
	sent := a.sent()
	if got.Outcome != appmodel.CommandOutcomeProposed || sent[0].invocation.Mode != appmodel.InvokeModePropose {
		t.Fatalf("propose class: %+v sent %+v", got, sent)
	}
	propose := appmodel.InvokeModePropose
	got = a.call(t, receiptKey(701, time.Now()), appmodel.GatewayCall{Tool: "node.set_widget", Arguments: `{"node":"5","value":2}`, Mode: &propose})
	sent = a.sent()
	if got.Outcome != appmodel.CommandOutcomeProposed || sent[1].invocation.Mode != appmodel.InvokeModePropose {
		t.Fatalf("asked propose: %+v sent %+v", got, sent)
	}
	got = a.call(t, receiptKey(702, time.Now()), appmodel.GatewayCall{Tool: "node.set_widget", Arguments: `{}`, Proposal: "p1", IdempotencyKey: "apply-p1"})
	sent = a.sent()
	if got.Outcome != appmodel.CommandOutcomeApplied || sent[2].invocation.Proposal != "p1" || sent[2].invocation.Mode != appmodel.InvokeModeApply {
		t.Fatalf("apply a proposal: %+v sent %+v", got, sent)
	}
	a.f.mu.Lock()
	records := append([]appmodel.CallRecord(nil), a.f.records...)
	a.f.mu.Unlock()
	for i, want := range []appmodel.CommandClass{appmodel.CommandClassPropose, appmodel.CommandClassPropose, appmodel.CommandClassEdit} {
		if records[i].Class == nil || *records[i].Class != want {
			t.Fatalf("record %d decided as %v, want %s", i, records[i].Class, want)
		}
	}
}
