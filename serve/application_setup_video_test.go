package main

import (
	"context"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	modelwire "github.com/openabstractions/abstraction-model/go/abstraction/model/api"
	modelhost "github.com/openabstractions/abstraction-provider-modelhost/go/host"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// setupManifestDirectory is an applications@3 OpenCode whose kept,
// signature-covered manifest declares one ModelNeed of modality.
func setupManifestDirectory(modality string) *applicationDirectory {
	d := setupDirectory(setupGrants())
	d.unconfirmed = map[string]selfMark{}
	m := registration.Manifest{Version: "applications@3", Name: "opencode", Title: "OpenCode",
		Offers: appmodel.CommandDeclaration{Version: "appmodel@1"}, Host: registration.HostRecipe{ReadinessDeadlineMs: 100},
		Needs: registration.Needs{Models: []registration.ModelNeed{{Modality: modality, RequiredFeatures: []string{}, Placement: registration.PlacementAny}}}}
	raw := registration.EncodeManifest(&m)
	d.v3 = &applicationV3Registrations{entries: map[string]applicationV3Registration{"opencode": {State: "enabled"}},
		files: map[string]applicationV3ManifestFile{"opencode": {Raw: raw, Digest: applicationV3Hash(raw),
			Source: applicationV3Source{Program: setupTestApp, FileKey: "fixture-held-key", Code: identity.Code{Trusted: true}, Proof: identity.ProofBound}}}}
	return d
}

// RequestSetup for video: the caller's kept manifest must declare a video
// ModelNeed before rights are decided; a pool below LTX-2.5's need answers
// cannot_run with one sentence; a served set answers already_set_up;
// otherwise the person is asked.
func TestRequestSetupVideo(t *testing.T) {
	ctx := context.Background()
	subject := rights.Subject{Account: setupTestAccount, Program: setupTestApp}

	d := setupManifestDirectory("image")
	calls := 0
	d.decide = func(context.Context, rights.Subject, string, string) (bool, error) { calls++; return true, nil }
	q := &setupRequests{lastRead: time.Now()}
	if got := q.request(ctx, d, subject, "video"); got.Outcome != applications.SetupOutcomeForbidden || got.Reason != "manifest:use:video" || calls != 0 || q.pending != nil {
		t.Fatal("undeclared video", got, calls)
	}

	pool := int64(32 << 30)
	fit := func(context.Context, modelwire.CatalogUse) modelwire.FitResult {
		return modelwire.FitResult{Outcome: modelwire.FitOutcomeFitted, PoolBytes: pool}
	}
	served := false
	check := videoSetupCheck(modelhost.LTX25, true, func(_ context.Context, model string) bool { return served && model == "ltx-2.5" }, fit)
	d = setupManifestDirectory("video")
	q = &setupRequests{lastRead: time.Now()}
	q.bind(setupChecks(runtimeSetupCheck(fit, nil), check))
	if got := q.request(ctx, d, subject, "video"); got.Outcome != applications.SetupOutcomeCannotRun ||
		got.Reason != "This computer has 32 GB free for models; LTX-2.5 needs 53 GB." || q.pending != nil {
		t.Fatal("small pool", got)
	}
	pool, served = 96<<30, true
	if got := q.request(ctx, d, subject, "video"); got.Outcome != applications.SetupOutcomeAlreadySetUp || q.pending != nil {
		t.Fatal("served", got)
	}
	served = false
	if got := q.request(ctx, d, subject, "video"); got.Outcome != applications.SetupOutcomeAsked || q.pending == nil || q.pending.Use != "video" || q.pending.Application != "OpenCode" {
		t.Fatal("asked", got, q.pending)
	}
	if outcome, reason, ok := videoSetupCheck(modelhost.LTX25, false, nil, fit)(ctx, setupTestAccount, "video"); !ok || outcome != applications.SetupOutcomeCannotRun || reason != "LTX-2.5 has no reviewed engine for this computer yet." {
		t.Fatal("no engine", outcome, reason, ok)
	}
}

// The runtime admits a video question only for a reviewed set stated by its
// own title, use policy and memory need.
func TestSetupVideoFactCheck(t *testing.T) {
	set := modelhost.LTX25
	fact := asks.SetupVideoFact{Model: set.Name, Title: "LTX-2.5", License: "LTX-2 Community License Agreement",
		Sources:       []asks.SetupVideoSource{{Repository: "Lightricks/LTX-2.5", Revision: "5e6e71018ee1756ed329b697a7b4aedc934dfce9"}},
		DownloadBytes: set.Size(), PeakBytes: set.Size() + 1, MemoryBytes: set.PeakBytes, Presets: []asks.SetupVideoPreset{}}
	if err := setupVideoFactCheck(fact); err != nil {
		t.Fatal(err)
	}
	if _, err := asks.SetupFactSlots(asks.SetupQuestionFacts{Requester: "OpenCode", RequestUse: "video", Video: &fact}); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*asks.SetupVideoFact){
		"model":   func(f *asks.SetupVideoFact) { f.Model = "ltx-9" },
		"title":   func(f *asks.SetupVideoFact) { f.Title = "Another" },
		"license": func(f *asks.SetupVideoFact) { f.License = "MIT" },
		"memory":  func(f *asks.SetupVideoFact) { f.MemoryBytes = 1 },
		"size":    func(f *asks.SetupVideoFact) { f.DownloadBytes = 1 },
	} {
		changed := fact
		change(&changed)
		if setupVideoFactCheck(changed) == nil {
			t.Fatal(name, "admitted")
		}
	}
}
