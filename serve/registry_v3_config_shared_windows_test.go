//go:build windows

package main

import (
	"os"
	"testing"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

func TestProviderConfigManagedFacetsShareKeyWithExactChildCustody(t *testing.T) {
	call, _ := messages1608Call(t)
	peer, err := call.Peer()
	if err != nil {
		t.Fatal(err)
	}
	subject, err := identity.SubjectOf(peer, listen.Program)
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	held, err := openRegistryProgramImage(t.Context(), peer, program)
	if err != nil {
		t.Fatal(err)
	}
	source := applicationV3Source{Program: program, FileKey: applicationV3HeldKey(held), Code: held.Code, Proof: held.Proof}
	held.Close()
	d := registration1459Directory(t, t.TempDir())
	d.owner = subject.Account
	r := testRights(t, t.TempDir())
	if err := r.policy.RegisterAction(host.ConfigKeyReadAction); err != nil {
		t.Fatal(err)
	}
	key := "logging.enabled"
	d.programConfigKeyKnown = func(k string) bool { return k == key }
	d.programConfigKeep = r.keepProviderConfig
	d.programConfigReconcile = r.retireProviderConfig
	p := &runtimeProviders{supervisors: map[string]*providerSupervisor{}}
	d.programConfigManaged = p.managedProviderConfigProof
	candidate := registry3Candidate(t, "shared-first")
	candidate.Key = program
	candidate.Spec.Program = program
	candidate.Source = registration.SourceInstallation
	candidate.Spec.Providers[0].ConfigKeys = []string{key}
	second := candidate.Spec.Providers[0]
	second.Name = "shared-second"
	second.Endpoint = second.Name
	candidate.Spec.Providers = append(candidate.Spec.Providers, second)
	created := d.publishProgramRegistration(t.Context(), "", true, candidate, &source, "installation")
	if created.Outcome != registration.OutcomeApplied {
		t.Fatal(created)
	}
	// Fixture supplies the protected producer's accepted declaration state.
	// Actual native peer, image capture, launch holds and factory remain real.
	ready := d.editProgramRegistration(t.Context(), func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		record, _ := s.programRecord(program)
		record.State = registration.StateEnabled
		s.programs[program] = record
		s.programApprovals[program] = registrationProgramApproval{}
		for _, facet := range record.Spec.Providers {
			s.programFacetConditions[program][registration.FacetReference{Kind: registration.FacetKindProvider, Name: facet.Name}] = registrationFacetCondition{}
		}
		return registration.Change{Outcome: registration.OutcomeApplied, Revision: record.Revision}, nil
	})
	if ready.Outcome != registration.OutcomeApplied {
		t.Fatal(ready)
	}
	rows, _, _, _, _, err := d.programRegistrations(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	process, err := peer.Process.AtLeast(identity.ProofKernel)
	if err != nil {
		t.Fatal(err)
	}
	var children []*providerConfigTestChild
	for _, file := range registrationProviderFiles(rows[0]) {
		image, captured, err := d.providerConfigLaunchImage(t.Context(), file)
		if err != nil || image == nil {
			t.Fatal("exact facet launch hold", file.ProviderFacet.Name, err)
		}
		child := &providerConfigTestChild{started: process.StartTime}
		children = append(children, child)
		s := &providerSupervisor{p: p, file: file, child: child, configImage: image, configSource: captured}
		p.supervisors[file.ProviderFacet.Name] = s
		t.Cleanup(func() { s.finishConfigChild(true) })
	}
	if len(children) != 2 {
		t.Fatal(children)
	}
	for i := range children {
		for j := range children {
			children[j].belongs = i == j
		}
		if err := d.providerConfigReadProof(t.Context(), peer, key); err != nil {
			t.Fatal("shared key rejected actual matching child", i, err)
		}
	}
	for _, child := range children {
		child.belongs = false
	}
	if d.providerConfigReadProof(t.Context(), peer, key) == nil {
		t.Fatal("foreign child received shared key")
	}
}
