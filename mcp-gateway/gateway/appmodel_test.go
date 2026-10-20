package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
)

type appModelLargeFixture struct {
	*appModelFixture
	result *appmodel.CommandResult
}

func (b *appModelLargeFixture) ApplicationsCall(context.Context, appmodel.GatewayCall) (applications.CallResult, error) {
	return applications.CallResult{Outcome: applications.OutcomeApplied, Answer: &appmodel.Envelope{Result: b.result}}, nil
}

type appModelWireCapture struct {
	io.WriteCloser
	mu   sync.Mutex
	body bytes.Buffer
}

func (w *appModelWireCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	_, _ = w.body.Write(p)
	w.mu.Unlock()
	return w.WriteCloser.Write(p)
}

func TestAppModelMaximumRecordIOTransport(t *testing.T) {
	result := &appmodel.CommandResult{Outcome: appmodel.CommandOutcomeApplied, Effects: `{"value":""}`}
	raw, err := appmodel.ToJSON(result)
	if err != nil {
		t.Fatal(err)
	}
	result.Effects = `{"value":"` + strings.Repeat("<", 262144-len(raw)) + `"}`
	raw, err = appmodel.ToJSON(result)
	if err != nil || len(raw) != 262144 {
		t.Fatalf("record bytes=%d: %v", len(raw), err)
	}
	backend := &appModelLargeFixture{appModelFixture: &appModelFixture{fixtureBackend: &fixtureBackend{}}, result: result}
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()
	capture := &appModelWireCapture{WriteCloser: serverWrite}
	t.Cleanup(func() { serverRead.Close(); clientWrite.Close(); clientRead.Close(); serverWrite.Close() })
	ss, err := NewServer(backend).Connect(t.Context(), &mcp.IOTransport{Reader: serverRead, Writer: capture}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "max-record", Version: "1"}, nil).Connect(t.Context(), &mcp.IOTransport{Reader: clientRead, Writer: clientWrite}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for i := 0; i < 2; i++ {
		got, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolApplicationsCall, Arguments: map[string]any{"application": "fixture", "instance": "live", "tool": "node.run", "arguments": map[string]any{}}})
		if err != nil || got.IsError {
			t.Fatalf("call%d: %+v %v", i, got, err)
		}
		if got.Content[0].(*mcp.TextContent).Text != string(raw) {
			t.Fatal("canonical text bytes changed")
		}
		encoded, err := json.Marshal(got.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var want, observed any
		if json.Unmarshal(raw, &want) != nil || json.Unmarshal(encoded, &observed) != nil || !reflect.DeepEqual(want, observed) {
			t.Fatal("structured view differs")
		}
	}
	capture.mu.Lock()
	lines := bytes.Split(capture.body.Bytes(), []byte{'\n'})
	maximum := 0
	for _, line := range lines {
		if len(line) > maximum {
			maximum = len(line)
		}
	}
	capture.mu.Unlock()
	t.Logf("canonical record=%d SDK stdout maximum line=%d", len(raw), maximum)
	if maximum < 3_000_000 {
		t.Fatalf("large HTML-escaped line not exercised: %d", maximum)
	}
}

type appModelFixture struct {
	*fixtureBackend
	calls   []appmodel.GatewayCall
	opens   []applications.ActivateRequest
	refusal applications.Outcome
}

func (b *appModelFixture) ApplicationsOpen(_ context.Context, request applications.ActivateRequest) (applications.ActivationResult, error) {
	b.opens = append(b.opens, request)
	switch request.Application {
	case "fixture":
		if request.Verb != "open" {
			return applications.ActivationResult{Outcome: applications.ActivationOutcomeInvalid, Reason: "verb"}, nil
		}
	case "required-payload":
		if request.Payload == "{}" {
			return applications.ActivationResult{Outcome: applications.ActivationOutcomeInvalid, Reason: "payload"}, nil
		}
	case "declined":
		return applications.ActivationResult{Outcome: applications.ActivationOutcomeForbidden, Reason: "consent", Title: "Declined App"}, nil
	case "blocked":
		return applications.ActivationResult{Outcome: applications.ActivationOutcomeForbidden, Reason: "blocked", Title: "Blocked App"}, nil
	case "failed":
		return applications.ActivationResult{Outcome: applications.ActivationOutcomeIdentityRefused, Reason: "owned_host", Title: "Failed App"}, nil
	case "lost":
		return applications.ActivationResult{Outcome: applications.ActivationOutcomeUnknown, Reason: "owned_host", Title: "Lost App"}, nil
	case "asking":
		return applications.ActivationResult{Outcome: applications.ActivationOutcomeNotReady, Reason: "consent"}, nil
	default:
		return applications.ActivationResult{Outcome: applications.ActivationOutcomeForbidden, Reason: "rights"}, nil
	}
	return applications.ActivationResult{Outcome: applications.ActivationOutcomeReady, Instance: "live", Started: len(b.opens) == 1}, nil
}

func assertAppModelUntrusted(t *testing.T, result *mcp.CallToolResult) {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("AppModel content count: %d", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Meta["org.openabstractions/untrusted"] != true || text.Annotations == nil || !slices.Equal(text.Annotations.Audience, []mcp.Role{"assistant"}) {
		t.Fatalf("AppModel content lacks untrusted assistant marks: %+v", result.Content[0])
	}
}

func TestAppModelResultPreservesMarkedRawJSON(t *testing.T) {
	raw := []byte(" {\n\"outcome\":\"applied\", \"effects\": {\"n\":9007199254740993,\"exponent\":1e2} }\n")
	result, err := appModelResult(raw, false)
	if err != nil || result.IsError {
		t.Fatalf("result: %+v %v", result, err)
	}
	assertAppModelUntrusted(t, result)
	if result.Content[0].(*mcp.TextContent).Text != string(raw) {
		t.Fatal("marked result changed original JSON bytes")
	}
	effects := result.StructuredContent.(map[string]any)["effects"].(map[string]any)
	if effects["n"] != json.Number("9007199254740993") || effects["exponent"] != json.Number("1e2") {
		t.Fatalf("structured number forms changed: %+v", effects)
	}
	if _, err := appModelResult(bytes.Repeat([]byte(" "), (1<<20)+1), false); err == nil {
		t.Fatal("oversized answer admitted")
	}
	if _, err := appModelResult([]byte("{"), false); err == nil {
		t.Fatal("malformed answer admitted")
	}
}

func (b *appModelFixture) ApplicationsDescribe(context.Context, appmodel.ApplicationsQuery) (applications.DescribeResult, error) {
	return applications.DescribeResult{Outcome: applications.OutcomePage, Answer: &appmodel.ApplicationListing{Outcome: appmodel.ReadingOutcomePage, Applications: []appmodel.ListedApplication{}}}, nil
}
func (b *appModelFixture) ApplicationsCommands(context.Context, appmodel.CommandsQuery) (applications.CommandsResult, error) {
	return applications.CommandsResult{Outcome: applications.OutcomePage, Answer: &appmodel.CommandListing{Outcome: appmodel.ReadingOutcomePage, Application: "fixture", Instance: "live", Commands: []appmodel.CommandSpec{}}}, nil
}
func (b *appModelFixture) ApplicationsCall(_ context.Context, call appmodel.GatewayCall) (applications.CallResult, error) {
	b.calls = append(b.calls, call)
	if b.refusal != 0 {
		return applications.CallResult{Outcome: b.refusal, Reason: "native-reason"}, nil
	}
	return applications.CallResult{Outcome: applications.OutcomeApplied, Answer: &appmodel.Envelope{Result: &appmodel.CommandResult{Outcome: appmodel.CommandOutcomeNotPermitted, Reason: "rights", Effects: `{ "n":9007199254740993, "exponent": 1e2 }`}}}, nil
}

func TestAppModelCatalogueAndRawRecords(t *testing.T) {
	b := &appModelFixture{fixtureBackend: &fixtureBackend{}}
	_, session := connected(t, b, "untrusted-metadata", "2026-07-28")
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 13 {
		t.Fatalf("generic inventory has %d tools", len(tools.Tools))
	}
	names := []string{}
	for _, tool := range tools.Tools {
		if tool.Name == ToolApplicationsCall && (tool.Annotations.ReadOnlyHint || tool.Annotations.IdempotentHint || tool.Annotations.OpenWorldHint == nil || !*tool.Annotations.OpenWorldHint || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint) {
			t.Fatalf("routed command hints: %+v", tool.Annotations)
		}
		names = append(names, tool.Name)
	}
	for _, name := range appmodel.GatewayTools {
		if !slices.Contains(names, name) {
			t.Fatalf("missing %s from %v", name, names)
		}
	}
	for _, name := range []string{ToolApplicationsDescribe, ToolApplicationsCommands} {
		args := map[string]any{}
		if name == ToolApplicationsCommands {
			args = map[string]any{"application": "fixture", "instance": "live"}
		}
		out, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || out.IsError {
			t.Fatalf("%s: %+v %v", name, out, err)
		}
		assertAppModelUntrusted(t, out)
	}
	inner := `{"n":9007199254740993, "exponent":1e2}`
	raw := json.RawMessage(`{"application":"fixture","instance":"live","tool":"note.edit","arguments":` + inner + `,"context":"doc","expected_revision":"r","idempotency_key":"k"}`)
	out, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolApplicationsCall, Arguments: raw})
	if err != nil || !out.IsError {
		t.Fatalf("call: %+v %v", out, err)
	}
	assertAppModelUntrusted(t, out)
	// The SDK client compacts its outgoing RawMessage before transport. The
	// adapter preserves the bytes it actually receives, including number forms.
	var wireInner bytes.Buffer
	if err := json.Compact(&wireInner, []byte(inner)); err != nil {
		t.Fatal(err)
	}
	if len(b.calls) != 1 || b.calls[0].Arguments != wireInner.String() {
		t.Fatalf("raw arguments changed: %+v", b.calls)
	}
	text := out.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, `{ "n":9007199254740993, "exponent": 1e2 }`) {
		t.Fatalf("raw answer lost: %s", text)
	}
	if out.StructuredContent.(map[string]any)["outcome"] != "not_permitted" {
		t.Fatalf("nested refusal changed: %+v", out)
	}
	for _, native := range []applications.Outcome{applications.OutcomeForbidden, applications.OutcomeUnknown, applications.OutcomeConflict, applications.OutcomeUnavailable} {
		b.refusal = native
		out, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolApplicationsCall, Arguments: raw})
		if err != nil || !out.IsError || out.StructuredContent.(map[string]any)["outcome"] != native.String() {
			t.Fatalf("native refusal changed: %+v %v", out, err)
		}
		assertAppModelUntrusted(t, out)
	}
}

func TestAppModelOuterOperationIdentity(t *testing.T) {
	oa := &OA{stateDir: t.TempDir(), state: casapi.BoundedFileStore{MaxBytes: maxRequestRecordBytes}}
	call := appmodel.GatewayCall{Application: "fixture", Instance: "live", Tool: "note.edit", Arguments: `{"x":1}`, IdempotencyKey: "key", Context: "one", ExpectedRevision: "r1"}
	first, err := oa.appModelCallKey(t.Context(), call)
	if err != nil {
		t.Fatal(err)
	}
	call.Arguments = `{"x":2}`
	changed, _ := oa.appModelCallKey(t.Context(), call)
	if changed != first {
		t.Fatal("changed body escaped original native receipt")
	}
	call.Context = "two"
	other, _ := oa.appModelCallKey(t.Context(), call)
	if other == first {
		t.Fatal("separate context shared operation identity")
	}
	// A keyless call is a new call each time: identical arguments never share
	// a key (FAC-M32).
	call.IdempotencyKey = ""
	one, _ := oa.appModelCallKey(t.Context(), call)
	two, _ := oa.appModelCallKey(t.Context(), call)
	if one == two {
		t.Fatal("keyless observations reused a receipt")
	}
}

func TestAppModelMalformedOuterRecordsRemainTyped(t *testing.T) {
	b := &appModelFixture{fixtureBackend: &fixtureBackend{}}
	_, session := connected(t, b, "arbitrary-client", "2026-07-28")
	for _, name := range []string{ToolApplicationsDescribe, ToolApplicationsCommands, ToolApplicationsCall} {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: json.RawMessage(`{"unknown":true}`)})
		if err != nil || !result.IsError {
			t.Fatalf("%s: %+v %v", name, result, err)
		}
		assertAppModelUntrusted(t, result)
		out := result.StructuredContent.(map[string]any)
		if out["outcome"] != "invalid" || out["reason"] != "unknown_field" {
			t.Fatalf("%s codec refusal changed: %+v", name, out)
		}
		text := []byte(result.Content[0].(*mcp.TextContent).Text)
		switch name {
		case ToolApplicationsDescribe:
			_, err = appmodel.DecodeApplicationListing(text)
		case ToolApplicationsCommands:
			_, err = appmodel.DecodeCommandListing(text)
		default:
			_, err = appmodel.DecodeCommandResult(text)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(b.calls) != 0 {
		t.Fatal("malformed outer call reached native backend")
	}
}

func TestAppModelAssociationRestartCapacityAndCorruption(t *testing.T) {
	oa := &OA{stateDir: t.TempDir()}
	call := appmodel.GatewayCall{Application: "fixture", Instance: "live", Tool: "note.edit", Arguments: `{}`, IdempotencyKey: "key", ExpectedRevision: "r1"}
	key, err := oa.appModelCallKey(t.Context(), call)
	if err != nil {
		t.Fatal(err)
	}
	restarted := &OA{stateDir: oa.stateDir}
	again, err := restarted.appModelCallKey(t.Context(), call)
	if err != nil || again != key {
		t.Fatalf("restart reminted %q %q: %v", key, again, err)
	}
	operation, err := appModelOperation(call)
	if err != nil {
		t.Fatal(err)
	}
	// Retain an expired native key rather than mint a fresh operation. Native
	// receipt admission owns its expiry and returns unknown without dispatch.
	old := "a3.1.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	record := appModelRequestRecord{Version: 1, Keys: map[string]string{operation: old}}
	for i := 0; len(record.Keys) < maxAppModelAssociations; i++ {
		record.Keys[fmt.Sprintf("%064x", i)] = key
	}
	path := filepath.Join(oa.stateDir, "appmodel-requests.json")
	raw, _ := json.Marshal(record)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	again, err = restarted.appModelCallKey(t.Context(), call)
	if err != nil || again != old {
		t.Fatalf("old operation reminted at capacity: %q %v", again, err)
	}
	call.IdempotencyKey = "new-key"
	if _, err := restarted.appModelCallKey(t.Context(), call); err == nil {
		t.Fatal("full association store admitted new operation")
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"keys":null}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.appModelCallKey(t.Context(), call); err == nil {
		t.Fatal("uncertain association store admitted new operation")
	}
}

// [FAC-M32] oa_applications_open hands the declared verb and its payload to the
// native activation owner and answers its ActivationResult with the instance.
func TestAppModelOpenToolSchemaAndAnswer(t *testing.T) {
	b := &appModelFixture{fixtureBackend: &fixtureBackend{}}
	_, session := connected(t, b, "untrusted-metadata", "2026-07-28")
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var open *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == ToolApplicationsOpen {
			open = tool
		}
	}
	if open == nil || open.Annotations == nil || open.Annotations.DestructiveHint != nil && *open.Annotations.DestructiveHint {
		t.Fatalf("open tool: %+v", open)
	}
	schema, _ := json.Marshal(open.InputSchema)
	var shape struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(schema, &shape); err != nil || len(shape.Required) != 0 || shape.Properties["application"]["type"] != "string" || shape.Properties["handle"]["type"] != "string" || shape.Properties["wait_ms"]["type"] != "integer" || shape.Properties["payload"]["type"] != "object" || shape.Properties["context"]["type"] != "string" {
		t.Fatalf("open schema: %s", schema)
	}
	call := func(args string) map[string]any {
		t.Helper()
		out, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolApplicationsOpen, Arguments: json.RawMessage(args)})
		if err != nil {
			t.Fatal(err)
		}
		// The native answer stays marked untrusted; the gateway's own
		// sentence for the person follows it.
		if len(out.Content) != 2 {
			t.Fatalf("open content count: %d", len(out.Content))
		}
		sentence, ok := out.Content[1].(*mcp.TextContent)
		if !ok || sentence.Meta["org.openabstractions/untrusted"] != nil || sentence.Text == "" {
			t.Fatalf("open sentence: %+v", out.Content[1])
		}
		assertAppModelUntrusted(t, &mcp.CallToolResult{Content: out.Content[:1]})
		record := out.StructuredContent.(map[string]any)
		record["is_error"] = out.IsError
		record["sentence"] = sentence.Text
		return record
	}
	first := call(`{"application":"fixture","verb":"open","payload":{}}`)
	if first["outcome"] != "ready" || first["instance"] != "live" || first["started"] != true || first["is_error"] != false || first["sentence"] != "Opened fixture. Use instance live with oa_applications_commands_list." {
		t.Fatalf("first open: %+v", first)
	}
	again := call(`{"application":"fixture","verb":"open","payload":{}}`)
	if again["instance"] != "live" || again["started"] != false {
		t.Fatalf("reopen: %+v", again)
	}
	if len(b.opens) != 2 || b.opens[0].Verb != "open" || string(b.opens[0].Payload) != "{}" {
		t.Fatalf("native requests: %+v", b.opens)
	}
	// No sentence sends the person to a manual step in the Panel: the agent
	// opens apps itself and talks to the person in its own chat.
	if refused := call(`{"application":"other","verb":"open","payload":{}}`); refused["outcome"] != "forbidden" || refused["is_error"] != true || refused["sentence"] != "You have no permission to open other. Tell the person in this chat." {
		t.Fatalf("refusal: %+v", refused)
	}
	// Not now is relayed as the person's answer, with the app's display name;
	// the agent asks in its own chat before it calls open again.
	if declined := call(`{"application":"declined","verb":"open","payload":{}}`); declined["outcome"] != "forbidden" || declined["sentence"] != "The person chose Not now for opening Declined App. Ask them in this chat whether to open it, and call open again only after they say yes." {
		t.Fatalf("Not now: %+v", declined)
	}
	if blocked := call(`{"application":"blocked","verb":"open","payload":{}}`); blocked["outcome"] != "forbidden" || blocked["is_error"] != true || blocked["sentence"] != "The person has blocked you from opening Blocked App." {
		t.Fatalf("blocked: %+v", blocked)
	}
	if failed := call(`{"application":"failed","verb":"open","payload":{}}`); failed["outcome"] != "identity_refused" || failed["is_error"] != true || failed["sentence"] != "Failed App did not open: Failed App's program could not be started. Try open again." {
		t.Fatalf("failed: %+v", failed)
	}
	if lost := call(`{"application":"lost","verb":"open","payload":{}}`); lost["outcome"] != "unknown" || lost["sentence"] != "Lost App did not open: OpenAbstractions lost track of Lost App's start. Try open again." {
		t.Fatalf("lost: %+v", lost)
	}
	for _, sentence := range []string{appModelOpenDescription} {
		if !strings.Contains(sentence, "Open a kept app") || !strings.Contains(sentence, "directly to the person in this chat") {
			t.Fatalf("open description: %s", sentence)
		}
	}
	if open.Description != appModelOpenDescription {
		t.Fatalf("listed open description: %s", open.Description)
	}
	if asking := call(`{"application":"asking","verb":"open","payload":{}}`); asking["outcome"] != "not_ready" || asking["is_error"] != true || !strings.Contains(asking["sentence"].(string), "asking is not open yet: OpenAbstractions may be waiting for the person or for asking to start.") {
		t.Fatalf("waiting: %+v", asking)
	}
	for _, invalid := range []struct{ args, reason, sentence string }{
		{`[]`, "invalid_arguments", "Pass oa_applications_open arguments as a JSON object."},
		{`{}`, "missing_application", "Pass application as the kept application's name."},
		{`{"application":7}`, "invalid_application", "Pass application as a non-empty string."},
		{`{"application":"fixture","verb":null}`, "invalid_verb", "Pass verb as a non-empty string."},
		{`{"application":"fixture","verb":"open","payload":[]}`, "invalid_payload", "Pass payload as a JSON object with the fields required by the declared verb."},
		{`{"application":"fixture","verb":"open","payload":null}`, "invalid_payload", "Pass payload as a JSON object with the fields required by the declared verb."},
		{`{"application":"fixture","verb":"open","payload":{},"context":[]}`, "invalid_context", "Pass context as a string, or omit it."},
		{`{"handle":false}`, "invalid_handle", "Pass handle as the non-empty string returned by oa_applications_open in this chat connection."},
		{`{"handle":"unknown"}`, "invalid_handle", errOpenHandle.message},
		{`{"wait_ms":"slow"}`, "invalid_wait_ms", "Pass wait_ms as a non-negative whole number of milliseconds, or omit it to wait 20000 milliseconds."},
	} {
		got := call(invalid.args)
		if got["outcome"] != "invalid" || got["reason"] != invalid.reason || got["sentence"] != invalid.sentence || got["is_error"] != true || len(b.opens) != 8 {
			t.Fatalf("invalid input %s: %+v, opens=%d", invalid.args, got, len(b.opens))
		}
	}
	if got := call(`{"application":"fixture","verb":"unrecognized","payload":{}}`); got["outcome"] != "invalid" || got["reason"] != "verb" ||
		got["sentence"] != "The verb is not declared by fixture."+openVerbsHint || len(b.opens) != 9 {
		t.Fatalf("native undeclared verb: %+v, opens=%d", got, len(b.opens))
	}
	if got := call(`{"application":"required-payload","verb":"open"}`); got["outcome"] != "invalid" || got["reason"] != "payload" ||
		got["sentence"] != "The payload does not match the selected verb's schema for required-payload."+openVerbsHint ||
		len(b.opens) != 10 || b.opens[9].Payload != "{}" {
		t.Fatalf("native invalid payload: %+v, opens=%+v", got, b.opens)
	}
}
