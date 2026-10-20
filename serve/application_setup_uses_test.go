package main

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func setupKeptHelper(program string) selfMark {
	revision, key := strings.Repeat("a", 48), applicationV3Hash([]byte(program))
	release := agentNativeRelease{Component: agentReleaseComponent, PublisherKey: "authenticode-dn-sha256:" + strings.Repeat("b", 64),
		Channel: "stable", Release: [4]uint32{0, 8, 0, 1}, ImageKey: key, ImageHash: strings.Repeat("c", 64), ImageBytes: 1, Authenticated: true}
	for _, modality := range []string{"chat", "image", "video"} {
		need := registration.ModelNeed{Modality: modality, RequiredFeatures: []string{}, Placement: registration.PlacementAny}
		hash, err := agentDeclarationHash(registration.EncodeModelNeed(&need))
		if err != nil {
			panic(err)
		}
		release.Declaration.Models = append(release.Declaration.Models, hash)
	}
	slices.Sort(release.Declaration.Models)
	return selfMark{Kind: "agent", Program: program, ProgramKey: key, RegistrationRevision: revision, Question: "fixture-keep",
		Agent: &applicationAgentMark{Account: setupTestAccount, Binding: strings.Repeat("d", 64), State: "enrolled", AnsweredBy: asks.AnsweredByOperator, AnsweredMs: 1,
			KeptRelease: &agentKeptRelease{Question: "fixture-keep", Revision: revision, FileKey: key, AnsweredMs: 1, Current: release}}}
}

func setupImageManifestDirectory() *applicationDirectory {
	d := setupDirectory(setupGrants())
	d.unconfirmed = map[string]selfMark{}
	m := registration.Manifest{Version: "applications@3", Name: "opencode", Title: "OpenCode",
		Offers: appmodel.CommandDeclaration{Version: "appmodel@1"}, Host: registration.HostRecipe{ReadinessDeadlineMs: 100},
		Needs: registration.Needs{Models: []registration.ModelNeed{{Modality: "image", RequiredFeatures: []string{}, Placement: registration.PlacementAny}}}}
	raw := registration.EncodeManifest(&m)
	d.v3 = &applicationV3Registrations{entries: map[string]applicationV3Registration{"opencode": {State: "enabled"}},
		files: map[string]applicationV3ManifestFile{"opencode": {Raw: raw, Digest: applicationV3Hash(raw),
			Source: applicationV3Source{Program: setupTestApp, FileKey: "fixture-held-key", Code: identity.Code{Trusted: true}, Proof: identity.ProofBound}}}}
	return d
}

func TestRequestSetupDeclaresUseBeforeRights(t *testing.T) {
	d := setupImageManifestDirectory()
	calls := 0
	d.decide = func(context.Context, rights.Subject, string, string) (bool, error) { calls++; return true, nil }
	q := &setupRequests{lastRead: time.Now()}
	subject := rights.Subject{Account: setupTestAccount, Program: setupTestApp}
	got := q.request(context.Background(), d, subject, "code")
	if got.Outcome != applications.SetupOutcomeForbidden || got.Reason != "manifest:use:code" || calls != 0 || q.pending != nil {
		t.Fatal(got, calls, q.pending)
	}
	got = q.request(context.Background(), d, subject, "image")
	if got.Outcome != applications.SetupOutcomeAsked || calls != 1 || q.pending == nil || q.pending.Application != "OpenCode" {
		t.Fatal(got, calls, q.pending)
	}
}

func TestRequestSetupRejectsUnkeptOrMismatchedUseBeforeRights(t *testing.T) {
	for name, change := range map[string]func(*applicationDirectory){
		"pending": func(d *applicationDirectory) {
			e := d.v3.entries["opencode"]
			e.State = "pending"
			d.v3.entries["opencode"] = e
		},
		"unconfirmed": func(d *applicationDirectory) { d.unconfirmed["opencode"] = selfMark{} },
		"unregistered": func(d *applicationDirectory) {
			delete(d.descriptors, "opencode")
			delete(d.v3.entries, "opencode")
			delete(d.v3.files, "opencode")
		},
		"unsigned": func(d *applicationDirectory) {
			f := d.v3.files["opencode"]
			f.Source.Code.Trusted = false
			d.v3.files["opencode"] = f
		},
		"mismatched": func(d *applicationDirectory) {
			f := d.v3.files["opencode"]
			f.Source.Program = setupTestOther
			d.v3.files["opencode"] = f
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := setupImageManifestDirectory()
			change(d)
			calls := 0
			d.decide = func(context.Context, rights.Subject, string, string) (bool, error) { calls++; return true, nil }
			q := &setupRequests{lastRead: time.Now()}
			got := q.request(context.Background(), d, rights.Subject{Account: setupTestAccount, Program: setupTestApp}, "image")
			if got.Outcome != applications.SetupOutcomeForbidden || got.Reason != "manifest:use:image" || calls != 0 || q.pending != nil {
				t.Fatal(got, calls, q.pending)
			}
		})
	}
}

func TestRequestSetupRetainedModalityDeclaration(t *testing.T) {
	d := setupImageManifestDirectory()
	f := d.v3.files["opencode"]
	f.Raw = []byte(strings.Replace(string(f.Raw), `"modality":"image"`, `"profile":"image"`, 1))
	f.Digest = applicationV3Hash(f.Raw)
	d.v3.files["opencode"] = f
	q := &setupRequests{lastRead: time.Now()}
	got := q.request(context.Background(), d, rights.Subject{Account: setupTestAccount, Program: setupTestApp}, "image")
	if got.Outcome != applications.SetupOutcomeAsked || q.pending == nil {
		t.Fatal(got)
	}
}

func TestRequestSetupKeptHelperRejectsPendingLegacyAndDetachedDeclarations(t *testing.T) {
	for name, change := range map[string]func(*selfMark){
		"pending":         func(m *selfMark) { m.Agent.State = "pending" },
		"legacy":          func(m *selfMark) { m.Agent.KeptRelease.Current.Declaration.Models = nil },
		"detached":        func(m *selfMark) { m.Agent.KeptRelease.Current.ImageKey = "different-current-image" },
		"unauthenticated": func(m *selfMark) { m.Agent.KeptRelease.Current.Authenticated = false },
	} {
		t.Run(name, func(t *testing.T) {
			d := setupDirectory(setupGrants())
			mark := d.unconfirmed["opencode"]
			change(&mark)
			d.unconfirmed["opencode"] = mark
			calls := 0
			d.decide = func(context.Context, rights.Subject, string, string) (bool, error) { calls++; return true, nil }
			q := &setupRequests{lastRead: time.Now()}
			got := q.request(context.Background(), d, rights.Subject{Account: setupTestAccount, Program: setupTestApp}, "code")
			if got.Outcome != applications.SetupOutcomeForbidden || got.Reason != "manifest:use:code" || calls != 0 || q.pending != nil {
				t.Fatal(got, calls, q.pending)
			}
		})
	}
}

func requestKeptSetup(t *testing.T, run *selfRegisteredRun, program string) applications.SetupResult {
	t.Helper()
	decide := run.d.decide
	run.d.decide = func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil }
	defer func() { run.d.decide = decide }()
	q := &setupRequests{lastRead: time.Now()}
	return q.request(run.ctx, run.d, rights.Subject{Account: run.d.owner, Program: program}, "code")
}

func TestRequestSetupNativeVerificationAllowsProgressAndRejectsStaleKeep(t *testing.T) {
	for _, change := range []string{"unchanged", "revision", "image", "binding", "revoked"} {
		t.Run(change, func(t *testing.T) {
			run, request := agentUpdateFixture(t)
			name := request.Members[0].Application
			program := strings.TrimSuffix(request.Members[0].StagedProgram, ".staged")
			read := run.d.agentImage
			entered, release := make(chan struct{}), make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			run.d.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
				image, err := read(ctx, program, binding)
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return image, ctx.Err()
				}
				return image, err
			}
			result := make(chan applications.SetupResult, 1)
			go func() { result <- requestKeptSetup(t, run, program) }()
			<-entered
			ctx, cancel := context.WithTimeout(run.ctx, time.Second)
			defer cancel()
			if !run.d.lock(ctx) {
				close(release)
				released = true
				<-result
				t.Fatal("native verification blocked directory progress")
			}
			err := run.d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
				marks, base, err := run.d.readSelfMarksAtDescriptorLocked(ctx)
				if err != nil {
					return err
				}
				mark := marks[name]
				switch change {
				case "revision":
					mark.RegistrationRevision = strings.Repeat("e", 48)
				case "image":
					mark.ProgramKey = "different-current-image"
				case "binding":
					mark.Agent.Binding = strings.Repeat("f", 64)
				case "revoked":
					mark.Agent.State, mark.Agent.AnsweredBy, mark.Agent.AnsweredMs, mark.Question = "none", "", 0, ""
				}
				marks[name] = mark
				run.d.selfBase = base
				return run.d.persistUnconfirmedAtDescriptorLocked(ctx, marks, legacy)
			})
			run.d.unlock()
			if err != nil {
				t.Fatal(err)
			}
			close(release)
			released = true
			got := <-result
			if change == "unchanged" {
				if got.Outcome != applications.SetupOutcomeAsked {
					t.Fatal(got)
				}
			} else if got.Outcome != applications.SetupOutcomeForbidden || got.Reason != "manifest:use:code" {
				t.Fatal("stale captured Keep admitted", got)
			}
		})
	}
}

func TestRequestSetupRejectsKeptMarkProgramMismatch(t *testing.T) {
	run, request := agentUpdateFixture(t)
	name := request.Members[0].Application
	program := strings.TrimSuffix(request.Members[0].StagedProgram, ".staged")
	if !run.d.lock(run.ctx) {
		t.Fatal("directory lock")
	}
	err := run.d.withRegistrationStorageLocked(run.ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, base, err := run.d.readSelfMarksAtDescriptorLocked(run.ctx)
		if err != nil {
			return err
		}
		mark := marks[name]
		mark.Program = run.f.answerer.Program
		if !mark.validAgent() {
			t.Fatal("mismatched mark must retain a valid shape")
		}
		marks[name] = mark
		run.d.selfBase = base
		return run.d.persistUnconfirmedAtDescriptorLocked(run.ctx, marks, legacy)
	})
	run.d.unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got := requestKeptSetup(t, run, program); got.Outcome != applications.SetupOutcomeForbidden || got.Reason != "manifest:use:code" {
		t.Fatal("different mark program borrowed native image", got)
	}
}

func TestRequestSetupPersonKeptUnsignedImageAndReplacement(t *testing.T) {
	run, request := agentUpdateFixture(t)
	member := request.Members[0]
	program := strings.TrimSuffix(member.StagedProgram, ".staged")
	image, err := run.d.currentAgentImage(run.ctx, member.Application)
	if err != nil || image.Release != nil {
		t.Fatal("fixture must use ordinary unsigned Keep", image, err)
	}
	if got := requestKeptSetup(t, run, program); got.Outcome != applications.SetupOutcomeAsked {
		t.Fatal("person-kept exact unsigned image refused", got)
	}
	decide := run.d.decide
	run.d.decide = func(context.Context, rights.Subject, string, string) (bool, error) { return false, nil }
	q := &setupRequests{lastRead: time.Now()}
	if got := q.request(run.ctx, run.d, rights.Subject{Account: run.d.owner, Program: program}, "code"); got.Outcome != applications.SetupOutcomeForbidden || !strings.HasPrefix(got.Reason, "rights:") || q.pending != nil {
		t.Fatal("unsigned Keep granted question authority", got)
	}
	run.d.decide = decide
	excluded := run.d.agentExcluded
	run.d.agentExcluded = func(context.Context, string, string) (bool, error) { return true, nil }
	if got := requestKeptSetup(t, run, program); got.Outcome != applications.SetupOutcomeForbidden {
		t.Fatal("installed shared gateway exclusion bypassed", got)
	}
	run.d.agentExcluded = excluded
	if err := os.Rename(program, program+".kept"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("replacement unkept image"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := requestKeptSetup(t, run, program); got.Outcome != applications.SetupOutcomeForbidden || got.Reason != "manifest:use:code" {
		t.Fatal("replacement borrowed Keep", got)
	}
}

func TestRequestSetupAfterCommittedAgentMaintenanceUsesCurrentImage(t *testing.T) {
	run, request := agentUpdateFixtureWithImage(t, func(run *selfRegisteredRun) {
		read := run.d.agentImage
		run.d.agentMaintenance = true
		run.d.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
			image, err := read(ctx, program, binding)
			if err != nil {
				return image, err
			}
			pin, err := updateImage(ctx, program)
			if err != nil {
				return image, err
			}
			release := setupKeptHelper(program).Agent.KeptRelease.Current
			release.ImageKey, release.ImageHash, release.ImageBytes = pin.Key, pin.Hash, pin.Bytes
			payload, err := os.ReadFile(program)
			if err != nil {
				return image, err
			}
			if strings.HasPrefix(string(payload), "new") {
				release.Release[3] = 2
			}
			image.Proof, image.Release = identity.ProofBound, &release
			return image, nil
		}
	})
	ready := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if ready.Reason != "update_ready" {
		t.Fatal(ready)
	}
	publishAgentUpdateFixture(t, request)
	if committed := run.d.commitAgentUpdate(run.ctx, run.f.panel, ready.Intent); committed.Reason != "updated" {
		t.Fatal(committed)
	}
	for _, member := range request.Members {
		mark := run.d.unconfirmed[member.Application]
		if mark.Agent.KeptRelease.FileKey == mark.ProgramKey || mark.Agent.KeptRelease.Revision == mark.RegistrationRevision {
			t.Fatal("fixture did not preserve original Keep provenance", mark)
		}
		if got := requestKeptSetup(t, run, strings.TrimSuffix(member.StagedProgram, ".staged")); got.Outcome != applications.SetupOutcomeAsked {
			t.Fatal("current maintained image refused", got)
		}
	}
}

func TestRequestSetupKeptHelperDeclarationStillNeedsQuestionRight(t *testing.T) {
	for _, use := range []string{"code", "image", "video"} {
		d := setupDirectory(map[string]bool{})
		q := &setupRequests{lastRead: time.Now()}
		got := q.request(context.Background(), d, rights.Subject{Account: setupTestAccount, Program: setupTestApp}, use)
		if got.Outcome != applications.SetupOutcomeForbidden || !strings.HasPrefix(got.Reason, "rights:") || q.pending != nil {
			t.Fatal(use, got)
		}
		d = setupDirectory(setupGrants())
		got = q.request(context.Background(), d, rights.Subject{Account: setupTestAccount, Program: setupTestApp}, use)
		if got.Outcome != applications.SetupOutcomeAsked || q.pending == nil {
			t.Fatal(use, got)
		}
	}
}
