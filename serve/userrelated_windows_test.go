package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestUserInstallFolderRefusesRootReparsePoint(t *testing.T) {
	base := t.TempDir()
	ordinary := filepath.Join(base, "ordinary")
	if err := os.Mkdir(ordinary, 0700); err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{ordinary, filepath.Join(base, "incoming")} {
		if _, err := userInstallFolder(folder + `\.`); err != nil {
			t.Fatalf("ordinary or incoming folder refused: %v", err)
		}
	}
	link := filepath.Join(base, "linked")
	junction(t, link, ordinary)
	if _, err := userInstallFolder(link + `\.`); err == nil || !strings.Contains(err.Error(), "reparse point; choose a regular local folder in the installer") {
		t.Fatalf("root reparse accepted: %v", err)
	}
}

func TestInstalledCoreFoldersResolveCustomAndNoProduct(t *testing.T) {
	canonical := filepath.Join(t.TempDir(), "OpenAbstractions")
	custom := filepath.Join(t.TempDir(), "custom installed core")
	code := "{11111111-2222-3333-4444-555555555555}"
	info := func(actual, property string) (string, error) {
		if actual != code {
			t.Fatalf("unexpected ProductCode %s", actual)
		}
		if property == "VersionString" {
			return "0.8.0", nil
		}
		if property == "InstallLocation" {
			return custom, nil
		}
		t.Fatalf("unexpected property %s", property)
		return "", nil
	}
	folders, notes, err := installedCoreFolders(info, func() ([]string, error) { return []string{code}, nil }, func() (string, error) { return canonical, nil })
	if err != nil || len(notes) != 0 || len(folders) != 2 || folders[0] != canonical || folders[1] != custom {
		t.Fatalf("folders=%q notes=%q err=%v", folders, notes, err)
	}
	folders, _, err = installedCoreFolders(func(string, string) (string, error) { t.Fatal("no-product read registration"); return "", nil }, func() ([]string, error) { return nil, nil }, func() (string, error) { t.Fatal("no-product resolved folder"); return "", nil })
	if err != nil || len(folders) != 0 {
		t.Fatalf("no-product folders=%q err=%v", folders, err)
	}
}

const (
	codeA = "{2692D2EB-EC1F-443C-BC91-4C654895184C}"
	codeB = "{A009F737-A7FA-48D8-8C0A-CEB4DAB49166}"
)

func TestInstalledCoreFoldersRefuseMixedMachineAndUserRegistration(t *testing.T) {
	info := func(code, property string) (string, error) {
		if code == codeB {
			if property != "VersionString" {
				t.Fatalf("machine lookup continued after rejecting per-user product: %s", property)
			}
			return "", errorUnknownProduct
		}
		if code != codeA {
			t.Fatalf("unexpected machine product %s", code)
		}
		if property == "VersionString" {
			return "0.8.0", nil
		}
		return testFolder, nil
	}
	folders, notes, err := installedCoreFolders(info, func() ([]string, error) {
		return []string{codeA, codeB}, nil
	}, func() (string, error) {
		t.Fatal("mixed registration cannot authorize even the canonical folder")
		return "", nil
	})
	if len(folders) != 0 || len(notes) != 0 || err == nil ||
		!strings.Contains(err.Error(), codeB+" is not installed for this account") {
		t.Fatalf("mixed registration folders=%q notes=%q err=%v", folders, notes, err)
	}
}

func TestRemovalExclusionUsesOwnFolderWithoutProductRegistration(t *testing.T) {
	for _, scope := range []string{"user", "machine"} {
		request := serviceRequest{command: "begin-removal", scope: scope, folder: testFolder + `\.`}
		info := func(string, string) (string, error) {
			t.Fatal("removal queried MSI registration inside its transaction")
			return "", errorUnknownProduct
		}
		folders, _, notes, err := serviceExclusionFolders(request, info)
		if err != nil || len(folders) != 1 || folders[0] != testFolder || len(notes) != 0 {
			t.Fatalf("%s: folders=%v notes=%v err=%v", scope, folders, notes, err)
		}
		for _, invalid := range []string{`relative`, `C:\`, `C:\bad"quote`} {
			request.folder = invalid
			if _, _, _, err := serviceExclusionFolders(request, info); err == nil {
				t.Fatalf("%s removal accepted folder %q", scope, invalid)
			}
		}
	}
}

func TestUpgradeExclusionStillRefusesUnknownRelatedProduct(t *testing.T) {
	for _, scope := range []string{"user", "machine"} {
		request := serviceRequest{command: "begin-upgrade", scope: scope, folder: testFolder, related: codeA}
		calls := 0
		info := func(code, property string) (string, error) {
			calls++
			if code != codeA || property != "VersionString" {
				t.Fatalf("unexpected registration query %s %s", code, property)
			}
			return "", errorUnknownProduct
		}
		folders, _, _, err := serviceExclusionFolders(request, info)
		if err == nil || !strings.Contains(err.Error(), "is not installed") || len(folders) != 0 || calls != 1 {
			t.Fatalf("%s: folders=%v calls=%d err=%v", scope, folders, calls, err)
		}
	}
}

func fakeProductInfo(values map[string]map[string]string, failures map[string]error) productInfoFunc {
	return func(code, property string) (string, error) {
		if err, ok := failures[code+"/"+property]; ok {
			return "", err
		}
		product, ok := values[code]
		if !ok {
			return "", errorUnknownProduct
		}
		value, ok := product[property]
		if !ok {
			return "", errorUnknownProperty
		}
		return value, nil
	}
}

func TestRelatedProductCodes(t *testing.T) {
	codes, err := relatedProductCodes(strings.ToLower(codeA) + ";" + codeB + ";" + codeA)
	if err != nil || len(codes) != 2 || codes[0] != codeA || codes[1] != codeB {
		t.Fatalf("codes=%v err=%v", codes, err)
	}
	for _, bad := range []string{"", ";", codeA + ";", "[WIX_UPGRADE_DETECTED]", strings.Trim(codeA, "{}"), codeA + " ;" + codeB, `"` + codeA + `"`} {
		if _, err := relatedProductCodes(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestRelatedProductsReadRegistration(t *testing.T) {
	values := map[string]map[string]string{
		codeA: {"VersionString": "0.1.5", "InstallLocation": `D:\Tools\OpenAbstractions\`},
		codeB: {"VersionString": "0.1.6"},
	}
	products, err := relatedProducts(codeA+";"+codeB, fakeProductInfo(values, nil))
	if err != nil || len(products) != 2 {
		t.Fatalf("products=%v err=%v", products, err)
	}
	if products[0].location != `D:\Tools\OpenAbstractions\` || products[0].version != "0.1.5" || products[1].location != "" {
		t.Fatalf("products=%+v", products)
	}
	if _, err := relatedProducts("{00000000-0000-0000-0000-000000000001}", fakeProductInfo(values, nil)); err == nil || !strings.Contains(err.Error(), "not installed for this account") {
		t.Fatalf("unregistered related product accepted: %v", err)
	}
	denied := map[string]error{codeA + "/InstallLocation": syscall.Errno(5)}
	if _, err := relatedProducts(codeA, fakeProductInfo(values, denied)); err == nil || !errors.Is(err, syscall.Errno(5)) {
		t.Fatalf("registration read failure hidden: %v", err)
	}
}

func TestUpgradeFoldersIncludeRecordedLocations(t *testing.T) {
	related := []relatedProduct{
		{code: codeA, version: "0.1.5", location: `D:\Tools\OpenAbstractions\`},
		{code: codeB, version: "0.1.6", location: testFolder + `\`},
		{code: "{00000000-0000-0000-0000-000000000002}", version: "0.1.4"},
	}
	folders, notes, err := upgradeFolders(testFolder+`\.`, related)
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 2 || !strings.EqualFold(folders[0], testFolder) || !strings.EqualFold(folders[1], `D:\Tools\OpenAbstractions`) {
		t.Fatalf("folders=%v", folders)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "records no install location") {
		t.Fatalf("notes=%v", notes)
	}
	if _, _, err := upgradeFolders(testFolder, []relatedProduct{{code: codeA, location: `relative\folder`}}); err == nil {
		t.Fatal("relative recorded location accepted")
	}
	if _, _, err := upgradeFolders(testFolder, []relatedProduct{{code: codeA, location: `D:\`}}); err == nil {
		t.Fatal("volume root recorded location accepted")
	}
}

// The binding reaches msi.dll and reports an unregistered product without
// changing anything.
func TestMsiProductInfoReportsUnknownProduct(t *testing.T) {
	_, err := msiProductInfo("{00000000-0000-0000-0000-00000000F00D}", "VersionString")
	if !errors.Is(err, errorUnknownProduct) {
		t.Fatalf("err=%v, want ERROR_UNKNOWN_PRODUCT", err)
	}
}
