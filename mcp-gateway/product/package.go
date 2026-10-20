package product

// PackageSpec is passive package@1 data. The digest references identify fields
// of the already admitted release asset row. This data does not admit a row.
type PackageSpec struct {
	ManifestVersion    int
	ID                 string
	Title              string
	Platform           string
	ConnectorDigestRef string
	ArtifactID         string
	ArtifactDigestRef  string
	Rollback           string
	Connector          string
	Registration       string
	HostSubfolder      string
	InstallFolder      string
	HostKey            string
	UpgradeCode        string
	// RetainedArchiveComponent identifies the earlier archive in Setup journals.
	RetainedArchiveComponent string
	RetainedArchiveLayout    retainedArchiveLayout
}

type retainedArchiveLayout struct {
	DataRoot        string
	User            string
	Input           string
	Temp            string
	ModelFile       string
	ModelGroup      string
	ModelCategories [3]string
	NativeRoot      string
	PortableRoot    string
	ImageRecipe     string
	ImagePolicy     string
}

// PackageDeclaration looks up the compiled package declaration by Catalogue ID.
func PackageDeclaration(id string) (PackageSpec, bool) {
	spec, ok := packageSpecs[id]
	return spec, ok
}

// RetainedArchiveDeclaration selects the compiled owner of Setup's historical
// archive slot. More than one declaration would make that slot ambiguous.
func RetainedArchiveDeclaration() (PackageSpec, bool) {
	var owner PackageSpec
	for _, spec := range packageSpecs {
		if spec.RetainedArchiveComponent == "" {
			continue
		}
		if owner.ID != "" {
			return PackageSpec{}, false
		}
		owner = spec
	}
	return owner, owner.ID != ""
}
