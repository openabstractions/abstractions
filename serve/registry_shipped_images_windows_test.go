//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-identity/fileid"
)

// These are private disposable images, never installed programs or host state.
func shippedImageFixture(t *testing.T) (string, string, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	runtime := filepath.Join(dir, "openabstractions.exe")
	program := filepath.Join(dir, "provider.exe")
	for _, path := range []string{runtime, program} {
		if err := os.WriteFile(path, data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	digest := sha256.Sum256(data)
	return runtime, program, shippedTestRecord(map[string]string{"provider.exe": hex.EncodeToString(digest[:])})
}

func TestShippedProviderHeldNativeProof(t *testing.T) {
	runtime, program, record := shippedImageFixture(t)
	prior := &applicationV3Source{Program: program, FileKey: "predecessor"}
	verification, source, recheck, close := observeShippedRegistrationImageAt(t.Context(), program, prior, runtime, record)
	if source == nil || verification != registration.VerificationUnverified {
		t.Fatal(verification, source)
	}
	defer close()
	key, err := fileid.FileKey(program)
	if err != nil || key != source.FileKey {
		t.Fatal(key, source, err)
	}
	if err := os.Rename(program, program+".swapped"); err == nil {
		t.Fatal("held native image allowed replacement")
	}
	if err := os.WriteFile(program, []byte("tamper"), 0700); err == nil {
		t.Fatal("held native image allowed write")
	}
	if err := recheck(); err != nil {
		t.Fatal(err)
	}
	if err := close(); err != nil {
		t.Fatal(err)
	}
	if err := recheck(); err == nil {
		t.Fatal("closed hold passed final check")
	}
}

func TestUnsignedPyTorchSetupRecipesRequireEmbeddedImage(t *testing.T) {
	runtime, provider, _ := shippedImageFixture(t)
	tools := filepath.Dir(runtime)
	name := oaProgramFiles([]string{pytorchProgram})[0]
	installed := filepath.Join(tools, name)
	if err := os.Rename(provider, installed); err != nil {
		t.Fatal(err)
	}
	savedMachine, savedUser := standingInstallFolders, standingUserInstallFolder
	t.Cleanup(func() { standingInstallFolders, standingUserInstallFolder = savedMachine, savedUser })
	standingInstallFolders = func() []string { return nil }
	standingUserInstallFolder = func() string { return tools }
	signedByRuntimeSigner(t)
	data, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	record := shippedTestRecord(map[string]string{name: hex.EncodeToString(digest[:])})
	executable := func() (string, error) { return runtime, nil }
	p := &runtimeProviders{installation: filepath.Join(tools, installationDeclarationsDir)}
	file := providerFile{Declaration: providerDeclaration{Name: pytorchName, Program: installed}}
	if p.shippedProvider(file) || !p.shippedPyTorchAt(file, executable, record) {
		t.Fatal("the private unsigned Setup image did not use its embedded recipe proof")
	}
	if p.shippedPyTorchAt(file, executable, "") {
		t.Fatal("an unsigned local image acquired recipes without an embedded record")
	}
	foreignTools := t.TempDir()
	foreign := filepath.Join(foreignTools, name)
	if err := os.WriteFile(foreign, data, 0700); err != nil {
		t.Fatal(err)
	}
	foreignFile := file
	foreignFile.Declaration.Program = foreign
	if p.shippedPyTorchAt(foreignFile, executable, record) {
		t.Fatal("foreign file with the same name and bytes acquired shipped recipes")
	}
	foreignInstallation := &runtimeProviders{installation: filepath.Join(foreignTools, installationDeclarationsDir)}
	if foreignInstallation.shippedPyTorchAt(foreignFile, executable, record) {
		t.Fatal("a separate tools folder acquired the current runtime's shipped recipes")
	}
	key, err := fileid.FileKey(installed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, append(data, 1), 0700); err != nil {
		t.Fatal(err)
	}
	if changedKey, err := fileid.FileKey(installed); err != nil || changedKey != key {
		t.Fatal("the revision fixture must retain file identity", changedKey, err)
	}
	if p.shippedPyTorchAt(file, executable, record) {
		t.Fatal("a rewrite of the deployed unsigned file kept its old recipe proof")
	}
}

func TestShippedProviderRefusesOutsideDowngradeAndUnrecorded(t *testing.T) {
	runtime, program, record := shippedImageFixture(t)
	data, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "provider.exe")
	if err := os.WriteFile(outside, data, 0700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, program, record string }{
		{"outside identical bytes", outside, record},
		{"unrecorded", program, shippedTestRecord(map[string]string{"other.exe": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})},
		{"development", program, ""},
		{"downgrade", program, shippedTestRecord(map[string]string{"provider.exe": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})},
	} {
		t.Run(test.name, func(t *testing.T) {
			v, s, _, close := observeShippedRegistrationImageAt(t.Context(), test.program, &applicationV3Source{FileKey: "old"}, runtime, test.record)
			if close != nil {
				close()
			}
			if v != 0 || s != nil {
				t.Fatal("unproven image admitted", v, s)
			}
		})
	}
}

func TestShippedProviderAdoptionHealsWholeRegistrationAndPreservesIntent(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "enabled", true: "disabled"}[disabled], func(t *testing.T) {
			p, d, _ := registry3LegacyFixture(t)
			runtime, program, record := shippedImageFixture(t)
			file := registry3LegacyFile("models", program)
			file.Declaration.Models = []string{"chat:one", "chat:two", "chat:three", "chat:four", "chat:five"}
			registry3WriteLegacy(t, p.dir, file)
			sibling := registry3LegacyFile("disabled-store", program)
			registry3WriteLegacy(t, p.installation, sibling)
			if err := os.WriteFile(p.disabledPath, []byte(`["disabled-store"]`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := p.adoptProgramRegistrations(t.Context()); err != nil {
				t.Fatal(err)
			}
			rows, _, _, _, _, err := d.programRegistrations(t.Context())
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			key := rows[0].Key
			removed := []byte(`{"version":1,"models":["person-removed"]}`)
			p.removedPath = filepath.Join(t.TempDir(), "removed.json")
			if err := os.WriteFile(p.removedPath, removed, 0600); err != nil {
				t.Fatal(err)
			}
			out := d.editProgramRegistration(t.Context(), func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
				r := s.programs[key]
				r.Verification = registration.VerificationChanged
				r.VerificationReason = "identity:changed"
				if disabled {
					r.State = registration.StateDisabled
					conditions := maps.Clone(s.programFacetConditions[key])
					for facet, c := range conditions {
						c.Disabled = true
						conditions[facet] = c
					}
					s.programFacetConditions[key] = conditions
				}
				s.programs[key] = r
				source := s.programSources[key]
				source.FileKey = "old-native-image"
				s.programSources[key] = source
				return registration.Change{Outcome: registration.OutcomeApplied}, nil
			})
			if out.Outcome != registration.OutcomeApplied {
				t.Fatal(out)
			}
			before, _, _, _, _, err := d.programRegistrations(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			conditions := maps.Clone(d.v3.programFacetConditions[key])
			p.observeShippedImage = func(ctx context.Context, p string, prior *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error) {
				return observeShippedRegistrationImageAt(ctx, p, prior, runtime, record)
			}
			activities := 0
			p.shippedImageUpdated = func() { activities++ }
			if err := p.adoptProgramRegistrations(t.Context()); err != nil {
				t.Fatal(err)
			}
			after, _, _, _, _, err := d.programRegistrations(t.Context())
			if err != nil || len(after) != 1 {
				t.Fatal(after, err)
			}
			if after[0].Verification != registration.VerificationUnverified || after[0].State != before[0].State || !reflect.DeepEqual(after[0].Spec, before[0].Spec) || !reflect.DeepEqual(conditions, d.v3.programFacetConditions[key]) {
				t.Fatal("whole registration intent changed", before, after)
			}
			if d.v3.programSources[key].FileKey == "old-native-image" || activities != 1 {
				t.Fatal("native source not rebound once", activities)
			}
			held, err := recaptureProgramKeepImage(t.Context(), d.v3.programSources[key])
			if err != nil {
				t.Fatal("activation rejected freshly rebound source", err)
			}
			if err := held.Recheck(t.Context()); err != nil {
				t.Fatal(err)
			}
			held.Close()
			stale := d.v3.programSources[key]
			stale.FileKey = "old-native-image"
			if held, err := recaptureProgramKeepImage(t.Context(), stale); err == nil {
				held.Close()
				t.Fatal("activation accepted predecessor source")
			}
			if got, _ := os.ReadFile(p.removedPath); !reflect.DeepEqual(got, removed) {
				t.Fatal("model removal intent changed")
			}
			if marks := d.programSelfEntries(); len(marks) != 0 {
				t.Fatal("maintenance asked for approval", marks)
			}
			if err := p.adoptProgramRegistrations(t.Context()); err != nil || activities != 1 {
				t.Fatal("repeat maintenance", activities, err)
			}
		})
	}
}

func TestShippedProviderAdoptionCASConflictThenRebind(t *testing.T) {
	p, d, _ := registry3LegacyFixture(t)
	runtime, program, record := shippedImageFixture(t)
	registry3WriteLegacy(t, p.dir, registry3LegacyFile("models", program))
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err := d.programRegistrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	key := rows[0].Key
	d.editProgramRegistration(t.Context(), func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		source := s.programSources[key]
		source.FileKey = "old"
		s.programSources[key] = source
		return registration.Change{Outcome: registration.OutcomeApplied}, nil
	})
	first := true
	p.observeShippedImage = func(ctx context.Context, p string, prior *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error) {
		v, s, r, c := observeShippedRegistrationImageAt(ctx, p, prior, runtime, record)
		if first {
			first = false
			out := d.editProgramRegistration(ctx, func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
				v := s.programs[key]
				v.Revision = "concurrent-person-revision"
				v.State = registration.StateDisabled
				s.programs[key] = v
				conditions := maps.Clone(s.programFacetConditions[key])
				for facet, condition := range conditions {
					condition.Disabled = true
					conditions[facet] = condition
				}
				s.programFacetConditions[key] = conditions
				return registration.Change{Outcome: registration.OutcomeApplied}, nil
			})
			if out.Outcome != registration.OutcomeApplied {
				t.Fatal(out)
			}
		}
		return v, s, r, c
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if d.v3.programSources[key].FileKey != "old" {
		t.Fatal("conflict overwrote image")
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err = d.programRegistrations(t.Context())
	if err != nil || rows[0].State != registration.StateDisabled || d.v3.programSources[key].FileKey == "old" {
		t.Fatal(rows, err)
	}
}

func TestShippedProviderAdoptionPreservesPendingApproval(t *testing.T) {
	p, d, _ := registry3LegacyFixture(t)
	runtime, program, record := shippedImageFixture(t)
	registry3WriteLegacy(t, p.dir, registry3LegacyFile("pending", program))
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err := d.programRegistrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	key := rows[0].Key
	approval := registrationProgramApproval{Pending: true, Question: "existing-person-question", Revision: rows[0].Revision}
	out := d.editProgramRegistration(t.Context(), func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		r := s.programs[key]
		r.State = registration.StatePendingApproval
		r.Verification = registration.VerificationChanged
		r.VerificationReason = "identity:changed"
		s.programs[key] = r
		source := s.programSources[key]
		source.FileKey = "old"
		s.programSources[key] = source
		s.programApprovals[key] = approval
		conditions := maps.Clone(s.programFacetConditions[key])
		for facet, c := range conditions {
			c.Pending = true
			conditions[facet] = c
		}
		s.programFacetConditions[key] = conditions
		return registration.Change{Outcome: registration.OutcomeApplied}, nil
	})
	if out.Outcome != registration.OutcomeApplied {
		t.Fatal(out)
	}
	heldSpec := rows[0].Spec
	changedFile := registry3LegacyFile("pending", program)
	changedFile.Declaration.Models = []string{"chat:upgrade-while-pending"}
	registry3WriteLegacy(t, p.dir, changedFile)
	p.observeShippedImage = func(ctx context.Context, p string, prior *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error) {
		return observeShippedRegistrationImageAt(ctx, p, prior, runtime, record)
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err = d.programRegistrations(t.Context())
	if err != nil || rows[0].State != registration.StatePendingApproval || d.v3.programApprovals[key] != approval {
		t.Fatal("pending person choice replaced", rows, err)
	}
	if !reflect.DeepEqual(rows[0].Spec, heldSpec) {
		t.Fatal("a record held for review received a changed declaration", rows[0].Spec)
	}
	if d.v3.programSources[key].FileKey != "old" {
		t.Fatal("pending old-image Keep transferred to replacement image")
	}
	for _, c := range d.v3.programFacetConditions[key] {
		if !c.Pending {
			t.Fatal("pending facet enabled")
		}
	}
	if rows[0].Verification != registration.VerificationChanged {
		t.Fatal("pending replacement became eligible", rows[0])
	}
	// The existing conditional review owner captures the replacement afresh;
	// its new question cannot consume the predecessor's answer.
	held, err := openRegistryProgramImage(t.Context(), fileid.Self, program)
	if err != nil {
		t.Fatal(err)
	}
	fresh := rows[0]
	fresh.Verification = registration.VerificationUnverified
	fresh.VerificationReason = "identity:unsigned"
	image := &applicationV3Source{Program: program, FileKey: applicationV3HeldKey(held), Code: held.Code, Proof: held.Proof}
	out = d.publishProgramRegistration(t.Context(), fresh.Revision, false, fresh, image, "operator")
	held.Close()
	if out.Outcome != registration.OutcomeApplied || out.Reason != "approval:pending" {
		t.Fatal(out)
	}
	if d.v3.programSources[key].FileKey == "old" || d.keepProgramOwned(t.Context(), key, program, approval.Question) {
		t.Fatal("fresh review retained old image/answer")
	}
	if !d.setProgramQuestion(t.Context(), key, program, "fresh-current-image-question", out.Revision) {
		t.Fatal("fresh question not retained")
	}
	if !d.keepProgramOwned(t.Context(), key, program, "fresh-current-image-question") {
		t.Fatal("fresh Keep failed current-image capture")
	}
}

func TestShippedProviderRetainedProgramWithoutLegacyFile(t *testing.T) {
	p, d, _ := registry3LegacyFixture(t)
	runtime, program, record := shippedImageFixture(t)
	registry3WriteLegacy(t, p.dir, registry3LegacyFile("retained", program))
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err := d.programRegistrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	key := rows[0].Key
	files, err := os.ReadDir(p.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if err := os.Remove(filepath.Join(p.dir, file.Name())); err != nil {
			t.Fatal(err)
		}
	}
	out := d.editProgramRegistration(t.Context(), func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		source := s.programSources[key]
		source.FileKey = "old"
		s.programSources[key] = source
		return registration.Change{Outcome: registration.OutcomeApplied}, nil
	})
	if out.Outcome != registration.OutcomeApplied {
		t.Fatal(out)
	}
	p.observeShippedImage = func(ctx context.Context, p string, prior *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error) {
		return observeShippedRegistrationImageAt(ctx, p, prior, runtime, record)
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if d.v3.programSources[key].FileKey == "old" {
		t.Fatal("canonical PROGRAM without legacy file was skipped")
	}
	after, _, _, _, _, err := d.programRegistrations(t.Context())
	if err != nil || !reflect.DeepEqual(rows[0].Spec, after[0].Spec) {
		t.Fatal(after, err)
	}
}

func TestShippedProviderAdoptionPreservesRemoveReview(t *testing.T) {
	p, d, _ := registry3LegacyFixture(t)
	runtime, program, record := shippedImageFixture(t)
	registry3WriteLegacy(t, p.dir, registry3LegacyFile("removing", program))
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err := d.programRegistrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	key := rows[0].Key
	approval := registrationProgramApproval{Removing: true, Question: "retained-remove-answer", Revision: rows[0].Revision}
	out := d.editProgramRegistration(t.Context(), func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		source := s.programSources[key]
		source.FileKey = "old"
		s.programSources[key] = source
		s.programApprovals[key] = approval
		return registration.Change{Outcome: registration.OutcomeApplied}, nil
	})
	if out.Outcome != registration.OutcomeApplied {
		t.Fatal(out)
	}
	p.observeShippedImage = func(ctx context.Context, p string, prior *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error) {
		return observeShippedRegistrationImageAt(ctx, p, prior, runtime, record)
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err = d.programRegistrations(t.Context())
	if err != nil || rows[0].Verification != registration.VerificationChanged || d.v3.programSources[key].FileKey != "old" || d.v3.programApprovals[key] != approval {
		t.Fatal("Remove intent crossed images", rows, err)
	}
}

func TestShippedProviderUnresolvedSnapshotIsCompleteAndOutsideCAS(t *testing.T) {
	run := newSelfRegisteredRun(t)
	if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	run.d.askKeep = nil
	p, _, _ := registry3LegacyFixture(t)
	d := run.d
	p.registrationDirectory = func() *applicationDirectory { return d }
	var files []providerFile
	for _, name := range []string{"one", "two"} {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.CreateTemp(filepath.Dir(self), "oa-shipped772-*.exe")
		if err != nil {
			t.Fatal(err)
		}
		program := file.Name()
		t.Cleanup(func() { os.Remove(program) })
		if _, err := file.Write(data); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		declaration := registry3LegacyFile(name, program)
		registry3WriteLegacy(t, p.dir, declaration)
		files = append(files, declaration)
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	out := d.editProgramRegistration(t.Context(), func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		for key, source := range s.programSources {
			source.FileKey = "old"
			s.programSources[key] = source
		}
		return registration.Change{Outcome: registration.OutcomeApplied}, nil
	})
	if out.Outcome != registration.OutcomeApplied {
		t.Fatal(out)
	}
	// The same adoption pass updates a facet and reports the unresolved image.
	// Its review candidate must name the revision committed by that update.
	for _, file := range files {
		file.Declaration.Models = append(file.Declaration.Models, "chat:new")
		registry3WriteLegacy(t, p.dir, file)
	}
	// The actual native tools root is used; empty development records refuse
	// rebind while the complete contained-only snapshot remains reviewable.
	reviewErrors := make(chan error, 16)
	group := &shippedProgramReview{report: func(err error) { reviewErrors <- err }}
	group.attach(run.k)
	calls := 0
	p.shippedImagesUnresolved = func(ctx context.Context, got []shippedImageReviewCandidate) {
		calls++
		if len(got) != 2 {
			t.Fatal("partial group", got)
		}
		if _, _, _, _, _, err := d.programRegistrations(ctx); err != nil {
			t.Fatal(err)
		}
		group.snapshot(ctx, got)
		for _, candidate := range got {
			record, _, _, ok := d.programReviewRecord(ctx, candidate.Key)
			if !ok || candidate.Revision != record.Revision {
				t.Fatal("unresolved review lost the committed facet revision", candidate, record, "group", group.question)
			}
		}
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("per-record notification", calls)
	}
	question := group.question
	if question == "" || len(group.members) != 2 {
		select {
		case err := <-reviewErrors:
			t.Fatal("group admission failed", err)
		default:
			t.Fatal("unresolved programs did not reach grouped review", group.members)
		}
	}
	for repeat := 0; repeat < 2; repeat++ {
		if err := p.adoptProgramRegistrations(t.Context()); err != nil {
			t.Fatal(err)
		}
		if group.question != question || len(group.members) != 2 {
			t.Fatal("unchanged adoption retired the current grouped review", question, group.question, group.members)
		}
	}
}

func TestShippedProviderOpenFailureKeepsIdentityUnavailable(t *testing.T) {
	runtime, program, record := shippedImageFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.exe")
	data, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, data, 0700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, runtime, program, want string }{
		{"runtime unavailable", runtime + ".missing", program, "identity:unavailable"},
		{"program unavailable", runtime, program + ".missing", "identity:unavailable"},
		{"proven outside", runtime, outside, "shipped:outside"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cause := ""
			v, source, _, close := observeShippedRegistrationImageAtCause(t.Context(), test.program, &applicationV3Source{FileKey: "old"}, test.runtime, record, &cause)
			if close != nil {
				close()
			}
			if v != 0 || source != nil || cause != test.want {
				t.Fatal("unproven image misclassified", v, source, cause, test.want)
			}
		})
	}
}

func TestShippedProviderTwoAdoptionsPreserveExistingDeclarationAuthority(t *testing.T) {
	runtime, program, record := shippedImageFixture(t)
	var results []registration.Registration
	var conditions []map[registration.FacetReference]registrationFacetCondition
	for _, rebind := range []bool{false, true} {
		p, d, _ := registry3LegacyFixture(t)
		file := registry3LegacyFile("installed", program)
		file.Declaration.Models = []string{"chat:existing"}
		registry3WriteLegacy(t, p.installation, file)
		if err := p.adoptProgramRegistrations(t.Context()); err != nil {
			t.Fatal(err)
		}
		rows, _, _, _, _, err := d.programRegistrations(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		key := rows[0].Key
		before := rows[0].Spec
		file.Declaration.Models = []string{"chat:existing", "chat:wider"}
		registry3WriteLegacy(t, p.installation, file)
		if rebind {
			out := d.editProgramRegistration(t.Context(), func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
				source := s.programSources[key]
				source.FileKey = "predecessor"
				s.programSources[key] = source
				return registration.Change{Outcome: registration.OutcomeApplied}, nil
			})
			if out.Outcome != registration.OutcomeApplied {
				t.Fatal(out)
			}
			p.observeShippedImage = func(ctx context.Context, p string, prior *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error) {
				return observeShippedRegistrationImageAt(ctx, p, prior, runtime, record)
			}
			if err := p.adoptProgramRegistrations(t.Context()); err != nil {
				t.Fatal(err)
			}
			rows, _, _, _, _, err = d.programRegistrations(t.Context())
			if err != nil || reflect.DeepEqual(before, rows[0].Spec) || len(rows[0].Spec.Providers[0].Models) != 2 {
				t.Fatal("upgrade pass left the installation's widened declaration for a later start", rows, err)
			}
		}
		// A following pass receives identical input and changes nothing, after
		// maintenance and when no image maintenance was necessary.
		if err := p.adoptProgramRegistrations(t.Context()); err != nil {
			t.Fatal(err)
		}
		rows, _, _, _, _, err = d.programRegistrations(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, rows[0])
		conditions = append(conditions, maps.Clone(d.v3.programFacetConditions[key]))
	}
	if len(results[0].Spec.Providers[0].Models) != 2 || !reflect.DeepEqual(results[0].Spec, results[1].Spec) || results[0].State != results[1].State || results[0].Source != results[1].Source || !reflect.DeepEqual(conditions[0], conditions[1]) {
		t.Fatal("maintenance changed declaration source authority", results)
	}
}

// The owner's upgrade: a new inventoryd.exe ships a wider local-stores
// declaration beside facets the Panel added for the person. The single
// adoption pass of the upgraded runtime's start applies the shipped change,
// keeps the person's facet and a declaration the person turned off.
func TestShippedProviderUpgradeAppliesInstalledDeclarationInSamePass(t *testing.T) {
	p, d, _ := registry3LegacyFixture(t)
	runtime, program, record := shippedImageFixture(t)
	stores := registry3LegacyFile("stores", program)
	stores.Declaration.Models = []string{"chat:ollama"}
	quiet := registry3LegacyFile("quiet", program)
	registry3WriteLegacy(t, p.installation, stores)
	registry3WriteLegacy(t, p.installation, quiet)
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, revision, err := p.readCached()
	if err != nil {
		t.Fatal(err)
	}
	mine := registry3LegacyFile("mine", program)
	mine.Declaration.Models = []string{"chat:person"}
	if out := p.addAdmitted(t.Context(), revision, mine, nil); out.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal("person's facet not added", out)
	}
	if err := os.WriteFile(p.disabledPath, []byte(`["quiet"]`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err := d.programRegistrations(t.Context())
	if err != nil || len(rows) != 1 || len(rows[0].Spec.Providers) != 3 {
		t.Fatal(rows, err)
	}
	key := rows[0].Key
	quietFacet := registration.FacetReference{Kind: registration.FacetKindProvider, Name: "quiet"}
	if !d.v3.programFacetConditions[key][quietFacet].Disabled {
		t.Fatal("fixture did not turn quiet off")
	}
	// The installer replaces the program and its declarations together.
	out := d.editProgramRegistration(t.Context(), func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		source := s.programSources[key]
		source.FileKey = "previous-installer-image"
		s.programSources[key] = source
		return registration.Change{Outcome: registration.OutcomeApplied}, nil
	})
	if out.Outcome != registration.OutcomeApplied {
		t.Fatal(out)
	}
	stores.Declaration.Models = []string{"chat:ollama", "chat:lemonade", "chat:swarmui"}
	quiet.Declaration.Models = []string{"chat:quiet-next"}
	registry3WriteLegacy(t, p.installation, stores)
	registry3WriteLegacy(t, p.installation, quiet)
	p.observeShippedImage = func(ctx context.Context, p string, prior *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error) {
		return observeShippedRegistrationImageAt(ctx, p, prior, runtime, record)
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err = d.programRegistrations(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	models := map[string][]string{}
	for _, facet := range rows[0].Spec.Providers {
		for _, m := range facet.Models {
			models[facet.Name] = append(models[facet.Name], m.Model)
		}
	}
	if !reflect.DeepEqual(models["stores"], []string{"ollama", "lemonade", "swarmui"}) || !reflect.DeepEqual(models["quiet"], []string{"quiet-next"}) {
		t.Fatal("upgrade pass left shipped declarations for a later start", models)
	}
	if !reflect.DeepEqual(models["mine"], []string{"person"}) || !d.v3.programFacetConditions[key][quietFacet].Disabled {
		t.Fatal("upgrade overwrote the person's facets", models, d.v3.programFacetConditions[key])
	}
	if d.v3.programSources[key].FileKey == "previous-installer-image" {
		t.Fatal("image was not rebound")
	}
	upgraded := rows[0].Revision
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err = d.programRegistrations(t.Context())
	if err != nil || rows[0].Revision != upgraded {
		t.Fatal("following start changed the upgraded registration", rows, err)
	}
}

// An upgrade whose new image fails the shipped proof applies nothing its
// installation declares: the record keeps its old spec, marked changed for
// the person's review, on this start and the next.
func TestShippedProviderFailedProofKeepsTheOldDeclaration(t *testing.T) {
	p, d, _ := registry3LegacyFixture(t)
	_, program, _ := shippedImageFixture(t)
	stores := registry3LegacyFile("stores", program)
	stores.Declaration.Models = []string{"chat:ollama"}
	registry3WriteLegacy(t, p.installation, stores)
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err := d.programRegistrations(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	key, before := rows[0].Key, rows[0].Spec
	out := d.editProgramRegistration(t.Context(), func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		source := s.programSources[key]
		source.FileKey = "previous-installer-image"
		s.programSources[key] = source
		return registration.Change{Outcome: registration.OutcomeApplied}, nil
	})
	if out.Outcome != registration.OutcomeApplied {
		t.Fatal(out)
	}
	stores.Declaration.Models = []string{"chat:ollama", "chat:lemonade"}
	registry3WriteLegacy(t, p.installation, stores)
	proofs := 0
	p.observeShippedImage = func(context.Context, string, *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error) {
		proofs++
		return 0, nil, nil, nil
	}
	reviews := 0
	p.shippedImagesUnresolved = func(_ context.Context, candidates []shippedImageReviewCandidate) { reviews += len(candidates) }
	for start := 1; start <= 2; start++ {
		if err := p.adoptProgramRegistrations(t.Context()); err != nil {
			t.Fatal(err)
		}
		rows, _, _, _, _, err = d.programRegistrations(t.Context())
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
		if !reflect.DeepEqual(rows[0].Spec, before) || rows[0].Verification != registration.VerificationChanged || d.v3.programSources[key].FileKey != "previous-installer-image" {
			t.Fatal("an unproven image received its installation's declaration", start, rows[0])
		}
	}
	if proofs != 2 || reviews != 2 {
		t.Fatal("the failed proof was not offered for review", proofs, reviews)
	}
}

func TestShippedProviderRemovedRecordRecreatedWithDisabledDeclaration(t *testing.T) {
	p, d, _ := registry3LegacyFixture(t)
	runtime, program, record := shippedImageFixture(t)
	file := registry3LegacyFile("removed", program)
	registry3WriteLegacy(t, p.dir, file)
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err := d.programRegistrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	key := rows[0].Key
	oldKey := d.v3.programSources[key].FileKey
	revision := rows[0].Revision
	data, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(program, program+".previous"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(p.dir, "removed.json")); err != nil {
		t.Fatal(err)
	}
	registry3WriteLegacy(t, p.installation, file)
	if err := os.WriteFile(p.disabledPath, []byte(`["removed"]`), 0600); err != nil {
		t.Fatal(err)
	}
	p.observeShippedImage = func(ctx context.Context, p string, prior *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error) {
		// Remove wins after the source observation and before receiving CAS.
		// The local prior remains captured even when the record is now absent.
		out := d.changeProgramFacet(ctx, revision, registration.FacetReference{Kind: registration.FacetKindProvider, Name: "removed"}, nil)
		if out.Outcome != registration.OutcomeApplied {
			t.Fatal(out)
		}
		return observeShippedRegistrationImageAt(ctx, p, prior, runtime, record)
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err = d.programRegistrations(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	condition := d.v3.programFacetConditions[key][registration.FacetReference{Kind: registration.FacetKindProvider, Name: "removed"}]
	if !condition.Disabled || rows[0].State != registration.StateDisabled || d.v3.programSources[key].FileKey == oldKey {
		t.Fatal("recreated declaration lost disabled intent", rows, condition)
	}
}

func TestShippedProviderTwoRebindsEmitOneActivity(t *testing.T) {
	p, d, _ := registry3LegacyFixture(t)
	type fixture struct{ runtime, record string }
	images := map[string]fixture{}
	for _, name := range []string{"one", "two"} {
		runtime, program, record := shippedImageFixture(t)
		// The upgrade pass observes each program by its record's subject spelling.
		images[fileid.NormalizeSubjectProgram(program)] = fixture{runtime, record}
		registry3WriteLegacy(t, p.dir, registry3LegacyFile(name, program))
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	out := d.editProgramRegistration(t.Context(), func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		for key, source := range s.programSources {
			source.FileKey = "old"
			s.programSources[key] = source
		}
		return registration.Change{Outcome: registration.OutcomeApplied}, nil
	})
	if out.Outcome != registration.OutcomeApplied {
		t.Fatal(out)
	}
	p.observeShippedImage = func(ctx context.Context, p string, prior *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error) {
		f := images[p]
		return observeShippedRegistrationImageAt(ctx, p, prior, f.runtime, f.record)
	}
	activities := 0
	p.shippedImageUpdated = func() { activities++ }
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if activities != 1 {
		t.Fatal("one upgrade pass emitted repeated Activity", activities)
	}
	for _, source := range d.v3.programSources {
		if source.FileKey == "old" {
			t.Fatal("program was not rebound")
		}
	}
}

func TestShippedProviderOutsideChangedAdopterRemainsFacadeReviewable(t *testing.T) {
	p, d, _ := registry3LegacyFixture(t)
	_, program, _ := shippedImageFixture(t) // outside the actual test runtime tools folder
	registry3WriteLegacy(t, p.dir, registry3LegacyFile("adopter", program))
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err := d.programRegistrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	key := rows[0].Key
	out := d.editProgramRegistration(t.Context(), func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		source := s.programSources[key]
		source.FileKey = "old"
		s.programSources[key] = source
		return registration.Change{Outcome: registration.OutcomeApplied}, nil
	})
	if out.Outcome != registration.OutcomeApplied {
		t.Fatal(out)
	}
	p.shippedImagesUnresolved = func(_ context.Context, got []shippedImageReviewCandidate) {
		if len(got) != 0 {
			t.Fatal("third-party adopter entered OA tools review", got)
		}
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err = d.programRegistrations(t.Context())
	if err != nil || rows[0].Verification != registration.VerificationChanged {
		t.Fatal(rows, err)
	}
	r, caller := registryObserverFixture(t)
	r.providers = p
	r.operator.providers = p
	r.operator.credentials.owner = caller.Account
	r.operator.credentials.runtimeRights.applications = d
	out = r.registerProgram(t.Context(), caller, rows[0].Revision, false, rows[0].Spec, fileid.Self)
	if out.Outcome != registration.OutcomeApplied || out.Reason != "approval:pending" || d.v3.programSources[key].FileKey == "old" {
		t.Fatal("ordinary Facade fresh review was blocked", out)
	}
}

func TestShippedProviderCommittedKeepRetainsConfigDecisionOnlyForProvenImage(t *testing.T) {
	for _, proven := range []bool{true, false} {
		t.Run(map[bool]string{true: "proven", false: "unproven"}[proven], func(t *testing.T) {
			p, d, _ := registry3LegacyFixture(t)
			runtime, program, record := shippedImageFixture(t)
			registry3WriteLegacy(t, p.dir, registry3LegacyFile("configured", program))
			if err := p.adoptProgramRegistrations(t.Context()); err != nil {
				t.Fatal(err)
			}
			rows, _, _, _, _, err := d.programRegistrations(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			key := rows[0].Key
			epoch := rows[0].Epoch
			approval := registrationProgramApproval{Question: "committed-Keep", Revision: rows[0].Revision, KeptGeneration: epoch}
			out := d.editProgramRegistration(t.Context(), func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
				r := s.programs[key]
				r.Spec.Providers[0].ConfigKeys = []string{"logging.enabled"}
				s.programs[key] = r
				s.programApprovals[key] = approval
				source := s.programSources[key]
				source.FileKey = "old"
				s.programSources[key] = source
				return registration.Change{Outcome: registration.OutcomeApplied}, nil
			})
			if out.Outcome != registration.OutcomeApplied {
				t.Fatal(out)
			}
			d.programConfigKeyKnown = func(key string) bool { return key == "logging.enabled" }
			if !proven {
				record = shippedTestRecord(map[string]string{"provider.exe": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
			}
			p.observeShippedImage = func(ctx context.Context, p string, prior *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error) {
				return observeShippedRegistrationImageAt(ctx, p, prior, runtime, record)
			}
			if err := p.adoptProgramRegistrations(t.Context()); err != nil {
				t.Fatal(err)
			}
			candidates, err := d.providerConfigCandidates(t.Context(), program, "logging.enabled")
			if !proven {
				if err == nil || len(candidates) != 0 {
					t.Fatal("unproven image consumed committed config decision", candidates)
				}
				return
			}
			if err != nil || len(candidates) != 1 || candidates[0].Managed || candidates[0].Epoch != epoch || d.v3.programApprovals[key] != approval {
				t.Fatal("committed decision was lost", candidates, err)
			}
			held, err := recaptureProgramKeepImage(t.Context(), candidates[0].Source)
			if err != nil {
				t.Fatal("current image cannot use retained decision", err)
			}
			held.Close()
		})
	}
}

func TestShippedProviderOwnerStateThroughRealKeepAndTwoStarts(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-declaration", true: "changed-declaration"}[changed], func(t *testing.T) {
			p, d, _ := registry3LegacyFixture(t)
			runtime, program, _ := shippedImageFixture(t)
			file := registry3LegacyFile("owner-model-service", program)
			file.Declaration.Models = []string{"chat:owner-existing"}
			registry3WriteLegacy(t, p.installation, file)
			if err := p.adoptProgramRegistrations(t.Context()); err != nil {
				t.Fatal(err)
			}
			read := func() registration.Registration {
				t.Helper()
				rows, _, _, _, _, err := d.programRegistrations(t.Context())
				if err != nil || len(rows) != 1 {
					t.Fatal(rows, err)
				}
				return rows[0]
			}
			installed := read()
			if installed.Source != registration.SourceInstallation {
				t.Fatal("fixture did not use installation admission", installed)
			}
			r, caller := registryObserverFixture(t)
			r.providers = p
			r.operator.providers = p
			r.operator.credentials.owner = caller.Account
			r.operator.credentials.runtimeRights.applications = d
			replaced := r.registerProgram(t.Context(), caller, installed.Revision, false, installed.Spec, fileid.Self)
			if replaced.Outcome != registration.OutcomeApplied || replaced.Reason != "approval:pending" {
				t.Fatal("real Replace failed", replaced)
			}
			const question = "n1-current-image-keep"
			if !d.setProgramQuestion(t.Context(), installed.Key, program, question, replaced.Revision) || !d.keepProgramOwned(t.Context(), installed.Key, program, question) {
				t.Fatal("real question binding and Keep failed")
			}
			kept := read()
			approval := d.v3.programApprovals[kept.Key]
			oldImage := d.v3.programSources[kept.Key]
			if kept.State != registration.StateEnabled || kept.Verification != registration.VerificationUnverified || approval.Pending || approval.KeptGeneration != kept.Epoch {
				t.Fatal("Keep was not committed", kept, approval)
			}
			data, err := os.ReadFile(program)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(program, program+".previous-build"); err != nil {
				t.Fatal(err)
			}
			data = append(data, []byte("\nrecorded next build\n")...)
			if err := os.WriteFile(program, data, 0700); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(data)
			record := shippedTestRecord(map[string]string{"provider.exe": hex.EncodeToString(digest[:])})
			newKey, err := fileid.FileKey(program)
			if err != nil || newKey == oldImage.FileKey {
				t.Fatal("replacement did not acquire a new native key", newKey, err)
			}
			if changed {
				file.Declaration.Models = []string{"chat:owner-next-declaration"}
				registry3WriteLegacy(t, p.installation, file)
			}
			activities, grouped := 0, 0
			observe := func(ctx context.Context, p string, prior *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error) {
				return observeShippedRegistrationImageAt(ctx, p, prior, runtime, record)
			}
			activity := func() { activities++ }
			group := func(_ context.Context, candidates []shippedImageReviewCandidate) { grouped += len(candidates) }
			p.observeShippedImage, p.shippedImageUpdated, p.shippedImagesUnresolved = observe, activity, group
			if err := p.adoptProgramRegistrations(t.Context()); err != nil {
				t.Fatal(err)
			}
			rebound := read()
			if rebound.State != registration.StateEnabled || rebound.Verification != registration.VerificationUnverified || d.v3.programApprovals[kept.Key] != approval || d.v3.programSources[kept.Key].FileKey != newKey {
				t.Fatal("image rebind changed the committed owner decision", kept, rebound)
			}
			if !changed && (!reflect.DeepEqual(rebound.Spec, kept.Spec) || rebound.Revision != kept.Revision || rebound.Epoch != kept.Epoch) {
				t.Fatal("unchanged upgrade altered the registration", kept, rebound)
			}
			if changed && (rebound.Spec.Providers[0].Models[0].Model != "owner-next-declaration" || rebound.Epoch == kept.Epoch || rebound.Revision == kept.Revision) {
				t.Fatal("upgrade pass left the changed installation declaration for a later start", rebound)
			}
			if activities != 1 || grouped != 0 || len(d.programSelfEntries()) != 0 {
				t.Fatal("proven owner upgrade asked again or repeated Activity", activities, grouped)
			}
			// A second runtime owner reads the persisted registration and declarations.
			d = registration1459Directory(t, filepath.Dir(d.dir))
			p = &runtimeProviders{dir: p.dir, installation: p.installation, disabledPath: p.disabledPath, removedPath: p.removedPath, report: p.report, supervisors: map[string]*providerSupervisor{}, retiring: map[string]*providerSupervisor{}, changes: make(chan struct{})}
			p.registrationDirectory = func() *applicationDirectory { return d }
			p.observeShippedImage, p.shippedImageUpdated, p.shippedImagesUnresolved = observe, activity, group
			if err := p.adoptProgramRegistrations(t.Context()); err != nil {
				t.Fatal(err)
			}
			second := read()
			if second.State != registration.StateEnabled || second.Verification != registration.VerificationUnverified || d.v3.programSources[kept.Key].FileKey != newKey || activities != 1 || grouped != 0 || len(d.programSelfEntries()) != 0 {
				t.Fatal("second start lost eligibility or asked again", second, activities, grouped)
			}
			if !changed {
				if !reflect.DeepEqual(second.Spec, kept.Spec) || second.Revision != kept.Revision || second.Epoch != kept.Epoch || d.v3.programApprovals[kept.Key].KeptGeneration != second.Epoch {
					t.Fatal("unchanged second start altered the committed decision", second)
				}
			} else {
				if !reflect.DeepEqual(second.Spec, rebound.Spec) || second.Revision != rebound.Revision || second.Epoch != rebound.Epoch || d.v3.programApprovals[kept.Key].KeptGeneration != kept.Epoch {
					t.Fatal("second start changed the reconciled upgrade", rebound, second)
				}
				t.Log("N2 retained: the upgrade pass renews Epoch while KeptGeneration remains the committed prior Epoch; eligibility remains Unverified/enabled")
			}
		})
	}
}
