//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	configwire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	configservice "github.com/openabstractions/abstraction-config/go/service"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

// The model host Setup declares through `models host` names model_context,
// and the person's Keep of that declaration designates the program to read
// and watch the setting without a further question (config CONTRACT.md
// CFG-X1). Before 2026-11-10 this declaration named no key, so every read
// was refused not_granted and each load ran at Automatic.
func TestModelHostSetupDeclarationReadsModelContext(t *testing.T) {
	call, closeCall := messages1608Call(t)
	defer closeCall()
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

	// Setup's declaration, as `models host` writes it.
	f := &modelHostOperatorFixture{page: registration.Page{Outcome: registration.OutcomePage}, outcome: registration.OutcomeApplied}
	writer, err := newModelHostRegistryWriter(t.Context(), f, f)
	if err != nil {
		t.Fatal(err)
	}
	w := newWaiting(0)
	defer w.stop()
	settings := hostSettings{program: program, engine: filepath.Join(t.TempDir(), "llama-server.exe"), endpoint: "modelhost"}
	var out bytes.Buffer
	if err := declareHost(w, writer, wire.DeclarationList{}, settings, []hostedModel{{Name: "qwen3.6-35b-a3b", Store: "store", Object: "object"}}, "models host", &out, false, "configured"); err != nil {
		t.Fatal(err)
	}
	if len(f.spec.Providers) != 1 || !slices.Equal(f.spec.Providers[0].ConfigKeys, []string{"model_context"}) {
		t.Fatalf("Setup's model host declaration names no model_context: %+v", f.spec.Providers)
	}

	d := registration1459Directory(t, t.TempDir())
	d.owner = subject.Account
	r := testRights(t, t.TempDir())
	if err := r.policy.RegisterAction(host.ConfigKeyReadAction); err != nil {
		t.Fatal(err)
	}
	d.programConfigKeyKnown = func(key string) bool {
		return slices.ContainsFunc(configwire.ConfigKeyDescriptors(), func(v configwire.KeyDescriptor) bool { return v.Key == key })
	}
	d.programConfigKeep = r.keepProviderConfig
	d.programConfigRemove = r.removeProviderConfig
	d.programConfigReconcile = r.retireProviderConfig
	candidate := registry3Candidate(t, "modelhost")
	candidate.Key = f.spec.Program
	candidate.Spec = f.spec
	candidate.State = registration.StatePendingApproval
	d.askKeep = func(string) error {
		record, _, _, ok := d.programSelfEntry(candidate.Key)
		if !ok || !d.setProgramQuestion(t.Context(), candidate.Key, program, "kept-question", record.Revision) {
			t.Error("Keep question not bound")
		}
		return nil
	}
	if created := d.publishProgramRegistration(t.Context(), "", true, candidate, &source, "operator"); created.Outcome != registration.OutcomeApplied {
		t.Fatal(created)
	}
	read := host.ConfigKeyReadPolicyFromRights(r.decider(), d.providerConfigReadProof)
	if err := read(t.Context(), peer, "model_context"); !errors.Is(err, configservice.ErrKeyNotDesignated) {
		t.Fatalf("a pending declaration read model_context, or was refused as other than undesignated: %v", err)
	}
	if !d.keepProgramOwned(t.Context(), candidate.Key, program, "kept-question") {
		t.Fatal("Keep refused")
	}
	if err := read(t.Context(), peer, "model_context"); err != nil {
		t.Fatalf("the kept model host is refused model_context: %v", err)
	}
	if err := read(t.Context(), peer, "prompt_cache_persist"); !errors.Is(err, configservice.ErrKeyNotDesignated) {
		t.Fatalf("a key the declaration does not name: %v, want the undesignated refusal", err)
	}
}

// The owner's R27 state: Setup's model host was kept on 2026-11-06, before
// its declaration named model_context, and no later Setup step declared it
// again. Each read was refused as undesignated (identity not_granted) and
// every load ran at Automatic. The runtime's start names the key on the kept
// record of its own model host and writes the read rule, keeping the
// record's generation and Keep.
func TestKeptModelHostBeforeContextKeyIsUpgradedAtStart(t *testing.T) {
	call, closeCall := messages1608Call(t)
	defer closeCall()
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

	f := &modelHostOperatorFixture{page: registration.Page{Outcome: registration.OutcomePage}, outcome: registration.OutcomeApplied}
	writer, err := newModelHostRegistryWriter(t.Context(), f, f)
	if err != nil {
		t.Fatal(err)
	}
	w := newWaiting(0)
	defer w.stop()
	settings := hostSettings{program: program, engine: filepath.Join(t.TempDir(), "llama-server.exe"), endpoint: "modelhost"}
	var out bytes.Buffer
	if err := declareHost(w, writer, wire.DeclarationList{}, settings, []hostedModel{{Name: "qwen3.6-35b-a3b", Store: "store", Object: "object"}}, "models host", &out, false, "configured"); err != nil {
		t.Fatal(err)
	}
	// The 2026-11-06 declaration: the same facet with no configuration key.
	f.spec.Providers[0].ConfigKeys = nil

	d := registration1459Directory(t, t.TempDir())
	d.owner = subject.Account
	r := testRights(t, t.TempDir())
	if err := r.policy.RegisterAction(host.ConfigKeyReadAction); err != nil {
		t.Fatal(err)
	}
	d.programConfigKeyKnown = func(key string) bool {
		return slices.ContainsFunc(configwire.ConfigKeyDescriptors(), func(v configwire.KeyDescriptor) bool { return v.Key == key })
	}
	d.programConfigKeep = r.keepProviderConfig
	d.programConfigRemove = r.removeProviderConfig
	d.programConfigReconcile = r.retireProviderConfig
	candidate := registry3Candidate(t, "modelhost")
	candidate.Key = f.spec.Program
	candidate.Spec = f.spec
	candidate.State = registration.StatePendingApproval
	d.askKeep = func(string) error {
		record, _, _, ok := d.programSelfEntry(candidate.Key)
		if !ok || !d.setProgramQuestion(t.Context(), candidate.Key, program, "kept-question", record.Revision) {
			t.Error("Keep question not bound")
		}
		return nil
	}
	if created := d.publishProgramRegistration(t.Context(), "", true, candidate, &source, "operator"); created.Outcome != registration.OutcomeApplied {
		t.Fatal(created)
	}
	if !d.keepProgramOwned(t.Context(), candidate.Key, program, "kept-question") {
		t.Fatal("Keep refused")
	}
	read := host.ConfigKeyReadPolicyFromRights(r.decider(), d.providerConfigReadProof)
	if err := read(t.Context(), peer, "model_context"); !errors.Is(err, configservice.ErrKeyNotDesignated) {
		t.Fatalf("the pre-key record read model_context, or was refused as other than undesignated: %v", err)
	}
	before, _, ok := keptRecord(d, candidate.Key)
	if !ok {
		t.Fatal("kept record missing")
	}

	// Another program's record is left alone.
	if err := d.upgradeModelHostContext(t.Context(), filepath.Join(t.TempDir(), "modelhostd.exe")); err != nil {
		t.Fatal(err)
	}
	if err := read(t.Context(), peer, "model_context"); !errors.Is(err, configservice.ErrKeyNotDesignated) {
		t.Fatalf("an upgrade for another program designated this one: %v", err)
	}

	if err := d.upgradeModelHostContext(t.Context(), program); err != nil {
		t.Fatal(err)
	}
	after, approval, ok := keptRecord(d, candidate.Key)
	if !ok || !slices.Equal(after.Spec.Providers[0].ConfigKeys, []string{"model_context"}) {
		t.Fatalf("upgraded record keys: %+v", after.Spec.Providers)
	}
	if after.Epoch != before.Epoch || after.State != registration.StateEnabled || approval.KeptGeneration != after.Epoch || approval.Revision != after.Revision {
		t.Fatalf("the upgrade changed the generation or the Keep: before %+v after %+v %+v", before, after, approval)
	}
	if err := read(t.Context(), peer, "model_context"); err != nil {
		t.Fatalf("the upgraded model host is refused model_context: %v", err)
	}
	if err := read(t.Context(), peer, "prompt_cache_persist"); !errors.Is(err, configservice.ErrKeyNotDesignated) {
		t.Fatalf("a key the upgrade does not name: %v, want the undesignated refusal", err)
	}
	// A second start changes nothing.
	if err := d.upgradeModelHostContext(t.Context(), program); err != nil {
		t.Fatal(err)
	}
	again, _, _ := keptRecord(d, candidate.Key)
	if again.Revision != after.Revision {
		t.Fatalf("a second upgrade rewrote the record: %s then %s", after.Revision, again.Revision)
	}
}

// keptRecord reads a program record and its Keep under the directory lock.
func keptRecord(d *applicationDirectory, key string) (registration.Registration, registrationProgramApproval, bool) {
	if !d.lock(context.Background()) {
		return registration.Registration{}, registrationProgramApproval{}, false
	}
	defer d.unlock()
	record, ok := d.v3.programRecord(key)
	return record, d.v3.programApprovals[key], ok
}
