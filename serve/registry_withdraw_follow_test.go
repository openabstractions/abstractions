package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// withdrawFollowDisable turns one adopted facet off the way the person does.
func withdrawFollowDisable(t *testing.T, d *applicationDirectory, facet registration.FacetReference) {
	t.Helper()
	ctx := t.Context()
	rows, _, _, _, _, err := d.programRegistrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if registrationHasFacet(row.Spec, facet) {
			off := false
			if out := d.changeProgramFacet(ctx, row.Revision, facet, &off); out.Outcome != registration.OutcomeApplied {
				t.Fatal("disable", facet, out)
			}
			return
		}
	}
	t.Fatal("facet not adopted", facet, rows)
}

func withdrawFollowRevision(t *testing.T, p *runtimeProviders) string {
	t.Helper()
	_, revision, err := p.readCached()
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

// A declaration the person turned off is still theirs to withdraw.
func TestRegistryWithdrawPersonDisabledOperatorDeclaration(t *testing.T) {
	p, d, program := registry3LegacyFixture(t)
	registry3WriteLegacy(t, p.dir, registry3LegacyFile("turned-off", program))
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	withdrawFollowDisable(t, d, registration.FacetReference{Kind: registration.FacetKindProvider, Name: "turned-off"})
	files, revision, err := p.readCached()
	if err != nil {
		t.Fatal(err)
	}
	if i := slicesIndexProvider(files, "turned-off"); i < 0 || !files[i].Disabled {
		t.Fatal("fixture is not a disabled operator declaration", files)
	}
	if out := p.removeAdmitted(t.Context(), revision, "turned-off", nil); out.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("withdraw of a person-disabled declaration: %v %q", out.Outcome, out.Reason)
	}
	if files, _, err := p.readCached(); err != nil || slicesIndexProvider(files, "turned-off") >= 0 {
		t.Fatal("withdrawn declaration still listed", files, err)
	}
}

// A disabled installation name is already withdrawn: the canonical path
// answers unknown for it.
func TestRegistryWithdrawDisabledInstallationStaysUnknown(t *testing.T) {
	p, _, program := registry3LegacyFixture(t)
	registry3WriteLegacy(t, p.installation, registry3LegacyFile("installed", program))
	registry3WriteLegacy(t, p.dir, registry3LegacyFile("operator", program))
	disabled, _ := json.Marshal([]string{"installed"})
	if err := os.WriteFile(p.disabledPath, disabled, 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	files, revision, err := p.readCached()
	if err != nil {
		t.Fatal(err)
	}
	if i := slicesIndexProvider(files, "installed"); i < 0 || !files[i].Disabled || files[i].Source == declarationSourceOperator {
		t.Fatal("fixture is not a disabled installation declaration", files)
	}
	if out := p.removeAdmitted(t.Context(), revision, "installed", nil); out.Outcome != wire.DeclarationEditOutcomeUnknown {
		t.Fatalf("withdraw of a disabled installation name: %v %q", out.Outcome, out.Reason)
	}
}

// RemoveHost follows the canonical rule: a disabled operator host leaves, a
// disabled installation host stays unknown.
func TestInferenceRemoveHostDisabledOperatorHost(t *testing.T) {
	p, d, _ := registry3LegacyFixture(t)
	host := func(name string) providerFile {
		return providerFile{Version: providerFileVersion, Declaration: providerDeclaration{Name: name, Transport: transportHTTP, Activation: "attach", Resources: []string{"profile:chat"}, Host: &providerHost{Base: "http://127.0.0.1:9/" + name, Kind: "openai-compatible", Hosted: true}}}
	}
	registry3WriteLegacy(t, p.dir, host("lab"))
	registry3WriteLegacy(t, p.installation, host("bundled"))
	disabled, _ := json.Marshal([]string{"bundled"})
	if err := os.WriteFile(p.disabledPath, disabled, 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	withdrawFollowDisable(t, d, registration.FacetReference{Kind: registration.FacetKindHost, Name: "lab"})
	rightsState := testRights(t, t.TempDir())
	if err := rightsState.install([]string{ActionProviderManage, inference.ActionHostManage}, []installationRule{{inference.ActionHostManage, "account"}}); err != nil {
		t.Fatal(err)
	}
	ceilings, err := inference.OpenCeilings(filepath.Join(t.TempDir(), "ceilings.json"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	operator := &inferenceOperator{credentials: &runtimeCredentials{runtimeRights: rightsState}, providers: p, router: router.New(), ceilings: ceilings, report: func(err error) { t.Log(err) }}
	caller := inference.Subject{Account: rightsState.owner, Program: rightsState.operators[0]}
	for _, f := range p.hostFiles() {
		if !f.Disabled {
			t.Fatal("fixture host is enabled", f)
		}
	}
	if out := operator.RemoveHost(t.Context(), caller, withdrawFollowRevision(t, p), "bundled"); out.Outcome != iwire.EditOutcomeUnknown {
		t.Fatalf("disabled installation host: %+v", out)
	}
	if out := operator.RemoveHost(t.Context(), caller, withdrawFollowRevision(t, p), "lab"); out.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("disabled operator host: %+v", out)
	}
	for _, f := range p.hostFiles() {
		if f.Declaration.Name == "lab" {
			t.Fatal("removed host still listed", f)
		}
	}
}

// withdrawFollowPending publishes a self-registered provider awaiting the
// person's Keep or Remove, with its question bound.
func withdrawFollowPending(t *testing.T, d *applicationDirectory, name string) (registration.Registration, string) {
	t.Helper()
	ctx := t.Context()
	candidate := registry3Candidate(t, name)
	candidate.Spec.Program = filepath.Join(t.TempDir(), "provider.exe")
	candidate.Key = candidate.Spec.Program
	candidate.State = registration.StatePendingApproval
	if err := os.WriteFile(candidate.Spec.Program, []byte("inert registration fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	image := &applicationV3Source{Program: candidate.Spec.Program, FileKey: identity.ProgramKey(candidate.Spec.Program), Code: identity.Code{Trusted: true, Subject: "synthetic test provenance"}, Proof: identity.ProofBound}
	d.programKeepImage = registry3SyntheticKeepImage
	question := "question-" + name
	d.askKeep = func(string) error {
		record, _, _, ok := d.programSelfEntry(candidate.Key)
		if !ok || !d.setProgramQuestion(ctx, candidate.Key, image.Program, question, record.Revision) {
			t.Fatal("question binding")
		}
		return nil
	}
	if out := d.publishProgramRegistration(ctx, "", true, candidate, image, "operator"); out.Outcome != registration.OutcomeApplied {
		t.Fatal(out)
	}
	if _, _, mark, ok := d.programSelfEntry(candidate.Key); !ok || mark.Question != question {
		t.Fatal("pending question", mark, ok)
	}
	return candidate, question
}

// A Withdraw while the person's Remove answer holds the approval answers
// conflict and leaves that answer to finish.
func TestRegistryWithdrawDuringRemoveAnswerConflicts(t *testing.T) {
	p, d, _ := registry3LegacyFixture(t)
	candidate, question := withdrawFollowPending(t, d, "racing")
	var during wire.DeclarationChange
	err := d.removeProgramSelfRegistered(t.Context(), candidate.Key, candidate.Spec.Program, question, func() error {
		during = p.removeAdmitted(t.Context(), withdrawFollowRevision(t, p), "racing", nil)
		return nil
	})
	if err != nil {
		t.Fatal("Remove answer", err)
	}
	if during.Outcome != wire.DeclarationEditOutcomeConflict || during.Reason != "removing" {
		t.Fatalf("Withdraw during Remove: %v %q", during.Outcome, during.Reason)
	}
	if rows, _, _, _, _, err := d.programRegistrations(t.Context()); err != nil || len(rows) != 0 {
		t.Fatal("Remove answer did not finish", rows, err)
	}
}

// A pending self-registered provider withdraws, and the Keep answer that
// arrives afterwards is stale.
func TestRegistryWithdrawPendingSelfRegisteredRefusesLaterKeep(t *testing.T) {
	p, d, _ := registry3LegacyFixture(t)
	candidate, question := withdrawFollowPending(t, d, "pending")
	if out := p.removeAdmitted(t.Context(), withdrawFollowRevision(t, p), "pending", nil); out.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("withdraw of a pending provider: %v %q", out.Outcome, out.Reason)
	}
	if d.keepProgramOwned(context.Background(), candidate.Key, candidate.Spec.Program, question) {
		t.Fatal("Keep after Withdraw accepted")
	}
	if rows, _, _, _, _, err := d.programRegistrations(t.Context()); err != nil || len(rows) != 0 {
		t.Fatal("Keep revived the withdrawn provider", rows, err)
	}
}

// A declaration whose program is gone lists why program:missing; one the
// person turned off lists why operator (facade FAC-R7).
func TestRegistryListsWhyADeclarationIsDisabled(t *testing.T) {
	p, d, program := registry3LegacyFixture(t)
	gone := registry3LegacyFile("comfy-image-1828", filepath.Join(t.TempDir(), "gone", "pytorchd.exe"))
	gone.DeclaredBy = `C:\Program Files\OpenAbstractions\tools\openabstractions.exe`
	registry3WriteLegacy(t, p.dir, gone)
	registry3WriteLegacy(t, p.dir, registry3LegacyFile("turned-off", program))
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	withdrawFollowDisable(t, d, registration.FacetReference{Kind: registration.FacetKindProvider, Name: "turned-off"})
	list, _, err := p.listPinned()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"comfy-image-1828": "program:missing", "turned-off": "operator"}
	for _, row := range list {
		if why, ok := want[row.State.Declaration.Name]; ok {
			if row.State.Readiness != wire.DeclarationReadinessDisabled || row.State.Why != why {
				t.Fatalf("%s lists %v/%q, want disabled/%q", row.State.Declaration.Name, row.State.Readiness, row.State.Why, why)
			}
			delete(want, row.State.Declaration.Name)
		}
	}
	if len(want) != 0 {
		t.Fatal("not listed", want)
	}
}

// Only an operator registration lists the runtime's own reason; installation
// and product names read operator (facade FAC-R7).
func TestProviderFileDisabledReasonBySource(t *testing.T) {
	for source, want := range map[string]string{declarationSourceProduct: "operator", declarationSourceInstallation: "operator", declarationSourceOperator: "program:missing"} {
		f := providerFile{Source: source, Disabled: true, disabledWhy: "program:missing"}
		if got := f.disabledReason(); got != want {
			t.Fatalf("%s: %q, want %q", source, got, want)
		}
	}
}
