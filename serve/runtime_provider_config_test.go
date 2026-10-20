package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestProviderConfigFactoryRemovalRestartAndRevocation(t *testing.T) {
	for _, mode := range []string{"system-remove", "person-revoke", "person-deny", "foreign-permit", "unrelated-edit"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			state := t.TempDir()
			r := testRights(t, state)
			if err := r.policy.RegisterAction(host.ConfigKeyReadAction); err != nil {
				t.Fatal(err)
			}
			program := filepath.Join(t.TempDir(), "provider.exe")
			key := "logging.retention_days"
			subject := rwire.Subject{Account: r.owner, Program: program}
			if err := r.keepProviderConfig(ctx, program, []string{key}); err != nil {
				t.Fatal(err)
			}
			current := r.policy.ReadRule(subject, host.ConfigKeyReadAction, key)
			if current.Outcome != rwire.RuleReadOutcomeFound || current.Record.SetBy != r.self() {
				t.Fatal(current)
			}
			switch mode {
			case "person-revoke", "person-deny", "foreign-permit":
				if mode == "foreign-permit" {
					deny := false
					edit, err := r.policy.ChangeRule(current.Revision, subject, host.ConfigKeyReadAction, key, rights.RuleEdit{Permit: &deny, By: rwire.Subject{Account: r.owner, Program: r.operators[1]}}, func() error { return nil })
					if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
						t.Fatal(edit, err)
					}
					current = r.policy.ReadRule(subject, host.ConfigKeyReadAction, key)
				}
				var permit *bool
				if mode != "person-revoke" {
					v := mode == "foreign-permit"
					permit = &v
				}
				edit, err := r.policy.ChangeRule(current.Revision, subject, host.ConfigKeyReadAction, key, rights.RuleEdit{Permit: permit, By: rwire.Subject{Account: r.owner, Program: r.operators[1]}, Exact: permit == nil}, func() error { return nil })
				if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
					t.Fatal(edit, err)
				}
			}
			if err := r.removeProviderConfig(ctx, program); err != nil {
				t.Fatal(err)
			}
			after := r.policy.ReadRule(subject, host.ConfigKeyReadAction, key)
			if mode == "foreign-permit" || mode == "person-deny" {
				if after.Outcome != rwire.RuleReadOutcomeFound || after.Record.SetBy == r.self() {
					t.Fatal("person rule changed", after)
				}
			} else if after.Outcome != rwire.RuleReadOutcomeUnknown {
				t.Fatal("system row retained", after)
			}
			if mode == "unrelated-edit" {
				permit := false
				edit, err := r.policy.ChangeRule(after.Revision, subject, host.ConfigEditAction, host.ConfigEditResource, rights.RuleEdit{Permit: &permit, By: r.self()}, func() error { return nil })
				if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
					t.Fatal(edit, err)
				}
			}
			reopened := testRights(t, state)
			if err := reopened.keepProviderConfig(ctx, program, []string{key}); err != nil {
				t.Fatal(err)
			}
			final := reopened.policy.ReadRule(subject, host.ConfigKeyReadAction, key)
			if mode == "system-remove" {
				if final.Outcome != rwire.RuleReadOutcomeFound || !final.Record.Rule.Permit || final.Record.SetBy != r.self() {
					t.Fatal("legitimate Keep did not restore", final)
				}
			} else if mode == "person-revoke" || mode == "unrelated-edit" {
				if final.Outcome != rwire.RuleReadOutcomeUnknown {
					t.Fatal("revocation/uncertainty regranted", final)
				}
			} else if final.Outcome != rwire.RuleReadOutcomeFound || final.Record.Rule.Permit != (mode == "foreign-permit") {
				t.Fatal(final)
			}
		})
	}
}

func TestProviderConfigMultiKeySystemRemovalCanRestore(t *testing.T) {
	r := testRights(t, t.TempDir())
	if err := r.policy.RegisterAction(host.ConfigKeyReadAction); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "provider.exe")
	keys := []string{"logging.retention_days", "logging.enabled"}
	if err := r.keepProviderConfig(t.Context(), program, keys); err != nil {
		t.Fatal(err)
	}
	if err := r.removeProviderConfig(t.Context(), program); err != nil {
		t.Fatal(err)
	}
	if err := r.keepProviderConfig(t.Context(), program, keys); err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		got := r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: program}, host.ConfigKeyReadAction, key)
		if got.Outcome != rwire.RuleReadOutcomeFound || !got.Record.Rule.Permit {
			t.Fatal(key, got)
		}
	}
}

func TestProviderConfigRetirementPreservesIndependentOperatorDefaults(t *testing.T) {
	for _, mode := range []string{"unowned", "overlap", "denied", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			state := t.TempDir()
			r := testRights(t, state)
			if err := r.policy.RegisterAction(host.ConfigKeyReadAction); err != nil {
				t.Fatal(err)
			}
			program := r.operators[0]
			key := "logging.enabled"
			subject := rwire.Subject{Account: r.owner, Program: program}
			if err := r.installFor([]string{program}, []installationRule{{host.ConfigKeyReadAction, key}}); err != nil {
				t.Fatal(err)
			}
			if mode != "unowned" {
				if err := r.keepProviderConfig(t.Context(), program, []string{key}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "denied" || mode == "revoked" {
				current := r.policy.ReadRule(subject, host.ConfigKeyReadAction, key)
				var permit *bool
				if mode == "denied" {
					deny := false
					permit = &deny
				}
				edit, err := r.policy.ChangeRule(current.Revision, subject, host.ConfigKeyReadAction, key, rights.RuleEdit{Permit: permit, By: rwire.Subject{Account: r.owner, Program: r.operators[1]}, Exact: permit == nil}, func() error { return nil })
				if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
					t.Fatal(edit, err)
				}
			}
			reopened := testRights(t, state)
			// Config's current composition supplies this lookup beside its explicit
			// operator rule installation, after checking typed-reader enablement.
			reopened.configReadDefault = func(p, k string) bool { return reopened.isOperator(p) && k == key }
			if err := reopened.retireProviderConfig(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			if err := reopened.removeProviderConfig(t.Context(), program); err != nil {
				t.Fatal(err)
			}
			got := reopened.policy.ReadRule(subject, host.ConfigKeyReadAction, key)
			if mode == "revoked" {
				if got.Outcome != rwire.RuleReadOutcomeUnknown {
					t.Fatal("person revoke changed", got)
				}
			} else if got.Outcome != rwire.RuleReadOutcomeFound || got.Record.Rule.Permit != (mode != "denied") {
				t.Fatal("independent default changed", got)
			}
			if mode == "unowned" && len(readMarker(t, reopened).ProviderConfig) != 0 {
				t.Fatal("operator installation became provider custody")
			}
		})
	}
}

func TestProviderConfigRemovePreservesOtherProviderBeforeAppliedMarker(t *testing.T) {
	r := testRights(t, t.TempDir())
	if err := r.policy.RegisterAction(host.ConfigKeyReadAction); err != nil {
		t.Fatal(err)
	}
	a, b := filepath.Join(t.TempDir(), "a.exe"), filepath.Join(t.TempDir(), "b.exe")
	key := "logging.enabled"
	for _, program := range []string{a, b} {
		if err := r.keepProviderConfig(t.Context(), program, []string{key}); err != nil {
			t.Fatal(err)
		}
	}
	// B's receipt precedes its policy write; Applied follows it. Reproduce
	// interruption between those writes while retaining B's actual factory row.
	marker := readMarker(t, r)
	applied := marker.Applied[:0]
	for _, target := range marker.Applied {
		if target.Program != b {
			applied = append(applied, target)
		}
	}
	marker.Applied = applied
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.marker, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.removeProviderConfig(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	for _, program := range []string{a, b} {
		got := r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: program}, host.ConfigKeyReadAction, key)
		if program == a && got.Outcome != rwire.RuleReadOutcomeUnknown {
			t.Fatal("removed A retained", got)
		}
		if program == b && (got.Outcome != rwire.RuleReadOutcomeFound || !got.Record.Rule.Permit) {
			t.Fatal("other provider's in-flight row retired", got)
		}
	}
}

func TestProviderConfigKeepReplacementAndRestartCleanup(t *testing.T) {
	state := t.TempDir()
	d := registration1459Directory(t, state)
	r := testRights(t, t.TempDir())
	if err := r.policy.RegisterAction(host.ConfigKeyReadAction); err != nil {
		t.Fatal(err)
	}
	candidate := registry3Candidate(t, "config-replace")
	program := filepath.Join(t.TempDir(), "provider.exe")
	if err := os.WriteFile(program, []byte("inert config registration fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	candidate.Key = program
	candidate.Spec.Program = program
	candidate.State = registration.StatePendingApproval
	oldKey, newKey := "logging.enabled", "logging.retention_days"
	candidate.Spec.Providers[0].ConfigKeys = []string{oldKey}
	source := applicationV3Source{Program: program, FileKey: identity.ProgramKey(program), Code: identity.Code{Trusted: true, Subject: "synthetic test provenance"}, Proof: identity.ProofBound}
	connect := func(d *applicationDirectory) {
		d.programKeepImage = registry3SyntheticKeepImage
		d.programConfigKeyKnown = func(k string) bool { return k == oldKey || k == newKey }
		d.programConfigKeep = r.keepProviderConfig
		d.programConfigRemove = r.removeProviderConfig
		d.programConfigReconcile = r.retireProviderConfig
		d.askKeep = func(string) error {
			record, _, _, ok := d.programSelfEntry(program)
			if !ok {
				t.Fatal("pending input absent")
			}
			if !d.setProgramQuestion(t.Context(), program, program, "current-question", record.Revision) {
				t.Fatal("question binding")
			}
			return nil
		}
	}
	connect(d)
	first := d.publishProgramRegistration(t.Context(), "", true, candidate, &source, "operator")
	if first.Outcome != registration.OutcomeApplied {
		t.Fatal(first)
	}
	d.programConfigKeep = func(ctx context.Context, p string, keys []string) error {
		if err := r.keepProviderConfig(ctx, p, keys); err != nil {
			return err
		}
		record, _, _, _ := d.programSelfEntry(program)
		replacement := candidate
		replacement.Spec.Providers = append([]registration.ProviderFacet(nil), candidate.Spec.Providers...)
		replacement.Spec.Providers[0].ConfigKeys = []string{newKey}
		got := d.publishProgramRegistration(ctx, record.Revision, false, replacement, &source, "operator")
		if got.Outcome != registration.OutcomeApplied {
			t.Fatal(got)
		}
		return nil
	}
	if d.keepProgramOwned(t.Context(), program, program, "current-question") {
		t.Fatal("stale Keep approved replacement")
	}
	if _, err := d.providerConfigCandidates(t.Context(), program, newKey); err == nil {
		t.Fatal("replacement pending proof accepted")
	}
	reopened := registration1459Directory(t, state)
	connect(reopened)
	if err := reopened.reconcileProviderConfigGrants(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reopened.keepProgramOwned(t.Context(), program, program, "current-question") {
		t.Fatal("replacement Keep refused")
	}
	subject := rwire.Subject{Account: r.owner, Program: program}
	if got := r.policy.ReadRule(subject, host.ConfigKeyReadAction, oldKey); got.Outcome != rwire.RuleReadOutcomeUnknown {
		t.Fatal("retired key retained", got)
	}
	if got := r.policy.ReadRule(subject, host.ConfigKeyReadAction, newKey); got.Outcome != rwire.RuleReadOutcomeFound {
		t.Fatal("new key missing", got)
	}
	for _, keys := range [][]string{{"future.key"}, {newKey, newKey}} {
		spec := candidate.Spec
		spec.Providers = append([]registration.ProviderFacet(nil), spec.Providers...)
		spec.Providers[0].ConfigKeys = keys
		if _, err := providerConfigKeys(spec, reopened.programConfigKeyKnown); err == nil {
			t.Fatal("unknown/duplicate keys accepted", keys)
		}
	}
}

func TestProviderConfigRemovalFaultDoesNotInventRestoration(t *testing.T) {
	r := testRights(t, t.TempDir())
	if err := r.policy.RegisterAction(host.ConfigKeyReadAction); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "provider.exe")
	key := "logging.retention_days"
	if err := r.keepProviderConfig(t.Context(), program, []string{key}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.removeProviderConfig(ctx, program); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(readMarker(t, r).ConfigRemoved) != 0 {
		t.Fatal("failed removal recorded")
	}
	current := r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: program}, host.ConfigKeyReadAction, key)
	if current.Outcome != rwire.RuleReadOutcomeFound {
		t.Fatal(current)
	}
	if err := r.removeProviderConfig(t.Context(), program); err != nil {
		t.Fatal(err)
	}
	// Simulate interruption after policy deletion and before recording the
	// successful system removal. Applied remains; absence alone grants nothing.
	marker := readMarker(t, r)
	marker.ConfigRemoved = nil
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.marker, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.keepProviderConfig(t.Context(), program, []string{key}); err != nil {
		t.Fatal(err)
	}
	if got := r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: program}, host.ConfigKeyReadAction, key); got.Outcome != rwire.RuleReadOutcomeUnknown {
		t.Fatal("uncertain deletion invented restoration", got)
	}
}

func TestProviderConfigRemoveBeforeDelayedKeepGrantRetiresOrphan(t *testing.T) {
	for _, mode := range []string{"active", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			d := registration1459Directory(t, t.TempDir())
			r := testRights(t, t.TempDir())
			if err := r.policy.RegisterAction(host.ConfigKeyReadAction); err != nil {
				t.Fatal(err)
			}
			key := "logging.enabled"
			program := filepath.Join(t.TempDir(), "provider.exe")
			if err := os.WriteFile(program, []byte("inert registration fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			source := applicationV3Source{Program: program, FileKey: identity.ProgramKey(program), Code: identity.Code{Trusted: true, Subject: "synthetic test provenance"}, Proof: identity.ProofBound}
			d.programKeepImage = registry3SyntheticKeepImage
			d.programConfigKeyKnown = func(k string) bool { return k == key }
			d.programConfigReconcile = r.retireProviderConfig
			d.askKeep = func(string) error {
				record, _, _, _ := d.programSelfEntry(program)
				if !d.setProgramQuestion(t.Context(), program, program, "question", record.Revision) {
					t.Fatal("question binding")
				}
				return nil
			}
			candidate := registry3Candidate(t, "late-keep")
			candidate.Key = program
			candidate.Spec.Program = program
			candidate.State = registration.StatePendingApproval
			candidate.Spec.Providers[0].ConfigKeys = []string{key}
			if err := r.keepProviderConfig(t.Context(), program, []string{key}); err != nil {
				t.Fatal(err)
			}
			if err := r.removeProviderConfig(t.Context(), program); err != nil {
				t.Fatal(err)
			}
			if out := d.publishProgramRegistration(t.Context(), "", true, candidate, &source, "operator"); out.Outcome != registration.OutcomeApplied {
				t.Fatal(out)
			}
			attempt, cancel := context.WithCancel(t.Context())
			defer cancel()
			d.programConfigKeep = func(ctx context.Context, p string, keys []string) error {
				record, _, _, _ := d.programSelfEntry(program)
				out := d.changeProgramFacet(ctx, record.Revision, registration.FacetReference{Kind: registration.FacetKindProvider, Name: candidate.Spec.Providers[0].Name}, nil)
				if out.Outcome != registration.OutcomeApplied {
					t.Fatal(out)
				}
				if err := r.keepProviderConfig(ctx, p, keys); err != nil {
					return err
				}
				if got := r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: program}, host.ConfigKeyReadAction, key); got.Outcome != rwire.RuleReadOutcomeFound {
					t.Fatal("counterexample did not restore late row", got)
				}
				if mode == "cancelled" {
					cancel()
				}
				return nil
			}
			if d.keepProgramOwned(attempt, program, program, "question") {
				t.Fatal("removed registration accepted stale Keep")
			}
			if got := r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: program}, host.ConfigKeyReadAction, key); got.Outcome != rwire.RuleReadOutcomeUnknown {
				t.Fatal("failed Keep retained orphan row", got)
			}
		})
	}
}
