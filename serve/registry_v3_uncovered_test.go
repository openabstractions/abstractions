package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func uncoveredDirectory(t *testing.T, state string) *applicationDirectory {
	t.Helper()
	d, err := openApplications(state, "account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := d.attachApplicationV3(context.Background(), (&applicationV3Gateway{}).decoder); err != nil {
		t.Fatal(err)
	}
	d.operator = func(p string) bool { return p == "operator" }
	return d
}

// Owner, 2026-11-09: a ComfyUI build self-registered on 2026-10-31, before
// the registry projected applications into program records (2026-11-02) and
// recorded acceptance times, kept its entry under the earlier manifest
// spelling and no program record. Every applications@3 read that could see
// it refused ("unsupported:encoded_size"), and with it the Panel's removal
// ("application ownership observation refused"). The registry now projects
// such an entry on load: it is read with its revision, and removing it by
// that revision succeeds.
func TestRegistryProjectsAnEntryKeptBeforeProgramRecords(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	d := uncoveredDirectory(t, state)
	dir := d.v3.dir

	program := registration1459Program("OpenAbstractions ComfyUI.exe")
	m := &registration.Manifest{Version: "applications@3", Name: "comfyui", Title: "ComfyUI — OA build",
		Host:   registration.HostRecipe{Arguments: []string{"owned-host"}, ReadinessDeadlineMs: 30000},
		Offers: appmodel.CommandDeclaration{Version: "appmodel@1"},
		Needs:  registration.Needs{Models: []registration.ModelNeed{{Modality: "image", RequiredFeatures: []string{}, Placement: registration.PlacementAny}}},
		Choosers: &registration.Choosers{Entries: []registration.Chooser{{Name: "oa-image-default", Modality: "image"}}}}
	raw := applicationV3PriorCanonical(registration.EncodeManifest(m))
	if !strings.Contains(string(raw), "readiness_timeout_ms") {
		t.Fatal("fixture is not the earlier spelling")
	}
	_, digest, err := (&applicationV3Gateway{}).decoder(raw, program, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The file carries no acceptance times, as files written before
	// 2026-11-02 do.
	file, err := json.Marshal(applicationV3ManifestFile{Raw: raw, Digest: digest,
		Source: applicationV3Source{Program: program, FileKey: applicationV3Hash([]byte(program)), Code: identity.Code{Trusted: true, Subject: "synthetic test provenance"}, Proof: identity.ProofBound}})
	if err != nil {
		t.Fatal(err)
	}
	fileRevision, revision := strings.Repeat("5", 48), strings.Repeat("2", 48)
	if err := os.WriteFile(filepath.Join(dir, fileRevision+".json"), file, 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := json.Marshal(registrationProgramIndex{Version: 1, Revision: strings.Repeat("b", 48),
		Entries:  map[string]applicationV3Registration{"comfyui": {Revision: revision, FileRevision: fileRevision, FileHash: applicationV3Hash(file), Source: "self", State: "enabled"}},
		Programs: []registrationProgramFile{}, Removals: []registrationProgramFile{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), index, 0o600); err != nil {
		t.Fatal(err)
	}

	d = uncoveredDirectory(t, state)
	records, _, _, _, _, err := d.programRegistrationsFor(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range records {
		for _, facet := range record.Spec.Applications {
			if facet.Name == "comfyui" {
				found = true
				if record.Revision != revision || facet.Manifest.Host.ReadinessDeadlineMs != 30000 {
					t.Fatalf("projected record: revision %q, readiness %d", record.Revision, facet.Manifest.Host.ReadinessDeadlineMs)
				}
			}
		}
	}
	if !found {
		t.Fatal("the entry kept before program records has no record; every read that can see it refuses")
	}
	operator := rights.Subject{Account: "account", Program: "operator"}
	if out := d.changeApplicationV3(ctx, operator, "comfyui", revision, "remove"); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("removal by the read revision: %+v", out)
	}
	d = uncoveredDirectory(t, state)
	if _, ok := d.v3.entries["comfyui"]; ok {
		t.Fatal("the removed entry came back after restart")
	}
}

// A load that projects an earlier entry onto a program whose record already
// holds another facet adds only that facet. The record's pending Keep, its
// changed verification and its provider's condition stay as recorded, after
// the load and after the next write.
func TestRegistryLoadKeepsAnExistingRecordsApprovalState(t *testing.T) {
	ctx := context.Background()
	s := &applicationV3Registrations{dir: t.TempDir()}
	if _, err := s.readProgramIndex(ctx, nil); err != nil {
		t.Fatal(err)
	}
	program := registration1459Program("shared.exe")
	key := identity.NormalizeSubjectProgram(program)
	provider := registration.FacetReference{Kind: registration.FacetKindProvider, Name: "selected"}
	at := registrationTime(time.Unix(1_700_000_000, 0))
	record := registration.Registration{Key: key, Revision: strings.Repeat("7", 48), Epoch: strings.Repeat("8", 48), Source: registration.SourceOperator,
		State: registration.StatePendingApproval, IdentityEvidence: registration.IdentityEvidenceProgram, Accepted: at, Verified: at,
		Verification: registration.VerificationChanged, VerificationReason: "image:changed",
		Spec: registration.RegistrationSpec{Program: key, Servers: []registration.ServerFacet{}, Remotes: []registration.RemoteFacet{}, Applications: []registration.ApplicationFacet{},
			Providers: []registration.ProviderFacet{{Name: "selected", Arguments: []string{}, Endpoint: "selected", Services: []string{"test/service@1"}, Guarantees: []string{},
				Activation: registration.ActivationOnDemand, Resources: []string{}, Models: []registration.ScopedModel{}, RequiredExtensions: []registration.RequiredExtension{},
				Engines: []registration.EngineFact{}, Recipes: []registration.ModelRecipe{}}}}}
	approval := registrationProgramApproval{Pending: true, Question: "keep-question", Revision: record.Revision}
	s.programs[key] = record
	s.programApprovals[key] = approval
	s.programFacetSources[key] = map[registration.FacetReference]registration.Source{provider: registration.SourceOperator}
	s.programFacetConditions[key] = map[registration.FacetReference]registrationFacetCondition{provider: {Pending: true}}

	m := &registration.Manifest{Version: "applications@3", Name: "comfyui", Title: "ComfyUI — OA build",
		Host: registration.HostRecipe{Arguments: []string{"owned-host"}, ReadinessDeadlineMs: 30000}, Offers: appmodel.CommandDeclaration{Version: "appmodel@1"}}
	raw := applicationV3PriorCanonical(registration.EncodeManifest(m))
	fileRevision := strings.Repeat("5", 48)
	f := applicationV3ManifestFile{Raw: raw, Digest: applicationV3Hash(raw), Source: applicationV3Source{Program: program, FileKey: applicationV3Hash([]byte(program)), Code: identity.Code{Trusted: true, Subject: "synthetic test provenance"}, Proof: identity.ProofBound}}
	b, _ := json.Marshal(f)
	if err := os.WriteFile(s.filePath(fileRevision), b, 0o600); err != nil {
		t.Fatal(err)
	}
	s.entries = map[string]applicationV3Registration{"comfyui": {Revision: strings.Repeat("2", 48), FileRevision: fileRevision, FileHash: applicationV3Hash(b), Source: "self", State: "enabled"}}
	s.files = map[string]applicationV3ManifestFile{"comfyui": f}

	check := func(when string, v registration.Registration, got registrationProgramApproval, conditions map[registration.FacetReference]registrationFacetCondition) {
		t.Helper()
		if got != approval || v.Verification != registration.VerificationChanged || v.VerificationReason != "image:changed" || v.Epoch != record.Epoch || v.Accepted != at || !conditions[provider].Pending {
			t.Fatalf("%s reset the record: approval %+v, verification %v %q, epoch %q, provider %+v", when, got, v.Verification, v.VerificationReason, v.Epoch, conditions[provider])
		}
	}
	if err := s.projectUncoveredLocked(ctx); err != nil {
		t.Fatal(err)
	}
	v := s.programs[key]
	check("the load", v, s.programApprovals[key], s.programFacetConditions[key])
	if v.State != registration.StatePendingApproval || len(v.Spec.Applications) != 1 || s.entries["comfyui"].Revision != record.Revision {
		t.Fatalf("the load: state %v, applications %d, entry revision %q", v.State, len(v.Spec.Applications), s.entries["comfyui"].Revision)
	}
	index, err := s.encodeProgramIndex(ctx, map[string]applicationV3Registration{"comfyui": s.entries["comfyui"]})
	if err != nil {
		t.Fatal(err)
	}
	reread := &applicationV3Registrations{dir: s.dir}
	if _, err := reread.readProgramIndex(ctx, index); err != nil {
		t.Fatal(err)
	}
	check("the next write", reread.programs[key], reread.programApprovals[key], reread.programFacetConditions[key])
}
