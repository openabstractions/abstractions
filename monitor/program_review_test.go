package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
)

type programReviewFixture struct {
	record               registration.Registration
	calls                []string
	sent                 registration.RegistrationSpec
	expected             string
	conflict             bool
	staleImage           bool
	admissionUnavailable bool
}

func (f *programReviewFixture) Registrations(context.Context, string, int64) (registration.Page, error) {
	f.calls = append(f.calls, "read")
	return registration.Page{Outcome: registration.OutcomePage, Revision: "book", Registrations: []registration.Registration{f.record}}, nil
}
func (f *programReviewFixture) Replace(_ context.Context, expected string, spec registration.RegistrationSpec) (registration.Change, error) {
	f.calls = append(f.calls, "replace")
	f.sent, f.expected = spec, expected
	if f.conflict {
		return registration.Change{Outcome: registration.OutcomeConflict, Reason: "identity:changed"}, nil
	}
	f.record.Revision = "fresh-image"
	f.record.State = registration.StatePendingApproval
	f.record.Verification = registration.VerificationUnverified
	if f.staleImage {
		f.record.Verification = registration.VerificationChanged
	}
	change := registration.Change{Outcome: registration.OutcomeApplied, Revision: f.record.Revision}
	if f.admissionUnavailable {
		change.Reason = "approval:unavailable"
		f.record.Reason = change.Reason
	}
	return change, nil
}

func TestProgramReviewNativeAdmissionFailureOffersExplicitRetry(t *testing.T) {
	f := newProgramReviewFixture()
	f.admissionUnavailable = true
	request := programReviewRequest{Action: "review", Key: f.record.Key, Revision: f.record.Revision}
	result, err := reviewProgram(t.Context(), f, request)
	if err != nil || result.Outcome != "unavailable" || result.Record == nil {
		t.Fatal(result, err)
	}
	f.admissionUnavailable = false
	f.record.Reason = "approval:unavailable"
	f.calls = nil
	request.Revision = f.record.Revision
	result, err = reviewProgram(t.Context(), f, request)
	if err != nil || result.Outcome != "pending" || !reflect.DeepEqual(f.calls, []string{"read", "replace", "observe", "read"}) {
		t.Fatal(result, err, f.calls)
	}
}
func (f *programReviewFixture) Observe(context.Context, string, time.Duration) (registration.Page, error) {
	f.calls = append(f.calls, "observe")
	return registration.Page{Outcome: registration.OutcomePage}, nil
}

func newProgramReviewFixture() *programReviewFixture {
	return &programReviewFixture{record: registration.Registration{
		Key: "program-key", Revision: "old-image", State: registration.StateEnabled,
		IdentityEvidence: registration.IdentityEvidenceProgram, Verification: registration.VerificationChanged,
		Spec: registration.RegistrationSpec{Program: `C:\fixture\provider.exe`,
			Providers: []registration.ProviderFacet{{Name: "inventory", Arguments: []string{"serve", "--config", "owned-config"},
				Services: []string{"abstraction.model/inventory@1"}, Resources: []string{"store:approved"},
				ConfigKeys: []string{"owned-key"}}},
			Servers: []registration.ServerFacet{{Name: "server", Base: "http://127.0.0.1:1234", Kind: "ollama", Modalities: []string{"chat"}}},
		},
	}}
}

func TestProgramReviewPreservesFullSpecAndWaitsForNativeKeepReadback(t *testing.T) {
	f := newProgramReviewFixture()
	original := f.record.Spec
	got, err := reviewProgram(t.Context(), f, programReviewRequest{Action: "review", Key: f.record.Key, Revision: f.record.Revision})
	if err != nil || got.Outcome != "pending" || got.Record == nil || got.Record.State != registration.StatePendingApproval {
		t.Fatalf("review = %+v, %v", got, err)
	}
	if !reflect.DeepEqual(f.sent, original) || f.expected != "old-image" ||
		!reflect.DeepEqual(f.calls, []string{"read", "replace", "observe", "read"}) {
		t.Fatalf("conditional full-spec path: expected=%q sent=%+v calls=%v", f.expected, f.sent, f.calls)
	}
}

// A pending program nobody asked about yet waits for the person's Review
// click, which replaces it so the registry asks (VISION 2026-10-06, amended
// 2026-11-10).
func TestProgramReviewAsksAboutAnUnaskedPendingProgram(t *testing.T) {
	f := newProgramReviewFixture()
	f.record.State, f.record.Verification, f.record.Reason = registration.StatePendingApproval, registration.VerificationUnverified, "approval:pending"
	got, err := reviewProgram(t.Context(), f, programReviewRequest{Action: "review", Key: f.record.Key, Revision: f.record.Revision})
	if err != nil || got.Outcome != "pending" || !reflect.DeepEqual(f.calls, []string{"read", "replace", "observe", "read"}) {
		t.Fatalf("review = %+v, %v, calls %v", got, err, f.calls)
	}
}

func TestProgramReviewRepairUsesNormalSetupAndPreservesRuntimeBinding(t *testing.T) {
	command := programRepairPanelCommand(`C:\fixture\panel.exe`, &explicitRuntime{Endpoint: "fixture-endpoint", Program: `C:\fixture\runtime.exe`, StateDir: `C:\fixture\state`})
	want := []string{`C:\fixture\panel.exe`, "-native", "-page", "setup", "-quiet", "-addr", "127.0.0.1:0", "-runtime-endpoint", "fixture-endpoint", "-runtime-program", `C:\fixture\runtime.exe`, "-runtime-state-dir", `C:\fixture\state`}
	if !reflect.DeepEqual(command.Args, want) {
		t.Fatal(command.Args)
	}
}

func TestProgramReviewFencesStaleDisabledPendingAndUnsupportedRecords(t *testing.T) {
	for _, mode := range []string{"stale-revision", "disabled", "pending", "application-facet", "replace-conflict", "stale-image-readback"} {
		t.Run(mode, func(t *testing.T) {
			f := newProgramReviewFixture()
			request := programReviewRequest{Action: "review", Key: f.record.Key, Revision: f.record.Revision}
			want := "conflict"
			switch mode {
			case "stale-revision":
				request.Revision = "foreign-revision"
			case "disabled":
				f.record.State = registration.StateDisabled
			case "pending":
				f.record.State = registration.StatePendingApproval
				f.record.Verification = registration.VerificationUnverified
				f.record.Reason = "Review the open OpenAbstractions question q-1."
				want = "pending"
			case "application-facet":
				f.record.Spec.Applications = []registration.ApplicationFacet{{Name: "native-app"}}
				want = "unsupported"
			case "replace-conflict":
				f.conflict = true
			case "stale-image-readback":
				f.staleImage = true
			}
			got, err := reviewProgram(t.Context(), f, request)
			if err != nil || got.Outcome != want {
				t.Fatalf("review=%+v err=%v", got, err)
			}
			if mode != "replace-conflict" && mode != "stale-image-readback" && !reflect.DeepEqual(f.calls, []string{"read"}) {
				t.Fatalf("refusal or existing Keep dispatched another edit: %v", f.calls)
			}
		})
	}
}
