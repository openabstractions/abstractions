package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	asks "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// This fixture tests projection only. Actual eligibility is exercised by the
// existing native runtime/Asks responder tests, never simulated as authority.
type asksFixture struct {
	messageFixture
	calls           int
	resolutionError error
	operationError  error
	answer          *asks.ResponderDecision
}

type asksOnlyFixture struct {
	Backend
	responder AsksResponder
}

type enrollmentFixture struct {
	asksFixture
	requests int
	verdict  string
}

func (b *enrollmentFixture) RequestAnsweringPermission(context.Context) (asks.PermissionObservation, error) {
	b.requests++
	if b.verdict == "current-blocked" {
		return asks.PermissionObservation{Outcome: asks.PermissionOutcomeDenied, PolicyRevision: "r2"}, nil
	}
	if b.requests > 1 && b.verdict == "allowed" {
		return asks.PermissionObservation{Outcome: asks.PermissionOutcomePermitted, PolicyRevision: "r2"}, nil
	}
	return asks.PermissionObservation{Outcome: asks.PermissionOutcomePending, Answer: &asks.Answer{ID: "enrollment-q", Pending: true}}, nil
}
func (b *enrollmentFixture) AwaitPermissionQuestions(_ context.Context, ids []string) (string, error) {
	if len(ids) != 1 || ids[0] != "enrollment-q" {
		panic("wrong native enrollment question")
	}
	return b.verdict, nil
}
func TestAsksEnrollmentUsesExactPermissionQuestion(t *testing.T) {
	for _, verdict := range []string{"waiting", "refused", "blocked", "allowed", "current-blocked"} {
		t.Run(verdict, func(t *testing.T) {
			b := &enrollmentFixture{asksFixture: asksFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}}, verdict: verdict}
			_, session := connected(t, b, "fixture", "2026-07-28")
			out, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "oa_asks_request_answering", Arguments: map[string]any{}})
			if err != nil || out.IsError {
				t.Fatal(out, err)
			}
			if verdict == "allowed" {
				if b.requests != 2 || b.calls != 1 {
					t.Fatal(b.requests, b.calls)
				}
			} else {
				if b.requests != 1 || b.calls != 0 {
					t.Fatal(b.requests, b.calls)
				}
				raw, _ := json.Marshal(out.StructuredContent)
				var result struct {
					Outcome   string
					Questions []string `json:"question_ids"`
				}
				wantQuestions := 1
				if verdict == "current-blocked" {
					wantQuestions = 0
				}
				if json.Unmarshal(raw, &result) != nil || result.Outcome == "" || len(result.Questions) != wantQuestions {
					t.Fatal(string(raw))
				}
			}
		})
	}
}

// Nil responder methods panic if any Asks tool bypasses the existing native
// lifecycle middleware, including enrollment and read-only list operations.
type suspendedAsksBackend struct {
	suspendedNativeBackend
	AsksBackend
}

func TestAsksNativeLifecycleSuspendsAllOperations(t *testing.T) {
	_, session := connected(t, &suspendedAsksBackend{}, "fixture", "2026-07-28")
	for _, name := range []string{"oa_asks_request_answering", "oa_asks_list_answerable", "oa_asks_list_choices", "oa_asks_answer", "oa_asks_answer_choice"} {
		out, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err != nil || !out.IsError || !strings.Contains(out.Content[0].(*mcp.TextContent).Text, "native receiving is being revalidated") {
			t.Fatalf("%s bypassed native lifecycle: %+v %v", name, out, err)
		}
	}
}

func (b *asksOnlyFixture) AsksResponder(context.Context) (AsksResponder, error) {
	return b.responder, nil
}

func (b *asksFixture) AsksResponder(context.Context) (AsksResponder, error) {
	if b.resolutionError != nil {
		return nil, b.resolutionError
	}
	return b, nil
}
func (b *asksFixture) RequestAnsweringContext(ctx context.Context) (rights.Decision, error) {
	b.calls++
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded request")
	}
	if b.operationError != nil {
		return rights.Decision{}, b.operationError
	}
	return rights.Decision{Outcome: rights.DecisionOutcomeNotGranted}, nil
}
func (b *asksFixture) ListAnswerableContext(context.Context, string, int64) (asks.OperatorPage, error) {
	b.calls++
	return asks.OperatorPage{Outcome: asks.OperatorPageOutcomeForbidden}, nil
}
func (b *asksFixture) ListChoicesContext(context.Context, string, int64) (asks.AnswerableChoicePage, error) {
	b.calls++
	return asks.AnswerableChoicePage{Outcome: asks.OperatorPageOutcomeForbidden}, nil
}
func (b *asksFixture) AnswerContext(context.Context, string, string) (asks.ResponderDecision, error) {
	b.calls++
	if b.answer != nil {
		return *b.answer, b.operationError
	}
	return asks.ResponderDecision{Outcome: asks.OperatorDecisionOutcomeForbidden, Effect: asks.ResponderEffectOutcomeNone}, nil
}

func TestAsksNativeProjectionPreservesPartialAndUncertainServiceResult(t *testing.T) {
	b := &asksFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}, answer: &asks.ResponderDecision{
		Outcome: asks.OperatorDecisionOutcomeAnswered, Effect: asks.ResponderEffectOutcomeConflict, TargetOutcome: "not_granted",
		Record: &asks.RecordMetadata{ID: "outside-prelist", Text: "Native question", Options: []string{"allow"}, Option: "allow"},
	}}
	_, session := connected(t, b, "fixture", "2026-07-28")
	args := &mcp.CallToolParams{Name: "oa_asks_answer", Arguments: map[string]any{"id": "outside-prelist", "option": "allow"}}
	out, err := session.CallTool(t.Context(), args)
	if err != nil || out.IsError {
		t.Fatal(out, err)
	}
	if !strings.Contains(out.Content[0].(*mcp.TextContent).Text, "answer_recorded=true; permission_applied=false") {
		t.Fatal("partial effect projection lost", out)
	}
	raw, err := json.Marshal(out.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := asks.DecodeResponderDecision(raw)
	if err != nil || decision.Outcome != asks.OperatorDecisionOutcomeAnswered || decision.Effect != asks.ResponderEffectOutcomeConflict || decision.TargetOutcome != "not_granted" || b.calls != 1 {
		t.Fatal(decision, err, b.calls)
	}
	b.operationError = errors.New("native answer reply lost")
	out, err = session.CallTool(t.Context(), args)
	if err == nil && !out.IsError || b.calls != 2 {
		t.Fatal("uncertain native answer repeated", out, err, b.calls)
	}
}

func TestAsksForbiddenGuidanceDoesNotRetryOrClaimACause(t *testing.T) {
	b := &asksFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}}
	_, session := connected(t, b, "fixture", "2026-07-28")
	for _, name := range []string{"oa_asks_answer", "oa_asks_answer_choice"} {
		before := b.calls
		out, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"id": "protected-or-unenrolled", "option": "allow"}})
		if err != nil || out.IsError || b.calls != before+1 {
			t.Fatal(out, err, b.calls)
		}
		if !strings.Contains(out.Content[0].(*mcp.TextContent).Text, AsksForbiddenGuidance) {
			t.Fatal(out)
		}
	}
}
func (b *asksFixture) AnswerChoiceContext(context.Context, string, string) (asks.ChoiceDecision, error) {
	b.calls++
	return asks.ChoiceDecision{Outcome: asks.ChoiceDecisionOutcomeForbidden}, nil
}

func TestAsksNativeProjectionStrictInputsAndRefusals(t *testing.T) {
	b := &asksFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}}
	_, s := connected(t, b, "forged-person", "2026-07-28")
	// The native subject boundary is stated once, in the instructions every
	// Asks tool is listed with, rather than in each tool's description.
	if instructions := s.InitializeResult().Instructions; !strings.Contains(instructions, asksPrincipalInstructions) ||
		!strings.Contains(instructions, "for MCP, this OA launcher; in an in-process plugin, the host program") ||
		!strings.Contains(instructions, "it does not authenticate an upstream vendor application, model or parent") {
		t.Fatal("native launcher principal was not disclosed")
	}
	tools, err := s.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal("dedicated inventory", tools, err)
	}
	_, messagesOnly := connected(t, &b.messageFixture, "fixture", "2026-07-28")
	baseline, err := messagesOnly.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]bool{"oa_asks_request_answering": true, "oa_asks_list_answerable": true, "oa_asks_list_choices": true, "oa_asks_answer": true, "oa_asks_answer_choice": true}
	for _, tool := range baseline.Tools {
		expected[tool.Name] = true
	}
	if len(tools.Tools) != len(expected) {
		t.Fatal("Asks changed the existing inventory", tools)
	}
	for _, tool := range tools.Tools {
		if !expected[tool.Name] {
			t.Fatal("unexpected native tool", tool.Name)
		}
		if strings.HasPrefix(tool.Name, "oa_asks_") && strings.Contains(tool.Description, "parent-party") {
			t.Fatal("unsupported upstream parent attribution", tool.Name)
		}
	}
	cases := []struct {
		name    string
		args    map[string]any
		outcome string
	}{
		{"oa_asks_request_answering", map[string]any{}, "not_granted"},
		{"oa_asks_list_answerable", map[string]any{"limit": 64}, "forbidden"},
		{"oa_asks_list_choices", map[string]any{"limit": 1, "cursor": "native-cursor"}, "forbidden"},
		{"oa_asks_answer", map[string]any{"id": "q", "option": "allow"}, "forbidden"},
		{"oa_asks_answer_choice", map[string]any{"id": "c", "option": "keep"}, "forbidden"},
	}
	for _, tc := range cases {
		for _, claim := range []string{"program", "account", "authority", "permission", "target", "expiry", "kind"} {
			args := map[string]any{}
			for k, v := range tc.args {
				args[k] = v
			}
			args[claim] = "forged"
			r, e := s.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.name, Arguments: args})
			if e == nil && !r.IsError {
				t.Fatalf("accepted %s %s", tc.name, claim)
			}
		}
		r, e := s.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if e != nil || r.IsError {
			t.Fatal(tc.name, r, e)
		}
		text := r.Content[0].(*mcp.TextContent)
		if !strings.Contains(text.Text, tc.outcome) || text.Meta["org.openabstractions/untrusted"] != true {
			t.Fatal(text)
		}
	}
	if b.calls != 5 {
		t.Fatal("extra dispatch", b.calls)
	}
	for _, args := range []map[string]any{{"limit": 0}, {"limit": 65}, {"limit": 1, "cursor": strings.Repeat("x", 257)}} {
		r, e := s.CallTool(t.Context(), &mcp.CallToolParams{Name: "oa_asks_list_choices", Arguments: args})
		if e == nil && !r.IsError {
			t.Fatal("page bound", args)
		}
	}
	b.resolutionError = errors.New("unsupported responder service")
	r, e := s.CallTool(t.Context(), &mcp.CallToolParams{Name: "oa_asks_request_answering", Arguments: map[string]any{}})
	if e == nil && !r.IsError {
		t.Fatal("unsupported service accepted")
	}
	if b.calls != 5 {
		t.Fatal("unsupported fallback")
	}
	b.resolutionError = nil
	b.operationError = errors.New("responder unknown_method")
	r, e = s.CallTool(t.Context(), &mcp.CallToolParams{Name: "oa_asks_request_answering", Arguments: map[string]any{}})
	if e == nil && !r.IsError {
		t.Fatal("operation failure accepted")
	}
	if b.calls != 6 {
		t.Fatal("operation failure retried")
	}
	_, generic := connected(t, &fixtureBackend{}, "oa-codex-integration", "2026-07-28")
	r, e = generic.CallTool(t.Context(), &mcp.CallToolParams{Name: "oa_asks_request_answering", Arguments: map[string]any{}})
	if e == nil && !r.IsError {
		t.Fatal("client label selected authority")
	}
	b.operationError = nil
	_, standalone := connected(t, &asksOnlyFixture{Backend: &fixtureBackend{}, responder: b}, "untrusted-client-label", "2026-07-28")
	r, e = standalone.CallTool(t.Context(), &mcp.CallToolParams{Name: "oa_asks_request_answering", Arguments: map[string]any{}})
	if e != nil || r.IsError || b.calls != 7 {
		t.Fatal("program-scoped Asks incorrectly depends on a Messages inbox", r, e, b.calls)
	}
}
