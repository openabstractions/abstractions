package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// identityDirectory is an applications@2 directory whose program pins come
// from images: each program path reads as the pin images[path] holds now.
func identityDirectory(t *testing.T, state string, images map[string]applicationLegacyPin) *applicationDirectory {
	t.Helper()
	d, err := openApplications(state, "account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	d.legacyPin = func(_ context.Context, program string) (applicationLegacyPin, error) {
		pin, ok := images[program]
		if !ok {
			return applicationLegacyPin{}, errors.New("no image")
		}
		return pin, nil
	}
	return d
}

func unsignedPin(program, digit string) applicationLegacyPin {
	digest := ""
	for len(digest) < 64 {
		digest += digit
	}
	return applicationLegacyPin{Program: program, Proof: identity.ProofUnsigned, Image: digest, ImageSize: 1024}
}

func signedPin(program, subject string) applicationLegacyPin {
	return applicationLegacyPin{Program: program, Code: identity.Code{Trusted: true, Subject: subject, Issuer: "Example CA"}, Proof: identity.ProofBound}
}

func identityProgram(t *testing.T, name string) string {
	return identity.NormalizeSubjectProgram(filepath.Join(t.TempDir(), name))
}

var identityOperator = rights.Subject{Account: "account", Program: `C:\operator\panel.exe`}

func startedWith(args ...string) *wire.ApplicationActivationRecipe {
	return &wire.ApplicationActivationRecipe{Arguments: args, Readiness: wire.ApplicationInterface{Name: "ready", Protocol: "p", Contract: "c"}, ReadinessTimeoutMs: 5000}
}

// A program that registered under a name keeps it: the same path holding a
// different image, registering again with new start arguments, is another
// identity. It meets conflict and the entry, its arguments and its pin stay
// as they were, on applications@2 and applications@3 alike.
func TestASamePathDifferentImageReregisterConflictsAndLeavesTheEntry(t *testing.T) {
	ctx := context.Background()
	program := identityProgram(t, "app.exe")
	images := map[string]applicationLegacyPin{program: unsignedPin(program, "a")}
	d := identityDirectory(t, t.TempDir(), images)
	kept := wire.ApplicationDescriptor{Name: "app", Program: program, Title: "App", Activation: startedWith("--serve")}
	if r := d.register(ctx, identityOperator, kept); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	images[program] = unsignedPin(program, "b")
	changed := kept
	changed.Activation = startedWith("--serve", "--run-anything")
	if r := d.register(ctx, identityOperator, changed); r.Outcome != wire.ApplicationOutcomeConflict {
		t.Fatal("a different image rewrote the entry:", r)
	}
	if got := d.descriptors["app"]; !sameActivation(got.Activation, kept.Activation) || d.pins["app"] != unsignedPin(program, "a") {
		t.Fatal("the conflict changed the entry", got, d.pins["app"])
	}

	v3 := registration1459Directory(t, t.TempDir())
	native := registration1459Program("native.exe")
	unsigned := func(title, digit string) (wire.ApplicationDescriptor, applicationV3ManifestFile) {
		v, f := registration1459File(t, "native", native, title)
		f.Source.Code, f.Source.Proof = identity.Code{}, identity.ProofUnsigned
		f.Image, f.ImageSize = unsignedPin(native, digit).Image, 1024
		return v, f
	}
	v, f := unsigned("Native", "a")
	if out := v3.publishApplicationV3(ctx, v, f, "self", ""); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	first := v3.v3.entries["native"]
	v, f = unsigned("Native with new arguments", "b")
	if out := v3.publishApplicationV3(ctx, v, f, "self", ""); out.Outcome != wire.ApplicationOutcomeConflict || out.Reason != "identity" {
		t.Fatal("a different image rewrote the @3 entry:", out)
	}
	if v3.v3.entries["native"] != first || v3.descriptors["native"].Title != "Native" {
		t.Fatal("the conflict changed the @3 entry")
	}
}

// The same signer updates silently: a new image signed by the recorded
// subject, issuer and team changes the arguments; another issuer naming the
// same subject is another publisher and conflicts.
func TestASameSignerUpdateApplies(t *testing.T) {
	ctx := context.Background()
	program := identityProgram(t, "signed.exe")
	images := map[string]applicationLegacyPin{program: signedPin(program, "Example Software")}
	d := identityDirectory(t, t.TempDir(), images)
	entry := wire.ApplicationDescriptor{Name: "signed", Program: program, Title: "Signed", Activation: startedWith("--one")}
	if r := d.register(ctx, identityOperator, entry); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	entry.Title, entry.Activation = "Signed 2", startedWith("--two")
	if r := d.register(ctx, identityOperator, entry); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("the same signer's update:", r)
	}
	if got := d.descriptors["signed"]; got.Title != "Signed 2" || !sameActivation(got.Activation, entry.Activation) {
		t.Fatal("not applied", got)
	}
	other := signedPin(program, "Example Software")
	other.Code.Issuer = "Other CA"
	images[program] = other
	entry.Activation = startedWith("--three")
	if r := d.register(ctx, identityOperator, entry); r.Outcome != wire.ApplicationOutcomeConflict {
		t.Fatal("another issuer updated it:", r)
	}

	v3 := registration1459Directory(t, t.TempDir())
	native := registration1459Program("signed-native.exe")
	registration1459Publish(t, v3, "native", native, "Native", "operator", "")
	registration1459Publish(t, v3, "native", native, "Native 2", "operator", "")
	if v3.descriptors["native"].Title != "Native 2" {
		t.Fatal("the same signer's @3 update was not applied")
	}
}

// A path-only entry recorded before pins existed is adopted on its first
// registration: an unchanged registration captures its pin, while a change
// to its start arguments waits for the person's Connect. Once pinned, a
// foreign image under the same path conflicts. A restart keeps the pin.
func TestAnUnpinnedLegacyEntryGetsItsPinAndAForeignChangeThenConflicts(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	program := identityProgram(t, "legacy.exe")
	images := map[string]applicationLegacyPin{program: unsignedPin(program, "c")}
	d := identityDirectory(t, state, images)
	legacy := wire.ApplicationDescriptor{Name: "legacy", Program: program, Title: "Legacy", Activation: startedWith("--old")}
	// An entry stored before pins existed: no pin beside it.
	if r := d.addDescriptor(ctx, legacy, nil); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	if _, pinned := d.pins["legacy"]; pinned {
		t.Fatal("a path-only entry has a pin")
	}
	rewritten := legacy
	rewritten.Activation = startedWith("--new")
	if r := d.register(ctx, identityOperator, rewritten); r.Outcome != wire.ApplicationOutcomeConflict {
		t.Fatal("an unpinned entry's arguments changed silently:", r)
	}
	if _, pinned := d.pins["legacy"]; pinned || !sameActivation(d.descriptors["legacy"].Activation, legacy.Activation) {
		t.Fatal("the refused change pinned or changed the entry")
	}
	retitled := legacy
	retitled.Title = "Legacy 2"
	if r := d.register(ctx, identityOperator, retitled); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("the adoption:", r)
	}
	if d.pins["legacy"] != unsignedPin(program, "c") || d.descriptors["legacy"].Title != "Legacy 2" {
		t.Fatal("the adoption did not capture the pin", d.pins["legacy"])
	}
	reopened := identityDirectory(t, state, images)
	if reopened.pins["legacy"] != unsignedPin(program, "c") {
		t.Fatal("the pin did not survive a restart")
	}
	images[program] = unsignedPin(program, "d")
	foreign := retitled
	foreign.Title = "Foreign"
	if r := reopened.register(ctx, identityOperator, foreign); r.Outcome != wire.ApplicationOutcomeConflict {
		t.Fatal("a foreign change after the pin:", r)
	}
	if reopened.descriptors["legacy"].Title != "Legacy 2" {
		t.Fatal("the foreign change rewrote the entry")
	}
	// The person's Connect replaces it: Remove, then a fresh Register pins
	// the new image.
	if r := reopened.remove(ctx, identityOperator, "legacy"); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	if _, pinned := reopened.pins["legacy"]; pinned {
		t.Fatal("the removed entry kept its pin")
	}
	if r := reopened.register(ctx, identityOperator, foreign); r.Outcome != wire.ApplicationOutcomeApplied || reopened.pins["legacy"] != unsignedPin(program, "d") {
		t.Fatal("the replacement", r, reopened.pins["legacy"])
	}
}

// A re-pin to a new path moves the entry's program: with message permits
// composed the publication passes through the message reservation, and the
// entry still ends as the app's own kept entry.
func TestARepinThroughTheMessageReservationKeepsTheEntrysSourceAndState(t *testing.T) {
	ctx := context.Background()
	d := registration1459Directory(t, t.TempDir())
	blocked := 0
	d.messageBlock = func(context.Context, string) error { blocked++; return nil }
	unsigned := func(program, digit string) (wire.ApplicationDescriptor, applicationV3ManifestFile) {
		v, f := registration1459File(t, "comfyui", program, "ComfyUI")
		f.Source.Code, f.Source.Proof = identity.Code{}, identity.ProofUnsigned
		f.Image, f.ImageSize = unsignedPin(program, digit).Image, 1024
		return v, f
	}
	r17 := registration1459Program(`cu\0.8.0-r17\launch-helper.exe`)
	r22 := registration1459Program(`cu\0.8.0-r22\launch-helper.exe`)
	v, f := unsigned(r17, "a")
	if out := d.publishApplicationV3(ctx, v, f, "self", ""); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	kept := d.changeApplicationV3(ctx, rights.Subject{Account: "account", Program: "operator"}, "comfyui", d.v3.entries["comfyui"].Revision, "enabled")
	if kept.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("keep:", kept)
	}
	blocked = 0
	v, f = unsigned(r22, "b")
	out := d.publishApplicationV3(ctx, v, f, "operator", kept.Revision)
	if out.Outcome != wire.ApplicationOutcomeApplied || blocked != 1 {
		t.Fatal("the re-pin through the reservation:", out, blocked)
	}
	if e := d.v3.entries["comfyui"]; e.Source != "self" || e.State != "enabled" || e.Question != "" || d.v3.files["comfyui"].Source.Program != r22 {
		t.Fatalf("the re-pinned entry: %+v", e)
	}
	if out := d.publishApplicationV3(ctx, v, f, "self", ""); out.Outcome != wire.ApplicationOutcomeApplied || d.v3.entries["comfyui"].State != "enabled" {
		t.Fatal("the app's republish after the re-pin:", out, d.v3.entries["comfyui"])
	}
}
