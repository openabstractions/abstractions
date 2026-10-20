package main

import (
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

func TestProgramChatTitlesUseCurrentNativeDisplayClaims(t *testing.T) {
	program := filepath.Join(t.TempDir(), "kept-agent.exe")
	entry := wire.ApplicationEntry{Scope: wire.ScopeLocal, Descriptor: wire.ApplicationDescriptor{Name: "agent", Program: program}, Instances: []wire.ApplicationInstance{{Contexts: []wire.ApplicationContext{{Name: "chat", Title: "unrelated presence claim"}, {Name: "missing"}}}}}
	names := newProgramTitler([]wire.ApplicationEntry{entry})
	if got := names.chatTitle(program, "chat"); got != "Unnamed chat" {
		t.Fatal("presence supplied chat name", got)
	}
	names.chatNames = map[string]string{"app:agent\x00chat": messageActivityLabel("Review\u202e\n plan")}
	if got := names.chatTitle(program, "chat"); got != "Review plan" {
		t.Fatal(got)
	}
	for _, context := range []string{"missing", "unknown", ""} {
		if got := names.chatTitle(program, context); got != "Unnamed chat" {
			t.Fatal(context, got)
		}
	}
	if got := names.chatTitle(filepath.Join(t.TempDir(), "foreign.exe"), "chat"); got != "Unnamed chat" {
		t.Fatal("foreign title reused", got)
	}
	entry.Instances = append(entry.Instances, wire.ApplicationInstance{Contexts: []wire.ApplicationContext{{Name: "chat", Title: "different claim"}}})
	names = newProgramTitler([]wire.ApplicationEntry{entry})
	if got := names.chatTitle(program, "chat"); got != "Unnamed chat" {
		t.Fatal("conflicting title selected", got)
	}
}
