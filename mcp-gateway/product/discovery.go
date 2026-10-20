package product

import (
	_ "embed"
	"slices"
)

// DiscoveryCatalog is the release projection of extension-owned discovery
// declarations. Setup admits each declaration before it reads a record.
func DiscoveryCatalog() string { return discoveryCatalog }

//go:embed extension_discovery.json
var discoveryCatalog string

type extensionSetupFacts struct {
	PackageID              string
	InstalledKind          string
	HostSubfolder          string
	InstallFolder          string
	ConnectorName          string
	HostKey                string
	UpgradeCode            string
	DesktopProgram         string
	PreferredSignedProgram string
	SignedScanFirstFolder  string
	SignedScanName         string
	LocalInstalls          string
	SettingsPaths          []string
	SettingsKeys           []string
	IntegrationMarker      string
	ProcessFolders         []string
	InstallBase            string
	PortableLaunchers      []string
}

// ExtensionSetupFacts returns the installer-owned host and layout facts that Setup reads.
func ExtensionSetupFacts() extensionSetupFacts {
	facts := extensionSetup
	facts.SettingsPaths = slices.Clone(facts.SettingsPaths)
	facts.SettingsKeys = slices.Clone(facts.SettingsKeys)
	facts.ProcessFolders = slices.Clone(facts.ProcessFolders)
	facts.PortableLaunchers = slices.Clone(facts.PortableLaunchers)
	return facts
}
