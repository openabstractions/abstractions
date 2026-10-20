package main

import (
	"os"
	"path/filepath"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// officialFixture stands in OA's compiled catalog, this account's folders and
// the signature check: the catalog names an official program for "example"
// below local app data with the signer "Example Corp", and signers maps each
// fixture program to the signer its file carries ("" for unsigned).
func officialFixture(t *testing.T, root string, signers map[string]string, catalog *setup.OfficialProgram) {
	t.Helper()
	savedFolders, savedCatalog, savedSigner := manifestAccountFolders, catalogOfficialProgram, officialSigner
	t.Cleanup(func() {
		manifestAccountFolders, catalogOfficialProgram, officialSigner = savedFolders, savedCatalog, savedSigner
	})
	manifestAccountFolders = func() (string, string, error) { return root, filepath.Join(root, "Pictures"), nil }
	catalogOfficialProgram = func(app setup.AppID) (setup.OfficialProgram, bool) {
		if app != "example" || catalog == nil {
			return setup.OfficialProgram{}, false
		}
		return *catalog, true
	}
	officialSigner = func(program string) (string, bool) {
		for path, signer := range signers {
			if identity.SameSubjectProgram(path, program) {
				return signer, signer != ""
			}
		}
		return "", false
	}
}

func fixtureProgram(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("MZ "+path), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The one exception to "the program lies inside {app}" is anchored in OA's
// compiled catalog, never in an install record any process of the account
// can write: a manifest may name the catalog's official program outside
// {app} only when the Panel itself verifies the catalog's signer on it. A
// forged manifest naming a script host such as PowerShell, which a
// verified publisher signs, is refused with or without any record; so are
// another program, the official program with another or no signature, an app
// the catalog names no program for, and a script host inside {app}.
func TestOnlyTheCatalogsOfficialProgramMayLieOutsideTheAppFolder(t *testing.T) {
	root := t.TempDir()
	official := fixtureProgram(t, filepath.Join(root, "Programs", "official", "Official.exe"))
	shell := fixtureProgram(t, filepath.Join(root, "Windows", "System32", "WindowsPowerShell", "v1.0", "powershell.exe"))
	node := fixtureProgram(t, filepath.Join(root, "Programs", "nodejs", "node.exe"))
	other := fixtureProgram(t, filepath.Join(root, "Programs", "other", "Other.exe"))
	appFolder := filepath.Join(root, "Programs", "OpenAbstractions", "apps", "example")
	manifest := filepath.Join(appFolder, "application-manifest.json")
	catalog := &setup.OfficialProgram{LocalAppData: "Programs/official/Official.exe", Signer: "Example Corp"}
	signed := map[string]string{official: "Example Corp", shell: "Microsoft Windows", node: "OpenJS Foundation", other: "Example Corp"}
	// A forged install record naming the script host changes nothing: the
	// Panel reads none for the program.
	saved := installedRecords
	t.Cleanup(func() { installedRecords = saved })
	installedRecords = func() map[string]string { return map[string]string{"example": manifest} }
	for _, test := range []struct {
		name, program string
		signers       map[string]string
		catalog       *setup.OfficialProgram
		accepted      bool
	}{
		{"the catalog's official program with its signer", "{local_app_data}/Programs/official/Official.exe", signed, catalog, true},
		{"the same program as an absolute path", official, signed, catalog, true},
		{"a forged manifest naming PowerShell", shell, signed, catalog, false},
		{"a forged manifest naming Node", node, signed, catalog, false},
		{"a catalog naming a script host", shell, signed, &setup.OfficialProgram{LocalAppData: "Windows/System32/WindowsPowerShell/v1.0/powershell.exe", Signer: "Microsoft Windows"}, false},
		{"another program the same publisher signs", other, signed, catalog, false},
		{"the official program with another signer", official, map[string]string{official: "Someone Else"}, catalog, false},
		{"the official program unsigned", official, map[string]string{official: ""}, catalog, false},
		{"an app the catalog names no program for", official, signed, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			officialFixture(t, root, test.signers, test.catalog)
			if err := os.MkdirAll(appFolder, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifest, []byte(manifestText(test.program, "Example")), 0o600); err != nil {
				t.Fatal(err)
			}
			descriptor, err := readApplicationManifest(manifest)
			if test.accepted {
				if err != nil || !identity.SameSubjectProgram(descriptor.Program, official) {
					t.Fatalf("refused the official program: %+v %v", descriptor, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted %s", descriptor.Program)
			}
		})
	}
	// A script host is never an app's program, inside {app} as well; an
	// ordinary program inside {app} needs no catalog entry.
	officialFixture(t, root, signed, nil)
	for program, accepted := range map[string]bool{
		fixtureProgram(t, filepath.Join(appFolder, "bin", "powershell.exe")): false,
		fixtureProgram(t, filepath.Join(appFolder, "bin", "example.exe")):    true,
	} {
		if err := os.WriteFile(manifest, []byte(manifestText(program, "Example")), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readApplicationManifest(manifest); (err == nil) != accepted {
			t.Fatalf("%s inside the app folder: %v", program, err)
		}
	}
}

// The OpenCode integration's actual manifest, in its own install root, names
// the official Desktop program outside {app}; the Panel reader accepts it
// through the real compiled catalog when the file carries the catalog's
// signer, and refuses it with another signer or none.
func TestOpenCodeInstallerManifestThroughActualPanelReader(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "adopters", "opencode", "installer", "application-manifest.json"))
	if os.IsNotExist(err) {
		t.Skip("no OpenCode installer source in this tree")
	}
	if err != nil {
		t.Fatal(err)
	}
	official, ok := setup.OfficialProgramOf(setup.OpenCode)
	if !ok {
		t.Fatal("the catalog names no official OpenCode program")
	}
	root := t.TempDir()
	desktop := fixtureProgram(t, filepath.Join(root, filepath.FromSlash(official.LocalAppData)))
	integration := filepath.Join(root, "Programs", "OpenAbstractions", "apps", "opencode")
	if err := os.MkdirAll(integration, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(integration, "openabstractions-application.json")
	if err := os.WriteFile(manifest, source, 0o600); err != nil {
		t.Fatal(err)
	}
	savedFolders, savedSigner := manifestAccountFolders, officialSigner
	t.Cleanup(func() { manifestAccountFolders, officialSigner = savedFolders, savedSigner })
	manifestAccountFolders = func() (string, string, error) { return root, filepath.Join(root, "Pictures"), nil }
	for signer, accepted := range map[string]bool{official.Signer: true, "Someone Else": false, "": false} {
		officialSigner = func(program string) (string, bool) {
			if !identity.SameSubjectProgram(program, desktop) {
				return "", false
			}
			return signer, signer != ""
		}
		descriptor, err := readApplicationManifest(manifest)
		if accepted {
			if err != nil || descriptor.Name != "opencode" || !identity.SameSubjectProgram(descriptor.Program, desktop) {
				t.Fatalf("the signed official Desktop: %+v %v", descriptor, err)
			}
		} else if err == nil {
			t.Fatalf("accepted the Desktop signed by %q", signer)
		}
	}
}

// The compiled catalog names the official program only for an integrated app
// that has one, below local app data, with a signer.
func TestTheCatalogNamesOfficialProgramsOnlyWithASigner(t *testing.T) {
	for _, choice := range setup.NewCatalog().Choices {
		official, ok := setup.OfficialProgramOf(choice.ID)
		if !ok {
			continue
		}
		if !choice.Application || official.Signer == "" || official.LocalAppData == "" || filepath.IsAbs(official.LocalAppData) ||
			identity.SharedInterpreterFileName(official.LocalAppData) {
			t.Fatalf("%s: official program %+v", choice.ID, official)
		}
	}
}
