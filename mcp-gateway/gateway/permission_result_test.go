package gateway

import (
	"context"
	"encoding/json"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	"os"
	"strings"
	"testing"
)

func TestPermissionObservationSharedCorpus(t *testing.T) {
	raw, err := os.ReadFile("permission_observation_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Observations []struct {
			Name, Outcome, Option, Want, Revision string
			AnswerPending                         bool `json:"answer_pending"`
		}
		Projections []struct {
			Verdict, Outcome, Message string
			Questions                 []string
		}
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, c := range corpus.Observations {
		t.Run(c.Name, func(t *testing.T) {
			outcome, ok := wire.ParsePermissionOutcome(c.Outcome)
			if !ok {
				t.Fatal(c.Outcome)
			}
			got := permissionObservationVerdict(wire.PermissionObservation{Outcome: outcome, PolicyRevision: c.Revision, Answer: &wire.Answer{Option: c.Option, Pending: c.AnswerPending}})
			word := []string{"allowed", "waiting", "refused", "blocked"}[got]
			if word != c.Want {
				t.Fatal(word, c.Want)
			}
		})
	}
	var schema jsonschema.Schema
	schemaRaw, err := os.ReadFile("permission_result.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range corpus.Projections {
		ids := c.Questions
		if ids == nil {
			ids = []string{"q1"}
		}
		result := permissionToolResult(c.Verdict, ids)
		state := result.StructuredContent.(map[string]any)
		if err := resolved.Validate(state); err != nil {
			t.Fatal(state, err)
		}
		if state["outcome"] != c.Outcome || state["message"] != c.Message || result.IsError || len(result.Content) != 1 {
			t.Fatal(result)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if strings.Contains(text, "Failed") {
			t.Fatal(text)
		}
	}
	for _, verdict := range []string{"waiting", "refused"} {
		if err := resolved.Validate(permissionToolResult(verdict, []string{}).StructuredContent); err == nil {
			t.Fatal("unadmitted question accepted for", verdict)
		}
	}
}

func TestPermissionDeadlineReplacesFailedWithTypedPending(t *testing.T) {
	b := &permissionCorpusBackend{wait: func(context.Context) (string, error) { return "waiting", context.DeadlineExceeded }}
	handler := permissionToolMiddleware(b, make(chan struct{}, 8))(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "Failed"}}, StructuredContent: map[string]any{"outcome": "forbidden", "reason": "rights:not_granted rights:question_pending:q1"}}, nil
	})
	got, err := handler(t.Context(), "tools/call", &mcp.CallToolRequest{})
	if err != nil {
		t.Fatal(err)
	}
	out := got.(*mcp.CallToolResult)
	raw, _ := json.Marshal(out)
	if out.IsError || strings.Contains(string(raw), "Failed") || out.StructuredContent.(map[string]any)["outcome"] != "awaiting_permission" {
		t.Fatal(string(raw))
	}
}

func TestPermissionNativeCatalogueAdvertisesCommonResult(t *testing.T) {
	b := &permissionCorpusBackend{}
	_, session := connected(t, b, "permission-schema", "2025-11-25")
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		raw, _ := json.Marshal(tool.Meta)
		if !strings.Contains(string(raw), "awaiting_permission") {
			t.Fatal(tool.Name, string(raw))
		}
		if tool.OutputSchema != nil {
			schema, _ := json.Marshal(tool.OutputSchema)
			if !strings.Contains(string(schema), "anyOf") || !strings.Contains(string(schema), "awaiting_permission") {
				t.Fatal(tool.Name, string(schema))
			}
		}
	}
}
