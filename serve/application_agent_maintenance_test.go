package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestAgentReleaseUsesExistingHeldResourceReader(t *testing.T) {
	raw := agentReleaseFixture(t, nil)
	image := manifestPE(raw, true)
	binary.LittleEndian.PutUint16(image[600:], uint16(len(agentReleaseResourceName)))
	for i, c := range agentReleaseResourceName {
		binary.LittleEndian.PutUint16(image[602+i*2:], uint16(c))
	}
	got, err := readApplicationNamedResource(bytes.NewReader(image), int64(len(image)), agentReleaseResourceName)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal(string(got), err)
	}
	if _, err := readApplicationManifestResource(bytes.NewReader(image), int64(len(image))); err != errApplicationManifestMissing {
		t.Fatal("private resource became an AppModel manifest", err)
	}
}

func TestAgentReleaseActualProducerDocument(t *testing.T) {
	path := os.Getenv("OA_AGENT_RELEASE_FIXTURE")
	if path == "" {
		t.Skip("existing producer qualification supplies the frozen inert catalogue document")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	document, snapshot, err := agentReleaseSnapshot(raw)
	if err != nil || len(snapshot.Tools) == 0 || len(snapshot.Tools) != len(document.Tools) {
		t.Fatal("actual producer and native parser differ", err)
	}
	if !agentReleaseSubset(snapshot, snapshot) {
		t.Fatal("actual producer snapshot does not preserve its own declaration")
	}
	mark := setupKeptHelper(setupTestApp)
	mark.Agent.KeptRelease.Current.Declaration = snapshot
	for _, modality := range []string{"chat", "image"} {
		if !keptAgentSetupModality(mark, setupTestAccount, setupTestApp, modality) {
			t.Fatal("actual producer lacks kept setup modality", modality)
		}
	}
}

func agentReleaseFixture(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	document := map[string]any{
		"version": 1, "component": agentReleaseComponent, "channel": "stable",
		"release": []int{0, 8, 0, 1}, "requests": []any{}, "needs": map[string]any{"files": []any{}, "models": []any{}}, "verbs": []any{},
		"tools": []any{map[string]any{"name": "existing", "inputSchema": map[string]any{"type": "object"}, "description": "Display"}},
	}
	if change != nil {
		change(document)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func agentMaintenanceFixture(t *testing.T) (*selfRegisteredRun, applications.AgentUpdateRequest) {
	t.Helper()
	return agentUpdateFixtureWithImage(t, func(run *selfRegisteredRun) {
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
			payload, err := os.ReadFile(program)
			if err != nil {
				return image, err
			}
			order := uint32(1)
			if strings.HasPrefix(string(payload), "new") {
				order = 2
			}
			if strings.HasPrefix(string(payload), "third") {
				order = 3
			}
			image.Proof = identity.ProofBound
			image.Release = &agentNativeRelease{Component: agentReleaseComponent, PublisherKey: "authenticode-dn-sha256:" + strings.Repeat("a", 64), Channel: "stable", Release: [4]uint32{0, 8, 0, order}, ImageKey: pin.Key, ImageHash: pin.Hash, ImageBytes: pin.Bytes, Authenticated: true}
			return image, nil
		}
	})
}

func TestAgentMaintenanceTwoUpdatesPreserveOriginalKeep(t *testing.T) {
	run, request := agentMaintenanceFixture(t)
	original := map[string]selfMark{}
	for _, m := range request.Members {
		original[m.Application] = run.d.unconfirmed[m.Application]
	}
	for round := 0; round < 2; round++ {
		before, _, err := run.f.a.book.ApplicationHistory()
		if err != nil {
			t.Fatal(err)
		}
		ready := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
		if ready.Outcome != applications.OutcomeApplied || ready.Reason != "update_ready" || ready.Question != "" {
			t.Fatalf("round %d: %+v", round, ready)
		}
		if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, ready.Intent); got.Reason != "update_ready" {
			t.Fatal(got)
		}
		if round > 0 {
			for _, m := range request.Members {
				_ = os.Remove(m.StagedProgram + ".previous")
			}
		}
		publishAgentUpdateFixture(t, request)
		committed := run.d.commitAgentUpdate(run.ctx, run.f.panel, ready.Intent)
		if committed.Reason != "updated" || len(committed.Enrollments) != 2 {
			t.Fatal(committed)
		}
		replay := run.d.commitAgentUpdate(run.ctx, run.f.panel, ready.Intent)
		if !reflect.DeepEqual(replay, committed) {
			t.Fatal("replay changed receipt", replay, committed)
		}
		for i, m := range request.Members {
			current := run.d.currentAgentEnrollment(run.ctx, m.Application)
			if current.Enrollment == nil || current.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
				t.Fatal(current)
			}
			mark := run.d.unconfirmed[m.Application]
			if mark.Question != original[m.Application].Question || mark.Agent.AnsweredMs != original[m.Application].Agent.AnsweredMs || mark.Agent.KeptRelease.Question != original[m.Application].Question || mark.Agent.KeptRelease.FileKey != original[m.Application].ProgramKey || mark.Agent.KeptRelease.Current.Release[3] != uint32(round+2) {
				t.Fatal("original Keep lost", mark)
			}
			request.Members[i].ExpectedRevision = current.Enrollment.Revision
		}
		after, _, err := run.f.a.book.ApplicationHistory()
		if err != nil || len(before) != len(after) {
			t.Fatal("maintenance synthesized occurrence", len(before), len(after), err)
		}
		if round == 0 {
			if err := os.WriteFile(request.SourceProgram, []byte("third installed gateway"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, m := range request.Members {
				if err := os.WriteFile(m.StagedProgram, []byte("third installed gateway"), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestAgentMaintenanceFreshReviewAndRefusalBoundaries(t *testing.T) {
	for _, kind := range []string{"missing", "unsigned", "same-version", "widened", "foreign", "channel", "downgrade", "revoked"} {
		t.Run(kind, func(t *testing.T) {
			run, request := agentMaintenanceFixture(t)
			read := run.d.agentImage
			run.d.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
				image, err := read(ctx, program, binding)
				if err != nil || image.Release == nil || image.Release.Release[3] == 1 {
					return image, err
				}
				switch kind {
				case "missing":
					image.Release = nil
				case "unsigned":
					image.Proof = identity.ProofUnsigned
					image.Release = nil
				case "same-version":
					image.Release.Release[3] = 1
				case "widened":
					image.Release.Declaration.Tools = []string{strings.Repeat("b", 64)}
				case "foreign":
					image.Release.PublisherKey = "authenticode-dn-sha256:" + strings.Repeat("c", 64)
				case "channel":
					image.Release.Channel = "foreign"
				case "downgrade":
					image.Release.Release = [4]uint32{0, 7, 0, 9}
				case "revoked":
					image.Proof = identity.ProofInvalid
					image.Release = nil
				}
				return image, nil
			}
			got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
			refused := kind == "foreign" || kind == "channel" || kind == "downgrade" || kind == "revoked"
			if refused {
				if got.Outcome != applications.OutcomeConflict || got.Intent != "" || got.Question != "" {
					t.Fatal(got)
				}
			} else if got.Reason != "permission_pending" || got.Question == "" {
				t.Fatal(got)
			}
		})
	}
}

func TestAgentMaintenanceRechecksSignerAndNativeKeepBeforeCommit(t *testing.T) {
	for _, kind := range []string{"revoked", "keep-retired", "connection-removed"} {
		t.Run(kind, func(t *testing.T) {
			run, request := agentMaintenanceFixture(t)
			ready := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
			if ready.Reason != "update_ready" {
				t.Fatal(ready)
			}
			switch kind {
			case "revoked":
				read := run.d.agentImage
				run.d.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
					image, err := read(ctx, program, binding)
					image.Release = nil
					image.Proof = identity.ProofInvalid
					return image, err
				}
			case "keep-retired":
				mark := run.d.unconfirmed[request.Members[0].Application]
				if _, _, err := run.f.a.book.RetireApplicationAuthorized(mark.Question, nil); err != nil {
					t.Fatal(err)
				}
			case "connection-removed":
				m := request.Members[0]
				got := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: m.Application, ExpectedRevision: m.ExpectedRevision, Enabled: false})
				if got.Outcome != applications.OutcomeApplied {
					t.Fatal(got)
				}
			}
			got := run.d.commitAgentUpdate(run.ctx, run.f.panel, ready.Intent)
			if got.Reason == "update_ready" || got.Reason == "updated" {
				t.Fatal("revoked authority survived", got)
			}
		})
	}
}

func TestAgentMaintenanceRequiresRetainedPriorBytesAtFinalCommit(t *testing.T) {
	run, request := agentMaintenanceFixture(t)
	ready := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if ready.Reason != "update_ready" {
		t.Fatal(ready)
	}
	publishAgentUpdateFixture(t, request)
	if err := os.Remove(request.Members[0].StagedProgram + ".previous"); err != nil {
		t.Fatal(err)
	}
	got := run.d.commitAgentUpdate(run.ctx, run.f.panel, ready.Intent)
	if got.Outcome != applications.OutcomeConflict {
		t.Fatal("Commit lost retained prior bytes", got)
	}
}

func TestAgentMaintenanceRepeatedCommitCannotWitnessPluginReady(t *testing.T) {
	run, request := agentMaintenanceFixture(t)
	prepared := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if prepared.Phase == nil || *prepared.Phase != applications.AgentUpdatePhaseStaged || prepared.ReadyUnixMs != nil {
		t.Fatal(prepared)
	}
	publishAgentUpdateFixture(t, request)
	for repeat := 0; repeat < 3; repeat++ {
		result := run.d.commitAgentUpdate(run.ctx, run.f.panel, prepared.Intent)
		if result.Reason != "updated" || result.Phase == nil || *result.Phase != applications.AgentUpdatePhaseCommitted || result.ReadyUnixMs != nil {
			t.Fatal("Commit count supplied fictitious plugin proof", result)
		}
	}
	notices, _, err := run.f.a.book.NotificationHistory()
	if err != nil || len(notices) != 0 {
		t.Fatal("unproved Ready emitted notification", notices, err)
	}
	request.UndoOf = prepared.Intent
	child := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if child.Outcome == applications.OutcomeApplied || child.Intent != "" || child.Question != "" {
		t.Fatal("unhandled Undo became an ordinary Update question", child)
	}
}

func TestAgentMaintenanceFreshReviewedLegacyUpdateEstablishesBaseline(t *testing.T) {
	run, request := agentUpdateFixture(t)
	original := map[string]selfMark{}
	for _, member := range request.Members {
		original[member.Application] = run.d.unconfirmed[member.Application]
	}
	read := run.d.agentImage
	run.d.agentMaintenance = true
	run.d.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
		image, err := read(ctx, program, binding)
		if err != nil {
			return image, err
		}
		raw, err := os.ReadFile(program)
		if err != nil {
			return image, err
		}
		version := uint32(0)
		if strings.HasPrefix(string(raw), "new") {
			version = 2
		}
		if strings.HasPrefix(string(raw), "third") {
			version = 3
		}
		if version == 0 {
			return image, nil
		}
		pin, err := updateImage(ctx, program)
		if err != nil {
			return image, err
		}
		image.Proof = identity.ProofBound
		image.Release = &agentNativeRelease{Component: agentReleaseComponent, PublisherKey: "authenticode-dn-sha256:" + strings.Repeat("a", 64), Channel: "stable", Release: [4]uint32{0, 8, 0, version}, ImageKey: pin.Key, ImageHash: pin.Hash, ImageBytes: pin.Bytes, Authenticated: true}
		return image, nil
	}
	fresh := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if fresh.Reason != "permission_pending" || fresh.Question == "" {
		t.Fatal("missing legacy facts bypassed fresh review", fresh)
	}
	answer(t, run.f, fresh.Question, "update")
	publishAgentUpdateFixture(t, request)
	committed := run.d.commitAgentUpdate(run.ctx, run.f.panel, fresh.Intent)
	if committed.Reason != "updated" {
		t.Fatal(committed)
	}
	for i, member := range request.Members {
		current := run.d.currentAgentEnrollment(run.ctx, member.Application)
		if current.Enrollment == nil || current.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
			t.Fatal(current)
		}
		mark := run.d.unconfirmed[member.Application]
		if mark.Agent.KeptRelease == nil || mark.Agent.KeptRelease.Question != original[member.Application].Question || mark.Agent.KeptRelease.FileKey != original[member.Application].ProgramKey || mark.Agent.KeptRelease.Current.Release[3] != 2 || mark.Question != fresh.Question {
			t.Fatal("fresh review lost actual original Keep or current baseline", mark)
		}
		request.Members[i].ExpectedRevision = current.Enrollment.Revision
		if err := os.WriteFile(member.StagedProgram, []byte("third installed gateway"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(request.SourceProgram, []byte("third installed gateway"), 0600); err != nil {
		t.Fatal(err)
	}
	next := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if next.Reason != "update_ready" || next.Question != "" {
		t.Fatal("fresh reviewed baseline did not retain maintenance continuity", next)
	}
}

func TestAgentReleaseCanonicalDeclarationAndSubset(t *testing.T) {
	raw := agentReleaseFixture(t, nil)
	document, kept, err := agentReleaseSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if document.Release != [4]uint32{0, 8, 0, 1} || len(kept.Tools) != 1 {
		t.Fatal(document, kept)
	}
	_, display, err := agentReleaseSnapshot(agentReleaseFixture(t, func(d map[string]any) {
		d["tools"].([]any)[0].(map[string]any)["description"] = "New display copy"
	}))
	if err != nil || !agentReleaseSubset(display, kept) {
		t.Fatal("display-only change widened declaration", err)
	}
	_, wider, err := agentReleaseSnapshot(agentReleaseFixture(t, func(d map[string]any) {
		d["tools"].([]any)[0].(map[string]any)["inputSchema"] = map[string]any{"type": "string"}
	}))
	if err != nil || agentReleaseSubset(wider, kept) {
		t.Fatal("changed schema acquired existing authority", err)
	}
	if !agentReleaseSubset(agentDeclarationSnapshot{}, kept) {
		t.Fatal("shrink refused")
	}
}

func TestAgentReleaseRefusesAmbiguousOrMissingProducerFacts(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"channel":        func(d map[string]any) { delete(d, "channel") },
		"order":          func(d map[string]any) { d["release"] = []int{0, 8, 0} },
		"zero":           func(d map[string]any) { d["release"] = []int{0, 0, 0, 0} },
		"overflow":       func(d map[string]any) { d["release"] = []int{0, 8, 0, 65536} },
		"publisher":      func(d map[string]any) { d["publisher"] = "untrusted JSON claim" },
		"missing-schema": func(d map[string]any) { delete(d["tools"].([]any)[0].(map[string]any), "inputSchema") },
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := agentReleaseSnapshot(agentReleaseFixture(t, change)); err == nil {
				t.Fatal("accepted incomplete release")
			}
		})
	}
	raw := agentReleaseFixture(t, nil)
	duplicate := append([]byte(`{"channel":"foreign",`), raw[1:]...)
	if _, _, err := agentReleaseSnapshot(duplicate); err == nil {
		t.Fatal("duplicate field accepted")
	}
}

func TestAgentReleaseEvidenceBindsExactImage(t *testing.T) {
	pin := agentUpdateImage{Key: "held-file-key", Hash: strings.Repeat("a", 64), Bytes: 32}
	release := &agentNativeRelease{Component: agentReleaseComponent, PublisherKey: "authenticode-dn-sha256:" + strings.Repeat("b", 64), Channel: "stable", Release: [4]uint32{0, 8, 0, 1}, ImageKey: pin.Key, ImageHash: pin.Hash, ImageBytes: pin.Bytes, Authenticated: true}
	if !release.validFor(pin) {
		t.Fatal("valid injected native evidence refused")
	}
	for _, mutate := range []func(*agentNativeRelease){
		func(r *agentNativeRelease) { r.Authenticated = false },
		func(r *agentNativeRelease) { r.ImageKey += "replaced" },
		func(r *agentNativeRelease) { r.ImageHash = strings.Repeat("c", 64) },
		func(r *agentNativeRelease) { r.PublisherKey = "CN=Same name" },
		func(r *agentNativeRelease) { r.Channel = "" },
	} {
		copy := cloneAgentRelease(release)
		mutate(copy)
		if copy.validFor(pin) {
			t.Fatal("unbound native evidence accepted")
		}
	}
}

func TestAgentMaintenanceDarwinReleaseRemainsIneligible(t *testing.T) {
	old := agentUpdateImage{Key: "old-held-key", Hash: strings.Repeat("a", 64), Bytes: 32}
	pin := agentUpdateImage{Key: "next-held-key", Hash: strings.Repeat("b", 64), Bytes: 48}
	prior := agentNativeRelease{Component: agentReleaseComponent, PublisherKey: "authenticode-dn-sha256:" + strings.Repeat("c", 64), Channel: "stable", Release: [4]uint32{0, 8, 0, 1}, ImageKey: old.Key, ImageHash: old.Hash, ImageBytes: old.Bytes, Authenticated: true}
	proof := &agentMaintenanceCapture{Kept: agentKeptRelease{Current: prior}}
	apple := cloneAgentRelease(&prior)
	apple.PublisherKey = "apple-team:FIXTURETEAM"
	apple.Release[3]++
	apple.ImageKey, apple.ImageHash, apple.ImageBytes = pin.Key, pin.Hash, pin.Bytes
	for name, release := range map[string]*agentNativeRelease{"nil-release": nil, "apple-team": apple} {
		t.Run(name, func(t *testing.T) {
			if release.validFor(pin) {
				t.Fatal("Darwin release acquired authenticated maintenance evidence")
			}
			if eligible, refuse := agentMaintenancePolicy(proof, old, applicationAgentImage{Release: release, Proof: identity.ProofBound}, pin); eligible || refuse {
				t.Fatal("missing supported evidence must retain fresh Update", eligible, refuse)
			}
			d := &applicationDirectory{agentMaintenance: true}
			if d.agentMaintenanceProof(context.Background(), &applicationAgentUpdate{Source: pin, SourceRelease: release}) {
				t.Fatal("enabled maintenance bypassed release eligibility")
			}
		})
	}
}
