package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
)

type enrollmentReadFixture struct {
	*fixtureBackend
	admitted bool
	called   string
	result   applications.AgentEnrollmentResult
}

func (b *enrollmentReadFixture) BeginNativeTool(ctx context.Context, _ string) (context.Context, func(), bool) {
	return ctx, func() {}, b.admitted
}
func (b *enrollmentReadFixture) ApplicationsEnrollmentRead(_ context.Context, application string) (applications.AgentEnrollmentResult, error) {
	b.called = application
	return b.result, nil
}

func TestEnrollmentReadUsesNativeAdmissionAndGeneratedResult(t *testing.T) {
	b := &enrollmentReadFixture{fixtureBackend: &fixtureBackend{}, result: applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &applications.AgentEnrollment{Application: "other", Revision: "revision", State: applications.AgentEnrollmentStateEnrolled}}}
	_, session := connected(t, b, "operator-spoof", "2026-07-28")
	call := func() *mcp.CallToolResult {
		t.Helper()
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolApplicationsEnrollmentRead, Arguments: map[string]any{"application": "other"}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := call(); !result.IsError || b.called != "" {
		t.Fatal("native admission bypass", result, b.called)
	}
	b.admitted = true
	result := call()
	if result.IsError || b.called != "other" {
		t.Fatal(result, b.called)
	}
	assertAppModelUntrusted(t, result)
	var decoded applications.AgentEnrollmentResult
	if err := applications.FromJSON([]byte(result.Content[0].(*mcp.TextContent).Text), &decoded); err != nil || decoded.Enrollment == nil || decoded.Enrollment.Program != "" {
		t.Fatal(decoded, err)
	}
	b.result = applications.AgentEnrollmentResult{Outcome: applications.OutcomeForbidden}
	if result := call(); !result.IsError || result.StructuredContent.(map[string]any)["outcome"] != "forbidden" {
		t.Fatal(result)
	}
	b.called = ""
	invalid, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolApplicationsEnrollmentRead, Arguments: json.RawMessage(`{"application":"other","program":"operator"}`)})
	if err == nil && (invalid == nil || !invalid.IsError) {
		t.Fatal("identity-shaped extra argument admitted")
	}
	if b.called != "" {
		t.Fatal("invalid input dispatched")
	}
}
