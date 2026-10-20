package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
)

// startingApp is an application whose open answers starting and whose
// startup context reads each phase of phases in turn, the last one for good.
type startingApp struct {
	*appModelFixture
	mu     sync.Mutex
	phases []string
	cause  string
	reads  int
	opens  int
	listed string
}

func (b *startingApp) ApplicationsOpen(_ context.Context, request applications.ActivateRequest) (applications.ActivationResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.opens++
	return applications.ActivationResult{Outcome: applications.ActivationOutcomeNotReady, Instance: "live", Title: "ComfyUI",
		Reason: "activation:refused: starting:launching: ComfyUI's server is launching"}, nil
}

func (b *startingApp) ApplicationsDescribe(_ context.Context, query appmodel.ApplicationsQuery) (applications.DescribeResult, error) {
	if query.Application != "" {
		return applications.DescribeResult{Outcome: applications.OutcomeForbidden, Reason: "only quiet discovery"}, nil
	}
	instance := "live"
	if b.listed != "" {
		instance = b.listed
	}
	ref := appmodel.ContextRef{Instance: instance, Context: "startup", Epoch: "e1"}
	return applications.DescribeResult{Outcome: applications.OutcomePage, Answer: &appmodel.ApplicationListing{Outcome: appmodel.ReadingOutcomePage,
		Applications: []appmodel.ListedApplication{{Application: "comfyui", Title: "ComfyUI", Instances: []appmodel.ListedInstance{
			{Instance: instance, Interface: "abstraction.facade/appmodel@1", Contexts: []appmodel.ContextEntry{{Context: ref, Title: "Backend startup", Revision: "1"}}}}}}}}, nil
}

func (b *startingApp) ApplicationsCall(_ context.Context, call appmodel.GatewayCall) (applications.CallResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if call.Tool != "app_read" || call.RequireReadPermit == nil || !*call.RequireReadPermit {
		return applications.CallResult{Outcome: applications.OutcomeForbidden, Reason: "only reads"}, nil
	}
	request, err := appmodel.DecodeReadRequest([]byte(call.Arguments))
	if err != nil || request.Context.Context != "startup" || request.Context.Epoch != "e1" {
		return applications.CallResult{Outcome: applications.OutcomeInvalid, Reason: "read"}, nil
	}
	phase := b.phases[min(b.reads, len(b.phases)-1)]
	b.reads++
	properties, _ := json.Marshal(map[string]any{"phase": phase, "integration_ready": false, "graph_status": "waiting", "cause": b.cause})
	return applications.CallResult{Outcome: applications.OutcomeApplied, Answer: &appmodel.Envelope{Snapshot: &appmodel.ContextSnapshot{
		Outcome: appmodel.ReadingOutcomePage, Context: request.Context, Revision: "1",
		Entities:    []appmodel.Entity{{ID: "backend", Kind: "backend", Label: "ComfyUI backend startup", Properties: string(properties)}},
		Collections: []appmodel.Collection{}, Selections: []appmodel.Selection{}}}}, nil
}

func openCall(t *testing.T, session *mcp.ClientSession, args string) (map[string]any, string) {
	t.Helper()
	out, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolApplicationsOpen, Arguments: json.RawMessage(args)})
	if err != nil {
		t.Fatal(err)
	}
	record, _ := out.StructuredContent.(map[string]any)
	if record == nil {
		t.Fatalf("open answer: %+v", out)
	}
	record["is_error"] = out.IsError
	return record, out.Content[len(out.Content)-1].(*mcp.TextContent).Text
}

type openDefaultsApp struct {
	*appModelFixture
	calls []applications.ActivateRequest
}

func (b *openDefaultsApp) ApplicationsOpen(ctx context.Context, request applications.ActivateRequest) (applications.ActivationResult, error) {
	b.calls = append(b.calls, request)
	if request.DefaultVerb != nil && *request.DefaultVerb {
		if request.Application != "fixture" {
			return applications.ActivationResult{Outcome: applications.ActivationOutcomeInvalid, Reason: "missing_verb"}, nil
		}
		request.Verb = "open"
	}
	return b.appModelFixture.ApplicationsOpen(ctx, request)
}

func TestOpenSendsDefaultSelectionToNativeActivation(t *testing.T) {
	b := &openDefaultsApp{appModelFixture: &appModelFixture{fixtureBackend: &fixtureBackend{}}}
	_, session := connected(t, b, "untrusted-metadata", "2026-07-28")
	if got, _ := openCall(t, session, `{"application":"fixture"}`); got["status"] != "opened" {
		t.Fatalf("both fields omitted: %+v", got)
	}
	if got, _ := openCall(t, session, `{"application":"fixture","payload":{"option":true}}`); got["status"] != "opened" {
		t.Fatalf("verb omitted: %+v", got)
	}
	if got, _ := openCall(t, session, `{"application":"fixture","verb":"open"}`); got["status"] != "opened" {
		t.Fatalf("payload omitted: %+v", got)
	}
	for _, name := range []string{"multiple", "read-only", "undisclosed"} {
		if got, sentence := openCall(t, session, `{"application":"`+name+`"}`); got["reason"] != "missing_verb" || !strings.Contains(sentence, "open_verbs") {
			t.Fatalf("%s: %+v %q", name, got, sentence)
		}
	}
	if got, _ := openCall(t, session, `{"application":"fixture","payload":null}`); got["reason"] != "invalid_payload" {
		t.Fatalf("invalid payload: %+v", got)
	}
	if got, _ := openCall(t, session, `{"application":"fixture","verb":"","default_verb":true}`); got["reason"] != "invalid_arguments" {
		t.Fatalf("native selection member supplied by MCP caller: %+v", got)
	}
	if len(b.calls) != 6 {
		t.Fatalf("native requests: %+v", b.calls)
	}
	for i, request := range b.calls {
		if i == 2 {
			if request.DefaultVerb != nil || request.Verb != "open" {
				t.Fatalf("explicit verb was changed: %+v", request)
			}
		} else if request.DefaultVerb == nil || !*request.DefaultVerb || request.Verb != "" {
			t.Fatalf("default request %d: %+v", i, request)
		}
	}
	if len(b.opens) != 3 || b.opens[0].Verb != "open" || b.opens[0].Payload != "{}" ||
		b.opens[1].Verb != "open" || b.opens[1].Payload != `{"option":true}` ||
		b.opens[2].Verb != "open" || b.opens[2].Payload != "{}" {
		t.Fatalf("activation requests: %+v", b.opens)
	}
}

// An open of a starting app answers starting with its phase and a handle
// once wait_ms runs out; the handle follows the same start to opened, and
// the follow asks and starts nothing (ruling 2026-11-12, point 5).
func TestOpenFollowsAStartingAppByHandle(t *testing.T) {
	b := &startingApp{appModelFixture: &appModelFixture{fixtureBackend: &fixtureBackend{}}, phases: []string{"loading_custom_nodes", "loading_custom_nodes", "listening", "running"}}
	_, session := connected(t, b, "untrusted-metadata", "2026-07-28")
	first, sentence := openCall(t, session, `{"application":"comfyui","verb":"open","payload":{},"wait_ms":0}`)
	handle, _ := first["handle"].(string)
	if first["status"] != "starting" || first["phase"] != "loading custom nodes" || handle == "" || first["is_error"] != false {
		t.Fatalf("first open: %+v", first)
	}
	prefix := "ComfyUI is starting (loading custom nodes). To wait for this start, call oa_applications_open with "
	arguments, ok := strings.CutPrefix(sentence, prefix)
	if !ok {
		t.Fatalf("first sentence: %q", sentence)
	}
	arguments, ok = strings.CutSuffix(arguments, ". This asks the person no question.")
	var next map[string]string
	if !ok || json.Unmarshal([]byte(arguments), &next) != nil || len(next) != 2 ||
		next["application"] != "comfyui" || next["handle"] != handle {
		t.Fatalf("follow-up arguments: %q", arguments)
	}
	_, other := connected(t, b, "other-connection", "2026-07-28")
	if rejected, guidance := openCall(t, other, arguments); rejected["outcome"] != "invalid" ||
		rejected["reason"] != "invalid_handle" || !strings.Contains(guidance, "chat connection that received it") {
		t.Fatalf("cross-connection handle: %+v %q", rejected, guidance)
	}
	opened, sentence := openCall(t, session, arguments)
	if opened["status"] != "opened" || opened["outcome"] != "ready" || opened["instance"] != "live" || opened["is_error"] != false || sentence != "Opened ComfyUI. Use instance live with oa_applications_commands_list." {
		t.Fatalf("followed open: %+v %q", opened, sentence)
	}
	if b.opens != 1 {
		t.Fatalf("the follow opened again: %d opens", b.opens)
	}
	if unknown, _ := openCall(t, session, `{"handle":"not-a-handle"}`); unknown["outcome"] != "invalid" {
		t.Fatalf("unknown handle: %+v", unknown)
	}
}

// A start that fails answers the application's plain cause.
func TestOpenOfAFailedStartSaysWhy(t *testing.T) {
	b := &startingApp{appModelFixture: &appModelFixture{fixtureBackend: &fixtureBackend{}}, phases: []string{"launching", "failed"},
		cause: "ComfyUI's server exited: OSError: [Errno 10048] error while attempting to bind on address ('127.0.0.1', 8188)"}
	_, session := connected(t, b, "untrusted-metadata", "2026-07-28")
	failed, sentence := openCall(t, session, `{"application":"comfyui","verb":"open","payload":{},"wait_ms":10000}`)
	if failed["outcome"] != "unavailable" || failed["is_error"] != true ||
		sentence != "ComfyUI did not open: "+b.cause+". Try open again." {
		t.Fatalf("failed open: %+v %q", failed, sentence)
	}
}

func TestOpenFollowDoesNotAdoptAnotherInstance(t *testing.T) {
	b := &startingApp{appModelFixture: &appModelFixture{fixtureBackend: &fixtureBackend{}}, listed: "other", phases: []string{"running"}}
	_, session := connected(t, b, "untrusted-metadata", "2026-07-28")
	first, _ := openCall(t, session, `{"application":"comfyui","verb":"open","payload":{},"wait_ms":0}`)
	handle, _ := first["handle"].(string)
	if first["status"] != "starting" || handle == "" {
		t.Fatalf("first open: %+v", first)
	}
	follow, _ := openCall(t, session, `{"handle":"`+handle+`","wait_ms":0}`)
	if follow["status"] != "starting" || follow["handle"] != handle || b.reads != 0 || b.opens != 1 {
		t.Fatalf("follow: %+v, reads=%d, opens=%d", follow, b.reads, b.opens)
	}
}

type reconnectingOpen struct {
	*startingApp
	cancel  context.CancelFunc
	dropped bool
}

func (b *reconnectingOpen) BeginNativeTool(ctx context.Context, _ string) (context.Context, func(), bool) {
	call, cancel := context.WithCancel(ctx)
	b.cancel = cancel
	return call, cancel, true
}

func (*reconnectingOpen) NativeToolUnavailable() (string, string) { return "reconnecting", "" }

func (b *reconnectingOpen) ApplicationsCall(ctx context.Context, call appmodel.GatewayCall) (applications.CallResult, error) {
	if b.reads == 1 && !b.dropped {
		b.dropped = true
		b.cancel()
		return applications.CallResult{}, errors.New("fixture admission suspended")
	}
	return b.startingApp.ApplicationsCall(ctx, call)
}

func TestOpenHandlePollSurvivesReconnecting(t *testing.T) {
	b := &reconnectingOpen{startingApp: &startingApp{
		appModelFixture: &appModelFixture{fixtureBackend: &fixtureBackend{}},
		phases:          []string{"launching", "running"},
	}}
	_, session := connected(t, b, "fixture", "2026-07-28")
	first, _ := openCall(t, session, `{"application":"comfyui","verb":"open","payload":{},"wait_ms":0}`)
	handle, _ := first["handle"].(string)
	if first["status"] != "starting" || handle == "" {
		t.Fatal(first)
	}
	follow, _ := json.Marshal(map[string]any{"handle": handle, "wait_ms": 5000})
	opened, _ := openCall(t, session, string(follow))
	if opened["status"] != "opened" || opened["instance"] != "live" || !b.dropped || b.opens != 1 {
		t.Fatalf("follow=%+v dropped=%v opens=%d", opened, b.dropped, b.opens)
	}
}

// A job followed by {"job": ...} alone takes the call's instance.
func TestAppJobTakesTheCallsInstance(t *testing.T) {
	if got := appJobArguments("app_job", `{"job":"j35"}`, "i1"); got != `{"instance":"i1","job":"j35"}` {
		t.Fatalf("filled: %s", got)
	}
	for _, row := range [][3]string{
		{"app_job", `{"instance":"other","job":"j35"}`, `{"instance":"other","job":"j35"}`},
		{"workflow.run", `{"workflow":"w"}`, `{"workflow":"w"}`},
		{"app_job", `[]`, `[]`},
	} {
		if got := appJobArguments(row[0], row[1], "i1"); got != row[2] {
			t.Fatalf("%s %s: %s", row[0], row[1], got)
		}
	}
}
