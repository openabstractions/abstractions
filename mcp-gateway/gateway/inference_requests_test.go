package gateway

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"reflect"
	"strings"
	"testing"
)

type inferenceRequestsFixture struct {
	*fixtureBackend
	admitted bool
	calls    int
	page     wire.RequestPage
}

func (b *inferenceRequestsFixture) BeginNativeTool(ctx context.Context, _ string) (context.Context, func(), bool) {
	return ctx, func() {}, b.admitted
}
func (b *inferenceRequestsFixture) InferenceRequests(_ context.Context, cursor, max int64) (wire.RequestPage, error) {
	b.calls++
	if cursor != 0 || max != 64 {
		panic("arguments changed")
	}
	return b.page, nil
}

func TestInferenceRequestsUsesNativeAdmissionAndExactPage(t *testing.T) {
	place := wire.ServedPlaceLocal
	b := &inferenceRequestsFixture{fixtureBackend: &fixtureBackend{}, page: wire.RequestPage{Outcome: wire.AuditOutcomePage, Entries: []wire.RequestEntry{{Entry: wire.AuditEntry{Sequence: 0, Route: wire.AuditRouteNative, Host: "local", Model: "local-model", Outcome: "completed"}, PreferredModel: "cloud-model", UnavailableHosts: []string{"cloud"}, Selection: "preferred_route_unavailable", Placement: &place}}, Next: 1, AtEnd: true}}
	_, session := connected(t, b, "operator-spoof", "2026-07-28")
	call := func() *mcp.CallToolResult {
		t.Helper()
		r, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolInferenceRequests, Arguments: map[string]any{"cursor": 0, "max_entries": 64}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := call(); !r.IsError || b.calls != 0 {
		t.Fatal("native admission bypass", r, b.calls)
	}
	b.admitted = true
	r := call()
	if r.IsError || b.calls != 1 {
		t.Fatal(r, b.calls)
	}
	assertAppModelUntrusted(t, r)
	var decoded wire.RequestPage
	if err := wire.FromJSON([]byte(r.Content[0].(*mcp.TextContent).Text), &decoded); err != nil || !reflect.DeepEqual(decoded, b.page) {
		t.Fatal(decoded, err)
	}
	b.page = wire.RequestPage{Outcome: wire.AuditOutcomeForbidden, Entries: []wire.RequestEntry{}}
	if r := call(); !r.IsError || r.StructuredContent.(map[string]any)["outcome"] != "forbidden" {
		t.Fatal(r)
	}
	b.page = wire.RequestPage{Outcome: wire.AuditOutcomePage, Entries: []wire.RequestEntry{{Entry: wire.AuditEntry{Route: wire.AuditRouteNative, Outcome: "completed", Reason: strings.Repeat("x", 1<<20)}}}, Next: 1, AtEnd: true}
	if r := call(); !r.IsError || r.StructuredContent.(map[string]any)["outcome"] != "unavailable" {
		t.Fatal("oversize page was not a typed whole-page refusal", r)
	}
	before := b.calls
	r, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolInferenceRequests, Arguments: map[string]any{"cursor": 0, "max_entries": 64, "program": "operator"}})
	if err == nil && (r == nil || !r.IsError) {
		t.Fatal("identity argument admitted")
	}
	if b.calls != before {
		t.Fatal("invalid input dispatched")
	}
}
