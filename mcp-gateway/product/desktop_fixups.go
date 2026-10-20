package product

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// DesktopFixup declares passive file and installer identity facts for a
// shipped desktop fix. Setup owns observation and verified MSI repair.
type DesktopFixup struct {
	// File is relative to the official program's folder. Package is
	// relative to the account's local application data, and PackageFile
	// names the verified replacement inside that package.
	File              string
	Stock             []string
	Fixed             []string
	ProgramVersions   []string `json:"Versions"`
	ProgramSHA256     map[string]string
	Package           string
	PackageFile       string
	Purpose           string
	RepairUpgradeCode string
}

//go:embed installer_fixups.json
var desktopFixupDeclarations []byte

var desktopFixups = func() map[string]DesktopFixup {
	var declarations map[string]DesktopFixup
	if err := json.Unmarshal(desktopFixupDeclarations, &declarations); err != nil {
		panic(fmt.Errorf("decode compiled desktop installer fixups: %w", err))
	}
	return declarations
}()

// DesktopInstallerFixup returns independent observation facts for a compiled
// official Catalog component.
func DesktopInstallerFixup(component string) (DesktopFixup, bool) {
	fixup, found := desktopFixups[component]
	fixup.Stock = slices.Clone(fixup.Stock)
	fixup.Fixed = slices.Clone(fixup.Fixed)
	fixup.ProgramVersions = slices.Clone(fixup.ProgramVersions)
	fixup.ProgramSHA256 = maps.Clone(fixup.ProgramSHA256)
	return fixup, found
}
