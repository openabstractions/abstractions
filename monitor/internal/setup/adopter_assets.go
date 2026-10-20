package setup

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

//go:embed adopter_assets.json
var adopterAssetsJSON []byte

const maxReleaseAssetBytes = 2 * 1024 * 1024 * 1024

type adopterAssetPins struct {
	Schema     int               `json:"schema"`
	ReleaseTag string            `json:"release_tag"`
	Apps       []adopterAssetPin `json:"apps"`
	// Extensions are the per-user packages that integrate official apps.
	Extensions         []adopterExtensionPin `json:"extensions,omitempty"`
	desktopIntegration *DesktopIntegration
	connectorPackages  map[string]admittedPackage
	// ReleaseAdmission is passive release metadata. The release facts owner
	// validates it; Setup derives its catalog exclusively from the app pins.
	ReleaseAdmission json.RawMessage `json:"release_admission,omitempty"`
}

type adopterAssetPin struct {
	Variant             string                  `json:"variant,omitempty"`
	CorrespondingSource *adopterSourceOffer     `json:"corresponding_source,omitempty"`
	Backend             *adopterBackendResource `json:"backend,omitempty"`
	ID                  string                  `json:"id"`
	BinaryZIP           string                  `json:"binary_zip"`
	BinarySHA256        string                  `json:"binary_sha256"`
	BinaryBytes         int64                   `json:"binary_bytes"`
	SourceZIP           string                  `json:"source_zip"`
	SourceSHA256        string                  `json:"source_sha256"`
	SourceBytes         int64                   `json:"source_bytes"`
	SourceCommit        string                  `json:"source_commit"`
	SourceURL           string                  `json:"source_url"`
	Signer              string                  `json:"signer"`
	Version             string                  `json:"version"`
	Platform            string                  `json:"platform"`
	PeakBytes           int64                   `json:"peak_bytes"`
	License             string                  `json:"license"`
	Publisher           string                  `json:"publisher"`
	InstallIdentity     string                  `json:"install_identity"`
	Action              string                  `json:"action"`
	ApplicationProgram  string                  `json:"application_program,omitempty"`
	ServiceProgram      string                  `json:"service_program,omitempty"`
	ArchiveToolProgram  string                  `json:"archive_tool_program,omitempty"`
	RecordBytes         int64                   `json:"record_bytes,omitempty"`
	MarkerBytes         int64                   `json:"marker_bytes,omitempty"`
	RecordRoots         []string                `json:"record_roots,omitempty"`
	// RecordsSHA256 pins an archive's Python RECORD set.
	RecordsSHA256 string `json:"records_sha256,omitempty"`
}

// adopterExtensionPin is the shared release document for per-user MSIs.
// ProductCode selects Windows Installer registration. ConnectorSHA256 pins
// a connector installed into an existing desktop app's extension folder.
type adopterExtensionPin struct {
	ID              string `json:"id"`
	File            string `json:"file"`
	SHA256          string `json:"sha256"`
	Bytes           int64  `json:"bytes"`
	Version         string `json:"version"`
	ProductCode     string `json:"product_code,omitempty"`
	PackageCode     string `json:"package_code,omitempty"`
	UpgradeCode     string `json:"upgrade_code,omitempty"`
	ConnectorSHA256 string `json:"connector_sha256,omitempty"`
	ConnectorBytes  int64  `json:"connector_bytes,omitempty"`
}

// admittedPackage binds the compiled declaration and exact pins from one release.
type admittedPackage struct {
	spec       product.PackageSpec
	pin        adopterExtensionPin
	releaseTag string
}

func (pins adopterAssetPins) releasePackage(id, goos, goarch string) (admittedPackage, bool) {
	entry, ok := pins.connectorPackages[id]
	if !ok || entry.spec.Platform != goos+"/"+goarch {
		return admittedPackage{}, false
	}
	return entry, true
}

var (
	assetNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.zip$`)
	assetSHA         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	assetCommit      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	assetTag         = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	assetIdentity    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	extensionFile    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.msi$`)
)

func exactJSONFields(value any) map[string]bool {
	fields := map[string]bool{}
	typeOf := reflect.TypeOf(value)
	for i := 0; i < typeOf.NumField(); i++ {
		name, _, _ := strings.Cut(typeOf.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			fields[name] = true
		}
	}
	return fields
}

func checkJSONKeys(dec *json.Decoder, allowed map[string]bool) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := make(map[string]bool)
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] || (allowed != nil && !allowed[key]) {
				return fmt.Errorf("duplicate or unknown JSON key %v", keyToken)
			}
			seen[key] = true
			var childKeys map[string]bool
			if allowed != nil {
				switch key {
				case "apps":
					childKeys = exactJSONFields(adopterAssetPin{})
				case "extensions":
					childKeys = exactJSONFields(adopterExtensionPin{})
				case "corresponding_source":
					childKeys = exactJSONFields(adopterSourceOffer{})
					if allowed["comfyui_ref"] {
						childKeys = exactJSONFields(adopterArchiveSet{})
					}
				case "archive":
					childKeys = exactJSONFields(adopterArchiveSet{})
				case "parts":
					childKeys = exactJSONFields(adopterArchivePin{})
				case "data_release":
					childKeys = exactJSONFields(adopterDataRelease{})
				case "backend":
					childKeys = exactJSONFields(adopterBackendResource{})
				case "provenance":
					childKeys = exactJSONFields(adopterBackendProvenance{})
				case "producer_receipt", "input_descriptor", "manifest", "backend_policy":
					childKeys = exactJSONFields(adopterArchivePin{})
				}
			}
			if err := checkJSONKeys(dec, childKeys); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case json.Delim('['):
		for dec.More() {
			if err := checkJSONKeys(dec, allowed); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	default:
		return nil
	}
}

// The shared archive's release row names the engine choice, while Setup
// installs its single compiled component. Keep both lookups on that choice.
func sharedAdopterArchiveChoice() (Choice, bool) {
	for _, choice := range compiledCatalog().Choices {
		if choice.ID == PyTorch && !choice.Application && len(choice.Components) == 1 {
			return choice, true
		}
	}
	return Choice{}, false
}

func parseAdopterAssets(data []byte) (adopterAssetPins, error) {
	return parseAdopterAssetsWithPackages(data, product.PackageDeclaration)
}

func parseAdopterAssetsWithPackages(data []byte, declaration func(string) (product.PackageSpec, bool)) (adopterAssetPins, error) {
	keys := json.NewDecoder(bytes.NewReader(data))
	if err := checkJSONKeys(keys, exactJSONFields(adopterAssetPins{})); err != nil {
		return adopterAssetPins{}, err
	}
	if _, err := keys.Token(); !errors.Is(err, io.EOF) {
		return adopterAssetPins{}, errors.New("unexpected JSON content after app pins")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var pins adopterAssetPins
	if err := dec.Decode(&pins); err != nil {
		return pins, err
	}
	if (pins.Schema != 1 && pins.Schema != 2) || !assetTag.MatchString(pins.ReleaseTag) || pins.Apps == nil {
		return pins, errors.New("invalid app pin schema, release tag or apps list")
	}
	var raw struct {
		Apps []map[string]json.RawMessage `json:"apps"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return pins, err
	}
	archiveChoice, hasArchive := sharedAdopterArchiveChoice()
	for index, row := range raw.Apps {
		// The shared engine keeps the schema 1 archive shape in either
		// release document (architect ruling 2026-11-08).
		shared := hasArchive && pins.Apps[index].ID == string(archiveChoice.ID)
		if pins.Schema == 2 && !shared {
			for _, key := range []string{"source_zip", "source_sha256", "source_bytes", "signer", "action", "install_identity", "records_sha256"} {
				if pins.Apps[index].ApplicationProgram != "" && (key == "action" || key == "install_identity") {
					continue
				}
				if _, ok := row[key]; ok {
					return pins, errors.New("portable field in native Desktop row")
				}
			}
		} else {
			for _, key := range []string{"variant", "corresponding_source", "backend"} {
				if _, ok := row[key]; ok {
					return pins, errors.New("native field in schema 1 row")
				}
			}
			for _, key := range []string{"application_program", "service_program", "archive_tool_program", "record_bytes", "record_roots", "marker_bytes"} {
				if _, ok := row[key]; ok {
					return pins, errors.New("native program field in schema 1 row")
				}
			}
		}
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, app := range pins.Apps {
		shared := hasArchive && app.ID == string(archiveChoice.ID)
		if (!isLegacyConnectorExtension(app.ID) && !shared) || ids[app.ID] {
			return pins, fmt.Errorf("unknown or duplicate adopter app %q", app.ID)
		}
		ids[app.ID] = true
		if pins.Schema == 1 && !shared {
			return pins, fmt.Errorf("%s requires the native application row", ApplicationName(AppID(app.ID)))
		}
		if pins.Schema == 2 && !shared {
			if err := app.validateDesktop(names); err != nil {
				return pins, err
			}
			continue
		}
		// Only the shared component reaches archive validation. Desktop
		// rows have either been validated above or refused.
		if app.Variant != "" || app.CorrespondingSource != nil || app.Backend != nil {
			return pins, errors.New("schema 1 cannot carry native Desktop metadata")
		}
		for _, name := range []string{app.BinaryZIP, app.SourceZIP} {
			if !assetNamePattern.MatchString(name) || names[name] {
				return pins, fmt.Errorf("invalid or duplicate adopter asset %q", name)
			}
			names[name] = true
		}
		if !assetSHA.MatchString(app.BinarySHA256) || !assetSHA.MatchString(app.SourceSHA256) ||
			app.BinaryBytes <= 0 || app.BinaryBytes >= maxReleaseAssetBytes ||
			app.SourceBytes <= 0 || app.SourceBytes >= maxReleaseAssetBytes ||
			!assetCommit.MatchString(app.SourceCommit) || app.Signer != "unsigned" ||
			app.Platform != "windows/amd64" || app.PeakBytes < app.BinaryBytes ||
			app.License == "" || app.Publisher == "" || app.Action != "archive_per_user" ||
			app.Version == "" || !assetIdentity.MatchString(app.InstallIdentity) || app.RecordsSHA256 != "" {
			return pins, fmt.Errorf("invalid pin metadata for %q", app.ID)
		}
		artifact := app.artifact(pins.ReleaseTag)
		if err := artifact.validate(); err != nil {
			return pins, fmt.Errorf("%s: %w", app.ID, err)
		}
	}
	extensions := map[string]bool{}
	var packages map[string]admittedPackage
	for _, extension := range pins.Extensions {
		if extensions[extension.ID] {
			return pins, fmt.Errorf("unexpected or duplicate extension %q", extension.ID)
		}
		if extension.hasProductIdentity() {
			if names[extension.File] {
				return pins, fmt.Errorf("duplicate extension filename %q", extension.File)
			}
			asset, err := extension.desktopPackage(pins.Schema, pins.ReleaseTag)
			if err != nil {
				return pins, err
			}
			if pins.desktopIntegration != nil {
				return pins, errors.New("the release has multiple desktop integration packages")
			}
			pins.desktopIntegration = asset
			names[extension.File] = true
		} else {
			spec, ok := declaration(extension.ID)
			member := ok && spec.ID == extension.ID && spec.ArtifactID == extension.ID
			if !isLegacyConnectorExtension(extension.ID) && (!member || extension.ConnectorSHA256 == "") {
				return pins, fmt.Errorf("extension %q requires a declared connector package with a connector digest", extension.ID)
			}
			if err := extension.validateConnector(names); err != nil {
				return pins, err
			}
			if member && extension.ConnectorSHA256 != "" {
				if packages == nil {
					packages = make(map[string]admittedPackage)
				}
				packages[extension.ID] = admittedPackage{spec: spec, pin: extension, releaseTag: pins.ReleaseTag}
			}
		}
		extensions[extension.ID] = true
	}
	pins.connectorPackages = packages
	return pins, nil
}

func (p adopterExtensionPin) validateConnector(names map[string]bool) error {
	if !extensionFile.MatchString(p.File) || names[p.File] || !assetSHA.MatchString(p.SHA256) ||
		p.Bytes <= 0 || p.Bytes >= 64<<20 || p.Version == "" ||
		(p.ConnectorSHA256 != "" || p.ConnectorBytes != 0) && (!assetSHA.MatchString(p.ConnectorSHA256) || p.ConnectorBytes <= 0 || p.ConnectorBytes >= 64<<20) {
		return fmt.Errorf("invalid extension pin %q", p.ID)
	}
	names[p.File] = true
	return nil
}

func (app adopterAssetPin) artifact(tag string) *Artifact {
	if app.ApplicationProgram != "" {
		return &Artifact{Version: app.Version, SourceCommit: app.SourceCommit, SourceURL: app.SourceURL, Platform: app.Platform, URL: releaseAssetURL(tag, app.BinaryZIP), SHA256: app.BinarySHA256, Bytes: app.BinaryBytes, PeakBytes: app.PeakBytes, License: app.License, Publisher: app.Publisher, Signature: "native", InstallIdentity: app.InstallIdentity, Action: app.Action, ApplicationProgram: app.ApplicationProgram, ServiceProgram: app.ServiceProgram, ArchiveToolProgram: app.ArchiveToolProgram, NativeResources: &nativeArchiveResources{Backend: *app.Backend, Source: *app.CorrespondingSource, RecordBytes: app.RecordBytes, MarkerBytes: app.MarkerBytes, RecordRoots: app.RecordRoots}}
	}
	if app.Variant != "" {
		return nil
	}
	return &Artifact{Version: app.Version, SourceCommit: app.SourceCommit,
		SourceURL: app.SourceURL, Platform: app.Platform,
		URL: releaseAssetURL(tag, app.BinaryZIP), SHA256: app.BinarySHA256,
		Bytes: app.BinaryBytes, PeakBytes: app.PeakBytes, License: app.License,
		Publisher: app.Publisher, Signature: app.Signer,
		InstallIdentity: app.InstallIdentity, Action: app.Action,
		SourceArchiveURL: releaseAssetURL(tag, app.SourceZIP), SourceArchiveSHA256: app.SourceSHA256,
		SourceArchiveBytes: app.SourceBytes, RecordsSHA256: app.RecordsSHA256}
}

func releaseAssetURL(tag, name string) string {
	return "https://github.com/openabstractions/redist/releases/download/" +
		url.PathEscape(tag) + "/" + url.PathEscape(name)
}

func projectAdopterAssets(c *Catalog, goos, goarch string, data []byte) error {
	pins, err := parseAdopterAssets(data)
	if err != nil {
		return err
	}
	c.projectConnectorPackages(pins, goos, goarch)
	if goos != "windows" || goarch != "amd64" {
		return nil
	}
	if pins.desktopIntegration != nil {
		c.DesktopIntegration = pins.desktopIntegration
	}
	for _, extension := range pins.Extensions {
		if !isLegacyConnectorExtension(extension.ID) || extension.hasProductIdentity() {
			continue
		}
		c.LegacyConnectorPin = connectorPackagePin(pins.ReleaseTag, extension)
	}
	archiveChoice, hasArchive := sharedAdopterArchiveChoice()
	for _, app := range pins.Apps {
		// Desktop rows are passive. Only the shared component supplies a
		// downloadable artifact to Setup.
		if !hasArchive || app.ID != string(archiveChoice.ID) {
			continue
		}
		for i := range c.Components {
			if strings.EqualFold(c.Components[i].ID, archiveChoice.Components[0]) {
				c.Components[i].Pending = ""
				c.Components[i].Artifact = app.artifact(pins.ReleaseTag)
			}
		}
	}
	return nil
}

// projectConnectorPackages retains only declared and admitted packages for
// the Catalog's own target. The compatibility view grants no admission.
func (c *Catalog) projectConnectorPackages(pins adopterAssetPins, goos, goarch string) {
	c.connectorPackages = nil
	for id := range pins.connectorPackages {
		binding, ok := pins.releasePackage(id, goos, goarch)
		if !ok {
			continue
		}
		if c.connectorPackages == nil {
			c.connectorPackages = make(map[string]admittedPackage)
		}
		c.connectorPackages[id] = binding
	}
}

func (c Catalog) integrationPackage(id string) (admittedPackage, bool) {
	binding, ok := c.connectorPackages[id]
	return binding, ok
}

func connectorPackagePin(tag string, extension adopterExtensionPin) *ConnectorPackagePin {
	return &ConnectorPackagePin{File: extension.File, SHA256: extension.SHA256, Bytes: extension.Bytes,
		Version: extension.Version, URL: releaseAssetURL(tag, extension.File),
		ConnectorSHA256: extension.ConnectorSHA256, ConnectorBytes: extension.ConnectorBytes}
}

// connectorPin reads the admitted package by Catalog ID. Only the historical
// extension can use the compatibility pin carried by earlier releases.
func (c Catalog) connectorPin(id string) (*ConnectorPackagePin, error) {
	legacy := isLegacyConnectorExtension(id)
	binding, ok := c.integrationPackage(id)
	if !ok {
		if legacy {
			return c.LegacyConnectorPin, nil
		}
		return nil, nil
	}
	pin := connectorPackagePin(binding.releaseTag, binding.pin)
	if legacy && (c.LegacyConnectorPin == nil || *c.LegacyConnectorPin != *pin) {
		return nil, errors.New("the integration package differs from its admitted release pin")
	}
	return pin, nil
}

// ConnectorPackagePin is the release's exact extension MSI and its optional
// connector image pin, recorded in the admitted asset list.
type ConnectorPackagePin struct {
	File            string
	SHA256          string
	Bytes           int64
	Version         string
	URL             string
	ConnectorSHA256 string
	ConnectorBytes  int64
}

// MaintenanceRevision is the connector digest used by maintenance records.
// An extension without a connector pin has no maintenance revision.
func (pin *ConnectorPackagePin) MaintenanceRevision() string {
	if pin == nil {
		return ""
	}
	return strings.ToLower(pin.ConnectorSHA256)
}

// Native Desktop metadata is passive. Burn owns its integrated installation.
type adopterArchivePin struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type adopterArchiveSet struct {
	Name   string              `json:"name"`
	Format string              `json:"format"`
	Bytes  int64               `json:"bytes"`
	SHA256 string              `json:"sha256"`
	Parts  []adopterArchivePin `json:"parts"`
}
type adopterDataRelease struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
}
type adopterSourceOffer struct {
	Archive     adopterArchiveSet  `json:"archive"`
	DataRelease adopterDataRelease `json:"data_release"`
}
type adopterBackendProvenance struct {
	ProducerReceipt adopterArchivePin `json:"producer_receipt"`
	InputDescriptor adopterArchivePin `json:"input_descriptor"`
	Manifest        adopterArchivePin `json:"manifest"`
	BackendPolicy   adopterArchivePin `json:"backend_policy"`
}
type adopterBackendResource struct {
	Schema              int                      `json:"schema"`
	Variant             string                   `json:"variant"`
	ComfyUIRef          string                   `json:"comfyui_ref"`
	PythonVersion       string                   `json:"python_version"`
	Archive             adopterArchiveSet        `json:"archive"`
	CorrespondingSource adopterArchiveSet        `json:"corresponding_source"`
	DataRelease         adopterDataRelease       `json:"data_release"`
	Provenance          adopterBackendProvenance `json:"provenance"`
}

var archiveMemberName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var dataRepository = regexp.MustCompile(`^openabstractions/[A-Za-z0-9][A-Za-z0-9._-]*$`)
var immutableDataTag = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func validArchiveMember(name string) bool {
	if !archiveMemberName.MatchString(name) || strings.HasSuffix(name, ".") {
		return false
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	switch stem {
	case "CON", "PRN", "AUX", "NUL":
		return false
	}
	return !regexp.MustCompile(`^(COM|LPT)[1-9]$`).MatchString(stem)
}
func (p adopterArchivePin) valid() bool {
	return validArchiveMember(p.Name) && p.Bytes > 0 && assetSHA.MatchString(p.SHA256)
}
func (a adopterArchiveSet) validate(format string, names map[string]bool) error {
	if a.Format != format || !strings.HasSuffix(a.Name, "."+format) || !(adopterArchivePin{a.Name, a.Bytes, a.SHA256}).valid() || a.Bytes > 8*1024*1024*1024 || len(a.Parts) < 1 || len(a.Parts) > 16 {
		return errors.New("invalid native archive set")
	}
	var total int64
	for i, p := range a.Parts {
		expected := a.Name
		if len(a.Parts) > 1 {
			expected = fmt.Sprintf("%s.%03d", a.Name, i+1)
		}
		key := strings.ToLower(p.Name)
		if !p.valid() || p.Name != expected || p.Bytes >= maxReleaseAssetBytes || names[key] {
			return errors.New("invalid or aliased native archive part")
		}
		names[key] = true
		total += p.Bytes
	}
	if total != a.Bytes || len(a.Parts) == 1 && (a.Parts[0].SHA256 != a.SHA256 || a.Parts[0].Bytes != a.Bytes) {
		return errors.New("native archive logical pin differs from parts")
	}
	return nil
}
func (d adopterDataRelease) valid() bool {
	if !dataRepository.MatchString(d.Repository) || !immutableDataTag.MatchString(d.Tag) {
		return false
	}
	switch strings.ToLower(d.Tag) {
	case "latest", "main", "master", "default", "head":
		return false
	}
	return true
}
func (app adopterAssetPin) validateDesktop(names map[string]bool) error {
	native := app.ApplicationProgram != ""
	if app.ID != "comfyui" || (!native && app.Variant != "oa-comfyui-desktop") || app.SourceZIP != "" || app.SourceSHA256 != "" || app.SourceBytes != 0 || app.Signer != "" || (!native && (app.Action != "" || app.InstallIdentity != "" || app.ApplicationProgram != "" || app.ServiceProgram != "" || app.ArchiveToolProgram != "" || app.RecordBytes != 0 || app.MarkerBytes != 0 || app.RecordRoots != nil)) || (native && (app.Variant != "oa-comfyui-native" || app.RecordBytes <= 0 || app.MarkerBytes <= 0 || len(app.RecordRoots) != 2 || app.Action != "archive_per_user" || app.InstallIdentity != app.Variant || !safeMemberPath(app.ApplicationProgram) || !safeMemberPath(app.ServiceProgram) || !safeMemberPath(app.ArchiveToolProgram) || app.ApplicationProgram == app.ServiceProgram || app.ApplicationProgram == app.ArchiveToolProgram)) || app.RecordsSHA256 != "" || app.CorrespondingSource == nil || app.Backend == nil {
		return errors.New("invalid native Desktop row")
	}
	if !assetNamePattern.MatchString(app.BinaryZIP) || !assetSHA.MatchString(app.BinarySHA256) || app.BinaryBytes <= 0 || app.BinaryBytes >= maxReleaseAssetBytes || !assetCommit.MatchString(app.SourceCommit) || app.Platform != "windows/amd64" || app.PeakBytes < app.BinaryBytes || app.Version == "" || app.License == "" || app.Publisher == "" {
		return errors.New("invalid native Desktop metadata")
	}
	source, err := url.Parse(app.SourceURL)
	if err != nil || source.Scheme != "https" || source.Host != "github.com" || source.User != nil || source.RawQuery != "" || source.Fragment != "" || !regexp.MustCompile(`^/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/commit/[0-9a-f]{40}$`).MatchString(source.Path) || !strings.HasSuffix(source.Path, "/commit/"+app.SourceCommit) {
		return errors.New("invalid native Desktop source URL")
	}
	names[strings.ToLower(app.BinaryZIP)] = true
	if !app.CorrespondingSource.DataRelease.valid() {
		return errors.New("invalid corresponding source release")
	}
	if err := app.CorrespondingSource.Archive.validate("zip", names); err != nil {
		return err
	}
	b := app.Backend
	if app.CorrespondingSource.DataRelease != b.DataRelease {
		return errors.New("native source and backend DATA releases differ")
	}
	if err := validateBackendResource(*b); err != nil {
		return err
	}
	if native {
		return app.artifact("v0.0.0").validateNativeResources()
	}
	return nil
}

func validateBackendResource(b adopterBackendResource) error {
	if b.Schema != 2 || b.Variant != "win-amd" || !assetCommit.MatchString(b.ComfyUIRef) || b.PythonVersion != "3.12.10" || !b.DataRelease.valid() {
		return errors.New("invalid native backend resource")
	}
	backendNames := map[string]bool{}
	if err := b.Archive.validate("7z", backendNames); err != nil {
		return err
	}
	if err := b.CorrespondingSource.validate("zip", backendNames); err != nil {
		return err
	}
	for name, p := range map[string]adopterArchivePin{"backend-producer.json": b.Provenance.ProducerReceipt, "backend-inputs.json": b.Provenance.InputDescriptor, "backend-manifest.json": b.Provenance.Manifest, "backend-amd-v1.json": b.Provenance.BackendPolicy} {
		bound := int64(4 * 1024 * 1024)
		if name == "backend-manifest.json" {
			bound = 16 * 1024 * 1024
		}
		if !p.valid() || p.Name != name || p.Bytes > bound {
			return errors.New("invalid native backend provenance pin")
		}
	}
	return nil
}
