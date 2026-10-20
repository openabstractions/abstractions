package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/placetest"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// comfyFolder makes a ComfyUI folder (one holding custom_nodes) at base.
func TestOfficialExtensionRequiresTheExactHostFactsOwner(t *testing.T) {
	ctx := context.Background()
	extension := OfficialExtensionFor(AppID("unrelated-extension"))
	if extension.Connector(t.TempDir()) != "" || extension.InstalledDesktopKind() != "" ||
		len(extension.Find(ExtensionHostFolders{})) != 0 {
		t.Fatal("unrelated extension inherited host locators")
	}
	if _, err := extension.Connect(ctx, Catalog{}, ExtensionHost{}, PackageConnection{}); !errors.Is(err, errNoOfficialExtension) {
		t.Fatalf("unrelated extension admitted the installer: %v", err)
	}
	if err := extension.SaveChosenFolder(ctx, t.TempDir()); !errors.Is(err, errNoOfficialExtension) {
		t.Fatalf("unrelated extension admitted a saved folder: %v", err)
	}
	if kind := fixtureOfficialExtension().InstalledDesktopKind(); kind != "comfy-desktop" {
		t.Fatalf("historical installed desktop kind changed: %q", kind)
	}
}

func fixtureOfficialExtension() OfficialExtension {
	return OfficialExtensionFor(ComfyUI)
}

func makeComfyFolder(t *testing.T, base string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(base, "custom_nodes"), 0700); err != nil {
		t.Fatal(err)
	}
	return base
}

func writeComfyFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func jsonPath(path string) string { return strings.ReplaceAll(path, `\`, `\\`) }

// signedComfyPrograms stands in for the signature check: programs carry a
// signature Windows trusts, and every other file carries none.
func signedComfyPrograms(t *testing.T, programs ...string) {
	t.Helper()
	previous := trustedExtensionProgram
	trustedExtensionProgram = func(program string) bool {
		for _, p := range programs {
			if strings.EqualFold(p, program) {
				return true
			}
		}
		return false
	}
	t.Cleanup(func() { trustedExtensionProgram = previous })
}

// Setup finds ComfyUI Desktop through config.json's basePath, Comfy
// Desktop's installs, and a portable folder, each once; a connected one is
// the row's ComfyUI.
func TestOfficialComfyIsFoundInEachPlace(t *testing.T) {
	root := discoveryFixtureRoot(t)
	roaming, local := filepath.Join(root, "Roaming"), filepath.Join(root, "Local")
	desktop := makeComfyFolder(t, filepath.Join(root, "Documents", "ComfyUI"))
	writeComfyFixture(t, filepath.Join(roaming, "ComfyUI", "config.json"), `{"installState":"installed","basePath":"`+jsonPath(desktop)+`"}`)
	writeComfyFixture(t, filepath.Join(local, "Programs", "@comfyorgcomfyui-electron", "ComfyUI.exe"), "program")
	installed := makeComfyFolder(t, filepath.Join(local, "Comfy-Desktop", "ComfyUI-Installs", "default", "ComfyUI"))
	writeComfyFixture(t, filepath.Join(local, "Programs", "Comfy Desktop", "Comfy Desktop.exe"), "program")
	writeComfyFixture(t, filepath.Join(local, "Programs", "Comfy Desktop", "Uninstall Comfy Desktop.exe"), "program")
	signedComfyPrograms(t, filepath.Join(local, "Programs", "Comfy Desktop", "Comfy Desktop.exe"))
	portable := filepath.Join(root, "ComfyUI_windows_portable")
	makeComfyFolder(t, filepath.Join(portable, "ComfyUI"))
	writeComfyFixture(t, filepath.Join(portable, "run_cpu.bat"), "@echo off")
	writeComfyFixture(t, filepath.Join(portable, "run_amd_gpu.bat"), "@echo off")

	found := fixtureOfficialExtension().Find(ExtensionHostFolders{Roaming: roaming, Local: local, Chosen: portable, Portables: []string{portable}})
	want := []ExtensionHost{
		{Kind: ExtensionPortable, Base: filepath.Join(portable, "ComfyUI"), Program: filepath.Join(portable, "run_amd_gpu.bat")},
		{Kind: ExtensionDesktop, Base: desktop, Program: filepath.Join(local, "Programs", "@comfyorgcomfyui-electron", "ComfyUI.exe")},
		{Kind: fixtureOfficialExtension().InstalledDesktopKind(), Base: installed, Program: filepath.Join(local, "Programs", "Comfy Desktop", "Comfy Desktop.exe")},
	}
	if len(found) != len(want) {
		t.Fatalf("found %+v", found)
	}
	for i := range want {
		if found[i] != want[i] {
			t.Fatalf("found[%d] = %+v, want %+v", i, found[i], want[i])
		}
	}
	if primary, ok := PrimaryExtensionHost(found); !ok || primary.Kind != ExtensionPortable {
		t.Fatalf("primary %+v", primary)
	}
	writeComfyFixture(t, filepath.Join(desktop, "custom_nodes", "comfyui-openabstractions", "__init__.py"), "")
	if primary, _ := PrimaryExtensionHost(fixtureOfficialExtension().Find(ExtensionHostFolders{Roaming: roaming, Local: local, Chosen: portable})); primary.Connected {
		t.Fatal("an integration without its connector counts as connected")
	}
	writeComfyFixture(t, fixtureOfficialExtension().Connector(desktop), "")
	primary, ok := PrimaryExtensionHost(fixtureOfficialExtension().Find(ExtensionHostFolders{Roaming: roaming, Local: local, Chosen: portable}))
	if !ok || primary.Kind != ExtensionDesktop || !primary.Connected {
		t.Fatalf("the connected ComfyUI is not the row's: %+v", primary)
	}
}

// Comfy Desktop's installations.json names its local installs: an installed
// one is found at installPath\ComfyUI wherever it lies, one still installing
// is passed over even inside ComfyUI-Installs, and Comfy Cloud and entries
// with a relative, remote, missing or file installPath are skipped.
func TestComfyDesktopInstallationsAreRead(t *testing.T) {
	root := t.TempDir()
	roaming, local := filepath.Join(root, "Roaming"), filepath.Join(root, "Local")
	program := filepath.Join(local, "Programs", "Comfy Desktop", "Comfy Desktop.exe")
	writeComfyFixture(t, program, "program")
	writeComfyFixture(t, filepath.Join(local, "Programs", "Comfy Desktop", "Uninstall Comfy Desktop.exe"), "program")
	signedComfyPrograms(t, program)
	folders := ExtensionHostFolders{Roaming: roaming, Local: local}
	installations := filepath.Join(roaming, "Comfy Desktop", "installations.json")

	if _, ok := fixtureOfficialExtension().DesktopToFinish(ExtensionHostFolders{Roaming: roaming, Local: filepath.Join(root, "nowhere")}); ok {
		t.Fatal("Comfy Desktop to finish without its program")
	}
	finish, ok := fixtureOfficialExtension().DesktopToFinish(folders)
	if !ok || finish.Program != program || finish.Base != "" {
		t.Fatalf("Comfy Desktop with no installations.json: %+v %v", finish, ok)
	}

	installing := filepath.Join(local, "Comfy-Desktop", "ComfyUI-Installs", "ComfyUI")
	makeComfyFolder(t, filepath.Join(installing, "ComfyUI"))
	elsewhere := filepath.Join(root, "D", "Comfy", "Second")
	elsewhereBase := filepath.Join(elsewhere, "ComfyUI")
	makeComfyFolder(t, elsewhereBase)
	file := filepath.Join(root, "file")
	writeComfyFixture(t, file, "")
	empty := filepath.Join(root, "empty")
	if err := os.MkdirAll(empty, 0700); err != nil {
		t.Fatal(err)
	}
	entry := func(path, status string) string {
		return `{"id":"inst-1","installPath":"` + jsonPath(path) + `","sourceId":"standalone","status":"` + status +
			`","launchMode":"window","browserPartition":"unique","launchArgs":"--enable-manager","variant":"win-amd"}`
	}
	cloud := `{"id":"inst-2","status":"installed","name":"Comfy Cloud","sourceId":"cloud","remoteUrl":"https://cloud.comfy.org/","launchMode":"window"}`
	writeComfyFixture(t, installations, "["+entry(installing, "installing")+","+cloud+"]")
	if found := fixtureOfficialExtension().Find(folders); len(found) != 0 {
		t.Fatalf("an install still installing was found: %+v", found)
	}
	if _, ok := fixtureOfficialExtension().DesktopToFinish(folders); !ok {
		t.Fatal("Comfy Desktop installing its first ComfyUI does not ask to finish its setup")
	}

	writeComfyFixture(t, installations, "["+strings.Join([]string{
		entry(installing, "installed"), cloud, entry(elsewhere, "installed"),
		entry("relative\\ComfyUI", "installed"), entry(`\\server\share\ComfyUI`, "installed"),
		entry(filepath.Join(root, "missing"), "installed"), entry(file, "installed"), entry(empty, "installed"),
		`"not an object"`, `{"installPath":3,"status":"installed"}`,
	}, ",")+"]")
	found := fixtureOfficialExtension().Find(folders)
	want := []ExtensionHost{
		{Kind: fixtureOfficialExtension().InstalledDesktopKind(), Base: filepath.Join(installing, "ComfyUI"), Program: program},
	}
	// The Desktop manifest reader accepts drive-letter paths. The default
	// installs folder is also discovered directly on POSIX fixture hosts.
	if runtime.GOOS == "windows" {
		want = append(want, ExtensionHost{Kind: fixtureOfficialExtension().InstalledDesktopKind(), Base: elsewhereBase, Program: program})
	}
	if !slices.Equal(found, want) {
		t.Fatalf("found %+v", found)
	}
	if _, ok := fixtureOfficialExtension().DesktopToFinish(folders); ok {
		t.Fatal("Comfy Desktop with an installed ComfyUI asks to finish its setup")
	}

	for _, body := range []string{"", "{}", `[{"installPath":`, strings.Repeat(" ", extensionHostSettingsLimit+1)} {
		writeComfyFixture(t, installations, body)
		found := fixtureOfficialExtension().Find(folders)
		if len(found) != 1 || found[0].Base != filepath.Join(installing, "ComfyUI") {
			t.Fatalf("%.20q: the installs folder is still read: %+v", body, found)
		}
	}
}

// The ordinary discovery path reads the compiled extension declaration even
// when the release has no extension package.
func TestOfficialDesktopDiscoveryUsesEmbeddedExtension(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Desktop install observations use Windows drive-letter paths")
	}
	root := t.TempDir()
	folders := ExtensionHostFolders{Roaming: filepath.Join(root, "Roaming")}
	record := filepath.Join(folders.Roaming, "Comfy Desktop", "installations.json")
	local := filepath.Join(root, "local")
	makeComfyFolder(t, filepath.Join(local, "ComfyUI"))
	for _, name := range []string{"cloud-source", "remote-address", "wrong-type"} {
		makeComfyFolder(t, filepath.Join(root, name, "ComfyUI"))
	}
	raw := `[{"installPath":"` + jsonPath(local) + `","status":"installed","remoteUrl":""},
   {"installPath":"` + jsonPath(filepath.Join(root, "cloud-source")) + `","status":"installed","sourceId":"cloud"},
   {"installPath":"` + jsonPath(filepath.Join(root, "remote-address")) + `","status":"installed","remoteUrl":"https://invalid.example"},
   {"installPath":"` + jsonPath(filepath.Join(root, "wrong-type")) + `","status":"installed","remoteUrl":7}]`
	writeComfyFixture(t, record, raw)
	c := compiledCatalog()
	if c.LegacyConnectorPin != nil {
		t.Fatal("the compiled discovery fixture unexpectedly includes a package")
	}
	found := fixtureOfficialExtension().Find(folders)
	if len(found) != 1 || found[0].Base != filepath.Join(local, "ComfyUI") || found[0].Kind != fixtureOfficialExtension().InstalledDesktopKind() {
		t.Fatalf("embedded discovery = %+v", found)
	}
}

func TestOfficialDesktopDiscoveryRefusesRecordAndUsesFallback(t *testing.T) {
	root := discoveryFixtureRoot(t)
	folders := ExtensionHostFolders{Roaming: filepath.Join(root, "Roaming"), Local: filepath.Join(root, "Local")}
	fallback := filepath.Join(folders.Local, "Comfy-Desktop", "ComfyUI-Installs", "fallback", "ComfyUI")
	makeComfyFolder(t, fallback)
	record := filepath.Join(folders.Roaming, "Comfy Desktop", "installations.json")
	writeComfyFixture(t, record, strings.Repeat(" ", 2<<20))
	raw := string(compiledCatalog().discovery[ComfyUI])
	for _, test := range []struct{ name, declaration, diagnostic string }{
		{"parent", strings.Replace(raw, "Comfy Desktop/installations.json", "../installations.json", 1), "invalid discovery record path"},
		{"absolute", strings.Replace(raw, "Comfy Desktop/installations.json", "/installations.json", 1), "invalid discovery record path"},
		{"oversize", raw, "discovery file exceeds 1048576 bytes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			line := "setup: desktop installation discovery is unavailable: " + test.diagnostic
			discoveryDiagnostics.Delete(line)
			t.Cleanup(func() { discoveryDiagnostics.Delete(line) })
			found := fixtureOfficialExtension().find(folders, []byte(test.declaration))
			if len(found) != 1 || found[0].Base != fallback {
				t.Fatalf("fallback = %+v", found)
			}
			if _, logged := discoveryDiagnostics.Load(line); !logged {
				t.Fatalf("refusal was not logged: %s", line)
			}
		})
	}
}

func discoveryFixtureRoot(t *testing.T) string {
	t.Helper()
	root, err := fileid.Open(t.Context(), fileid.Self, t.TempDir(), fileid.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	return root.FileID().Path()
}

func TestOfficialDesktopDiscoveryUsesOrderedSettingsHints(t *testing.T) {
	root := discoveryFixtureRoot(t)
	folders := ExtensionHostFolders{Roaming: filepath.Join(root, "Roaming")}
	config := filepath.Join(folders.Roaming, "ComfyUI", "config.json")
	first := makeComfyFolder(t, filepath.Join(root, "first"))
	second := makeComfyFolder(t, filepath.Join(root, "second"))
	third := makeComfyFolder(t, filepath.Join(root, "third"))
	for _, test := range []struct {
		name     string
		settings map[string]any
		want     string
		logHint  bool
	}{
		{"ordered", map[string]any{"basePath": first, "base_path": second, "installPath": third}, first, false},
		{"missing", map[string]any{"base_path": second, "installPath": third}, second, false},
		{"empty", map[string]any{"basePath": "", "base_path": second, "installPath": third}, second, false},
		{"rejected-directory", map[string]any{"basePath": filepath.Join(root, "missing"), "base_path": second, "installPath": third}, second, false},
		{"wrong-type", map[string]any{"basePath": map[string]string{"secret": "do not log"}, "base_path": second, "installPath": third}, second, true},
		{"legacy-final", map[string]any{"installPath": third}, third, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			line := "setup: desktop settings hint 0 has another shape"
			discoveryDiagnostics.Delete(line)
			t.Cleanup(func() { discoveryDiagnostics.Delete(line) })
			raw, err := json.Marshal(test.settings)
			if err != nil {
				t.Fatal(err)
			}
			writeComfyFixture(t, config, string(raw))
			found := fixtureOfficialExtension().Find(folders)
			if len(found) != 1 || found[0].Kind != ExtensionDesktop || found[0].Base != test.want {
				t.Fatalf("settings discovery = %+v, want %s", found, test.want)
			}
			if _, logged := discoveryDiagnostics.Load(line); logged != test.logHint {
				t.Fatalf("unexpected hint diagnostic: %v", logged)
			}
		})
	}
}

// Open starts a program in the account's own Programs folder only when it
// carries a signature Windows trusts: an unsigned comfy.exe there leaves the
// row at Get ComfyUI, and an install's row has no program to open.
func TestAnUnsignedComfyProgramIsNotOpened(t *testing.T) {
	root := t.TempDir()
	roaming, local := filepath.Join(root, "Roaming"), filepath.Join(root, "Local")
	writeComfyFixture(t, filepath.Join(local, "Programs", "MyComfy", "comfy.exe"), "MZ not signed")
	writeComfyFixture(t, filepath.Join(local, "Programs", "Comfy Desktop", "Comfy Desktop.exe"), "MZ not signed")
	folders := ExtensionHostFolders{Roaming: roaming, Local: local}
	if finish, ok := fixtureOfficialExtension().DesktopToFinish(folders); ok {
		t.Fatalf("an unsigned program asks to finish Comfy Desktop's setup: %+v", finish)
	}
	installed := makeComfyFolder(t, filepath.Join(local, "Comfy-Desktop", "ComfyUI-Installs", "default", "ComfyUI"))
	found := fixtureOfficialExtension().Find(folders)
	if len(found) != 1 || found[0].Base != installed || found[0].Program != "" {
		t.Fatalf("found %+v", found)
	}
	if err := OpenExtensionHost(found[0], nativeArchiveAppTitle); err == nil || !strings.Contains(err.Error(), "Start menu") {
		t.Fatalf("Open of an install without a signed program: %v", err)
	}
}

func TestOfficialDesktopProgramDoesNotFinishAnotherDesktopInstall(t *testing.T) {
	local := t.TempDir()
	electron := filepath.Join(local, "Programs", "@comfyorgcomfyui-electron", "ComfyUI.exe")
	writeComfyFixture(t, electron, "signed official desktop")
	alternate := filepath.Join(local, "Programs", "MyComfy", "comfy.exe")
	signedComfyPrograms(t, electron, alternate)
	if got := fixtureOfficialExtension().locator.singletonProgram(local); got != electron {
		t.Fatalf("official Desktop program = %q, want %q", got, electron)
	}
	if finish, ok := fixtureOfficialExtension().DesktopToFinish(ExtensionHostFolders{Local: local}); ok {
		t.Fatalf("official Desktop was selected for another Desktop's setup: %+v", finish)
	}
	writeComfyFixture(t, alternate, "signed alternate desktop")
	if finish, ok := fixtureOfficialExtension().DesktopToFinish(ExtensionHostFolders{Local: local}); !ok || finish.Program != alternate || finish.Base != "" {
		t.Fatalf("alternate Desktop setup = %+v, found %v", finish, ok)
	}
}

// A settings file of another shape, or naming a folder without custom_nodes,
// finds nothing; so does a folder the person chose that holds no ComfyUI.
func TestDeclaredHostProcessRootsStayBesideTheSelectedBase(t *testing.T) {
	parent := t.TempDir()
	base := filepath.Join(parent, "host")
	got := hostProcessFolders(base, fixtureOfficialExtension().processFolders)
	want := []string{base, filepath.Join(parent, "standalone-env"), filepath.Join(parent, "python_embeded")}
	if !slices.Equal(got, want) {
		t.Fatalf("observed folders %v, want %v", got, want)
	}
	if got := hostProcessFolders("relative", fixtureOfficialExtension().processFolders); got != nil {
		t.Fatalf("relative host selected process folders: %v", got)
	}
}

func TestDeclaredHostLocatorUsesPackageLayoutAndSettingsOrder(t *testing.T) {
	root := t.TempDir()
	roots := ExtensionHostFolders{Local: filepath.Join(root, "local"), Roaming: filepath.Join(root, "roaming")}
	locator := extensionHostLocator{
		spec: product.PackageSpec{ID: "example-app", Title: "Example App", HostSubfolder: "plugins",
			InstallFolder: "reviewed-extension", Connector: "example.exe"},
		installBase: "workspace", localInstalls: "app-instances",
		kinds:        extensionHostKinds{installed: "installed-example"},
		settingsKeys: []string{"base", "legacyBase"}, settingsPaths: []string{"settings.json"},
		integrationMarker: "extension.ready", signature: func(string) bool { return true },
		search: signedHostSearch{preferred: "Programs/Example/Example.exe"},
	}
	preferred := filepath.Join(root, "preferred")
	fallback := filepath.Join(roots.Local, "app-instances", "older", "workspace")
	launcher := filepath.Join(roots.Local, "Programs", "Example", "Example.exe")
	writeComfyFixture(t, launcher, "fixture")
	writeComfyFixture(t, packageConnector(locator.spec, preferred), "connector")
	writeComfyFixture(t, filepath.Join(fallback, "plugins", "empty"), "")
	raw, err := json.Marshal(map[string]string{"base": preferred, "legacyBase": fallback})
	if err != nil {
		t.Fatal(err)
	}
	writeComfyFixture(t, filepath.Join(roots.Roaming, "settings.json"), string(raw))
	got := locator.find(roots, nil)
	if len(got) != 2 || got[0].Base != preferred || got[1].Base != fallback || got[0].Program != launcher || got[0].Connected || got[0].Kind != "installed-example" {
		t.Fatalf("declared hosts before marker: %+v", got)
	}
	raw, err = json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil || len(wire) != 3 ||
		wire["kind"] != "installed-example" || wire["base"] != preferred || wire["connected"] != false {
		t.Fatalf("observed host projection: %s, %v", raw, err)
	}
	writeComfyFixture(t, filepath.Join(filepath.Dir(packageConnector(locator.spec, preferred)), "extension.ready"), "")
	got = locator.find(roots, nil)
	if len(got) != 2 || !got[0].Connected || got[1].Connected {
		t.Fatalf("declared hosts after marker: %+v", got)
	}
}

func TestOfficialHostLocatorUsesDirectoryIdentity(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "ComfyK")
	other := filepath.Join(root, "ComfyK")
	makeComfyFolder(t, base)
	makeComfyFolder(t, other)
	alias := filepath.Join(root, "alias")
	var err error
	if runtime.GOOS == "windows" {
		err = placetest.Junction(alias, base)
	} else {
		err = os.Symlink(base, alias)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(alias); err != nil {
			t.Error(err)
		}
	})
	pending := pendingExtensionHosts{names: map[string]bool{alias: true},
		identities: map[string]bool{hostDirectoryKey(alias): true}}
	if !pending.contains(base) {
		t.Fatal("pending host alias did not suppress its fallback")
	}
	missing := filepath.Join(root, "in-progress")
	pending.names[missing] = true
	if !pending.contains(missing) {
		t.Fatal("the exact pending spelling was lost before the folder existed")
	}
	makeComfyFolder(t, missing)
	if !pending.contains(missing) {
		t.Fatal("the exact pending spelling exposed a newly created fallback")
	}

	same, err := fileid.SameFile(base, other)
	if err != nil {
		t.Fatal(err)
	}
	if got := pending.contains(other); got != same {
		t.Fatalf("pending host identity = %t, filesystem = %t", got, same)
	}
	locator := fixtureOfficialExtension().locator
	got := locator.find(ExtensionHostFolders{Chosen: base, Portables: []string{alias, other}}, nil)
	if same {
		if len(got) != 1 || got[0].Base != base {
			t.Fatalf("aliases displaced the first host: %+v", got)
		}
	} else if len(got) != 2 || got[0].Base != base || got[1].Base != other {
		t.Fatalf("distinct hosts were dropped or reordered: %+v", got)
	}
}

func TestOfficialHostLocatorKeepsSignedCandidateOrder(t *testing.T) {
	local := t.TempDir()
	known := filepath.Join(local, "Programs", "Comfy Desktop", "Comfy Desktop.exe")
	first := filepath.Join(local, "Comfy-Desktop", "Comfy fallback.exe")
	later := filepath.Join(local, "Programs", "A Comfy", "Comfy Desktop copy.exe")
	skipped := filepath.Join(local, "Programs", "@comfyorgcomfyui-electron", "Comfy other.exe")
	uninstall := filepath.Join(local, "Comfy-Desktop", "Comfy uninstall.exe")
	for _, program := range []string{known, first, later, skipped, uninstall} {
		writeComfyFixture(t, program, "fixture")
	}
	signedComfyPrograms(t, known, first, later, skipped, uninstall)
	if got := fixtureOfficialExtension().locator.signedProgram(local); got != known {
		t.Fatalf("trusted preferred program = %q, want %q", got, known)
	}
	signedComfyPrograms(t, first, later, skipped, uninstall)
	if got := fixtureOfficialExtension().locator.signedProgram(local); got != first {
		t.Fatalf("first trusted fallback = %q, want %q", got, first)
	}
	signedComfyPrograms(t, later, skipped, uninstall)
	if got := fixtureOfficialExtension().locator.signedProgram(local); got != later {
		t.Fatalf("trusted Programs fallback = %q, want %q", got, later)
	}
	signedComfyPrograms(t, skipped, uninstall)
	if got := fixtureOfficialExtension().locator.signedProgram(local); got != "" {
		t.Fatalf("excluded candidate selected: %q", got)
	}
}

func TestOfficialComfyReadsSettingsDefensively(t *testing.T) {
	root := discoveryFixtureRoot(t)
	roaming := filepath.Join(root, "Roaming")
	empty := filepath.Join(root, "empty")
	if err := os.MkdirAll(empty, 0700); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", "[]", `{"basePath":3}`, `{"basePath":"relative"}`, `{"basePath":"` + jsonPath(empty) + `"}`,
		`{"basePath":"` + jsonPath(filepath.Join(root, "missing")) + `"}`, strings.Repeat(" ", extensionHostSettingsLimit+1)} {
		writeComfyFixture(t, filepath.Join(roaming, "ComfyUI", "config.json"), body)
		writeComfyFixture(t, filepath.Join(roaming, "Comfy Desktop", "config.json"), body)
		if found := fixtureOfficialExtension().Find(ExtensionHostFolders{Roaming: roaming, Local: filepath.Join(root, "Local"), Chosen: empty}); len(found) != 0 {
			t.Fatalf("%q found %+v", body, found)
		}
	}
	if _, ok := PrimaryExtensionHost(nil); ok {
		t.Fatal("no ComfyUI has a primary")
	}
}

// The chosen folder is kept only when it holds a ComfyUI: the portable
// folder or its ComfyUI folder.
func TestTheChosenComfyFolderIsKept(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	ctx := context.Background()
	empty := t.TempDir()
	if err := fixtureOfficialExtension().SaveChosenFolder(ctx, empty); err == nil || fixtureOfficialExtension().ChosenFolder() != "" {
		t.Fatal("a folder without ComfyUI was kept")
	}
	home, err := CurrentHome()
	if err != nil {
		t.Fatal(err)
	}
	oldFolder, portable := t.TempDir(), t.TempDir()
	makeComfyFolder(t, filepath.Join(oldFolder, "ComfyUI"))
	makeComfyFolder(t, filepath.Join(portable, "ComfyUI"))
	legacyRecord := filepath.Join(home.Config, "comfyui-folder.json")
	historical, err := json.Marshal(map[string]string{"folder": oldFolder})
	if err != nil {
		t.Fatal(err)
	}
	writeComfyFixture(t, legacyRecord, string(historical))
	if fixtureOfficialExtension().ChosenFolder() != oldFolder {
		t.Fatalf("legacy recognition chose %q", fixtureOfficialExtension().ChosenFolder())
	}
	if err := fixtureOfficialExtension().SaveChosenFolder(ctx, portable); err != nil || fixtureOfficialExtension().ChosenFolder() != portable {
		t.Fatalf("current choice %q, error %v", fixtureOfficialExtension().ChosenFolder(), err)
	}
	if raw, err := os.ReadFile(legacyRecord); err != nil || string(raw) != string(historical) {
		t.Fatalf("picker changed the historical record: %q, %v", raw, err)
	}
	if err := fixtureOfficialExtension().SaveChosenFolder(ctx, empty); err == nil || fixtureOfficialExtension().ChosenFolder() != portable {
		t.Fatal("an invalid choice changed the persisted portable folder")
	}
	record := fixtureOfficialExtension().hostSelection().currentRecord(home.Config)
	writeComfyFixture(t, record, `{"folder":"relative"}`)
	if fixtureOfficialExtension().ChosenFolder() != "" {
		t.Fatal("invalid current choice fell back to the older folder")
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"folder": empty})
	if err != nil {
		t.Fatal(err)
	}
	writeComfyFixture(t, legacyRecord, string(raw))
	if fixtureOfficialExtension().ChosenFolder() != "" {
		t.Fatal("a historical absolute value without a portable installation was accepted")
	}
}

func installPinnedComfyPackage(ctx context.Context, c Catalog, base string, install func(context.Context, string, string) error) (func(context.Context) error, error) {
	return fixtureOfficialExtension().runPackage(ctx, c, base, func(ctx context.Context, msi string, _ product.PackageSpec, _ func(context.Context) error) error {
		return install(ctx, msi, base)
	})
}

func TestTheComfyExtensionComesFromThePackageFolder(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	ctx := context.Background()
	rejectInstall := func(context.Context, string, string) error { t.Fatal("unexpected installer call"); return nil }
	if _, err := installPinnedComfyPackage(ctx, Catalog{}, "", rejectInstall); !errors.Is(err, errPackageUnavailable) {
		t.Fatalf("a catalogue without the pin: %v", err)
	}
	body := []byte("extension package")
	sum := sha256.Sum256(body)
	pin := &ConnectorPackagePin{File: "comfyui-0.8.0.msi", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(body)), Version: "0.8.0",
		URL: "https://github.com/openabstractions/redist/releases/download/v0.8.0/comfyui-0.8.0.msi"}
	c := Catalog{LegacyConnectorPin: pin}
	saved := LocalPackageFolder
	t.Cleanup(func() { LocalPackageFolder = saved })
	LocalPackageFolder = ""
	if _, err := installPinnedComfyPackage(ctx, c, "", rejectInstall); !errors.Is(err, errPackageUnavailable) {
		t.Fatalf("a build without a package folder: %v", err)
	}
	LocalPackageFolder = t.TempDir()
	writeComfyFixture(t, filepath.Join(LocalPackageFolder, pin.File), "extension packagX")
	if _, err := installPinnedComfyPackage(ctx, c, "", rejectInstall); err == nil || errors.Is(err, errPackageUnavailable) {
		t.Fatalf("other bytes at the pinned size: %v", err)
	}
	writeComfyFixture(t, filepath.Join(LocalPackageFolder, pin.File), string(body))
	publish, err := installPinnedComfyPackage(ctx, c, "", func(context.Context, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := publish(ctx); err != nil {
		t.Fatal(err)
	}
	copied, err := fixtureOfficialExtension().packageFiles().current()
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(copied); err != nil || string(raw) != string(body) || filepath.Dir(copied) == LocalPackageFolder {
		t.Fatalf("copy %s: %q %v", copied, raw, err)
	}
	config, err := extensionHostConfig(ctx, fixtureOfficialExtension().spec, `C:\Comfy`)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(config); string(raw) != `{"basePath":"C:\\Comfy"}` {
		t.Fatalf("host config %s", raw)
	}
}

func TestLegacyPackageConnectUsesTheCompiledOrderAndExactMode(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	oldFolder := LocalPackageFolder
	t.Cleanup(func() { LocalPackageFolder = oldFolder })
	LocalPackageFolder = t.TempDir()
	base := t.TempDir()
	makeComfyFolder(t, base)
	const candidate = "reviewed historical MSI"
	pin := &ConnectorPackagePin{URL: releaseAssetURL("v0.8.0", "legacy.msi"),
		SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(candidate))), Bytes: int64(len(candidate))}
	writeComfyFixture(t, filepath.Join(LocalPackageFolder, "legacy.msi"), candidate)
	current, err := fixtureOfficialExtension().packageFiles().current()
	if err != nil {
		t.Fatal(err)
	}
	var record PackageMaintenanceRecord
	confirmed := false
	ctx := context.Background()
	handled, err := fixtureOfficialExtension().connect(ctx, Catalog{LegacyConnectorPin: pin}, ExtensionHost{Base: base},
		PackageConnection{
			Expect: func(context.Context, string, string, string) {},
			ConfirmLegacy: func(_ context.Context, registration, program string) error {
				confirmed = record.Connection != nil && record.Connection.InstallerAcknowledged &&
					registration == fixtureOfficialExtension().target(base).registration && program == fixtureOfficialExtension().Connector(base)
				return nil
			},
			Save: func(_ context.Context, before, after *PackageConnectionReceipt) error {
				return record.SaveConnection(before, after)
			},
			Publish: record.PublishConnection,
			Commit:  record.CommitConnection,
		}, func(_ context.Context, packagePath, installBase string, spec product.PackageSpec, mode extensionPackageMode, beforeEffect func() error) error {
			if err := beforeEffect(); err != nil {
				return err
			}
			raw, err := os.ReadFile(current)
			if err != nil || string(raw) != candidate || packagePath == current || installBase != base ||
				spec.ID != fixtureOfficialExtension().target(base).catalogID || mode != extensionReplacePackage {
				t.Fatalf("compatibility install: bytes %q, target %q, mode %v, read error %v", raw, installBase, mode, err)
			}
			return nil
		})
	if err != nil || !handled || !confirmed || record.Connection != nil {
		t.Fatalf("historical transaction: handled %t confirmed %t record %+v error %v", handled, confirmed, record.Connection, err)
	}
}

func TestCompiledConnectorPackageUsesAdmittedPinsForStaging(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	saved := LocalPackageFolder
	t.Cleanup(func() { LocalPackageFolder = saved })
	LocalPackageFolder = t.TempDir()
	const body, connector, file = "admitted MSI", "admitted connector", "extension.msi"
	packageDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	connectorDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(connector)))
	doc := fmt.Appendf(nil, `{"schema":2,"release_tag":"v0.8.0","apps":[],"extensions":[{"id":%q,"file":%q,"sha256":%q,"bytes":%d,"version":"0.8.0","connector_sha256":%q,"connector_bytes":%d}]}`,
		ComfyUI, file, packageDigest, len(body), connectorDigest, len(connector))
	c := Catalog{}
	if err := projectAdopterAssets(&c, "windows", "amd64", doc); err != nil {
		t.Fatal(err)
	}
	compiled, ok := c.integrationPackage(string(ComfyUI))
	if !ok {
		t.Fatal("compiled release binding is missing")
	}
	if digest, err := fixtureOfficialExtension().PackageDigestForConnector(c, connectorDigest, int64(len(connector))); err != nil || digest != packageDigest {
		t.Fatalf("admitted current package digest = %q, %v", digest, err)
	}
	writeComfyFixture(t, filepath.Join(LocalPackageFolder, file), body)
	ctx := context.Background()
	base := makeComfyFolder(t, t.TempDir())
	writeComfyFixture(t, fixtureOfficialExtension().Connector(base), connector)
	installCalls := 0
	install := func(_ context.Context, path, _ string) error {
		installCalls++
		got, err := os.ReadFile(path)
		if err != nil || string(got) != body {
			t.Fatalf("staged bytes = %q, %v", got, err)
		}
		return nil
	}
	if publish, err := installPinnedComfyPackage(ctx, c, base, install); err != nil || publish == nil || installCalls != 1 {
		t.Fatalf("compiled package staging: publish=%t, calls=%d, error=%v", publish != nil, installCalls, err)
	}
	held, err := fixtureOfficialExtension().HoldConnector(ctx, c, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	registered := false
	ops := PackageConnection{
		Admit:  func(context.Context, string, string, bool) error { return nil },
		Expect: func(context.Context, string, string, string) {},
		Confirm: func(_ context.Context, name, path string) error {
			registered = true
			if name != ComfyApplicationName || path != fixtureOfficialExtension().Connector(base) {
				t.Fatalf("registered target %q %q", name, path)
			}
			return nil
		},
		Save: func(context.Context, *PackageConnectionReceipt, *PackageConnectionReceipt) error { return nil },
		Commit: func(ctx context.Context, _ PackageConnectionReceipt, publish func(context.Context) error) error {
			return publish(ctx)
		},
	}
	found := ExtensionHost{Base: base}
	installWithProof := func(ctx context.Context, path, base string, _ product.PackageSpec, _ extensionPackageMode, mark func() error) error {
		if err := mark(); err != nil {
			return err
		}
		return install(ctx, path, base)
	}
	if handled, err := fixtureOfficialExtension().connect(ctx, c, found, ops, installWithProof); !handled || err != nil || !registered || installCalls != 2 {
		t.Fatalf("compiled Connect: registered=%t, calls=%d, error=%v", registered, installCalls, err)
	}
	mismatch := c
	binding := compiled
	binding.spec.Registration = "different-registration"
	mismatch.connectorPackages = map[string]admittedPackage{string(ComfyUI): binding}
	registered = false
	if handled, err := fixtureOfficialExtension().connect(ctx, mismatch, found, ops, installWithProof); !handled || err == nil || registered || installCalls != 2 {
		t.Fatalf("mismatched target: registered=%t, calls=%d, error=%v", registered, installCalls, err)
	}
	for name, change := range map[string]func(*product.PackageSpec){
		"layout": func(spec *product.PackageSpec) { spec.InstallFolder = "other-folder" },
		"config": func(spec *product.PackageSpec) { spec.HostKey = "otherKey" },
		"family": func(spec *product.PackageSpec) { spec.UpgradeCode = "{3B877AF4-3190-4197-BD32-10DA59CDC6CC}" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := compiled
			change(&changed.spec)
			mismatch := c
			mismatch.connectorPackages = map[string]admittedPackage{string(ComfyUI): changed}
			if handled, err := fixtureOfficialExtension().connect(ctx, mismatch, found, ops, installWithProof); !handled || err == nil || registered || installCalls != 2 {
				t.Fatalf("Connect accepted a different host binding: %t, %v", handled, err)
			}
			if publish, err := installPinnedComfyPackage(ctx, mismatch, base, install); publish != nil || err == nil || installCalls != 2 {
				t.Fatalf("replacement staged a different host binding: %t, %v", publish != nil, err)
			}
			if held, err := fixtureOfficialExtension().HoldConnector(ctx, mismatch, base); err == nil {
				_ = held.Close()
				t.Fatal("Registry re-pin accepted a different host binding")
			}
		})
	}
	for name, change := range map[string]func(*ConnectorPackagePin){
		"package":   func(pin *ConnectorPackagePin) { pin.SHA256 = strings.Repeat("a", 64) },
		"connector": func(pin *ConnectorPackagePin) { pin.ConnectorBytes++ },
		"release":   func(pin *ConnectorPackagePin) { pin.URL = strings.Replace(pin.URL, "/v0.8.0/", "/v0.7.0/", 1) },
	} {
		t.Run(name, func(t *testing.T) {
			mismatch := c
			pin := *c.LegacyConnectorPin
			change(&pin)
			mismatch.LegacyConnectorPin = &pin
			// Refusal comes before the local package lookup, even if no file is available.
			folder := LocalPackageFolder
			LocalPackageFolder = ""
			t.Cleanup(func() { LocalPackageFolder = folder })
			if publish, err := installPinnedComfyPackage(ctx, mismatch, base, install); publish != nil || err == nil || errors.Is(err, errPackageUnavailable) || installCalls != 2 {
				t.Fatalf("mismatched pin: publish=%t, calls=%d, error=%v", publish != nil, installCalls, err)
			}
			if _, err := fixtureOfficialExtension().HoldConnector(ctx, mismatch, base); err == nil {
				t.Fatal("mismatched connector binding was accepted")
			}
			if digest, err := fixtureOfficialExtension().PackageDigestForConnector(mismatch, connectorDigest, int64(len(connector))); err == nil || digest != "" {
				t.Fatalf("mismatched pin used for legacy Undo: %q, %v", digest, err)
			}
		})
	}
}

func TestComfyReplacementPreservesTheSavedPackageUntilConfirmation(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	oldFolder := LocalPackageFolder
	t.Cleanup(func() { LocalPackageFolder = oldFolder })
	LocalPackageFolder = t.TempDir()
	const original, candidate = "original MSI", "candidate MSI"
	candidateSHA := sha256.Sum256([]byte(candidate))
	pin := &ConnectorPackagePin{File: "comfyui-0.8.0.msi", Bytes: int64(len(candidate)), SHA256: hex.EncodeToString(candidateSHA[:]),
		URL: "https://github.com/openabstractions/redist/releases/download/v0.8.0/comfyui-0.8.0.msi"}
	writeComfyFixture(t, filepath.Join(LocalPackageFolder, pin.File), candidate)
	current, err := fixtureOfficialExtension().packageFiles().current()
	if err != nil {
		t.Fatal(err)
	}
	writeComfyFixture(t, current, original)
	assertBytes := func(path, want string) {
		t.Helper()
		if raw, err := os.ReadFile(path); err != nil || string(raw) != want {
			t.Fatalf("%s: got %q, %v, want %q", filepath.Base(path), raw, err, want)
		}
	}
	ctx := context.Background()
	c := Catalog{LegacyConnectorPin: pin}
	failure := errors.New("replacement MSI refused")
	verifyStage := func(path, base string) {
		t.Helper()
		if path == current || filepath.Base(path) != filepath.Base(current) || base != "fixture target" {
			t.Fatalf("installer received %q for %q", path, base)
		}
		assertBytes(path, candidate)
		assertBytes(current, original)
		if runtime.GOOS == "windows" {
			if file, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
				_ = file.Close()
				t.Fatal("the staged package was openable for writing during installation")
			}
			if err := os.Rename(path, path+".moved"); err == nil {
				t.Fatal("the staged package could be renamed during installation")
			}
		}
	}
	originalSHA := sha256.Sum256([]byte(original))
	for attempt := 1; attempt <= 2; attempt++ {
		saved, digest, err := fixtureOfficialExtension().KeepPreviousPackage(ctx)
		if err != nil || digest != hex.EncodeToString(originalSHA[:]) {
			t.Fatalf("attempt %d backup digest %q: %v", attempt, digest, err)
		}
		publish, err := installPinnedComfyPackage(ctx, c, "fixture target", func(_ context.Context, path, base string) error {
			verifyStage(path, base)
			return failure
		})
		if publish != nil || !errors.Is(err, failure) {
			t.Fatalf("attempt %d returned publish=%t, err=%v", attempt, publish != nil, err)
		}
		assertBytes(current, original)
		assertBytes(saved, original)
	}
	publish, err := installPinnedComfyPackage(ctx, c, "fixture target", func(_ context.Context, path, base string) error {
		verifyStage(path, base)
		return nil
	})
	if err != nil || publish == nil {
		t.Fatalf("staged successful install: publish=%t, err=%v", publish != nil, err)
	}
	assertBytes(current, original)
	if err := publish(ctx); err != nil {
		t.Fatal(err)
	}
	assertBytes(current, candidate)
}

func TestComfyRetainsDistinctUndoPackageDigests(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	ctx := context.Background()
	current, err := fixtureOfficialExtension().packageFiles().current()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(current) != "comfyui-openabstractions.msi" {
		t.Fatalf("saved predecessor lookup changed: %s", current)
	}
	const original, next = "original MSI", "next MSI"
	writeComfyFixture(t, current, original)
	first, firstDigest, err := fixtureOfficialExtension().KeepPreviousPackage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(filepath.Dir(current), "comfyui-openabstractions-previous.msi")
	writeComfyFixture(t, legacy, original)
	writeComfyFixture(t, current, next)
	second, secondDigest, err := fixtureOfficialExtension().KeepPreviousPackage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	retry, retryDigest, err := fixtureOfficialExtension().KeepPreviousPackage(ctx)
	if err != nil || second != retry || secondDigest != retryDigest || first == second {
		t.Fatalf("retained package paths: first %q second %q retry %q (%v)", first, second, retry, err)
	}
	for path, want := range map[string]string{first: original, legacy: original, second: next} {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != want {
			t.Fatalf("retained %s: %q (%v), want %q", path, raw, err, want)
		}
	}
	for _, item := range []struct{ path, digest string }{{first, firstDigest}, {second, secondDigest}, {legacy, firstDigest}} {
		release, err := HoldSavedPackage(ctx, item.path, item.digest)
		if err != nil {
			t.Fatal(err)
		}
		if err := release(); err != nil {
			t.Fatal(err)
		}
	}
}

// Connect ends with the connector registering itself.
func TestConnectRegistersTheConnector(t *testing.T) {
	for output, want := range map[string][2]string{
		`{"outcome":"applied","revision":"r1"}` + "\ntrailing": {"applied", ""},
		`{"outcome":"conflict","reason":"exists"}`:             {"conflict", "exists"},
		`{"outcome":"forbidden","reason":"signed_source"}`:     {"forbidden", "signed_source"},
		"not json": {"", ""},
	} {
		if outcome, reason := extensionRegistrationAnswer([]byte(output)); outcome != want[0] || reason != want[1] {
			t.Errorf("%q: %q %q", output, outcome, reason)
		}
	}
	base := filepath.Join(t.TempDir(), "ComfyUI")
	writeComfyFixture(t, filepath.Join(base, "custom_nodes", "comfyui-openabstractions", "__init__.py"), "")
	writeComfyFixture(t, fixtureOfficialExtension().Connector(base), "")
	var registered []string
	previous := registerCompatibilityConnector
	registerCompatibilityConnector = func(_ context.Context, connector string, _ string) error {
		registered = append(registered, connector)
		return nil
	}
	t.Cleanup(func() { registerCompatibilityConnector = previous })
	if err := fixtureOfficialExtension().Reconnect(context.Background(), Catalog{}, ExtensionHost{Kind: ExtensionPortable, Base: base, Connected: true}); err != nil {
		t.Fatal(err)
	}
	if len(registered) != 1 || registered[0] != fixtureOfficialExtension().Connector(base) {
		t.Fatalf("registered %v", registered)
	}
}

// Only "applied" adds the connector. A conflict carries the service's reason
// so the Panel can replace an earlier connector, and never reads as success.
func TestConnectReportsARegistrationConflict(t *testing.T) {
	if err := fixtureOfficialExtension().RegistrationOutcome("applied", ""); err != nil {
		t.Fatal(err)
	}
	var conflict *ExtensionRegistrationConflict
	if err := fixtureOfficialExtension().RegistrationOutcome("conflict", "identity"); !errors.As(err, &conflict) || conflict.Reason != "identity" || err.Error() == "" {
		t.Fatalf("identity conflict: %v", err)
	}
	if err := fixtureOfficialExtension().RegistrationOutcome("forbidden", "signed_source"); err == nil || errors.As(err, &conflict) {
		t.Fatalf("forbidden: %v", err)
	}
	base := filepath.Join(t.TempDir(), "ComfyUI")
	writeComfyFixture(t, filepath.Join(base, "custom_nodes", "comfyui-openabstractions", "__init__.py"), "")
	writeComfyFixture(t, fixtureOfficialExtension().Connector(base), "")
	previous := registerCompatibilityConnector
	registerCompatibilityConnector = func(context.Context, string, string) error {
		return fixtureOfficialExtension().RegistrationOutcome("conflict", "identity")
	}
	t.Cleanup(func() { registerCompatibilityConnector = previous })
	if err := fixtureOfficialExtension().Reconnect(context.Background(), Catalog{}, ExtensionHost{Kind: ExtensionPortable, Base: base, Connected: true}); !errors.As(err, &conflict) {
		t.Fatalf("connect over an earlier connector: %v", err)
	}
}

// The Panel holds the connector against the pin the extension carries; other
// bytes, or a build pinning no connector, hold nothing.
func TestHoldComfyConnectorChecksItsPin(t *testing.T) {
	base := filepath.Join(t.TempDir(), "ComfyUI")
	body := "comfyui-connector 0.8.1"
	writeComfyFixture(t, fixtureOfficialExtension().Connector(base), body)
	sum := sha256.Sum256([]byte(body))
	c := Catalog{LegacyConnectorPin: &ConnectorPackagePin{ConnectorSHA256: hex.EncodeToString(sum[:]), ConnectorBytes: int64(len(body))}}
	held, err := fixtureOfficialExtension().HoldConnector(context.Background(), c, base)
	if err != nil || held.Path != fixtureOfficialExtension().Connector(base) {
		t.Fatal(held, err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixtureOfficialExtension().HoldConnector(context.Background(), Catalog{LegacyConnectorPin: &ConnectorPackagePin{}}, base); err == nil {
		t.Fatal("held without a connector pin")
	}
	writeComfyFixture(t, fixtureOfficialExtension().Connector(base), "comfyui-connector 0.8.X")
	if _, err := fixtureOfficialExtension().HoldConnector(context.Background(), c, base); err == nil {
		t.Fatal("held other bytes")
	}
}

// A connected ComfyUI's connector registers as it is only when it is the one
// this build pins, or the build pins none; otherwise Connect installs the
// pinned integration again first, so the connector that registers is the
// one the Panel's Connect click names.
func TestComfyConnectorPinnedComparesTheConnector(t *testing.T) {
	base := filepath.Join(t.TempDir(), "ComfyUI")
	writeComfyFixture(t, fixtureOfficialExtension().Connector(base), "comfyui-connector pinned")
	sum := sha256.Sum256([]byte("comfyui-connector pinned"))
	pinned := Catalog{LegacyConnectorPin: &ConnectorPackagePin{ConnectorSHA256: hex.EncodeToString(sum[:]), ConnectorBytes: int64(len("comfyui-connector pinned"))}}
	if !fixtureOfficialExtension().Pinned(context.Background(), Catalog{}, base) {
		t.Error("a build that pins no connector refused the connector on disk")
	}
	if !fixtureOfficialExtension().Pinned(context.Background(), pinned, base) {
		t.Error("the pinned connector reads as another")
	}
	writeComfyFixture(t, fixtureOfficialExtension().Connector(base), "comfyui-connector built elsewhere")
	if fixtureOfficialExtension().Pinned(context.Background(), pinned, base) {
		t.Error("another connector reads as the pinned one")
	}
}
