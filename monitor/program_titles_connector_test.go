package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// OA's agent connectors read as the agent each one connects, by their
// installed image or their registration, and never by a file name.
func TestProgramTitlesNameAgentConnectors(t *testing.T) {
	root := t.TempDir()
	saved := agentConnectorRoot
	agentConnectorRoot = func() string { return root }
	t.Cleanup(func() { agentConnectorRoot = saved })
	images := map[string]string{}
	for _, name := range []string{"oa-claude-integration", "oa-codex-integration", "oa-opencode-integration"} {
		image := filepath.Join(root, connectorImageLeaf(t, name))
		if err := os.WriteFile(image, []byte("connector"), 0o600); err != nil {
			t.Fatal(err)
		}
		images[name] = image
	}
	elsewhere := filepath.Join(t.TempDir(), connectorImageLeaf(t, "oa-codex-integration"))
	if err := os.WriteFile(elsewhere, []byte("copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Every file here carries this Panel's signer; the folder still decides.
	signedByPanelSigner(t, images["oa-claude-integration"], images["oa-codex-integration"], images["oa-opencode-integration"], elsewhere)
	titler := newProgramTitler(nil)
	for name, want := range map[string]string{"oa-claude-integration": "Claude", "oa-codex-integration": "Codex", "oa-opencode-integration": "OpenCode"} {
		if got := titler.name(images[name]); got != want {
			t.Fatalf("%s reads %q", name, got)
		}
		if got, ok := titler.applicationTitle(name); !ok || got != want {
			t.Fatalf("registered name %s reads %q", name, got)
		}
		if !titler.oaOwnProgram(images[name]) {
			t.Fatalf("%s is not one of OA's own programs", name)
		}
	}
	if title, ok := agentConnectorTitle("oa-codex-integration-" + strings.Repeat("ab", 16)); !ok || title != "Codex" {
		t.Fatal("Darwin connector name untitled", title)
	}
	for _, name := range []string{"oa-codex-integration-xyz", "codex", "oa-codex-integration2"} {
		if _, ok := agentConnectorTitle(name); ok {
			t.Fatal("not a connector", name)
		}
	}
	if _, ok := titler.title(elsewhere); ok || titler.oaOwnProgram(elsewhere) {
		t.Fatal("a same-named file outside Setup's folder read as a connector")
	}
	// A registered connector reads as its agent, not its registered title.
	program := images["oa-opencode-integration"]
	registered := newProgramTitler([]fwire.ApplicationEntry{{Scope: fwire.ScopeLocal, Descriptor: fwire.ApplicationDescriptor{Name: "oa-opencode-integration", Program: program, Title: "OpenCode integration"}}})
	if got := registered.name(program); got != "OpenCode" || !registered.oaOwnProgram(program) {
		t.Fatalf("registered connector reads %q", got)
	}
	if got, ok := registered.applicationTitle("oa-opencode-integration"); !ok || got != "OpenCode" {
		t.Fatalf("registered connector name reads %q", got)
	}
	// A registration under a connector name whose program is another file
	// reads by its own title.
	other := filepath.Join(t.TempDir(), "connector.exe")
	misplaced := newProgramTitler([]fwire.ApplicationEntry{{Scope: fwire.ScopeLocal, Descriptor: fwire.ApplicationDescriptor{Name: "oa-opencode-integration", Program: other, Title: "OpenCode integration"}}})
	if got := misplaced.name(other); got != "OpenCode integration" || misplaced.oaOwnProgram(other) {
		t.Fatalf("a connector name on another program reads %q", got)
	}
	// A saved message pair is one Panel row with its Remove sentence.
	row, removed := messageRuleWords(registered, policyRuleView{Subject: rightsclient.Subject{Account: "a", Program: program}, Action: "abstraction.facade/application.message", Resource: "app:oa-codex-integration", Permit: true})
	if row != "OpenCode can send messages to Codex" || removed != "OpenCode can no longer send messages to Codex." {
		t.Fatalf("message row %q / %q", row, removed)
	}
	if row, _ := messageRuleWords(registered, policyRuleView{Subject: rightsclient.Subject{Account: "a", Program: program}, Action: "abstraction.facade/application.message", Resource: "app:oa-codex-integration", Permit: false}); row != "" {
		t.Fatal("a block read as an allowed pair", row)
	}
}
