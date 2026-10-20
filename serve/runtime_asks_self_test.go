package main

import (
	"os"
	"path/filepath"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-identity/listen"
)

// writeProgram writes a small regular file standing in for an executable.
func writeProgram(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("program "+name), 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// ASK-R3: each party to a question is refused as its responder, and a
// different program is not.
func TestSelfAnswerNamesEveryPartyToTheQuestion(t *testing.T) {
	dir := t.TempDir()
	target := writeProgram(t, dir, "target-app.exe")
	runtimeProgram := writeProgram(t, dir, "openabstractions.exe")
	asker := writeProgram(t, dir, "asking-app.exe")
	other := writeProgram(t, dir, "other-app.exe")
	none := asks.Publisher{}
	plain := asks.Record{}

	if !selfAnswer(target, none, plain, target, runtimeProgram) {
		t.Error("the target answered its own question")
	}
	if !selfAnswer(runtimeProgram, none, plain, target, runtimeProgram) {
		t.Error("the runtime answered a question it asked")
	}
	if selfAnswer(other, none, plain, target, runtimeProgram) {
		t.Error("an unrelated program was refused")
	}

	bound := asks.Record{Via: listen.Seen{Bound: true, Path: asker}}
	if !selfAnswer(asker, none, bound, target, runtimeProgram) {
		t.Error("the verified asker answered its own question")
	}
	unbound := asks.Record{Via: listen.Seen{Path: asker}}
	if selfAnswer(asker, none, unbound, target, runtimeProgram) {
		t.Error("an unverified Via path refused a responder")
	}

	signed := asks.Record{Publisher: "CN=Example Apps", PublisherTrusted: true}
	for _, c := range []struct {
		name      string
		record    asks.Record
		publisher asks.Publisher
		refused   bool
	}{
		{"same trusted publisher", signed, asks.Publisher{Subject: "CN=Example Apps", Trusted: true}, true},
		{"same subject, responder untrusted", signed, asks.Publisher{Subject: "CN=Example Apps"}, false},
		{"another trusted publisher", signed, asks.Publisher{Subject: "CN=Someone Else", Trusted: true}, false},
		{"target publisher untrusted", asks.Record{Publisher: "CN=Example Apps"}, asks.Publisher{Subject: "CN=Example Apps", Trusted: true}, false},
		{"no recorded publisher", plain, asks.Publisher{Subject: "CN=Example Apps", Trusted: true}, false},
	} {
		if got := selfAnswer(other, c.publisher, c.record, target, runtimeProgram); got != c.refused {
			t.Errorf("%s: refused %v, want %v", c.name, got, c.refused)
		}
	}
}

// ASK-R3: a hard link to the target's executable is the target; a copy of
// it is a different file.
func TestSelfAnswerRefusesTheTargetsExecutableUnderAnotherPath(t *testing.T) {
	dir := t.TempDir()
	target := writeProgram(t, dir, "target-app.exe")
	link := filepath.Join(dir, "helper-link.exe")
	if err := os.Link(target, link); err != nil {
		t.Skipf("hard links unavailable here: %v", err)
	}
	copied := writeProgram(t, dir, "copied-app.exe")
	if !selfAnswer(link, asks.Publisher{}, asks.Record{}, target, "") {
		t.Error("a hard link to the target answered the target's question")
	}
	if selfAnswer(copied, asks.Publisher{}, asks.Record{}, target, "") {
		t.Error("a separate file was refused as the target")
	}
	if selfAnswer(filepath.Join(dir, "missing.exe"), asks.Publisher{}, asks.Record{}, target, "") {
		t.Error("a missing file matched the target")
	}
}
