//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func registry3UnsignedPersonFixture(t *testing.T) (*runtimeRegistry, *selfRegisteredRun, inference.Subject, string) {
	t.Helper()
	run := newSelfRegisteredRun(t)
	if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	// This provider-only fixture has no application roles, matching the existing
	// answerer fixture's pure empty role owner.
	run.d.roleDecode = func([]byte, string) (bool, error) { return false, nil }
	runtimeRights := run.f.a.rights
	runtimeRights.applications = run.d
	if err := runtimeRights.policy.RegisterAction(ActionProviderManage); err != nil {
		t.Fatal(err)
	}
	if err := runtimeRights.policy.Set(run.f.panel, ActionProviderManage, "account", true); err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{dir: t.TempDir(), disabledPath: filepath.Join(t.TempDir(), "disabled.json"), report: func(err error) { t.Log(err) }, supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{})}
	p.registrationDirectory = func() *applicationDirectory { return run.d }
	r := &runtimeRegistry{providers: p, operator: &inferenceOperator{providers: p, credentials: &runtimeCredentials{runtimeRights: runtimeRights}}}
	return r, run, inference.Subject{Account: run.f.panel.Account, Program: run.f.panel.Program}, copyTestBinaryTo(t, t.TempDir(), "unsigned-provider")
}

func registry3RegisterUnsigned(t *testing.T, r *runtimeRegistry, run *selfRegisteredRun, caller inference.Subject, program string) registration.Registration {
	t.Helper()
	candidate := registry3Candidate(t, "unsigned")
	candidate.Spec.Program = program
	out := r.registerProgram(run.ctx, caller, "", true, candidate.Spec, fileid.Self)
	if out.Outcome != registration.OutcomeApplied {
		t.Fatal("supported unsigned image refused", out)
	}
	rows, _, _, _, _, err := run.d.programRegistrations(run.ctx)
	if err != nil || len(rows) != 1 || rows[0].State != registration.StatePendingApproval || rows[0].Verification != registration.VerificationUnverified || rows[0].VerificationReason != "identity:unsigned" || rows[0].Publisher != nil || len(rows[0].Checks) != 0 {
		t.Fatal("unsigned admission evidence", rows, err)
	}
	_, source, _, ok := run.d.programSelfEntry(rows[0].Key)
	if !ok || source.FileKey != identity.ProgramKey(program) || source.Proof != identity.ProofUnsigned || source.Code.Trusted {
		t.Fatal("unsigned captured identity", source, ok)
	}
	return rows[0]
}

func TestRegistryV3NativeUnsignedRequiresActualPersonKeep(t *testing.T) {
	r, run, caller, program := registry3UnsignedPersonFixture(t)
	if held, err := openTrustedApplicationImage(run.ctx, fileid.Self, program); err == nil {
		_ = held.Close()
		t.Fatal("Assistant trusted-image gate admitted unsigned image")
	}
	record := registry3RegisterUnsigned(t, r, run, caller, program)
	q := programKeepQuestion(t, run, record.Key)
	if q.Publisher != "" || q.PublisherTrusted {
		t.Fatal("unsigned publisher fabricated", q)
	}
	run.f.enroll(t, run.f.answerer)
	if _, err := run.f.a.authorizeAnswerer(run.ctx, answerer{subject: run.f.answerer}, q, run.f.a.scope, "keep"); err == nil {
		t.Fatal("agent answered person Keep")
	}
	answer(t, run.f, q.ID, "keep")
	waitDirectory(t, run.d, func() bool {
		v, ok := programRegistrationNow(run.d, record.Key)
		return ok && v.State == registration.StateEnabled
	})
	rows, _, _, _, _, err := run.d.programRegistrations(run.ctx)
	if err != nil || len(rows) != 1 || rows[0].Verification != registration.VerificationUnverified || rows[0].Publisher != nil || len(rows[0].Checks) != 0 {
		t.Fatal("Keep invented verification/attestation", rows, err)
	}
	if len(r.providers.supervisors) != 0 || len(run.d.descriptors) != 0 {
		t.Fatal("unsigned Keep executed a provider or invented app facet")
	}
}

func TestRegistryV3NativeUnsignedPersonDeclineHasNoExecutionEffect(t *testing.T) {
	r, run, caller, program := registry3UnsignedPersonFixture(t)
	record := registry3RegisterUnsigned(t, r, run, caller, program)
	q := programKeepQuestion(t, run, record.Key)
	answer(t, run.f, q.ID, "remove")
	waitDirectory(t, run.d, func() bool { _, ok := programRegistrationNow(run.d, record.Key); return !ok })
	if len(r.providers.supervisors) != 0 || len(run.d.descriptors) != 0 {
		t.Fatal("decline executed provider or invented app facet")
	}
	if run.d.keepProgramOwned(run.ctx, record.Key, program, q.ID) {
		t.Fatal("declined image consumed Keep")
	}
}

func TestRegistryV3NativeUnsignedReplacementCannotConsumeOldKeep(t *testing.T) {
	r, run, caller, program := registry3UnsignedPersonFixture(t)
	record := registry3RegisterUnsigned(t, r, run, caller, program)
	q := programKeepQuestion(t, run, record.Key)
	if err := os.Rename(program, program+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("different unsigned object"), 0600); err != nil {
		t.Fatal(err)
	}
	answer(t, run.f, q.ID, "keep")
	waitUnfollowed(t, run.k, q.ID)
	current, ok := programRegistrationNow(run.d, record.Key)
	if !ok || current.State != registration.StatePendingApproval || current.Revision != record.Revision || len(r.providers.supervisors) != 0 {
		t.Fatal("replacement consumed original image Keep", current)
	}
}

func TestRegistryV3NativeImageAdmissionRejectsUnreadableAndReparse(t *testing.T) {
	r, caller := registryObserverFixture(t)
	d := registration1459Directory(t, t.TempDir())
	r.operator.credentials.runtimeRights.applications = d
	real := t.TempDir()
	program := filepath.Join(real, "unsigned.exe")
	if err := os.WriteFile(program, []byte("ordinary unsigned fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "junction")
	junction(t, link, real)
	for _, name := range []string{"unreadable", "reparse"} {
		t.Run(name, func(t *testing.T) {
			input := program
			if name == "unreadable" {
				writing, err := os.OpenFile(program, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer writing.Close()
			} else {
				input = filepath.Join(link, "unsigned.exe")
			}
			candidate := registry3Candidate(t, "refused")
			candidate.Spec.Program = input
			out := r.registerProgram(t.Context(), caller, "", true, candidate.Spec, fileid.Self)
			if out.Outcome != registration.OutcomeForbidden || out.Reason != "identity:probe" || out.Refusal == nil || out.Refusal.Source != rights.RefusalSourceIdentity {
				t.Fatal("unsafe image admitted", out)
			}
			rows, _, _, _, _, err := d.programRegistrations(t.Context())
			if err != nil || len(rows) != 0 || len(d.programSelfEntries()) != 0 {
				t.Fatal("refusal published state or Keep", rows, err)
			}
		})
	}
}

func TestRegistryV3NativeImageHoldPreventsReplacement(t *testing.T) {
	program := filepath.Join(t.TempDir(), "unsigned.exe")
	if err := os.WriteFile(program, []byte("unsigned fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	held, err := openRegistryProgramImage(context.Background(), fileid.Self, program)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if os.Rename(program, program+".replaced") == nil {
		t.Fatal("held identity allowed locator replacement")
	}
	if err := held.Recheck(t.Context()); err != nil {
		t.Fatal("unchanged held identity", err)
	}
}

func TestRegistryV3NativeOnlySupportedCodeVerdictsAdmit(t *testing.T) {
	for _, proof := range []identity.Proof{identity.ProofNone, identity.ProofInvalid, identity.ProofUnmet} {
		if registrationProgramCodeAccepted(identity.Code{}, proof) {
			t.Fatal("negative proof admitted", proof)
		}
	}
	if registrationProgramCodeAccepted(identity.Code{Trusted: true}, identity.ProofUnsigned) || registrationProgramCodeAccepted(identity.Code{}, identity.ProofBound) {
		t.Fatal("inconsistent proof admitted")
	}
}
