package main

import (
	"context"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func unsignedFile(t *testing.T, name, program, title string) (wire.ApplicationDescriptor, applicationV3ManifestFile) {
	t.Helper()
	v, f := registration1459File(t, name, program, title)
	f.Source.Code = identity.Code{Status: "no signature"}
	f.Source.Proof = identity.ProofUnsigned
	f.Image, f.ImageSize = strings.Repeat("ab", 32), 4096
	return v, f
}

// A signed registration keeps its signature as its only continuity owner;
// an unsigned one carries its image pin instead, and nothing else is valid.
func TestSignedRegistrationIsUnaffectedByUnsignedPins(t *testing.T) {
	_, signed := registration1459File(t, "signed", registration1459Program("signed.exe"), "Signed")
	if !signed.valid() || !signed.Source.signed() || signed.Image != "" {
		t.Fatal("a signed registration changed shape")
	}
	pinned := signed
	pinned.Image, pinned.ImageSize = strings.Repeat("ab", 32), 1
	if pinned.valid() {
		t.Fatal("a signed registration accepted an image pin")
	}
	_, unsigned := unsignedFile(t, "unsigned", registration1459Program("unsigned.exe"), "Unsigned")
	if !unsigned.valid() || unsigned.Source.signed() {
		t.Fatal("an unsigned pinned registration is invalid")
	}
	unpinned := unsigned
	unpinned.Image = ""
	if unpinned.valid() {
		t.Fatal("an unsigned registration without a pin is valid")
	}
	unverified := unsigned
	unverified.Source.Proof = identity.ProofNone
	if unverified.valid() {
		t.Fatal("an unverified image registered as unsigned")
	}
	d := registration1459Directory(t, t.TempDir())
	if r := registration1459Publish(t, d, "signed", registration1459Program("signed.exe"), "Signed", "operator", ""); d.v3.entries["signed"].State != "enabled" || r == "" {
		t.Fatal("a signed operator registration no longer enables directly")
	}
	if d.unsignedProgram(context.Background(), registration1459Program("signed.exe")) {
		t.Fatal("a signed program reads as unsigned")
	}
}

// Only the program itself registers an unsigned image; an operator cannot
// vouch for one, and the person keeps it before it is used.
func TestUnsignedRegistrationIsSelfOnlyAndPending(t *testing.T) {
	state := t.TempDir()
	d := registration1459Directory(t, state)
	program := registration1459Program("unsigned.exe")
	v, f := unsignedFile(t, "unsigned", program, "Unsigned")
	if out := d.publishApplicationV3(context.Background(), v, f, "operator", ""); out.Outcome != wire.ApplicationOutcomeInvalid {
		t.Fatal("an operator registered an unsigned image", out)
	}
	if out := d.publishApplicationV3(context.Background(), v, f, "self", ""); out.Outcome != wire.ApplicationOutcomeApplied || d.v3.entries["unsigned"].State != "pending" {
		t.Fatal("an unsigned self-registration is not pending", out)
	}
	if !d.unsignedProgram(context.Background(), program) {
		t.Fatal("the unsigned registration is not known as unsigned")
	}
	reopened := registration1459Directory(t, state)
	if got := reopened.v3.files["unsigned"]; got.Image != f.Image || got.ImageSize != f.ImageSize || got.Source.signed() {
		t.Fatal("the pin did not survive a restart", got)
	}
}

// An assistant cannot tell who an unsigned program is: once the program is
// registered unsigned, its first-use questions go to the person only. A
// signed program's routine question stays answerable.
func TestAnAssistantCannotAnswerAnUnsignedProgramsFirstUseQuestion(t *testing.T) {
	f := newAnswererFixture(t)
	if edit := f.enroll(t, f.answerer); edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("enroll %v", edit.Outcome)
	}
	comfy := f.ask(t, f.comfy, host.ModelLookupAction, "hf")
	lemonade := f.ask(t, f.lemonade, host.ModelLookupAction, "hf")
	if !f.answerable(f.me(), comfy, "allow") || !f.answerable(f.me(), lemonade, "allow") {
		t.Fatal("routine questions are not answerable before registration")
	}
	d := f.a.rights.applications
	v, file := unsignedFile(t, "comfyui", f.comfy.Program, "ComfyUI")
	if out := d.publishApplicationV3(context.Background(), v, file, "self", ""); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	sv, signed := registration1459File(t, "lemonade", f.lemonade.Program, "Lemonade")
	if out := d.publishApplicationV3(context.Background(), sv, signed, "operator", ""); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	if f.answerable(f.me(), comfy, "allow") || f.answerable(f.me(), comfy, "refuse") {
		t.Fatal("an assistant may answer an unsigned program's first-use question")
	}
	if outcome, _ := f.answer(f.me(), comfy.ID, "allow"); outcome != "forbidden" {
		t.Fatal("an assistant answered an unsigned program's first-use question", outcome)
	}
	if !f.answerable(f.me(), lemonade, "allow") {
		t.Fatal("a signed program's routine question is no longer answerable")
	}
}
