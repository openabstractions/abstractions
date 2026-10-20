package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// A program that registered itself under a connector name reads by its own
// title and "(added itself)", shows a Publisher line and is none of OA's own
// programs; only Setup's registration of the installed connector image reads
// as the agent.
func TestSelfRegisteredConnectorNameReadsAsItself(t *testing.T) {
	root := t.TempDir()
	saved := agentConnectorRoot
	agentConnectorRoot = func() string { return root }
	t.Cleanup(func() { agentConnectorRoot = saved })
	evil := filepath.Join(t.TempDir(), "Temp", "evil.exe")
	name := "oa-claude-integration-" + strings.Repeat("ab", 32)
	names := newProgramTitler([]fwire.ApplicationEntry{{Scope: fwire.ScopeLocal, SelfRegistered: true,
		Descriptor: fwire.ApplicationDescriptor{Name: name, Program: evil, Title: "evil"}}})
	if got := names.name(evil); got != "evil (added itself)" {
		t.Errorf("title %q", got)
	}
	if names.oaOwnProgram(evil) {
		t.Error("a self-registered connector name made an own program")
	}
	if got, ok := names.applicationTitle(name); !ok || got != "evil (added itself)" {
		t.Errorf("registered name reads %q", got)
	}
	if got := messagePartyTitle(names, evil, name); got != "evil (added itself)" {
		t.Errorf("message party reads %q", got)
	}
	records := &applicationRequestRecords{publisher: func(string) string { return "Publisher: not verified." }}
	if got := records.ownApplicationPublisher(evil); got != "Publisher: not verified." {
		t.Errorf("pending request publisher %q", got)
	}
	// The same entry registered by Setup for a program outside its folder is
	// no connector either.
	names = newProgramTitler([]fwire.ApplicationEntry{{Scope: fwire.ScopeLocal,
		Descriptor: fwire.ApplicationDescriptor{Name: name, Program: evil, Title: "evil"}}})
	if got := names.name(evil); got != "evil" || names.oaOwnProgram(evil) {
		t.Errorf("an operator entry for another file reads %q", got)
	}
}

// connectorImageLeaf is a connector image's file name on this platform:
// "<name>.exe" on Windows and the bare name on Darwin. Other platforms have
// no connector images.
func connectorImageLeaf(t *testing.T, name string) string {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		return name + ".exe"
	case "darwin":
		return name
	}
	t.Skip("agent connector images exist on Windows and Darwin")
	return ""
}

// installedConnectorImages points Setup's connector folder at a test folder
// holding an image for each name and returns the images by name.
func installedConnectorImages(t *testing.T, names ...string) map[string]string {
	t.Helper()
	root := t.TempDir()
	saved := agentConnectorRoot
	agentConnectorRoot = func() string { return root }
	t.Cleanup(func() { agentConnectorRoot = saved })
	images := map[string]string{}
	for _, name := range names {
		images[name] = filepath.Join(root, connectorImageLeaf(t, name))
		if err := os.WriteFile(images[name], []byte("MZ"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	signed := make([]string, 0, len(images))
	for _, image := range images {
		signed = append(signed, image)
	}
	signedByPanelSigner(t, signed...)
	return images
}

// signedByPanelSigner makes oaSameSigner vouch for exactly the given files,
// as Windows does for OA's signed gateway copies.
func signedByPanelSigner(t *testing.T, programs ...string) {
	t.Helper()
	saved := oaSameSigner
	t.Cleanup(func() { oaSameSigner = saved })
	oaSameSigner = func(program string) bool { return slices.Contains(programs, program) }
}

// A file under a free exact connector name in Setup's folder, with no
// registration, reads as its agent only when this Panel's signer signed it:
// a program that writes itself there as oa-codex-integration.exe while
// Codex is not connected, and registers itself as "evil2", reads by its own
// title and keeps its Publisher line.
func TestUnsignedFileUnderAFreeConnectorNameReadsAsItself(t *testing.T) {
	root := t.TempDir()
	saved := agentConnectorRoot
	agentConnectorRoot = func() string { return root }
	t.Cleanup(func() { agentConnectorRoot = saved })
	signedByPanelSigner(t)
	planted := filepath.Join(root, connectorImageLeaf(t, "oa-codex-integration"))
	if err := os.WriteFile(planted, []byte("MZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	names := newProgramTitler([]fwire.ApplicationEntry{{Scope: fwire.ScopeLocal, SelfRegistered: true,
		Descriptor: fwire.ApplicationDescriptor{Name: "evil2", Program: planted, Title: "evil2"}}})
	if got := names.name(planted); got != "evil2 (added itself)" || names.oaOwnProgram(planted) {
		t.Errorf("the planted file reads %q own %v", got, names.oaOwnProgram(planted))
	}
	if (*programTitler)(nil).oaOwnProgram(planted) {
		t.Error("the planted file is own for the nil titler")
	}
	records := &applicationRequestRecords{publisher: func(string) string { return "Publisher: not verified." }}
	if got := records.ownApplicationPublisher(planted); got != "Publisher: not verified." {
		t.Errorf("the planted file lost its Publisher line: %q", got)
	}
	signedByPanelSigner(t, planted)
	if got := newProgramTitler(nil).name(planted); got != "Codex" || !(*programTitler)(nil).oaOwnProgram(planted) {
		t.Errorf("a signed image reads %q", got)
	}
}

// Setup's connector folder is the person's own to write. A program that
// copies itself there under a digest name and registers itself as "evil"
// reads by its own title, keeps its Publisher line and is none of OA's own
// programs; a digest name reads as the agent only through Setup's
// registration of that image under its routing name, on Darwin.
func TestDigestNamedCopyInConnectorFolderReadsAsItself(t *testing.T) {
	root := t.TempDir()
	saved := agentConnectorRoot
	agentConnectorRoot = func() string { return root }
	t.Cleanup(func() { agentConnectorRoot = saved })
	exact := filepath.Join(root, connectorImageLeaf(t, "oa-claude-integration"))
	for _, digest := range []string{strings.Repeat("ab", 16), strings.Repeat("ab", 32)} {
		leaf := "oa-claude-integration-" + digest
		if runtime.GOOS == "windows" {
			leaf += ".exe"
		}
		copied := filepath.Join(root, leaf)
		for _, program := range []string{copied, exact} {
			if err := os.WriteFile(program, []byte("MZ"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		// Even a copy that carries this Panel's signer reads as the agent by
		// its file alone only under the exact connector name.
		signedByPanelSigner(t, copied, exact)
		self := newProgramTitler([]fwire.ApplicationEntry{{Scope: fwire.ScopeLocal, SelfRegistered: true,
			Descriptor: fwire.ApplicationDescriptor{Name: "evil", Program: copied, Title: "evil"}}})
		if got := self.name(copied); got != "evil (added itself)" || self.oaOwnProgram(copied) {
			t.Errorf("%s registered as evil reads %q own %v", leaf, got, self.oaOwnProgram(copied))
		}
		if got := newProgramTitler(nil).name(copied); got == "Claude" || newProgramTitler(nil).oaOwnProgram(copied) {
			t.Errorf("%s reads %q by its file alone", leaf, got)
		}
		records := &applicationRequestRecords{publisher: func(string) string { return "Publisher: not verified." }}
		if got := records.ownApplicationPublisher(copied); got != "Publisher: not verified." {
			t.Errorf("%s lost its Publisher line: %q", leaf, got)
		}
		// Setup's own registration of a Darwin digest image under its
		// routing name reads as the agent. The file holds the complete digest.
		setup := newProgramTitler([]fwire.ApplicationEntry{{Scope: fwire.ScopeLocal,
			Descriptor: fwire.ApplicationDescriptor{Name: "oa-claude-integration-" + digest[:32], Program: copied, Title: "Claude integration"}}})
		want := "Claude integration"
		if runtime.GOOS == "darwin" && len(digest) == 64 {
			want = "Claude"
		}
		if got := setup.name(copied); got != want {
			t.Errorf("Setup's registration of %s reads %q, want %q", leaf, got, want)
		}
		// The exact image name reads as the agent by its file.
		if got := newProgramTitler(nil).name(exact); got != "Claude" || !newProgramTitler(nil).oaOwnProgram(exact) {
			t.Errorf("the installed image reads %q", got)
		}
	}
}

// A message party with a program reads as an agent only by that program or
// a registration Setup made; its connector name alone leaves the bare name.
// A rule row, which names no program, still reads a connector name as its
// agent.
func TestMessagePartyWithAProgramIgnoresItsConnectorName(t *testing.T) {
	saved := agentConnectorRoot
	agentConnectorRoot = func() string { return t.TempDir() }
	t.Cleanup(func() { agentConnectorRoot = saved })
	evil := filepath.Join(t.TempDir(), "evil.exe")
	name := "oa-claude-integration-" + strings.Repeat("ab", 16)
	if got := messagePartyTitle(newProgramTitler(nil), evil, name); got != name {
		t.Errorf("an unregistered party reads %q", got)
	}
	if got := messagePartyTitle(nil, evil, "oa-claude-integration"); got != "oa-claude-integration" {
		t.Errorf("an unread registry titles the party %q", got)
	}
	if got := messagePartyTitle(newProgramTitler(nil), "", "oa-codex-integration"); got != "Codex" {
		t.Errorf("a rule row reads %q", got)
	}
}

// panelInstalledPerMachine adds this test binary's folder to the per-machine
// install folders, as when the Panel and its runtime run from Program Files.
func panelInstalledPerMachine(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	saved := oaInstallFolders
	t.Cleanup(func() { oaInstallFolders = saved })
	oaInstallFolders = func() []string { return append(saved(), filepath.Dir(exe)) }
}

// shippedFolders points OA's per-machine install folder at a test folder,
// with no per-user install folder.
func shippedFolders(t *testing.T, machine string) {
	t.Helper()
	savedMachine, savedUser := oaInstallFolders, oaUserInstallFolder
	t.Cleanup(func() { oaInstallFolders, oaUserInstallFolder = savedMachine, savedUser })
	oaInstallFolders = func() []string { return []string{machine} }
	oaUserInstallFolder = func() string { return "" }
}

// A provider takes a product title and caption only when OpenAbstractions
// shipped it: the installation placed it while this Panel runs from the
// per-machine install folder, or its program sits in OA's install folders. An app that may manage providers and names its own
// program lemonade-image, modelhost or pytorch-image reads by that name in
// words, and so does a program under Setup's apps folder, where ComfyUI's
// custom nodes run third-party code.
func TestProviderProductTitlesNeedAShippedDeclaration(t *testing.T) {
	machine, elsewhere := t.TempDir(), t.TempDir()
	shippedFolders(t, machine)
	apps := filepath.Join(t.TempDir(), "OpenAbstractions", "apps")
	declared := func(name, program, by string) fwire.DeclarationState {
		return fwire.DeclarationState{DeclaredBy: by, Declaration: fwire.Declaration{Name: name, Program: program}}
	}
	evil := filepath.Join(elsewhere, "evil.exe")
	for _, c := range []struct {
		state       fwire.DeclarationState
		title, does string
	}{
		{declared("lemonade-image", filepath.Join(machine, "lemonaded.exe"), evil), "Lemonade", "Pictures, speech and transcription"},
		{declared("comfy-image-1828", filepath.Join(apps, "comfyui", "1.0", "provider.exe"), evil), "Comfy image", "Provides services to your apps"},
		{declared("lemonade-image", filepath.Join(apps, "comfyui", "1.0", "python.exe"), evil), "Lemonade image", "Provides services to your apps"},
		{declared("modelhost", filepath.Join(elsewhere, "modelhostd.exe"), providerDeclaredByInstallation), "Modelhost", "Provides services to your apps"},
		{declared("lemonade-image", evil, evil), "Lemonade image", "Provides services to your apps"},
		{declared("modelhost", evil, evil), "Modelhost", "Provides services to your apps"},
		{declared("pytorch-image", evil, evil), "Pytorch image", "Provides services to your apps"},
		{declared("pytorch-image", filepath.Join(apps+"-other", "pytorchd.exe"), evil), "Pytorch image", "Provides services to your apps"},
		{declared("pytorch-image", filepath.Join(machine, "sub", "pytorchd.exe"), evil), "Pytorch image", "Provides services to your apps"},
		{declared("pytorch-image", "pytorchd.exe", evil), "Pytorch image", "Provides services to your apps"},
	} {
		if title, does := providerDisplay(c.state.Declaration.Name, shippedProvider(c.state)); title != c.title || does != c.does {
			t.Errorf("%s at %s reads %q, %q; want %q, %q", c.state.Declaration.Name, c.state.Declaration.Program, title, does, c.title, c.does)
		}
	}
	titler := newProgramTitler(nil)
	titler.providers = []fwire.DeclarationState{declared("lemonade-image", evil, evil)}
	if got := titler.name(evil); got != "Lemonade image" {
		t.Errorf("a self-named provider reads %q", got)
	}
	// An installation in a folder only an administrator writes vouches for
	// the declarations it placed.
	panelInstalledPerMachine(t)
	if title, _ := providerDisplay("modelhost", shippedProvider(declared("modelhost", filepath.Join(elsewhere, "modelhostd.exe"), providerDeclaredByInstallation))); title != "llama.cpp" {
		t.Errorf("a per-machine installation's declaration reads %q", title)
	}
}

// The signed-description cache keys on the file's identity as well as its
// path: a file replaced at the same path is verified again. product's
// TestSameSignerCacheMissesAReplacedFile covers the signer cache.
func TestSignerCachesMissAReplacedFile(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("signatures are read on Windows only")
	}
	dir := t.TempDir()
	program := filepath.Join(dir, "tool.exe")
	if err := os.WriteFile(program, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := programCacheKey(program)
	if key == program+"\x00" {
		t.Fatal("no file identity for an existing file")
	}
	now := time.Now()
	signedDescriptionsMu.Lock()
	signedDescriptions[key] = describedProgram{at: now, description: "Trusted Tool"}
	signedDescriptionsMu.Unlock()
	t.Cleanup(func() {
		signedDescriptionsMu.Lock()
		clear(signedDescriptions)
		signedDescriptionsMu.Unlock()
	})
	if got := cachedSignedDescription(program); got != "Trusted Tool" {
		t.Fatalf("the cached description was not used: %q", got)
	}
	// Replace the file: the original moves aside and an unsigned file takes
	// its path.
	if err := os.Rename(program, program+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if programCacheKey(program) == key {
		t.Fatal("the replacement has the original's identity")
	}
	if got := cachedSignedDescription(program); got != "" {
		t.Errorf("the replaced file reads the cached description %q", got)
	}
}
