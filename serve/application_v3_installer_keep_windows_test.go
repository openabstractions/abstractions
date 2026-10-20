//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	applications "github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
)

func TestInstallerKeepReservationBindsPublishedPredecessorAndCandidate(t *testing.T) {
	run := repinMessageRun(t)
	run.register(t)
	pending, _, _ := run.entry()
	run.keep(t)
	predecessor, _, _ := run.entry()
	program := repinUpdate(t, run, "0.8.0-r39")
	held, err := openSignedApplicationManifest(run.ctx, fileid.Self, program)
	if err != nil {
		t.Fatal(err)
	}
	candidate := applicationV3ManifestFile{
		Raw: held.Bytes, Digest: applicationV3Hash(held.Bytes),
		Source: applicationV3Source{Program: applicationV3HeldProgram(held), FileKey: applicationV3HeldKey(held), Code: held.Code, Proof: held.Proof},
		Image:  held.ImageSHA256, ImageSize: held.ImageSize,
	}
	held.Close()
	now := run.d.clock()
	expectation := connectExpectation{program: candidate.Source.Program, sha256: candidate.Image, expires: now.Add(time.Minute)}
	if _, ok := reserveInstallerKeep("comfyui", expectation, pending, pending.Revision, candidate, now); ok {
		t.Fatal("reserved a pending registration")
	}
	reservation, ok := reserveInstallerKeep("comfyui", expectation, predecessor, predecessor.Revision, candidate, now)
	if !ok {
		t.Fatal("could not reserve the kept predecessor")
	}
	if got, ok := reservation.carry("comfyui", expectation, predecessor, "", candidate, now); !ok || got != predecessor.FileRevision {
		t.Fatalf("first lineage: %q, %v", got, ok)
	}
	if got, ok := reservation.carry("comfyui", expectation, predecessor, "existing-lineage", candidate, now); !ok || got != "existing-lineage" {
		t.Fatalf("successive lineage: %q, %v", got, ok)
	}
	foreign := candidate
	foreign.Source.FileKey += "-foreign"
	if _, ok := reservation.carry("comfyui", expectation, predecessor, "", foreign, now); ok {
		t.Fatal("reservation matched a replacement file")
	}
	if _, ok := reservation.carry("comfyui", expectation, predecessor, "", candidate, expectation.expires); ok {
		t.Fatal("expired reservation carried a lineage")
	}

	// A failed real index write leaves the predecessor selected. The pure
	// reservation can be checked again without consuming the expectation.
	run.d.v3.beforeWrite = func(_ context.Context, _ map[string]applicationV3Registration) error {
		return errors.New("index write refused")
	}
	if out := run.d.registerApplicationV3(run.ctx, run.f.panel, fileid.Self, program, predecessor.Revision); out.Outcome == wire.ApplicationOutcomeApplied {
		t.Fatal("failed index write published the candidate", out)
	}
	run.d.v3.beforeWrite = nil
	if current, _, _ := run.entry(); current != predecessor {
		t.Fatalf("failed index write changed predecessor: %+v", current)
	}
	if out := run.d.changeApplicationV3(run.ctx, run.f.panel, "comfyui", predecessor.Revision, "remove"); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("Remove:", out)
	}
	run.register(t)
	run.keep(t)
	readded, _, _ := run.entry()
	if readded.FileRevision == predecessor.FileRevision {
		t.Fatal("re-add reused the removed file revision")
	}
	if _, ok := reservation.carry("comfyui", expectation, readded, "", candidate, now); ok {
		t.Fatal("reservation survived removal and re-add")
	}
}

func TestInstallerKeepInterruptedPublicationResumesWithSameHeldCandidate(t *testing.T) {
	run, updated := stuckRepin(t)
	entry, _, _ := run.entry()
	predecessor, ok := strings.CutPrefix(entry.Question, messageRemovalQuestion)
	if !ok || predecessor == "" {
		t.Fatalf("missing predecessor revision: %+v", entry)
	}
	// Register opens and measures the same candidate again. Its acceptance
	// time changes, so it cannot reconstruct the reserved manifest bytes.
	run.d.now = func() time.Time { return time.Now().Add(time.Minute) }
	out := run.d.registerApplicationV3(run.ctx, run.f.panel, fileid.Self, updated, predecessor)
	if out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("same candidate did not recover the interrupted re-pin:", out)
	}
	if current, _, _ := run.entry(); current.State != "enabled" || current.Revision != out.Revision || current.FileRevision != entry.FileRevision {
		t.Fatalf("retry did not finalize the staged file: %+v", current)
	}
	requireOneHolder(t, run, updated)
	requireNoComfyMessagePermit(t, run)
}

func TestInstallerKeepPendingPublicationFencesBothImagesButHidesPredecessor(t *testing.T) {
	run := repinMessageRun(t)
	run.register(t)
	predecessor := run.keep(t)
	updated := repinUpdate(t, run, "0.8.0-r39")
	invalidate := run.d.messageBlock
	var duringInvalidation bool
	run.d.messageBlock = func(ctx context.Context, name string) error {
		duringInvalidation = true
		view, found, err := run.d.applicationV3Declaration(ctx, name)
		if err != nil || !found || view.State != "removing" || !identity.SameSubjectProgram(view.Program, updated) {
			t.Fatalf("pending read must currently see the staged candidate: %+v, %v, %v", view, found, err)
		}
		for _, revision := range []string{predecessor.Revision, view.Revision} {
			held, err := run.d.revalidateApplicationV3(ctx, fileid.Self, name, revision)
			if held != nil {
				held.Close()
			}
			if err == nil {
				t.Fatalf("pending replacement admitted image at revision %s", revision)
			}
		}
		return invalidate(ctx, name)
	}
	out := run.d.registerApplicationV3(run.ctx, run.f.panel, fileid.Self, updated, predecessor.Revision)
	if !duringInvalidation || out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("re-pin did not run real invalidation and publish: %v, %+v", duringInvalidation, out)
	}
	requireNoComfyMessagePermit(t, run)
}

func TestInstallerKeepPinnedPublicationCarriesOnlyAtCommit(t *testing.T) {
	run := repinMessageRun(t)
	run.register(t)
	run.keep(t)
	predecessor, _, _ := run.entry()
	program := repinUpdate(t, run, "0.8.0-r39-pin")
	installerKeepExpect(t, run, program)
	invalidate := run.d.messageBlock
	run.d.messageBlock = func(ctx context.Context, name string) error {
		staged, _, _ := run.entry()
		if staged.KeepIdentity != "" || staged.KeepPending.FileRevision != predecessor.FileRevision {
			t.Fatalf("identity was carried before the final write: %+v", staged)
		}
		rows, _, _, result := run.d.applicationV3Declarations(ctx, run.f.panel)
		if result != wire.ApplicationOutcomePage || len(rows) != 1 || !identity.SameSubjectProgram(rows[0].Program, run.program) || rows[0].State != "removing" || rows[0].Revision != predecessor.Revision {
			t.Fatalf("quiet read lost the predecessor: %+v, %v", rows, result)
		}
		for _, revision := range []string{predecessor.Revision, staged.Revision} {
			if live, err := run.d.revalidateApplicationV3(ctx, fileid.Self, name, revision); err == nil {
				live.Close()
				t.Fatal("pending replacement admitted effect authority")
			}
		}
		return invalidate(ctx, name)
	}
	out := run.d.registerApplicationV3(run.ctx, run.f.panel, fileid.Self, program, predecessor.Revision)
	entry, _, _ := run.entry()
	if out.Outcome != wire.ApplicationOutcomeApplied || entry.KeepIdentity != predecessor.FileRevision || entry.KeepPending.FileRevision != "" {
		t.Fatalf("final publication did not carry the predecessor identity: %+v, %+v", out, entry)
	}
	// The installed program registers itself again immediately after Setup.
	subject := run.subject
	subject.Program = program
	out = run.d.registerApplicationV3(run.ctx, subject, fileid.Self, program, "")
	afterSelf, _, _ := run.entry()
	if out.Outcome != wire.ApplicationOutcomeApplied || afterSelf.KeepIdentity != predecessor.FileRevision {
		t.Fatalf("register-self lost the installer lineage: %+v, %+v", out, afterSelf)
	}
	requireNoComfyMessagePermit(t, run)
}

func installerKeepExpect(t *testing.T, run *unsignedRun, program string) {
	t.Helper()
	held, err := openSignedApplicationManifest(run.ctx, fileid.Self, program)
	if err != nil {
		t.Fatal(err)
	}
	pin := held.ImageSHA256
	held.Close()
	run.d.connectPanel = run.d.operator
	if out := run.d.expectConnect(run.ctx, run.f.panel, applications.ConnectExpectation{Application: "comfyui", Program: program, SHA256: pin}); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("installer expectation:", out)
	}
}

func TestInstallerKeepPinnedPublicationWithoutMessageReservation(t *testing.T) {
	run := repinMessageRun(t)
	run.d.messageBlock = nil
	run.register(t)
	run.keep(t)
	before, _, _ := run.entry()
	program := repinUpdate(t, run, "0.8.0-direct")
	installerKeepExpect(t, run, program)
	out := run.d.registerApplicationV3(run.ctx, run.f.panel, fileid.Self, program, before.Revision)
	after, _, _ := run.entry()
	if out.Outcome != wire.ApplicationOutcomeApplied || after.KeepIdentity != before.FileRevision || after.KeepPending != (installerKeepPending{}) {
		t.Fatalf("direct publication lost lineage: %+v, %+v", out, after)
	}
}

func TestInstallerKeepPinnedPublicationRenewsAfterRestart(t *testing.T) {
	for _, kind := range []string{"exact", "no expectation", "wrong pin", "expired", "unbound panel"} {
		t.Run(kind, func(t *testing.T) {
			run := repinMessageRun(t)
			run.register(t)
			run.keep(t)
			before, _, _ := run.entry()
			program := repinUpdate(t, run, "0.8.0-resume")
			installerKeepExpect(t, run, program)
			invalidate := run.d.messageBlock
			run.d.messageBlock = func(context.Context, string) error { return errors.New("interrupted") }
			if out := run.d.registerApplicationV3(run.ctx, run.f.panel, fileid.Self, program, before.Revision); out.Outcome != wire.ApplicationOutcomeUnavailable {
				t.Fatal("interrupted publication:", out)
			}
			staged, _, _ := run.entry()
			run.restart(t)
			run.f.a.rights.applications = run.d
			run.f.a.keepAgentEnrollments(run.ctx, run.d)
			run.d.operator = func(program string) bool { return identity.SameSubjectProgram(program, run.f.panel.Program) }
			run.d.connectPanel, run.d.messageBlock = run.d.operator, invalidate
			switch kind {
			case "exact":
				installerKeepExpect(t, run, program)
			case "unbound panel":
				installerKeepExpect(t, run, program)
				run.d.connectPeer = func(context.Context, *identity.Peer) bool {
					t.Fatal("installer Keep invoked live Panel proof without a bound peer")
					return false
				}
			case "wrong pin":
				if out := run.d.expectConnect(run.ctx, run.f.panel, applications.ConnectExpectation{Application: "comfyui", Program: program, SHA256: strings.Repeat("a", 64)}); out.Outcome != wire.ApplicationOutcomeApplied {
					t.Fatal("wrong-pin expectation was not recorded:", out)
				}
			case "expired":
				run.d.now = func() time.Time { return time.Unix(0, staged.KeepPending.Expires).Add(time.Second) }
				installerKeepExpect(t, run, program)
			}
			out := run.d.registerApplicationV3(run.ctx, run.f.panel, fileid.Self, program, staged.Revision)
			after, _, _ := run.entry()
			if kind == "exact" {
				if out.Outcome != wire.ApplicationOutcomeApplied || after.KeepIdentity != before.FileRevision {
					t.Fatalf("exact renewed publication failed: %+v, %+v", out, after)
				}
			} else if out.Outcome == wire.ApplicationOutcomeApplied || after != staged || kind == "expired" && out.Reason != "installer_expectation_expired" {
				t.Fatalf("invalid renewal changed the staged entry: %+v, %+v", out, after)
			}
		})
	}
}

func TestInstallerKeepReceiptRecoversAfterPublicationWithoutCallback(t *testing.T) {
	run := repinMessageRun(t)
	run.register(t)
	run.keep(t)
	before, _, _ := run.entry()
	program := repinUpdate(t, run, "0.8.0-receipt")
	installerKeepExpect(t, run, program)
	out, finish := run.d.registerApplicationV3Held(run.ctx, run.f.panel, fileid.Self, program, before.Revision)
	if out.Outcome != wire.ApplicationOutcomeApplied || finish == nil {
		t.Fatalf("registration did not commit before callback: %+v", out)
	}
	after, file, _ := run.entry()
	if after.KeepReceipt.ID == "" || after.KeepIdentity != before.FileRevision {
		t.Fatalf("durable registry receipt is missing: %+v", after)
	}
	path := filepath.Join(run.d.dir, connectKeptFile)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bookkeeping ran before the postpublication callback: %v", err)
	}
	run.restart(t)
	var holds int
	run.d.programHoldKeep = func(got string) error {
		if !identity.SameSubjectProgram(got, program) {
			t.Fatalf("holds went to another image: %q", got)
		}
		holds++
		return nil
	}
	run.d.writeKeptHolds(run.ctx)
	run.d.writeKeptHolds(run.ctx)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kept map[string]connectKept
	if err := json.Unmarshal(raw, &kept); err != nil {
		t.Fatal(err)
	}
	record := kept["comfyui"]
	if record.Receipt != after.KeepReceipt.ID || record.FileKey != file.Source.FileKey || record.SHA256 != file.Image || !record.HoldsWritten || holds != 1 {
		t.Fatalf("receipt did not recover idempotently: %+v, holds=%d", record, holds)
	}
}

func TestInstallerKeepDirectPublicationUsesOneExpiryDecision(t *testing.T) {
	for _, expired := range []bool{false, true} {
		run := repinMessageRun(t)
		run.d.messageBlock = nil
		run.register(t)
		run.keep(t)
		before, _, _ := run.entry()
		program := repinUpdate(t, run, "0.8.0-expiry")
		installerKeepExpect(t, run, program)
		deadline := run.d.connects.pending["comfyui"].expires
		samples := 0
		run.d.now = func() time.Time {
			samples++
			// Register takes two timestamps before it enters the registry.
			// The third sample is the locked publication decision.
			if expired || samples > 3 {
				return deadline
			}
			return deadline.Add(-time.Nanosecond)
		}
		out, _ := run.d.registerApplicationV3Held(run.ctx, run.f.panel, fileid.Self, program, before.Revision)
		if out.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatalf("expired=%v: publication failed: %+v", expired, out)
		}
		run.restart(t)
		after, _, _ := run.entry()
		if expired {
			if after.KeepIdentity != "" || after.KeepReceipt != (installerKeepReceipt{}) {
				t.Fatalf("expired expectation wrote lineage or receipt: %+v", after)
			}
		} else if after.KeepIdentity != before.FileRevision || after.KeepReceipt.At != deadline.Add(-time.Nanosecond).UnixNano() {
			t.Fatalf("last valid timestamp lost lineage or receipt: %+v (samples=%d)", after, samples)
		}
	}
}
