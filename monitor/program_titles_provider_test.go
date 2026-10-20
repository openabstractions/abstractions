package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// A registered provider's program reads by the provider's title, never by its
// file stem and "(not registered)" (architect ruling 2026-11-09, R17's
// "inventoryd (not registered)" and "lemonaded (not registered)").
func TestProviderProgramsReadByProviderTitle(t *testing.T) {
	panelInstalledPerMachine(t)
	titler := newProgramTitler(nil)
	titler.providers = []fwire.DeclarationState{
		{DeclaredBy: providerDeclaredByInstallation, Declaration: fwire.Declaration{Name: "lemonade-image", Program: platformProgram(`C:\OA\tools\lemonaded.exe`)}},
		{DeclaredBy: providerDeclaredByInstallation, Declaration: fwire.Declaration{Name: "local-stores", Program: platformProgram(`C:\OA\tools\inventoryd.exe`)}},
		{DeclaredBy: providerDeclaredByInstallation, Declaration: fwire.Declaration{Name: "pytorch", Program: platformProgram(`C:\OA\tools\pytorchd.exe`)}},
		{Declaration: fwire.Declaration{Name: "my-new_provider", Program: platformProgram(`C:\Other\provider.exe`)}},
		{Declaration: fwire.Declaration{Name: "openabstractions-helper", Program: platformProgram(`C:\Other\helper.exe`)}},
	}
	for program, want := range map[string]string{
		platformProgram(`C:\OA\tools\lemonaded.exe`):  "Lemonade",
		platformProgram(`C:\OA\tools\inventoryd.exe`): "Model folders",
		platformProgram(`C:\OA\tools\pytorchd.exe`):   "Diffusers",
		platformProgram(`C:\Other\provider.exe`):      "My new provider",
		platformProgram(`C:\Other\stray.exe`):         "stray (not registered)",
		platformProgram(`C:\Other\helper.exe`):        "helper (not registered)",
	} {
		if got := titler.name(program); got != want {
			t.Errorf("name(%s) = %q, want %q", program, got, want)
		}
	}
	if titler.isOwn(platformProgram(`C:\OA\tools\lemonaded.exe`)) {
		t.Error("a provider title grants no own-program standing")
	}
	if title, does := providerDisplay("lemonade-image", true); title != "Lemonade" || does != "Pictures, speech and transcription" {
		t.Errorf("providerDisplay = %q, %q", title, does)
	}
	for name, want := range map[string]string{"oa-setup-chat-467d9c6e43391a07c5853578": "Chat model", "comfy-image-1828": "Comfy image", "image-2-go": "Image 2 go"} {
		if title, _ := providerDisplay(name, true); title != want {
			t.Errorf("providerDisplay(%s) = %q, want %q", name, title, want)
		}
	}
	if title, _ := providerDisplay("---", true); title != "Unnamed provider" {
		t.Errorf("an empty name reads %q", title)
	}
}

// Engines read by their upstream projects (architect ruling 2026-11-09,
// second block): never "PyTorch", "modelhostd" or "pytorchd".
func TestEngineProvidersReadByUpstreamName(t *testing.T) {
	for name, want := range map[string]string{"modelhost": "llama.cpp", "pytorch-image": "Diffusers", "thenoise-image": "TheNoise"} {
		if title, _ := providerDisplay(name, true); title != want {
			t.Errorf("providerDisplay(%s) = %q, want %q", name, title, want)
		}
	}
	if title, caption := providerDisplay("pytorch-1828", true); title != "Diffusers" || caption != "Provides services to your apps" {
		t.Errorf("shipped models-host declaration reads %q, %q", title, caption)
	}
	if title, _ := providerDisplay("pytorch-1828", false); title != "Pytorch" {
		t.Errorf("unshipped lookalike reads %q", title)
	}
}

// A second installation's programs read by the product names, beside this
// Panel's own (architect ruling 2026-11-09, second block). The per-machine
// folder vouches by path; the per-user folder, which any user-level program
// can write, vouches only for a file signed by this Panel's own signer.
func TestOwnProgramsTrustOnlyProtectedOrSameSignerFolders(t *testing.T) {
	savedMachine, savedUser, savedSigner := oaInstallFolders, oaUserInstallFolder, oaSameSigner
	t.Cleanup(func() { oaInstallFolders, oaUserInstallFolder, oaSameSigner = savedMachine, savedUser, savedSigner })
	machine, user := t.TempDir(), t.TempDir()
	oaInstallFolders = func() []string { return []string{machine} }
	oaUserInstallFolder = func() string { return user }
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	// A program dropped in the user-writable folder: the real signer check
	// finds an unsigned file, so it is no own program while this Panel runs
	// from another folder.
	dropped := filepath.Join(user, "Abstraction Panel"+suffix)
	if err := os.WriteFile(dropped, []byte("not a signed Panel"), 0o755); err != nil {
		t.Fatal(err)
	}
	titler := newProgramTitler(nil)
	for _, program := range []string{dropped, filepath.Join(user, "openabstractions"+suffix)} {
		if titler.isOwn(program) || titler.name(program) == oaPanelTitle || titler.name(program) == oaProductTitle {
			t.Errorf("%s in the user-writable folder reads %q as an own program", program, titler.name(program))
		}
	}
	for program, want := range map[string]string{
		filepath.Join(machine, "openabstractions"+suffix):     "OpenAbstractions",
		filepath.Join(machine, "openabstractions-mcp"+suffix): "OpenAbstractions",
		filepath.Join(machine, "Abstraction Panel"+suffix):    "OpenAbstractions Panel",
	} {
		if got := titler.name(program); got != want || !titler.isOwn(program) {
			t.Errorf("name(%s) = %q own %v, want %q", program, got, titler.isOwn(program), want)
		}
	}
	if got := titler.name(filepath.Join(machine, "other"+suffix)); got != "other (not registered)" {
		t.Errorf("another program in the folder reads %q", got)
	}
	// A file signed by this Panel's own signer counts.
	oaSameSigner = func(program string) bool { return program == dropped }
	titler = newProgramTitler(nil)
	if got := titler.name(dropped); got != oaPanelTitle || !titler.isOwn(dropped) {
		t.Errorf("a same-signer Panel reads %q", got)
	}
	if titler.isOwn(filepath.Join(user, "openabstractions"+suffix)) {
		t.Error("only the verified file counts")
	}
}

// The Device page names a program no title names by its signed file
// description, and keeps "(not registered)" out; other pages keep the stem.
func TestDeviceNameUsesSignedDescription(t *testing.T) {
	modelHost := platformProgram(`C:\OA\tools\modelhostd.exe`)
	saved := signedDescription
	t.Cleanup(func() { signedDescription = saved })
	signedDescription = func(program string) string {
		if program == `C:\Program Files\Edge\msedge.exe` {
			return "Microsoft Edge"
		}
		return ""
	}
	panelInstalledPerMachine(t)
	titler := newProgramTitler(nil)
	titler.providers = []fwire.DeclarationState{{DeclaredBy: providerDeclaredByInstallation, Declaration: fwire.Declaration{Name: "modelhost", Program: modelHost}}}
	for program, want := range map[string]string{
		`C:\Program Files\Edge\msedge.exe`: "Microsoft Edge",
		modelHost:                          "llama.cpp",
		`C:\lms\llama-server.exe`:          "",
		"dwm":                              "Desktop Window Manager",
		"csrss":                            "Client Server Runtime Process",
		"WorkloadsSessionHost":             "",
	} {
		if got := titler.deviceName(program); got != want {
			t.Errorf("deviceName(%s) = %q, want %q", program, got, want)
		}
	}
	if got := titler.name(`C:\Program Files\Edge\msedge.exe`); got != "msedge (not registered)" {
		t.Errorf("Permissions keeps the stem: %q", got)
	}
	resources := []cardResourceState{{Resource: "card:0", Holders: []cardHolder{{Program: `C:\Program Files\Edge\msedge.exe`}, {Program: `C:\lms\llama-server.exe`}}}}
	if got := titleHolders(resources, titler); len(got) != 1 || got[`C:\Program Files\Edge\msedge.exe`] != "Microsoft Edge" {
		t.Errorf("holder titles %q", got)
	}
}

// A signed description loses format characters, so a right-to-left override
// cannot make it read as another name; a product name or a control
// character names nothing.
func TestDeviceDescriptionDropsFormatCharacters(t *testing.T) {
	for raw, want := range map[string]string{
		"Microsoft Edge":                     "Microsoft Edge",
		"Evil\u202Eexe.egdE tfosorciM":       "Evilexe.egdE tfosorciM",
		"\u200BDesktop Window Manager\uFEFF": "Desktop Window Manager",
		"OpenAbstractions":                   "",
		"Line\nbreak":                        "",
	} {
		if got := deviceDescription(raw); got != want {
			t.Errorf("deviceDescription(%q) = %q, want %q", raw, got, want)
		}
	}
}

// A signed description that claims a product name is no name.
func TestSignedDescriptionRefusesReservedTitles(t *testing.T) {
	if !reservedTitle("OpenAbstractions") {
		t.Skip("the reserved-title rule changed")
	}
	if runtime.GOOS != "windows" {
		if got := cachedSignedDescription(`/usr/bin/true`); got != "" {
			t.Errorf("another platform reads %q", got)
		}
		return
	}
	if got := cachedSignedDescription(`\server\share\tool.exe`); got != "" {
		t.Errorf("a network path reads %q", got)
	}
	if got := cachedSignedDescription(filepath.Join(t.TempDir(), "unsigned.exe")); got != "" {
		t.Errorf("a missing file reads %q", got)
	}
}

// platformProgram spells a Windows fixture program as this platform's
// absolute program: C:\OA\x.exe is /OA/x elsewhere.
func platformProgram(windows string) string {
	if runtime.GOOS == "windows" {
		return windows
	}
	posix := strings.ReplaceAll(strings.TrimPrefix(windows, "C:"), `\`, "/")
	return filepath.Clean(strings.TrimSuffix(posix, ".exe"))
}
