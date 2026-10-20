package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	model "github.com/openabstractions/abstraction-model/go/client"
)

// Fit's ranked answer keeps its order, marks the first entry that fits or is
// already served as the pick, and leaves out the source and digest.
func TestSetupFitKeepsRankAndMarksThePick(t *testing.T) {
	out := setupFitFrom(model.FitResult{Outcome: model.FitOutcomeFitted, CatalogVersion: "2026.10", PoolBytes: 30e9, Entries: []model.FitEntry{
		{Title: "Large", Family: "large", Fit: model.FitNo, Availability: model.AvailabilityDownload, DownloadBytes: 60e9, NeedBytes: 70e9, Context: 32768, License: "Apache-2.0", Digest: "sha256:aa"},
		{Title: "Paired", Family: "paired", Fit: model.FitNo, Availability: model.AvailabilityServed, Machine: "studio", NeedBytes: 50e9, Context: 32768, Digest: "sha256:bb"},
		{Title: "Middle", Family: "middle", Fit: model.FitFits, Availability: model.AvailabilityDownload, DownloadBytes: 23e9, NeedBytes: 25e9, Context: 32768, Digest: "sha256:cc"},
		{Title: "Small", Family: "small", Fit: model.FitFits, Availability: model.AvailabilityDownload, DownloadBytes: 5e9, NeedBytes: 6e9, Context: 32768},
	}})
	if out.Outcome != "fitted" || out.CatalogVersion != "2026.10" || out.PoolBytes != 30e9 || len(out.Entries) != 4 {
		t.Fatalf("fit = %+v", out)
	}
	titles := []string{}
	for _, e := range out.Entries {
		titles = append(titles, e.Title)
	}
	if strings.Join(titles, ",") != "Large,Paired,Middle,Small" {
		t.Fatalf("rank order = %v", titles)
	}
	// A paired machine serving an entry ranked above makes it ready there,
	// never this computer's pick (model.LocalPick).
	if !out.Entries[1].Ready || out.Entries[1].Machine != "studio" || out.Entries[1].Pick || !out.Entries[2].Pick || out.Entries[0].Pick || out.Entries[3].Pick {
		t.Fatalf("pick = %+v", out.Entries)
	}
	if out.Entries[0].Fit != "no" || out.Entries[2].Fit != "fits" || out.Entries[2].DownloadBytes != 23e9 || out.Entries[0].License != "Apache-2.0" {
		t.Fatalf("entries = %+v", out.Entries)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "sha256") || strings.Contains(string(raw), "digest") || strings.Contains(string(raw), "source") {
		t.Fatalf("hidden fields leaked: %s", raw)
	}
}

// A refused or unavailable Fit carries no entries and no pick.
func TestSetupFitPassesARefusalThrough(t *testing.T) {
	out := setupFitFrom(model.FitResult{Outcome: model.FitOutcomeForbidden})
	if out.Outcome != "forbidden" || out.Entries == nil || len(out.Entries) != 0 || out.PoolBytes != 0 {
		t.Fatalf("refusal = %+v", out)
	}
}

// An unknown use is refused before any service is resolved.
func TestSetupFitRefusesAnUnknownUse(t *testing.T) {
	for _, use := range []string{"", "everything", "Code"} {
		if _, err := (&OA{}).SetupFit(context.Background(), SetupFitInput{Use: use}); err == nil || !strings.Contains(err.Error(), "use must be") {
			t.Fatalf("use %q: %v", use, err)
		}
	}
}

// The tool is listed read-only and answers under the gateway's own principal:
// a principal without inventory.read hears forbidden.
func TestSetupFitToolIsReadOnly(t *testing.T) {
	b := &fixtureBackend{principal: "a", ledger: &fixtureLedger{owner: map[string]string{}, state: map[string]string{}}}
	_, cs := connected(t, b, "client", "2025-11-25")
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range listed.Tools {
		if tool.Name == ToolSetupFit {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
				t.Fatalf("annotations = %+v", tool.Annotations)
			}
		}
	}
	if !found {
		t.Fatal("oa_setup_fit is not listed")
	}
	_, out := call[SetupFitOutput](t, cs, ToolSetupFit, map[string]any{"use": "code"})
	if out.Outcome != "fitted" || len(out.Entries) != 1 || out.Entries[0].Family != "fixture-code" || !out.Entries[0].Pick {
		t.Fatalf("fit = %+v", out)
	}
	denied := &fixtureBackend{principal: "no-inventory-read", ledger: b.ledger}
	_, ds := connected(t, denied, "client", "2025-11-25")
	_, refused := call[SetupFitOutput](t, ds, ToolSetupFit, map[string]any{"use": "code"})
	if refused.Outcome != "forbidden" || len(refused.Entries) != 0 {
		t.Fatalf("refused = %+v", refused)
	}
	if b.providerEffects != 0 || denied.providerEffects != 0 {
		t.Fatal("Fit caused a provider effect")
	}
}
