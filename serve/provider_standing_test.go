package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// signedByRuntimeSigner makes product standing's signer check vouch for
// exactly the given files, as Windows does for OA's signed programs.
func signedByRuntimeSigner(t *testing.T, programs ...string) {
	t.Helper()
	saved := standingSameSigner
	t.Cleanup(func() { standingSameSigner = saved })
	standingSameSigner = func(program string) bool { return slices.Contains(programs, program) }
}

// installedPerMachine makes a test folder the per-machine install folder,
// with no per-user install folder, and returns it.
func installedPerMachine(t *testing.T) string {
	t.Helper()
	machine := t.TempDir()
	savedMachine, savedUser := standingInstallFolders, standingUserInstallFolder
	t.Cleanup(func() { standingInstallFolders, standingUserInstallFolder = savedMachine, savedUser })
	standingInstallFolders = func() []string { return []string{machine} }
	standingUserInstallFolder = func() string { return "" }
	return machine
}

// In the default per-user installation the runtime's folder is the
// person's to write. A program that drops itself there and declares
// lemonade-image, through Declare or as a declaration file in the
// installation's declarations folder, ranks as no shipped picture engine
// unless the runtime's signer signed it (security review F1, 2026-11-09).
func TestPerUserRuntimeFolderRanksOnlySameSignerProviders(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tools := filepath.Dir(exe)
	savedMachine, savedUser := standingInstallFolders, standingUserInstallFolder
	t.Cleanup(func() { standingInstallFolders, standingUserInstallFolder = savedMachine, savedUser })
	standingInstallFolders = func() []string { return nil }
	standingUserInstallFolder = func() string { return tools }
	p := &runtimeProviders{installation: filepath.Join(tools, installationDeclarationsDir)}
	dropped := filepath.Join(tools, "lemonaded.exe")
	files := []providerFile{
		{Source: declarationSourceOperator, Declaration: providerDeclaration{Name: product.Lemonade, Program: dropped}},
		{Source: declarationSourceInstallation, Declaration: providerDeclaration{Name: product.Lemonade, Program: dropped}},
	}
	last := len(pictureProviderPreference)
	signedByRuntimeSigner(t)
	for _, f := range files {
		if p.shippedProvider(f) || pictureProviderRank(f.Declaration.Name, p.shippedProvider(f)) != last {
			t.Errorf("an unsigned %s declaration of %s beside the per-user runtime ranks as shipped", f.Source, dropped)
		}
	}
	signedByRuntimeSigner(t, dropped)
	for _, f := range files {
		if !p.shippedProvider(f) || pictureProviderRank(f.Declaration.Name, true) == last {
			t.Errorf("a signed %s declaration of %s is not shipped", f.Source, dropped)
		}
	}
	// A product record keeps its standing.
	if !p.shippedProvider(providerFile{Source: declarationSourceProduct}) {
		t.Error("a product record lost its standing")
	}
}

// A per-machine installation's declarations and programs keep their
// standing by path; a junction to that folder has none.
func TestPerMachineRuntimeFolderVouchesByPathOnly(t *testing.T) {
	machine := installedPerMachine(t)
	signedByRuntimeSigner(t)
	p := &runtimeProviders{installation: filepath.Join(machine, installationDeclarationsDir)}
	elsewhere := filepath.Join(t.TempDir(), "evil.exe")
	if !p.shippedProvider(providerFile{Source: declarationSourceInstallation, Declaration: providerDeclaration{Name: product.Lemonade, Program: elsewhere}}) {
		t.Error("a per-machine installation's declaration is not shipped")
	}
	if !p.shippedProvider(providerFile{Source: declarationSourceOperator, Declaration: providerDeclaration{Name: product.Lemonade, Program: filepath.Join(machine, "lemonaded.exe")}}) {
		t.Error("a per-machine program is not shipped")
	}
	if p.shippedProvider(providerFile{Source: declarationSourceOperator, Declaration: providerDeclaration{Name: product.Lemonade, Program: elsewhere}}) {
		t.Error("a program elsewhere is shipped")
	}
	if runtime.GOOS != "windows" {
		return
	}
	if err := os.WriteFile(filepath.Join(machine, "lemonaded.exe"), []byte("MZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "tools")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, machine).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
	if p.shippedProvider(providerFile{Source: declarationSourceOperator, Declaration: providerDeclaration{Name: product.Lemonade, Program: filepath.Join(link, "lemonaded.exe")}}) {
		t.Error("a program through a junction to the per-machine folder is shipped")
	}
	linked := &runtimeProviders{installation: filepath.Join(link, installationDeclarationsDir)}
	if linked.shippedProvider(providerFile{Source: declarationSourceInstallation, Declaration: providerDeclaration{Name: product.Lemonade, Program: elsewhere}}) {
		t.Error("an installation read through a junction to the per-machine folder vouches by path")
	}
}
