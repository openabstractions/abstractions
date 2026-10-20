package product

// LegacyPackageSpec is passive Setup compatibility for one compiled adopter.
// FolderRecord is a readback locator. Release pins still require package admission.
type LegacyPackageSpec struct {
	RetireAfter, Order, ConnectStep, MaintenanceMutex, FolderRecord string
	Routes                                                          LegacyPackageRoutes
	Display                                                         LegacyPackageDisplay
	Copy                                                            LegacyPackageCopy
	Pins                                                            LegacyPackagePins
}

// LegacyPackageRoutes maps Setup's closed operation IDs to their historical paths.
type LegacyPackageRoutes struct {
	Status, Register, Activate, Remove, Connect, Open, Folder, RemoveRetained string
}

// LegacyPackageDisplay is plain Catalog card copy for a historical adapter.
type LegacyPackageDisplay struct {
	Publisher, License, Source, Finish, RetainedTitle, RetainedRemovalTitle string
}

// LegacyPackageCopy is plain text already presented by Setup's compatibility path.
type LegacyPackageCopy struct {
	HostMissing, FolderInvalid, FolderGone, RetainedNotInstalled, PackageUnavailable, StartFromMenu string
}

// LegacyPackagePins references the existing release-extension digest fields.
type LegacyPackagePins struct {
	PackageSHA256Ref, ConnectorSHA256Ref string
}

// LegacyPackageDeclaration reads the compiled compatibility for an exact package.
func LegacyPackageDeclaration(id string) (LegacyPackageSpec, bool) {
	spec, ok := legacyPackageSpecs[id]
	return spec, ok
}
