package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// junctionTo makes a junction to folder in a fresh test folder and returns
// it.
func junctionTo(t *testing.T, folder string) string {
	t.Helper()
	link := filepath.Join(t.TempDir(), "tools")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, folder).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
	return link
}

// A process started as <junction>\openabstractions.exe keeps that spelling.
// Its maker may point the junction at OpenAbstractions' tools folder before
// the Panel reads it, so a program reached through a junction to that
// folder is none of OA's own programs: it keeps its Publisher line and the
// Panel's own level. An 8.3 alias of an own program still counts.
func TestProgramThroughJunctionToToolsFolderIsNotOwn(t *testing.T) {
	machine := filepath.Join(t.TempDir(), "OpenAbstractions tools")
	if err := os.Mkdir(machine, 0o700); err != nil {
		t.Fatal(err)
	}
	shippedFolders(t, machine)
	signedByPanelSigner(t)
	for _, name := range []string{"openabstractions.exe", "Abstraction Panel.exe"} {
		if err := os.WriteFile(filepath.Join(machine, name), []byte("MZ"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := junctionTo(t, machine)
	real, linked := filepath.Join(machine, "openabstractions.exe"), filepath.Join(link, "openabstractions.exe")
	titler := newProgramTitler(nil)
	if !titler.isOwn(real) || !(*programTitler)(nil).oaOwnProgram(real) {
		t.Fatal("the tools folder's runtime is not own")
	}
	if titler.isOwn(linked) || titler.oaOwnProgram(linked) || (*programTitler)(nil).oaOwnProgram(linked) {
		t.Errorf("%s reads as an own program", linked)
	}
	if got := titler.name(linked); got == oaProductTitle {
		t.Errorf("%s reads %q", linked, got)
	}
	records := &applicationRequestRecords{publisher: func(string) string { return "Publisher: not verified." }}
	if got := records.ownApplicationPublisher(linked); got != "Publisher: not verified." {
		t.Errorf("%s lost its Publisher line: %q", linked, got)
	}
	if oaPanelProgram(filepath.Join(link, "Abstraction Panel.exe")) {
		t.Error("a Panel through a junction reads as this Panel")
	}
	short := factsShortPath(t, real)
	if !titler.isOwn(short) || titler.name(short) != oaProductTitle {
		t.Errorf("the 8.3 alias %s of the runtime reads %q own %v", short, titler.name(short), titler.isOwn(short))
	}
}

// A connector image reached through a junction to Setup's connector folder
// reads as no agent connector, by its file or by Setup's registration.
func TestConnectorThroughJunctionIsNotOwn(t *testing.T) {
	root := t.TempDir()
	saved := agentConnectorRoot
	agentConnectorRoot = func() string { return root }
	t.Cleanup(func() { agentConnectorRoot = saved })
	leaf := connectorImageLeaf(t, "oa-claude-integration")
	image := filepath.Join(root, leaf)
	if err := os.WriteFile(image, []byte("MZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(junctionTo(t, root), leaf)
	signedByPanelSigner(t, image, linked)
	registered := newProgramTitler([]fwire.ApplicationEntry{{Scope: fwire.ScopeLocal,
		Descriptor: fwire.ApplicationDescriptor{Name: "oa-claude-integration", Program: image, Title: "Claude integration"}}})
	if !registered.oaOwnProgram(image) {
		t.Fatal("the installed connector image is not own")
	}
	if registered.oaOwnProgram(linked) || (*programTitler)(nil).oaOwnProgram(linked) {
		t.Errorf("%s reads as an own connector", linked)
	}
}
