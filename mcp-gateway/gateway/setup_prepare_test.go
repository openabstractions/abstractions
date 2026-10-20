package gateway

import (
	"context"
	"strings"
	"testing"
)

// oa_setup_prepare is an effect tool: it asks OpenAbstractions' setup step
// and passes RequestSetup's outcome and reason through, never a download
// decision.
func TestSetupPrepareToolPassesTheOutcomeThrough(t *testing.T) {
	b := &fixtureBackend{principal: "a", ledger: &fixtureLedger{owner: map[string]string{}, state: map[string]string{}}}
	_, cs := connected(t, b, "client", "2025-11-25")
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range listed.Tools {
		if tool.Name == ToolSetupPrepare {
			found = true
			a := tool.Annotations
			if a == nil || a.ReadOnlyHint || a.IdempotentHint || a.OpenWorldHint == nil || !*a.OpenWorldHint || a.DestructiveHint == nil || *a.DestructiveHint {
				t.Fatalf("annotations = %+v", a)
			}
		}
	}
	if !found {
		t.Fatal("oa_setup_prepare is not listed")
	}
	if _, out := call[SetupPrepareOutput](t, cs, ToolSetupPrepare, map[string]any{"use": "code"}); out.Outcome != "asked" || out.Reason != "" {
		t.Fatalf("prepare = %+v", out)
	}
	_, ds := connected(t, &fixtureBackend{principal: "no-inventory-read", ledger: b.ledger}, "client", "2025-11-25")
	if _, out := call[SetupPrepareOutput](t, ds, ToolSetupPrepare, map[string]any{"use": "code"}); out.Outcome != "forbidden" || out.Reason != "no person reachable on this runtime" {
		t.Fatalf("refused = %+v", out)
	}
}

// An unknown use is refused before any service is resolved.
func TestSetupPrepareRefusesAnUnknownUse(t *testing.T) {
	for _, use := range []string{"", "chat", "audio"} {
		if _, err := (&OA{}).SetupPrepare(context.Background(), SetupPrepareInput{Use: use}); err == nil || !strings.Contains(err.Error(), "use must be") {
			t.Fatalf("use %q: %v", use, err)
		}
	}
}
