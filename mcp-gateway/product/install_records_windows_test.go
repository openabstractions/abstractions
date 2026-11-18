package product

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// The tools folder of every OpenAbstractions installation Windows Installer
// records is listed, whatever the environment says: Windows Installer keeps
// InstallLocation, which APPLICATIONFOLDER may put anywhere. This test reads
// this machine's records and only reads them. An installed Panel in a
// recorded folder is among InstalledToolFolders, which the Panel's guard
// reads, under the forged per-user folders this package's tests run with.
func TestRegisteredToolFoldersReadTheInstallerRecords(t *testing.T) {
	registered, err := RegisteredToolFolders()
	if err != nil {
		t.Fatal(err)
	}
	all, err := InstalledToolFolders()
	if err != nil {
		t.Fatal(err)
	}
	for _, folder := range registered {
		if !filepath.IsAbs(folder) || !strings.EqualFold(filepath.Base(folder), "tools") {
			t.Errorf("registered folder %q is no absolute tools folder", folder)
		}
		if !slices.Contains(all, folder) {
			t.Errorf("install folders %q miss the registered %q", all, folder)
		}
		if _, err := os.Stat(filepath.Join(folder, "Abstraction Panel.exe")); err == nil {
			t.Logf("registered installation with a Panel: %s", folder)
		}
	}
	if len(registered) == 0 {
		t.Log("Windows Installer records no OpenAbstractions installation here")
	}
}

// recordsFixture answers Windows Installer reads from a table: products by
// index, and InstallLocation by product and context. A context a product has
// no row for answers ERROR_UNKNOWN_PRODUCT.
func recordsFixture(products []string, locations map[string]map[uint32]any) installQueries {
	return installQueries{
		related: func(upgrade string, index uint32) (string, error) {
			if upgrade != coreUpgradeCode {
				return "", errorUnknownProduct
			}
			if int(index) >= len(products) {
				return "", windows.ERROR_NO_MORE_ITEMS
			}
			return products[index], nil
		},
		property: func(product string, context uint32, property string) (string, error) {
			if property != "InstallLocation" {
				return "", errorUnknownProperty
			}
			switch answer := locations[product][context].(type) {
			case string:
				return answer, nil
			case error:
				return "", answer
			}
			return "", errorUnknownProduct
		},
	}
}

func replaceInstallRecords(t *testing.T, q installQueries) {
	saved := installRecords
	installRecords = q
	t.Cleanup(func() { installRecords = saved })
}

// An installation APPLICATIONFOLDER put outside ProgramFiles and the profile
// is among InstalledToolFolders, the list the Panel's guard reads, in
// whichever context Windows Installer records it. Relative and empty
// locations add nothing.
func TestInstalledToolFoldersListAnInstallationMovedByApplicationFolder(t *testing.T) {
	machine := `{11111111-1111-1111-1111-111111111111}`
	user := `{22222222-2222-2222-2222-222222222222}`
	odd := `{33333333-3333-3333-3333-333333333333}`
	replaceInstallRecords(t, recordsFixture([]string{machine, user, odd}, map[string]map[uint32]any{
		machine: {4: `D:\Elsewhere\OpenAbstractions\`},
		user:    {1: errorUnknownProperty, 2: `E:\Apps\OA`},
		odd:     {2: `relative\OA`, 4: ""},
	}))
	folders, err := InstalledToolFolders()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`D:\Elsewhere\OpenAbstractions\tools`, `E:\Apps\OA\tools`} {
		if !slices.Contains(folders, want) {
			t.Errorf("install folders %q miss the recorded %q", folders, want)
		}
	}
	for _, folder := range folders {
		if folder != "" && !filepath.IsAbs(folder) {
			t.Errorf("install folders list the relative %q", folder)
		}
	}
}

// A record that cannot be read is an error, which the guard reads as guard,
// and every folder read before it stays listed. More related products than
// are read is an error too.
func TestRegisteredToolFoldersReportUnreadRecords(t *testing.T) {
	first := `{11111111-1111-1111-1111-111111111111}`
	broken := `{22222222-2222-2222-2222-222222222222}`
	replaceInstallRecords(t, recordsFixture([]string{first, broken}, map[string]map[uint32]any{
		first:  {4: `D:\Elsewhere\OpenAbstractions`},
		broken: {2: syscall.Errno(1610)},
	}))
	folders, err := RegisteredToolFolders()
	if err == nil {
		t.Fatal("an unreadable record read as no error")
	}
	if !slices.Contains(folders, `D:\Elsewhere\OpenAbstractions\tools`) {
		t.Errorf("folders %q dropped the record read before the error", folders)
	}
	if _, err := InstalledToolFolders(); err == nil {
		t.Error("InstalledToolFolders hid the unreadable record")
	}
	many := make([]string, maxCoreProducts+1)
	for i := range many {
		many[i] = first
	}
	replaceInstallRecords(t, recordsFixture(many, nil))
	if _, err := RegisteredToolFolders(); err == nil {
		t.Errorf("%d related products read as all of them", len(many))
	}
}
