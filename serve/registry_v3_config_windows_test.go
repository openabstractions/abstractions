//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestProviderConfigNativeKeepProofLifecycle(t *testing.T) {
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
	state := t.TempDir()
	d := registration1459Directory(t, state)
	d.owner = subject.Account
	r := testRights(t, t.TempDir())
	if err := r.policy.RegisterAction(host.ConfigKeyReadAction); err != nil {
		t.Fatal(err)
	}
	key := "logging.retention_days"
	connect := func(d *applicationDirectory) {
		d.programConfigKeyKnown = func(v string) bool { return v == key }
		d.programConfigKeep = r.keepProviderConfig
		d.programConfigRemove = r.removeProviderConfig
		d.programConfigReconcile = r.retireProviderConfig
		d.askKeep = func(handle string) error {
			record, _, _, ok := d.programSelfEntry(program)
			if !ok {
				t.Fatal("missing question input")
			}
			if !d.setProgramQuestion(t.Context(), program, program, "kept-question", record.Revision) {
				t.Fatal("question bind")
			}
			return nil
		}
	}
	connect(d)
	candidate := registry3Candidate(t, "config-native")
	candidate.Key = program
	candidate.Spec.Program = program
	candidate.State = registration.StatePendingApproval
	candidate.Spec.Providers[0].ConfigKeys = []string{key}
	created := d.publishProgramRegistration(t.Context(), "", true, candidate, &source, "operator")
	if created.Outcome != registration.OutcomeApplied {
		t.Fatal(created)
	}
	if d.providerConfigReadProof(t.Context(), peer, key) == nil {
		t.Fatal("pending declaration authorized")
	}
	if !d.keepProgramOwned(t.Context(), program, program, "kept-question") {
		t.Fatal("Keep refused")
	}
	if err := d.providerConfigReadProof(t.Context(), peer, key); err != nil {
		t.Fatal("actual native kept image refused", err)
	}
	registrationReads, err := d.providerConfigCandidates(t.Context(), program, key)
	if err != nil {
		t.Fatal(err)
	}
	registrationRead := registrationReads[0]
	process, err := peer.Process.AtLeast(identity.ProofKernel)
	if err != nil {
		t.Fatal(err)
	}
	image := &providerConfigTestHold{}
	child := &providerConfigTestChild{belongs: true, started: process.StartTime}
	p := &runtimeProviders{supervisors: map[string]*providerSupervisor{}}
	s := &providerSupervisor{p: p, file: providerFile{RegistrationKey: registrationRead.Key, Generation: registrationRead.Epoch, ProviderFacet: &candidate.Spec.Providers[0]}, child: child, configImage: image, configSource: source}
	p.supervisors[registrationRead.Facet] = s
	if err := p.managedProviderConfigProof(t.Context(), peer, registrationRead); err != nil {
		t.Fatal("managed image+job refused", err)
	}
	child.belongs = false
	if p.managedProviderConfigProof(t.Context(), peer, registrationRead) == nil {
		t.Fatal("foreign process authorized")
	}
	child.belongs = true
	s.configImage = nil
	if p.managedProviderConfigProof(t.Context(), peer, registrationRead) == nil {
		t.Fatal("PID-only custody authorized")
	}
	s.configImage = image
	image.err = errors.New("image changed")
	if p.managedProviderConfigProof(t.Context(), peer, registrationRead) == nil {
		t.Fatal("changed held image authorized")
	}
	image.err = nil
	delete(p.supervisors, registrationRead.Facet)
	if p.managedProviderConfigProof(t.Context(), peer, registrationRead) == nil {
		t.Fatal("retiring supervisor authorized")
	}
	s.finishConfigChild(false)
	if image.closed != 0 || s.child == nil || s.configImage == nil {
		t.Fatal("unconfirmed exit released custody")
	}
	s.finishConfigChild(true)
	if image.closed != 1 || s.child != nil || s.configImage != nil {
		t.Fatal("confirmed exit retained designation")
	}
	if d.providerConfigReadProof(t.Context(), peer, "foreign.key") == nil {
		t.Fatal("foreign key authorized")
	}
	if d.providerConfigReadProof(t.Context(), &identity.Peer{}, key) == nil {
		t.Fatal("unproved peer authorized")
	}
	reopened := registration1459Directory(t, state)
	reopened.owner = subject.Account
	connect(reopened)
	if err := reopened.providerConfigReadProof(t.Context(), peer, key); err != nil {
		t.Fatal("restart lost held identity declaration", err)
	}
	// Changing the captured FileKey invalidates the proof even with the same
	// native account, program spelling and receiving process.
	original := reopened.v3.programSources[program]
	changed := original
	changed.FileKey = "replaced-file-key"
	setSource := func(source applicationV3Source) {
		out := reopened.editProgramRegistration(t.Context(), func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
			s.programSources[program] = source
			record, _ := s.programRecord(program)
			return registration.Change{Outcome: registration.OutcomeApplied, Revision: record.Revision}, nil
		})
		if out.Outcome != registration.OutcomeApplied {
			t.Fatal(out)
		}
	}
	setSource(changed)
	if reopened.providerConfigReadProof(t.Context(), peer, key) == nil {
		t.Fatal("replaced FileKey authorized")
	}
	setSource(original)
	rows, _, _, _, _, err := reopened.programRegistrations(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	removed := reopened.changeProgramFacet(t.Context(), rows[0].Revision, registration.FacetReference{Kind: registration.FacetKindProvider, Name: candidate.Spec.Providers[0].Name}, nil)
	if removed.Outcome != registration.OutcomeApplied {
		t.Fatal(removed)
	}
	if reopened.providerConfigReadProof(t.Context(), peer, key) == nil {
		t.Fatal("removed facet authorized")
	}
	rule := r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: program}, host.ConfigKeyReadAction, key)
	if rule.Outcome != rwire.RuleReadOutcomeUnknown {
		t.Fatal("removed system grant retained", rule)
	}
}

type providerConfigTestHold struct {
	closed int
	err    error
}

func (h *providerConfigTestHold) Recheck(context.Context) error { return h.err }
func (h *providerConfigTestHold) Close() error                  { h.closed++; return nil }

type providerConfigTestChild struct {
	belongs bool
	started time.Time
}

func (*providerConfigTestChild) PID() int                                { return 123 }
func (*providerConfigTestChild) Stop() error                             { return nil }
func (*providerConfigTestChild) Wait() (int, error)                      { return 0, nil }
func (p *providerConfigTestChild) StartTime() time.Time                  { return p.started }
func (p *providerConfigTestChild) Contains(int, time.Time) (bool, error) { return p.belongs, nil }
