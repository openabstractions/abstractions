package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// panelInstalledPerUser makes this test binary's folder the per-user install
// folder, as the default per-user installation places the Panel in
// %LOCALAPPDATA%\Programs\OpenAbstractions\tools, with no per-machine
// installation, and returns that folder.
func panelInstalledPerUser(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(exe)
	savedMachine, savedUser := oaInstallFolders, oaUserInstallFolder
	t.Cleanup(func() { oaInstallFolders, oaUserInstallFolder = savedMachine, savedUser })
	oaInstallFolders = func() []string { return nil }
	oaUserInstallFolder = func() string { return dir }
	return dir
}

// In the default per-user installation the Panel's own folder is the
// person's to write. A program that replaces the MCP launcher there, or
// drops a provider beside the Panel and declares it lemonade-image, gains no
// product title, no own-program standing and no installation standing
// unless this Panel's signer signed it (security review F1, 2026-11-09).
func TestPerUserPanelFolderVouchesOnlyForSameSignerPrograms(t *testing.T) {
	dir := panelInstalledPerUser(t)
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	launcher := filepath.Join(dir, "openabstractions-mcp"+suffix)
	provider := filepath.Join(dir, "lemonaded"+suffix)
	operator := fwire.DeclarationState{DeclaredBy: provider, Declaration: fwire.Declaration{Name: "lemonade-image", Program: provider}}
	installed := fwire.DeclarationState{DeclaredBy: providerDeclaredByInstallation, Declaration: fwire.Declaration{Name: "lemonade-image", Program: provider}}

	signedByPanelSigner(t)
	titler := newProgramTitler(nil)
	if titler.isOwn(launcher) || titler.name(launcher) == oaProductTitle || (*programTitler)(nil).oaOwnProgram(launcher) {
		t.Errorf("an unsigned launcher beside the per-user Panel reads %q as an own program", titler.name(launcher))
	}
	records := &applicationRequestRecords{publisher: func(string) string { return "Publisher: not verified." }}
	if got := records.ownApplicationPublisher(launcher); got != "Publisher: not verified." {
		t.Errorf("the replaced launcher lost its Publisher line: %q", got)
	}
	for _, state := range []fwire.DeclarationState{operator, installed} {
		if shippedProvider(state) {
			t.Errorf("an unsigned lemonade-image declared by %s beside the per-user Panel is shipped", state.DeclaredBy)
		}
		titler.providers = []fwire.DeclarationState{state}
		if got := titler.name(provider); got != "Lemonade image" {
			t.Errorf("an unsigned provider declared by %s reads %q", state.DeclaredBy, got)
		}
	}

	// The same files signed by this Panel's signer behave as before.
	signedByPanelSigner(t, launcher, provider)
	titler = newProgramTitler(nil)
	if !titler.isOwn(launcher) || titler.name(launcher) != oaProductTitle {
		t.Errorf("a signed launcher reads %q own %v", titler.name(launcher), titler.isOwn(launcher))
	}
	for _, state := range []fwire.DeclarationState{operator, installed} {
		if !shippedProvider(state) {
			t.Errorf("a signed lemonade-image declared by %s is not shipped", state.DeclaredBy)
		}
		titler.providers = []fwire.DeclarationState{state}
		if got := titler.name(provider); got != "Lemonade" {
			t.Errorf("a signed provider declared by %s reads %q", state.DeclaredBy, got)
		}
	}
}

// A junction the person makes to the per-machine install folder is no
// install folder: whoever made it may point it elsewhere later.
func TestJunctionToInstallFolderIsNoInstallFolder(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are Windows folders")
	}
	machine := t.TempDir()
	shippedFolders(t, machine)
	signedByPanelSigner(t)
	for _, name := range []string{"openabstractions-mcp.exe", "lemonaded.exe"} {
		if err := os.WriteFile(filepath.Join(machine, name), []byte("MZ"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(t.TempDir(), "tools")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, machine).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
	if !newProgramTitler(nil).isOwn(filepath.Join(machine, "openabstractions-mcp.exe")) {
		t.Fatal("the per-machine folder's launcher is not own")
	}
	if !shippedProvider(fwire.DeclarationState{DeclaredBy: "app", Declaration: fwire.Declaration{Name: "lemonade-image", Program: filepath.Join(machine, "lemonaded.exe")}}) {
		t.Fatal("the per-machine folder's provider is not shipped")
	}
	state := fwire.DeclarationState{DeclaredBy: "app", Declaration: fwire.Declaration{Name: "lemonade-image", Program: filepath.Join(link, "lemonaded.exe")}}
	if shippedProvider(state) {
		t.Error("a provider through a junction to the per-machine folder is shipped")
	}
}
