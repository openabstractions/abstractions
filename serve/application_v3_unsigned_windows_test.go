//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// These tests register real unsigned PE files through the production
// applications@3 decoder: the manifest resource is an applications@3
// manifest, and the image carries no signature.
func unsignedTestDecoder(raw []byte, program string, held *heldApplicationManifest) (wire.ApplicationDescriptor, string, error) {
	return (&applicationV3Gateway{}).decoder(raw, program, held)
}

type unsignedRun struct {
	*selfRegisteredRun
	program string
	subject rwire.Subject
}

func newUnsignedRun(t *testing.T, title string) *unsignedRun {
	t.Helper()
	run := newSelfRegisteredRun(t)
	if err := run.d.attachApplicationV3(run.ctx, unsignedTestDecoder); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "comfy-helper.exe")
	writeUnsignedImage(t, program, title, false)
	return &unsignedRun{selfRegisteredRun: run, program: program,
		subject: rwire.Subject{Account: run.f.answerer.Account, Program: identity.NormalizeSubjectProgram(program)}}
}

func writeUnsignedImage(t *testing.T, program, title string, variant bool) {
	t.Helper()
	raw := registration.EncodeManifest(&registration.Manifest{Version: "applications@3", Name: "comfyui", Title: title,
		Host: registration.HostRecipe{ReadinessDeadlineMs: 1000}, Offers: appmodel.CommandDeclaration{Version: "appmodel@1"}})
	if err := os.WriteFile(program, manifestPE(raw, variant), 0o600); err != nil {
		t.Fatal(err)
	}
}

func imagePin(t *testing.T, program string) (string, int64) {
	t.Helper()
	b, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), int64(len(b))
}

func (run *unsignedRun) entry() (applicationV3Registration, applicationV3ManifestFile, bool) {
	run.d.mu.Lock()
	defer run.d.mu.Unlock()
	e, ok := run.d.v3.entries["comfyui"]
	return e, run.d.v3.files["comfyui"], ok
}

func (run *unsignedRun) register(t *testing.T) applicationV3Registration {
	t.Helper()
	out := run.d.registerApplicationV3(run.ctx, run.subject, fileid.Self, run.program, "")
	if out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("unsigned self-registration: %+v", out)
	}
	e, f, ok := run.entry()
	if !ok || e.State != "pending" || f.Source.signed() {
		t.Fatalf("unsigned registration is not pending and unsigned: %+v %+v", e, f.Source)
	}
	image, size := imagePin(t, run.program)
	if f.Image != image || f.ImageSize != size {
		t.Fatal("the registration does not pin the held image", f.Image, f.ImageSize)
	}
	return e
}

func (run *unsignedRun) keep(t *testing.T) applicationV3Registration {
	t.Helper()
	q := selfRegisteredQuestion(t, run.f, run.d, "comfyui")
	if q.PublisherTrusted {
		t.Fatal("an unsigned registration recorded a trusted publisher")
	}
	answer(t, run.f, q.ID, "keep")
	waitDirectory(t, run.d, func() bool { e, _, _ := run.entry(); return e.State == "enabled" })
	e, _, _ := run.entry()
	return e
}

func pendingReview(t *testing.T, f *answererFixture) asks.Record {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, _, err := f.a.book.ApplicationHistoryScoped()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Record.Key == asks.ProgramReviewKey && e.Record.Pending() {
				return e.Record
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no review question was asked")
	return asks.Record{}
}

func TestUnsignedManifestReaderReturnsItsPin(t *testing.T) {
	program := filepath.Join(t.TempDir(), "unsigned.exe")
	if err := os.WriteFile(program, manifestPE([]byte("{}"), false), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := openSignedApplicationManifest(context.Background(), fileid.Self, program)
	if err != nil {
		t.Fatal(err)
	}
	image, size := imagePin(t, program)
	if h.Signed || h.Pinned || h.ImageSHA256 != image || h.ImageSize != size || string(h.Bytes) != "{}" {
		t.Fatalf("unsigned read: signed=%t pinned=%t %s %d", h.Signed, h.Pinned, h.ImageSHA256, h.ImageSize)
	}
	if err := h.ownedImage().admit(context.Background()); err == nil {
		t.Fatal("an unpinned unsigned image was admitted as an own host")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(program, program+".moved"); err != nil {
		t.Fatal("the reader retained the file", err)
	}
}

func TestUnsignedProgramRegistersPendingAndKeepAdmitsIt(t *testing.T) {
	run := newUnsignedRun(t, "ComfyUI")
	pending := run.register(t)
	if _, err := run.d.revalidateApplicationV3(run.ctx, fileid.Self, "comfyui", pending.Revision); err == nil {
		t.Fatal("a pending unsigned program was admitted before the person kept it")
	}
	kept := run.keep(t)
	held, err := run.d.revalidateApplicationV3(run.ctx, fileid.Self, "comfyui", kept.Revision)
	if err != nil {
		t.Fatal("the kept unsigned program was refused", err)
	}
	defer held.Close()
	if !held.Pinned || held.Signed {
		t.Fatal("the kept image was not admitted by its pin")
	}
	if err := held.ownedImage().admit(run.ctx); err != nil {
		t.Fatal("the pinned image cannot start as its own host", err)
	}
}

func TestUnsignedProgramRemoveDeletesIt(t *testing.T) {
	run := newUnsignedRun(t, "ComfyUI")
	run.register(t)
	q := selfRegisteredQuestion(t, run.f, run.d, "comfyui")
	answer(t, run.f, q.ID, "remove")
	waitDirectory(t, run.d, func() bool { _, _, ok := run.entry(); return !ok })
	read := run.f.a.rights.policy.ReadRule(run.subject, ActionApplicationRegisterSelf, "account")
	if read.Record == nil || read.Record.Rule.Permit {
		t.Fatalf("Remove did not block the program from registering itself again: %+v", read)
	}
}

func TestUnsignedProgramChangedImageAsksForReview(t *testing.T) {
	run := newUnsignedRun(t, "ComfyUI")
	run.register(t)
	kept := run.keep(t)
	writeUnsignedImage(t, run.program, "ComfyUI", true)
	if _, err := run.d.revalidateApplicationV3(run.ctx, fileid.Self, "comfyui", kept.Revision); !errors.Is(err, errApplicationV3ImageChanged) {
		t.Fatal("a changed unsigned image was not paused", err)
	}
	review := pendingReview(t, run.f)
	_, keptFile, _ := run.entry()
	title, body, details, err := asks.ProgramReviewPresentation(review.Content.Slots)
	changed, _ := imagePin(t, run.program)
	if err != nil || title != "Use the changed ComfyUI?" || review.Text != body ||
		body != "Use it only if you updated it yourself." || details != "Program: "+keptFile.Source.Program+"\nCurrent file SHA-256: "+changed {
		t.Fatalf("review words: %q %q %q %v", title, body, details, err)
	}
	if len(review.Options) != 2 || review.Options[0] != "keep" || review.Options[1] != "not_now" {
		t.Fatal("review options", review.Options)
	}
	if run.f.answerable(run.f.me(), review, "keep") {
		t.Fatal("an assistant may answer the review")
	}
	answer(t, run.f, review.ID, "keep")
	waitDirectory(t, run.d, func() bool { e, _, _ := run.entry(); return e.Revision != kept.Revision })
	e, f, _ := run.entry()
	image, size := imagePin(t, run.program)
	if e.State != "enabled" || f.Image != image || f.ImageSize != size {
		t.Fatalf("Keep did not pin the reviewed image: %+v", e)
	}
	held, err := run.d.revalidateApplicationV3(run.ctx, fileid.Self, "comfyui", e.Revision)
	if err != nil {
		t.Fatal("the reviewed image was refused", err)
	}
	held.Close()
	// The new pin is the registration's durable record.
	run.restart(t)
	if _, f, _ := run.entry(); f.Image != image || f.ImageSize != size || f.Source.signed() {
		t.Fatal("the reviewed pin did not survive a restart", f.Image, f.ImageSize)
	}
	held, err = run.d.revalidateApplicationV3(run.ctx, fileid.Self, "comfyui", e.Revision)
	if err != nil {
		t.Fatal("the reviewed image was refused after a restart", err)
	}
	held.Close()
}

// The review names the program by the title the person kept: an image that
// declares another title is no continuation and is never kept through it.
func TestUnsignedProgramChangedTitleIsNotReviewed(t *testing.T) {
	run := newUnsignedRun(t, "ComfyUI")
	run.register(t)
	kept := run.keep(t)
	writeUnsignedImage(t, run.program, "ComfyUI Renamed", true)
	if _, err := run.d.revalidateApplicationV3(run.ctx, fileid.Self, "comfyui", kept.Revision); !errors.Is(err, errApplicationV3ImageChanged) {
		t.Fatal("a changed unsigned image was not paused", err)
	}
	run.waitReviewClosed(t)
	run.noPendingReview(t)
	if e, f, _ := run.entry(); e.Revision != kept.Revision || f.Image == "" {
		t.Fatal("a retitled image changed the registration")
	}
	target := asks.ProgramReviewTarget{Program: run.subject.Program, Revision: kept.Revision, Title: "ComfyUI"}
	target.Image, _ = imagePin(t, run.program)
	if err := run.d.keepUnsignedImage(run.ctx, "comfyui", target); err == nil {
		t.Fatal("Keep adopted an image that declares another title")
	}
}

func (run *unsignedRun) restart(t *testing.T) {
	t.Helper()
	run.start(t)
	if err := run.d.attachApplicationV3(run.ctx, unsignedTestDecoder); err != nil {
		t.Fatal(err)
	}
}

func (run *unsignedRun) waitReviewClosed(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		run.k.unsigned.mu.Lock()
		open := run.k.unsigned.open["comfyui"]
		run.k.unsigned.mu.Unlock()
		if !open {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the review did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (run *unsignedRun) noPendingReview(t *testing.T) {
	t.Helper()
	entries, _, err := run.f.a.book.ApplicationHistoryScoped()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Record.Key == asks.ProgramReviewKey && e.Record.Pending() {
			t.Fatal("a review question is pending", e.Record.Text)
		}
	}
}

func (run *unsignedRun) declineReview(t *testing.T, review asks.Record) {
	t.Helper()
	answer(t, run.f, review.ID, "not_now")
	deadline := time.Now().Add(10 * time.Second)
	for {
		run.k.unsigned.mu.Lock()
		declined, open := run.k.unsigned.declined["comfyui"], run.k.unsigned.open["comfyui"]
		run.k.unsigned.mu.Unlock()
		if declined != "" && !open {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Not now was not applied")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUnsignedProgramNotNowStaysPaused(t *testing.T) {
	run := newUnsignedRun(t, "ComfyUI")
	run.register(t)
	kept := run.keep(t)
	writeUnsignedImage(t, run.program, "ComfyUI", true)
	if _, err := run.d.revalidateApplicationV3(run.ctx, fileid.Self, "comfyui", kept.Revision); !errors.Is(err, errApplicationV3ImageChanged) {
		t.Fatal("a changed unsigned image was not paused", err)
	}
	review := pendingReview(t, run.f)
	run.declineReview(t, review)
	if _, err := run.d.revalidateApplicationV3(run.ctx, fileid.Self, "comfyui", kept.Revision); !errors.Is(err, errApplicationV3ImageChanged) {
		t.Fatal("Not now admitted the changed image", err)
	}
	run.waitReviewClosed(t)
	run.noPendingReview(t)
	if e, _, _ := run.entry(); e.Revision != kept.Revision {
		t.Fatal("Not now changed the registration")
	}
	// The person starting the program is asked again.
	run.d.unsignedLaunch("comfyui", true)
	if _, err := run.d.revalidateApplicationV3(run.ctx, fileid.Self, "comfyui", kept.Revision); !errors.Is(err, errApplicationV3ImageChanged) {
		t.Fatal("a start admitted the changed image", err)
	}
	again := pendingReview(t, run.f)
	if again.ID == review.ID {
		t.Fatal("the answered review was reused")
	}
	run.declineReview(t, again)
	// A restart forgets Not now: the next use asks again.
	run.restart(t)
	if _, err := run.d.revalidateApplicationV3(run.ctx, fileid.Self, "comfyui", kept.Revision); !errors.Is(err, errApplicationV3ImageChanged) {
		t.Fatal("a restart admitted the changed image", err)
	}
	if after := pendingReview(t, run.f); after.ID == again.ID || after.ID == review.ID {
		t.Fatal("the restart reused an answered review")
	}
}

// A failed unsigned read releases the reader's hold on the file.
func TestUnsignedManifestReaderReleasesTheFileOnError(t *testing.T) {
	program := filepath.Join(t.TempDir(), "unsigned.bin")
	if err := os.WriteFile(program, []byte("not a program image"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := openSignedApplicationManifest(context.Background(), fileid.Self, program)
	if err == nil || h != nil {
		if h != nil {
			h.Close()
		}
		t.Fatal("an unsigned file without a declaration was read")
	}
	if err := os.Rename(program, program+".moved"); err != nil {
		t.Fatal("the failed reader retained the file", err)
	}
}

func TestUnsignedProgramCannotTakeAReservedTitle(t *testing.T) {
	run := newUnsignedRun(t, "OpenAbstractions Helper")
	if out := run.d.registerApplicationV3(run.ctx, run.subject, fileid.Self, run.program, ""); out.Outcome == wire.ApplicationOutcomeApplied {
		t.Fatal("an unsigned program took a reserved title", out)
	}
	if _, _, ok := run.entry(); ok {
		t.Fatal("a refused registration was stored")
	}
}

// Setup re-pins, as the installing operator, the entry of an unsigned app it
// updated (architect ruling 2026-11-10). The person kept the earlier version
// at cu\0.8.0-r17; the update installs cu\0.8.0-r22. Setup's Register names
// the entry's revision and the new program: the entry keeps its source and
// Keep, takes the new path and image pin, and the app's own republish of that
// image then updates it silently. A stale revision, a pending entry and an
// operator's unsigned registration without a revision change nothing; an
// image other than the pinned one at the new path meets conflict "identity".
func TestSetupRepinsAnUnsignedUpdateAndTheAppRepublishesSilently(t *testing.T) {
	run := newUnsignedRun(t, "ComfyUI")
	run.d.operator = func(program string) bool { return identity.SameSubjectProgram(program, run.f.panel.Program) }
	run.register(t)
	kept := run.keep(t)
	updated := filepath.Join(filepath.Dir(filepath.Dir(run.program)), "0.8.0-r22", "launch-helper.exe")
	if err := os.MkdirAll(filepath.Dir(updated), 0o700); err != nil {
		t.Fatal(err)
	}
	writeUnsignedImage(t, updated, "ComfyUI", true)

	if out := run.d.registerApplicationV3(run.ctx, run.f.panel, fileid.Self, updated, ""); out.Outcome != wire.ApplicationOutcomeForbidden || out.Reason != "unsigned_source" {
		t.Fatal("an operator registered an unsigned image without the entry's revision:", out)
	}
	if out := run.d.registerApplicationV3(run.ctx, run.f.panel, fileid.Self, updated, "stale"+kept.Revision[5:]); out.Outcome != wire.ApplicationOutcomeConflict || out.Reason != "revision" {
		t.Fatal("a stale re-pin:", out)
	}
	// The person kept the app by its title: an image declaring another
	// title is no continuation, and the Keep never carries over to it.
	retitled := filepath.Join(filepath.Dir(updated), "retitled-helper.exe")
	writeUnsignedImage(t, retitled, "ComfyUI Renamed", true)
	if out := run.d.registerApplicationV3(run.ctx, run.f.panel, fileid.Self, retitled, kept.Revision); out.Outcome != wire.ApplicationOutcomeConflict || out.Reason != "identity" {
		t.Fatal("a re-pin to another title:", out)
	}
	if e, f, _ := run.entry(); e != kept || !identity.SameSubjectProgram(f.Source.Program, run.program) {
		t.Fatal("a refused re-pin changed the entry", e, f.Source.Program)
	}

	out := run.d.registerApplicationV3(run.ctx, run.f.panel, fileid.Self, updated, kept.Revision)
	if out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("Setup's re-pin:", out)
	}
	repinned, f, _ := run.entry()
	image, size := imagePin(t, updated)
	if repinned.Source != "self" || repinned.State != "enabled" || !identity.SameSubjectProgram(f.Source.Program, updated) || f.Image != image || f.ImageSize != size {
		t.Fatalf("the re-pin did not keep the entry with the new pin: %+v %s", repinned, f.Source.Program)
	}

	// The app's own republish from the new path carries the pin: silent.
	self := rwire.Subject{Account: run.subject.Account, Program: identity.NormalizeSubjectProgram(updated)}
	if out := run.d.registerApplicationV3(run.ctx, self, fileid.Self, updated, ""); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("the app's republish after the re-pin:", out)
	}
	if e, _, _ := run.entry(); e.State != "enabled" || e.Source != "self" {
		t.Fatal("the republish asked again", e)
	}
	run.noPendingReview(t)
	held, err := run.d.revalidateApplicationV3(run.ctx, fileid.Self, "comfyui", func() string { e, _, _ := run.entry(); return e.Revision }())
	if err != nil {
		t.Fatal("the re-pinned image cannot start", err)
	}
	held.Close()

	// Another image at the re-pinned path is another identity.
	writeUnsignedImage(t, updated, "ComfyUI", false)
	before, _, _ := run.entry()
	if out := run.d.registerApplicationV3(run.ctx, self, fileid.Self, updated, ""); out.Outcome != wire.ApplicationOutcomeConflict || out.Reason != "identity" {
		t.Fatal("an image Setup did not pin:", out)
	}
	if e, _, _ := run.entry(); e != before {
		t.Fatal("the identity conflict changed the entry")
	}
}

// A pending entry is the person's to keep: an operator's re-pin of it meets
// conflict "state" and the entry stays pending.
func TestSetupRepinLeavesAPendingEntryToThePerson(t *testing.T) {
	run := newUnsignedRun(t, "ComfyUI")
	run.d.operator = func(program string) bool { return identity.SameSubjectProgram(program, run.f.panel.Program) }
	pending := run.register(t)
	updated := filepath.Join(t.TempDir(), "launch-helper.exe")
	writeUnsignedImage(t, updated, "ComfyUI", true)
	if out := run.d.registerApplicationV3(run.ctx, run.f.panel, fileid.Self, updated, pending.Revision); out.Outcome != wire.ApplicationOutcomeConflict || out.Reason != "state" {
		t.Fatal("a re-pin of a pending entry:", out)
	}
	if e, _, _ := run.entry(); e != pending {
		t.Fatal("the refused re-pin changed the pending entry")
	}
}
