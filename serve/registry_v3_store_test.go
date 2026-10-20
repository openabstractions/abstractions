package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// These inert CAS fixtures supply captured observations directly to the private
// receiving seam. They establish persistence/conditional semantics, no platform
// signature or live endpoint qualification.
func registry3Candidate(t *testing.T, name string) registration.Registration {
	t.Helper()
	program := registration1459Program(name + ".exe")
	return registration.Registration{Key: program, Spec: registration.RegistrationSpec{Program: program, Providers: []registration.ProviderFacet{{Name: name, Arguments: []string{}, Endpoint: name, Services: []string{inference.Contract}, Guarantees: []string{}, Activation: registration.ActivationOnDemand, Resources: []string{}, Models: []registration.ScopedModel{{Modality: "chat", Model: "literal:model"}}, RequiredExtensions: []registration.RequiredExtension{}, Engines: []registration.EngineFact{}, Recipes: []registration.ModelRecipe{}}}, Servers: []registration.ServerFacet{}, Remotes: []registration.RemoteFacet{}, Applications: []registration.ApplicationFacet{}}, Source: registration.SourceOperator, State: registration.StateEnabled, IdentityEvidence: registration.IdentityEvidenceProgram, Accepted: registrationTime(time.Now()), Verified: registrationTime(time.Now()), Verification: registration.VerificationUnverified, VerificationReason: "fixture:unverified", Applications: []registration.ApplicationReading{}, Described: []registration.ServiceState{}}
}

func TestRegistryV3RetainedDraftPreservesEarlierSpecByteBoundary(t *testing.T) {
	v := registry3Candidate(t, "retained-bound")
	v.Revision = strings.Repeat("c", 48)
	v.Epoch = "retained-epoch"
	v.Spec.Providers[0].Arguments = make([]string, 64)
	for i := 0; i < 63; i++ {
		v.Spec.Providers[0].Arguments[i] = strings.Repeat("a", 4096)
	}
	v.Spec.Providers[0].Arguments[63] = "~"
	seed, err := json.Marshal(registrationProgramPayload{Registration: registration.EncodeRegistration(&v)})
	if err != nil {
		t.Fatal(err)
	}
	prior, _, err := retainedDraftMembers(seed, retainedProgramMembers(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	readSpec := func(raw []byte) []byte {
		var payload struct {
			Registration struct {
				Spec json.RawMessage `json:"spec"`
			} `json:"registration"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Registration.Spec
	}
	// Every field comes from the generated writer; only the three historical
	// spellings and one bounded literal argument differ. Earlier raw/compact
	// spec bounds both equal262144; the new field name adds two bytes.
	padding := 262144 - len(readSpec(prior)) + 1
	if padding < 1 || padding > 4096 {
		t.Fatal("fixture argument bound", padding)
	}
	prior = bytes.Replace(prior, []byte(`"~"`), []byte(`"`+strings.Repeat("z", padding)+`"`), 1)
	if len(readSpec(prior)) != 262144 {
		t.Fatal("fixture old compact bound", len(readSpec(prior)))
	}
	row, _, err := decodeProgramPayload(prior)
	if err != nil || row.Epoch != v.Epoch {
		t.Fatal("earlier accepted spec cannot be lost at the rename boundary", err)
	}
	// Both installed and operator declarations use this reader. A producer's
	// unchanged old-boundary content gains exactly two bytes from servers.
	currentSpec, _, err := retainedDraftMembers(readSpec(prior), []retainedDraftMember{{"", "", "hosts", "servers"}}, true, nil)
	if err != nil || len(currentSpec) != 262146 {
		t.Fatal("renamed declaration boundary", len(currentSpec), err)
	}
	for _, installed := range []bool{false, true} {
		dir := t.TempDir()
		name := filepath.Join(dir, "retained-bound.json")
		if err := os.WriteFile(name, currentSpec, 0600); err != nil {
			t.Fatal(err)
		}
		report, reported := reportCollector()
		p := &runtimeProviders{report: report}
		loaded, err := p.readDeclarationDir(dir, nil, installed)
		if err != nil || len(loaded) != 1 || len(reported()) != 0 || !bytes.Equal(loaded[0].observed, currentSpec) || loaded[0].Generation != applicationV3Hash(currentSpec) {
			t.Fatal("renamed current declaration lost", installed, len(loaded), reported(), err)
		}
		// Keep compact content unchanged; only physical formatting exceeds the
		// exact new cap. This must refuse and preserve the original source.
		oversize := append(bytes.Repeat([]byte(" "), maxRegistrationSpecBytes+1-len(currentSpec)), currentSpec...)
		if err := os.WriteFile(name, oversize, 0600); err != nil {
			t.Fatal(err)
		}
		loaded, err = p.readDeclarationDir(dir, nil, installed)
		if err != nil || len(loaded) != 0 {
			t.Fatal("physical declaration byte+1 accepted", installed, err)
		}
		if got, err := os.ReadFile(name); err != nil || !bytes.Equal(got, oversize) {
			t.Fatal("refused declaration changed", err)
		}
	}
}

func TestRegistryV3RetainedDraftRestorePreservesOriginalPayloadAndEpoch(t *testing.T) {
	ctx := t.Context()
	state := t.TempDir()
	d := registration1459Directory(t, state)
	candidate := registry3Candidate(t, "retained-draft")
	candidate.Spec.Servers = []registration.ServerFacet{{Name: "retained-server", Base: "https://retained.fixture.invalid", Kind: "openai-compatible", Hosted: true, Modalities: []string{"chat"}}}
	if out := d.publishProgramRegistration(ctx, "", true, candidate, nil, "operator"); out.Outcome != registration.OutcomeApplied {
		t.Fatal(out)
	}
	ref := d.v3.programFiles[candidate.Key]
	current, err := d.v3.readProgramFile(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	// Construct the exact earlier private writer spelling, preserving every
	// value. This is an inert accepted-state fixture, never a live owner store.
	prior, used, err := retainedDraftMembers(current, retainedProgramMembers(), false, nil)
	if err != nil || len(used) != 2 {
		t.Fatal("prior fixture", len(used), err)
	}
	oldRow, _, err := decodeProgramPayload(prior)
	if err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(d.v3.dir, "index.json")
	indexRaw, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var index registrationProgramIndex
	if err := json.Unmarshal(indexRaw, &index); err != nil {
		t.Fatal(err)
	}
	for i := range index.Programs {
		if index.Programs[i].Revision == ref.Revision {
			index.Programs[i].Hash = applicationV3Hash(prior)
			index.Programs[i].Bytes = int64(len(prior))
		}
	}
	indexRaw, err = json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	path := d.v3.filePath(ref.Revision)
	if err := os.WriteFile(path, prior, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, indexRaw, 0600); err != nil {
		t.Fatal(err)
	}
	for replay := 0; replay < 2; replay++ {
		restored := registration1459Directory(t, state)
		rows, _, _, _, _, err := restored.programRegistrations(ctx)
		if err != nil || len(rows) != 1 || rows[0].Epoch != oldRow.Epoch || rows[0].Revision != oldRow.Revision || len(rows[0].Spec.Servers) != 1 {
			t.Fatal("accepted record changed", rows, err)
		}
		legacy := registrationProviderFiles(rows[0])
		if len(legacy) == 0 || legacy[0].Generation != oldRow.Epoch {
			t.Fatal("live provider binding changed", legacy)
		}
		public := registration.EncodeRegistration(&rows[0])
		if bytes.Contains(public, []byte(`"generation"`)) || !bytes.Contains(public, []byte(`"epoch"`)) || !bytes.Contains(public, []byte(`"servers"`)) {
			t.Fatal("current export used prior fields")
		}
		if actual, err := os.ReadFile(path); err != nil || !bytes.Equal(actual, prior) {
			t.Fatal("accepted original payload rewritten", err)
		}
		if actual, err := os.ReadFile(indexPath); err != nil || !bytes.Equal(actual, indexRaw) {
			t.Fatal("accepted original index rewritten", err)
		}
	}
	changed := append(bytes.Clone(prior), ' ')
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	fresh, err := openApplications(state, "account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.attachApplicationV3(ctx, registration1459Decoder); err == nil {
		t.Fatal("changed payload bypassed original hash/custody")
	}
	if actual, err := os.ReadFile(path); err != nil || !bytes.Equal(actual, changed) {
		t.Fatal("corrupt payload was removed or reset", err)
	}
}

func TestRegistryV3ConditionalPersistenceAndLegacyProjection(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	d := registration1459Directory(t, state)
	candidate := registry3Candidate(t, "fixture")
	first := d.publishProgramRegistration(ctx, "", true, candidate, nil, "operator")
	if first.Outcome != registration.OutcomeApplied || first.Revision == "" {
		t.Fatal(first)
	}
	if out := d.publishProgramRegistration(ctx, "", true, candidate, nil, "operator"); out.Outcome != registration.OutcomeConflict {
		t.Fatal("create overwritten", out)
	}
	if out := d.publishProgramRegistration(ctx, "stale", false, candidate, nil, "operator"); out.Outcome != registration.OutcomeConflict {
		t.Fatal("stale replaced", out)
	}
	replacement := candidate
	replacement.Spec.Providers[0].Models = []registration.ScopedModel{{Modality: "chat", Model: "literal:second"}}
	replaced := d.publishProgramRegistration(ctx, first.Revision, false, replacement, nil, "operator")
	if replaced.Outcome != registration.OutcomeApplied || replaced.Revision == first.Revision {
		t.Fatal(replaced)
	}
	restarted := registration1459Directory(t, state)
	rows, _, declarers, _, _, err := restarted.programRegistrations(ctx)
	if err != nil || len(rows) != 1 || rows[0].Revision != replaced.Revision || rows[0].Epoch == "" || declarers[candidate.Key] != "operator" {
		t.Fatalf("restart %v %+v", err, rows)
	}
	projected := registrationProviderFiles(rows[0])
	if len(projected) != 1 || projected[0].ProviderFacet == nil || projected[0].ProviderFacet.Models[0].Model != "literal:second" {
		t.Fatal(projected)
	}
	legacy := projected[0].Declaration.wire()
	if len(legacy.Models) != 1 || legacy.Models[0] != "chat:literal:second" {
		t.Fatal(legacy)
	}
	if raw, err := os.ReadFile(filepath.Join(state, "applications-v3", "registrations", "index.json")); err != nil || len(raw) > applicationV3IndexLimit {
		t.Fatal("bounded atomic index", len(raw), err)
	}
}

func TestRegistryV3TypedFacetLifecycleAndRemovalReceipt(t *testing.T) {
	ctx := context.Background()
	d := registration1459Directory(t, t.TempDir())
	candidate := registry3Candidate(t, "shared")
	candidate.Spec.Servers = []registration.ServerFacet{{Name: "shared", Base: "http://127.0.0.1:8080", Kind: "openai-compatible", Hosted: true, Modalities: []string{"chat"}}}
	candidate.Verification = registration.VerificationMissing
	candidate.VerificationReason = "identity:missing"
	created := d.publishProgramRegistration(ctx, "", true, candidate, nil, "operator")
	if created.Outcome != registration.OutcomeApplied {
		t.Fatal(created)
	}
	disabled := false
	changed := d.changeProgramFacet(ctx, created.Revision, registration.FacetReference{Kind: registration.FacetKindHost, Name: "shared"}, &disabled)
	if changed.Outcome != registration.OutcomeApplied {
		t.Fatal(changed)
	}
	rows, _, _, _, _, err := d.programRegistrations(ctx)
	if err != nil || len(rows) != 1 || rows[0].State != registration.StateDisabled || rows[0].Verification != registration.VerificationMissing || len(rows[0].Spec.Providers) != 1 || len(rows[0].Spec.Servers) != 1 {
		t.Fatalf("intent/probe conflated %+v %v", rows, err)
	}
	if stale := d.changeProgramFacet(ctx, created.Revision, registration.FacetReference{Kind: registration.FacetKindProvider, Name: "shared"}, nil); stale.Outcome != registration.OutcomeConflict {
		t.Fatal(stale)
	}
	removed := d.changeProgramFacet(ctx, changed.Revision, registration.FacetReference{Kind: registration.FacetKindHost, Name: "shared"}, nil)
	if removed.Outcome != registration.OutcomeApplied || removed.Removal == nil || removed.Removal.Facet.Kind != registration.FacetKindHost {
		t.Fatal(removed)
	}
	rows, receipts, _, _, _, err := d.programRegistrations(ctx)
	if err != nil || len(rows) != 1 || len(rows[0].Spec.Providers) != 1 || len(rows[0].Spec.Servers) != 0 || len(receipts) != 1 {
		t.Fatalf("exact facet removal %+v %+v %v", rows, receipts, err)
	}
	for _, cleanup := range receipts[0].Cleanup {
		if cleanup.Owner != "abstraction.facade" && cleanup.State == registration.CleanupStateDone {
			t.Fatal("unbuilt cleanup claimed done", cleanup)
		}
	}
	last := d.changeProgramFacet(ctx, removed.Revision, registration.FacetReference{Kind: registration.FacetKindProvider, Name: "shared"}, nil)
	if last.Outcome != registration.OutcomeApplied {
		t.Fatal(last)
	}
	rows, receipts, _, _, _, err = d.programRegistrations(ctx)
	if err != nil || len(rows) != 0 || len(receipts) != 2 {
		t.Fatalf("last facet %+v %+v %v", rows, receipts, err)
	}
}

func TestRegistryV3PendingApprovalAndInstalledRemoval(t *testing.T) {
	ctx := context.Background()
	d := registration1459Directory(t, t.TempDir())
	candidate := registry3Candidate(t, "pending")
	candidate.State = registration.StatePendingApproval
	candidate.Source = registration.SourceSelfRegistration
	created := d.publishProgramRegistration(ctx, "", true, candidate, nil, "operator")
	enabled := true
	out := d.changeProgramFacet(ctx, created.Revision, registration.FacetReference{Kind: registration.FacetKindProvider, Name: "pending"}, &enabled)
	if out.Outcome != registration.OutcomeForbidden || out.Refusal == nil || out.Refusal.Decision.String() != "person_only" {
		t.Fatal(out)
	}
	candidate = registry3Candidate(t, "installed")
	candidate.Source = registration.SourceInstallation
	created = d.publishProgramRegistration(ctx, "", true, candidate, nil, "installation")
	out = d.changeProgramFacet(ctx, created.Revision, registration.FacetReference{Kind: registration.FacetKindProvider, Name: "installed"}, nil)
	if out.Outcome != registration.OutcomeConflict || out.Reason != "source:installation" {
		t.Fatal(out)
	}
}

func TestRegistryV3RecipeIdentityAndPerKindNameValidation(t *testing.T) {
	candidate := registry3Candidate(t, "same")
	candidate.Spec.Servers = []registration.ServerFacet{{Name: "same", Base: "http://127.0.0.1:80", Kind: "openai-compatible", Hosted: true, Modalities: []string{"chat"}}}
	if reason := registrationSpecValid(candidate.Spec); reason != "" {
		t.Fatal("cross-kind collision", reason)
	}
	candidate.Spec.Providers = append(candidate.Spec.Providers, candidate.Spec.Providers[0])
	if reason := registrationSpecValid(candidate.Spec); reason != "provider:name" {
		t.Fatal("same kind duplicate", reason)
	}
}
