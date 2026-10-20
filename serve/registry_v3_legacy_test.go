package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestShippedProviderPublisherContinuity(t *testing.T) {
	trusted := identity.Code{Trusted: true, Subject: "publisher", Issuer: "issuer", TeamID: "team"}
	prior := &applicationV3Source{Code: trusted}
	if !shippedPublisherContinuous(prior, trusted, identity.ProofBound, trusted, identity.ProofBound) {
		t.Fatal("continuous publisher refused")
	}
	if shippedPublisherContinuous(prior, identity.Code{}, identity.ProofBound, identity.Code{}, 0) {
		t.Fatal("trusted predecessor downgraded")
	}
	if shippedPublisherContinuous(nil, identity.Code{}, identity.ProofBound, trusted, identity.ProofBound) {
		t.Fatal("signed runtime accepted unsigned image")
	}
	other := trusted
	other.Issuer = "other"
	if shippedPublisherContinuous(prior, other, identity.ProofBound, identity.Code{}, 0) {
		t.Fatal("publisher changed")
	}
	if !shippedPublisherContinuous(nil, identity.Code{}, identity.ProofBound, identity.Code{}, identity.ProofBound) {
		t.Fatal("unsigned fresh-install verdict refused")
	}
}

func registry3LegacyFixture(t *testing.T) (*runtimeProviders, *applicationDirectory, string) {
	t.Helper()
	d := registration1459Directory(t, t.TempDir())
	p := &runtimeProviders{dir: t.TempDir(), installation: t.TempDir(), disabledPath: filepath.Join(t.TempDir(), "disabled.json"), report: func(err error) { t.Log(err) }, supervisors: map[string]*providerSupervisor{}, retiring: map[string]*providerSupervisor{}, changes: make(chan struct{})}
	p.registrationDirectory = func() *applicationDirectory { return d }
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return p, d, program
}

func TestRegistryV3LegacyAddMissingImagePersistsWithoutIdentityEvidence(t *testing.T) {
	p, d, _ := registry3LegacyFixture(t)
	program := filepath.Join(t.TempDir(), "missing-provider.exe")
	file := registry3LegacyFile("missing-image", program)
	_, revision, err := p.readCached()
	if err != nil {
		t.Fatal(err)
	}
	if out := p.addAdmitted(t.Context(), revision, file, nil); out.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal("missing image declaration could not persist", out)
	}
	// Reload through the real strict store rather than reusing the mutated record.
	d = registration1459Directory(t, filepath.Dir(d.dir))
	p.registrationDirectory = func() *applicationDirectory { return d }
	rows, _, _, _, _, err := d.programRegistrations(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if rows[0].IdentityEvidence != registration.IdentityEvidenceNone || rows[0].Verification != registration.VerificationMissing || rows[0].VerificationReason != "identity:missing" {
		t.Fatal("missing image acquired identity evidence", rows[0])
	}
	if _, exists := d.v3.programSources[rows[0].Key]; exists {
		t.Fatal("missing image acquired retained native image proof")
	}
	list, _, err := p.list()
	if err != nil || len(list) != 1 || list[0].Readiness == wire.DeclarationReadinessReady {
		t.Fatal("missing image became ready", list, err)
	}
}

func TestRegistryV3LegacyPendingSiblingNeverBorrowsEnabledIntent(t *testing.T) {
	p, d, program := registry3LegacyFixture(t)
	ctx := t.Context()
	registry3WriteLegacy(t, p.dir, registry3LegacyFile("pending", program))
	registry3WriteLegacy(t, p.dir, registry3LegacyFile("enabled", program))
	if err := p.adoptProgramRegistrations(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err := d.programRegistrations(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	key := rows[0].Key
	facet := registration.FacetReference{Kind: registration.FacetKindProvider, Name: "pending"}
	// This private receiving fixture supplies an existing per-facet pending mark.
	// The scalar PROGRAM intent must not grant that target or clear its mark.
	out := d.editProgramRegistration(ctx, func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		conditions := maps.Clone(s.programFacetConditions[key])
		conditions[facet] = registrationFacetCondition{Pending: true}
		s.programFacetConditions[key] = conditions
		record := s.programs[key]
		revision, err := applicationToken()
		if err != nil {
			return registration.Change{}, err
		}
		record.Revision = revision
		s.programs[key] = record
		return registration.Change{Outcome: registration.OutcomeApplied}, nil
	})
	if out.Outcome != registration.OutcomeApplied {
		t.Fatal(out)
	}
	before1, _, err := p.list()
	if err != nil {
		t.Fatal(err)
	}
	before2, _, err := p.listPinned()
	if err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err = d.programRegistrations(ctx)
	if err != nil || rows[0].State != registration.StateEnabled {
		t.Fatal("aggregate enabled intent", rows, err)
	}
	files, _, err := p.readCached()
	if err != nil {
		t.Fatal(err)
	}
	a := files[slicesIndexProvider(files, "pending")]
	b := files[slicesIndexProvider(files, "enabled")]
	p.mu.Lock()
	pendingEligible := p.registrationBindingCurrentLocked("pending", a.bindingID())
	enabledEligible := p.registrationBindingCurrentLocked("enabled", b.bindingID())
	p.mu.Unlock()
	if pendingEligible || !enabledEligible || !a.Disabled || b.Disabled {
		t.Fatal("target eligibility borrowed sibling intent", pendingEligible, enabledEligible, a.Disabled, b.Disabled)
	}
	enable := true
	if out := d.changeProgramFacet(ctx, rows[0].Revision, facet, &enable); out.Outcome != registration.OutcomeForbidden || out.Refusal == nil || out.Refusal.Decision != rights.RefusalDecisionPersonOnly {
		t.Fatal("pending mark cleared without person Keep", out)
	}
	after1, _, err := p.list()
	if err != nil {
		t.Fatal(err)
	}
	after2, _, err := p.listPinned()
	if err != nil {
		t.Fatal(err)
	}
	for i := range before1 {
		if string(wire.EncodeDeclarationState(&before1[i])) != string(wire.EncodeDeclarationState(&after1[i])) || string(wire.EncodePinnedDeclarationState(&before2[i])) != string(wire.EncodePinnedDeclarationState(&after2[i])) {
			t.Fatal("refused enable changed old1/2 bytes")
		}
	}
}

func registry3LegacyFile(name, program string) providerFile {
	return providerFile{Version: providerFileVersion, Declaration: providerDeclaration{Name: name, Program: program, Arguments: []string{}, Endpoint: name, Transport: transportNative, Contracts: []string{inference.Contract}, Activation: "on_demand", Models: []string{"chat:model"}}}
}
func registry3WriteLegacy(t *testing.T, dir string, file providerFile) {
	t.Helper()
	if invalid := validProviderDeclaration(file.Declaration); invalid != "" {
		t.Fatal(invalid)
	}
	raw, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file.Declaration.Name+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryV3LegacyMixedSourceAndDisabledIntent(t *testing.T) {
	p, d, program := registry3LegacyFixture(t)
	ctx := t.Context()
	a := registry3LegacyFile("installed", program)
	b := registry3LegacyFile("operator", program)
	registry3WriteLegacy(t, p.installation, a)
	registry3WriteLegacy(t, p.dir, b)
	disabled, _ := json.Marshal([]string{"installed"})
	if err := os.WriteFile(p.disabledPath, disabled, 0600); err != nil {
		t.Fatal(err)
	}
	old, _, _, err := p.readInputsWithDisabled(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range old {
		if !file.Disabled {
			p.supervisors[file.Declaration.Name] = newFixtureProviderSupervisor(t, p, file)
		}
	}
	old1, _, err := p.list()
	if err != nil {
		t.Fatal(err)
	}
	old2, _, err := p.listPinned()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.adoptProgramRegistrations(ctx); err != nil {
		t.Fatal(err)
	}
	current, _, _, err := p.readWithDisabled(true)
	if err != nil {
		t.Fatal(err)
	}
	new1, _, err := p.list()
	if err != nil {
		t.Fatal(err)
	}
	new2, _, err := p.listPinned()
	if err != nil {
		t.Fatal(err)
	}
	for i := range old1 {
		if string(wire.EncodeDeclarationState(&old1[i])) != string(wire.EncodeDeclarationState(&new1[i])) || string(wire.EncodePinnedDeclarationState(&old2[i])) != string(wire.EncodePinnedDeclarationState(&new2[i])) {
			t.Fatal("old1/2 frame changed during adoption")
		}
	}
	for _, before := range old {
		after := current[slicesIndexProvider(current, before.Declaration.Name)]
		if !reflect.DeepEqual(before.Declaration, after.Declaration) || before.Source != after.Source || before.Disabled != after.Disabled {
			t.Fatal("legacy projection changed", before, after)
		}
	}
	rows, _, _, _, _, err := d.programRegistrations(ctx)
	if err != nil || len(rows) != 1 || rows[0].State != registration.StateEnabled {
		t.Fatal("aggregate intent", rows, err)
	}
	installed := registration.FacetReference{Kind: registration.FacetKindProvider, Name: "installed"}
	operator := registration.FacetReference{Kind: registration.FacetKindProvider, Name: "operator"}
	if out := d.changeProgramFacet(ctx, rows[0].Revision, installed, nil); out.Outcome != registration.OutcomeConflict {
		t.Fatal("installation removal", out)
	}
	if out := d.changeProgramFacet(ctx, rows[0].Revision, operator, nil); out.Outcome != registration.OutcomeApplied {
		t.Fatal("operator removal", out)
	}
	rows, _, _, _, _, err = d.programRegistrations(ctx)
	if err != nil || len(rows) != 1 || len(rows[0].Spec.Providers) != 1 || rows[0].Spec.Providers[0].Name != "installed" || rows[0].State != registration.StateDisabled {
		t.Fatal("unrelated installed facet", rows, err)
	}
}

func TestRegistryV3LegacyRemoveRestoresActualFallback(t *testing.T) {
	for _, alreadyDisabled := range []bool{false, true} {
		t.Run(fmt.Sprint(alreadyDisabled), func(t *testing.T) {
			p, d, program := registry3LegacyFixture(t)
			ctx := t.Context()
			installed := registry3LegacyFile("fallback", program)
			installed.Declaration.Models = []string{"chat:installed"}
			operator := registry3LegacyFile("fallback", program)
			operator.Declaration.Models = []string{"chat:operator"}
			registry3WriteLegacy(t, p.installation, installed)
			registry3WriteLegacy(t, p.dir, operator)
			if alreadyDisabled {
				if err := p.disable("fallback"); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.adoptProgramRegistrations(ctx); err != nil {
				t.Fatal(err)
			}
			_, revision, err := p.readCached()
			if err != nil {
				t.Fatal(err)
			}
			if out := p.removeAdmitted(ctx, revision, "fallback", nil); out.Outcome != wire.DeclarationEditOutcomeApplied || out.Reason != "" {
				t.Fatal(out)
			}
			files, _, err := p.readCached()
			if err != nil || len(files) != 1 || files[0].Disabled != alreadyDisabled || files[0].Source != declarationSourceInstallation || !reflect.DeepEqual(files[0].Declaration.Models, installed.Declaration.Models) {
				t.Fatal("actual lower-source fallback", files, err)
			}
			d = registration1459Directory(t, filepath.Dir(d.dir))
			p.registrationDirectory = func() *applicationDirectory { return d }
			if err := p.adoptProgramRegistrations(ctx); err != nil {
				t.Fatal(err)
			}
			files, _, err = p.readCached()
			if err != nil || len(files) != 1 || files[0].Disabled != alreadyDisabled || files[0].Source != declarationSourceInstallation || !reflect.DeepEqual(files[0].Declaration.Models, installed.Declaration.Models) {
				t.Fatal("old raw operator resurrected after restart", files, err)
			}
		})
	}
}

func slicesIndexProvider(files []providerFile, name string) int {
	for i, f := range files {
		if f.Declaration.Name == name {
			return i
		}
	}
	return -1
}

func TestRegistryV3LegacyMeasuredPayloadCustodyAndReclaim(t *testing.T) {
	p, d, _ := registry3LegacyFixture(t)
	ctx := t.Context()
	large := providerFile{Version: providerFileVersion, Declaration: providerDeclaration{Name: "largehost", Transport: transportHTTP, Activation: "attach", Resources: []string{"profile:chat"}, Host: &providerHost{Base: "http://127.0.0.1/" + strings.Repeat("x", registrationRecordFileLimit+1024), Kind: "openai-compatible", Hosted: true}}}
	registry3WriteLegacy(t, p.dir, large)
	if err := p.adoptProgramRegistrations(ctx); err != nil {
		t.Fatal("old directory migration", err)
	}
	if _, _, _, _, _, err := d.programRegistrations(ctx); !errors.Is(err, errRegistrationEncodedSize) {
		t.Fatal("oversize initial migration", err)
	}
	refs := d.v3.legacyProgramFiles["largehost"]
	if len(refs) != 1 || refs[0].Bytes <= registrationRecordFileLimit {
		t.Fatal("actual measured custody", refs)
	}
	oldPath := d.v3.filePath(refs[0].Revision)
	files, revision, err := p.readCached()
	if err != nil || len(files) != 1 || files[0].Declaration.Host.Base != large.Declaration.Host.Base {
		t.Fatal("old accepted host projection", err)
	}
	small := large
	copy := *large.Declaration.Host
	small.Declaration.Host = &copy
	small.Declaration.Host.Base = "http://127.0.0.1/"
	if out := p.replaceAdmitted(ctx, revision, small, nil); out.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal("measured payload return", out)
	}
	if _, _, _, _, _, err := d.programRegistrations(ctx); err != nil {
		t.Fatal("typed return after measured payload", err)
	}
	// A second bounded receiving transaction reclaims the superseded reference
	// using its actual persisted byte bound and hash, never a widened guess.
	_, revision, err = p.readCached()
	if err != nil {
		t.Fatal(err)
	}
	if out := p.replaceAdmitted(ctx, revision, small, nil); out.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal(out)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("superseded measured payload not reclaimed", err)
	}
}

func TestRegistryV3LegacyCapacityReceivingRoundTrip(t *testing.T) {
	p, d, program := registry3LegacyFixture(t)
	ctx := context.Background()
	small := registry3LegacyFile("capacity", program)
	registry3WriteLegacy(t, p.dir, small)
	if err := p.adoptProgramRegistrations(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err := d.programRegistrations(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	_, revision, err := p.readCached()
	if err != nil {
		t.Fatal(err)
	}
	large := small
	large.Declaration.Arguments = make([]string, 64)
	for i := range large.Declaration.Arguments {
		large.Declaration.Arguments[i] = strings.Repeat("x", 4096)
	}
	if invalid := validProviderDeclaration(large.Declaration); invalid != "" {
		t.Fatal("old-valid capacity fixture", invalid)
	}
	if out := p.replaceAdmitted(ctx, revision, large, nil); out.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal("old receiving large write", out)
	}
	if rows, _, _, _, _, err := d.programRegistrations(ctx); !errors.Is(err, errRegistrationEncodedSize) || len(rows) != 0 {
		t.Fatal("stale/partial typed state", rows, err)
	}
	old, revision, err := p.readCached()
	if err != nil || len(old) != 1 || !reflect.DeepEqual(old[0].Declaration.Arguments, large.Declaration.Arguments) {
		t.Fatal("large old projection", len(old), err)
	}
	d = registration1459Directory(t, filepath.Dir(d.dir))
	p.registrationDirectory = func() *applicationDirectory { return d }
	if rows, _, _, _, _, err := d.programRegistrations(ctx); !errors.Is(err, errRegistrationEncodedSize) || len(rows) != 0 {
		t.Fatal("restart lost opaque whole-state disposition", rows, err)
	}
	if out := p.replaceAdmitted(ctx, revision, small, nil); out.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal("return to admissible", out)
	}
	rows, _, _, _, _, err = d.programRegistrations(ctx)
	if err != nil || len(rows) != 1 || len(rows[0].Spec.Providers[0].Arguments) != 0 {
		t.Fatal("typed return", rows, err)
	}
}
