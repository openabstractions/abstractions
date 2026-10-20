package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
)

// Existing CAS/question fixtures supply synthetic signing evidence explicitly.
// Their injected image owner still checks actual file identity and holds it;
// native admission/Keep tests use the default verifier, never this seam.
func registry3SyntheticKeepImage(ctx context.Context, source applicationV3Source) (activationImageHold, error) {
	return holdActivationImage(ctx, applicationAgentImage{Program: source.Program, Key: source.FileKey})
}

// The private receiving seam supplies explicitly synthetic signing provenance.
// These tests prove CAS/question custody; the real person-only Asks corpus and
// platform image admission have their own owners and positive evidence.
func TestRegistryV3ProgramKeepHooksPersistAndFenceReplacement(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	d := registration1459Directory(t, state)
	candidate := registry3Candidate(t, "keeper")
	candidate.Spec.Program = filepath.Join(t.TempDir(), "provider.exe")
	candidate.Key = candidate.Spec.Program
	candidate.State = registration.StatePendingApproval
	if err := os.WriteFile(candidate.Spec.Program, []byte("inert registration fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	image := &applicationV3Source{Program: candidate.Spec.Program, FileKey: identity.ProgramKey(candidate.Spec.Program), Code: identity.Code{Trusted: true, Subject: "synthetic test provenance"}, Proof: identity.ProofBound}
	if image.FileKey == "" {
		t.Fatal("actual fixture file identity unavailable")
	}
	questions := 0
	connect := func(directory *applicationDirectory) {
		directory.programKeepImage = registry3SyntheticKeepImage
		directory.askKeep = func(handle string) error {
			if handle != "program:"+candidate.Key {
				t.Fatalf("question scope %q", handle)
			}
			record, source, mark, ok := directory.programSelfEntry(candidate.Key)
			if !ok || record.State != registration.StatePendingApproval || source.FileKey != image.FileKey || source.Code.Subject != image.Code.Subject || mark.registrationRevision() != record.Revision {
				t.Fatal("captured pending program", record, source, mark, ok)
			}
			questions++
			question := "question-" + strings.Repeat("x", questions)
			if !directory.setProgramQuestion(ctx, candidate.Key, image.Program, question, record.Revision) {
				t.Fatal("question binding")
			}
			return nil
		}
	}
	connect(d)
	first := d.publishProgramRegistration(ctx, "", true, candidate, image, "operator")
	if first.Outcome != registration.OutcomeApplied || questions != 1 || len(d.descriptors) != 0 {
		t.Fatal(first, questions, d.descriptors)
	}
	reopened := registration1459Directory(t, state)
	connect(reopened)
	_, source, mark, ok := reopened.programSelfEntry(candidate.Key)
	if !ok || mark.Question != "question-x" || source.FileKey != image.FileKey {
		t.Fatal("restart lost question/source", mark, source, ok)
	}
	if !reopened.keepProgramOwned(ctx, candidate.Key, image.Program, mark.Question) {
		t.Fatal("current Keep refused")
	}
	rows, _, _, _, _, err := reopened.programRegistrations(ctx)
	if err != nil || len(rows) != 1 || rows[0].State != registration.StateEnabled || len(reopened.programSelfEntries()) != 0 {
		t.Fatal("Keep intent", rows, err)
	}
	replacement := reopened.publishProgramRegistration(ctx, rows[0].Revision, false, candidate, image, "operator")
	if replacement.Outcome != registration.OutcomeApplied || questions != 2 {
		t.Fatal(replacement, questions)
	}
	if reopened.keepProgramOwned(ctx, candidate.Key, image.Program, "question-x") {
		t.Fatal("old question approved replacement")
	}
	if !reopened.keepProgramOwned(ctx, candidate.Key, image.Program, "question-xx") {
		t.Fatal("replacement Keep refused")
	}
}

func TestRegistryV3ProgramPendingWithoutCapturedImageIsUnaskable(t *testing.T) {
	d := registration1459Directory(t, t.TempDir())
	candidate := registry3Candidate(t, "unknown")
	candidate.State = registration.StatePendingApproval
	out := d.publishProgramRegistration(t.Context(), "", true, candidate, nil, "operator")
	if out.Outcome != registration.OutcomeApplied {
		t.Fatal(out)
	}
	if _, _, _, ok := d.programSelfEntry(candidate.Key); ok {
		t.Fatal("missing image fabricated publisher/approval input")
	}
	if len(d.programSelfEntries()) != 0 {
		t.Fatal("missing image exposed a Keep question")
	}
}
