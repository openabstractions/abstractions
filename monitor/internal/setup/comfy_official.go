package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// OfficialExtension binds Setup to the selected compiled Catalog package,
// its approved host layout and its optional release compatibility policy.
type OfficialExtension struct {
	spec           product.PackageSpec
	legacy         product.LegacyPackageSpec
	locator        extensionHostLocator
	processFolders []string
	fallbackFolder string
}

const (
	ExtensionDesktop  = "desktop"
	ExtensionPortable = "portable"
)

var errNoOfficialExtension = errors.New("no official extension package was selected")

func OfficialExtensionFor(id AppID) OfficialExtension {
	facts := product.ExtensionSetupFacts()
	spec, ok := product.PackageDeclaration(string(id))
	if !ok || facts.PackageID != spec.ID || facts.InstalledKind == "" ||
		facts.HostSubfolder != spec.HostSubfolder || facts.InstallFolder != spec.InstallFolder ||
		facts.ConnectorName != spec.Connector || facts.HostKey != spec.HostKey || facts.UpgradeCode != spec.UpgradeCode {
		return OfficialExtension{}
	}
	legacy, _ := product.LegacyPackageDeclaration(spec.ID)
	return OfficialExtension{
		spec: spec, legacy: legacy, processFolders: facts.ProcessFolders, fallbackFolder: facts.InstallFolder,
		locator: extensionHostLocator{
			spec: spec, installBase: facts.InstallBase, localInstalls: facts.LocalInstalls,
			settingsPaths: facts.SettingsPaths, settingsKeys: append([]string{spec.HostKey}, facts.SettingsKeys...),
			desktopProgram: facts.DesktopProgram, portableLaunchers: facts.PortableLaunchers,
			integrationMarker: facts.IntegrationMarker, signature: trustedExtensionProgram,
			kinds: extensionHostKinds{portable: ExtensionPortable, singleton: ExtensionDesktop, installed: facts.InstalledKind},
			search: signedHostSearch{
				preferred: facts.PreferredSignedProgram,
				firstScan: facts.SignedScanFirstFolder,
				name:      facts.SignedScanName,
				exclude:   filepath.ToSlash(filepath.Dir(filepath.FromSlash(facts.DesktopProgram))),
			},
		},
	}
}

func (extension OfficialExtension) Find(folders ExtensionHostFolders) []ExtensionHost {
	return extension.find(folders, compiledCatalog().discovery[AppID(extension.spec.ID)])
}

func (extension OfficialExtension) find(folders ExtensionHostFolders, declaration json.RawMessage) []ExtensionHost {
	if extension.spec.ID == "" {
		return nil
	}
	return extension.locator.find(folders, declaration)
}

// DesktopToFinish offers the separately installed launcher only until
// discovery finds a matching installed desktop host.
func (extension OfficialExtension) DesktopToFinish(folders ExtensionHostFolders) (ExtensionHost, bool) {
	if extension.spec.ID == "" {
		return ExtensionHost{}, false
	}
	program := extension.locator.signedProgram(folders.Local)
	if program == "" {
		return ExtensionHost{}, false
	}
	for _, host := range extension.Find(folders) {
		if host.Kind == extension.locator.kinds.installed {
			return ExtensionHost{}, false
		}
	}
	return ExtensionHost{Kind: extension.locator.kinds.installed, Program: program}, true
}

func (extension OfficialExtension) InstalledDesktopKind() string {
	return extension.locator.kinds.installed
}

// PrimaryExtensionHost prefers the first connected host in discovery order.
func PrimaryExtensionHost(found []ExtensionHost) (ExtensionHost, bool) {
	for _, host := range found {
		if host.Connected {
			return host, true
		}
	}
	if len(found) == 0 {
		return ExtensionHost{}, false
	}
	return found[0], true
}

func (extension OfficialExtension) Connector(base string) string {
	if extension.spec.ID == "" {
		return ""
	}
	return packageConnector(extension.spec, base)
}

// trustedExtensionProgram checks the installed program with the existing native
// signature owner. The exact unsigned launcher path is checked by the locator.
var trustedExtensionProgram = func(program string) bool {
	info, err := os.Stat(program)
	if err != nil {
		return false
	}
	seen := extensionSignature{size: info.Size(), modified: info.ModTime()}
	if known, ok := extensionSignatures.Load(program); ok && known.(extensionSignature).size == seen.size && known.(extensionSignature).modified.Equal(seen.modified) {
		return known.(extensionSignature).trusted
	}
	code, _, err := identity.VerifyFile(program, nil)
	seen.trusted = err == nil && code.Trusted
	extensionSignatures.Store(program, seen)
	if !seen.trusted {
		logDiscoveryOnce("setup: %s is not started from the Panel: it carries no signature Windows trusts", program)
	}
	return seen.trusted
}

type extensionSignature struct {
	size     int64
	modified time.Time
	trusted  bool
}

var extensionSignatures sync.Map

func (extension OfficialExtension) PortableFolder(chosen string) bool {
	if extension.spec.ID == "" {
		return false
	}
	_, _, ok := extension.locator.portable(chosen)
	return ok
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (extension OfficialExtension) hostSelection() portableHostSelection {
	return portableHostSelection{packageID: extension.spec.ID, legacyRecord: extension.legacy.FolderRecord, locator: extension.locator}
}

func (extension OfficialExtension) AccountFolders() ExtensionHostFolders {
	if extension.spec.ID == "" {
		return ExtensionHostFolders{}
	}
	return extension.hostSelection().accountFolders(AppID(extension.spec.ID))
}

func (extension OfficialExtension) ChosenFolder() string {
	return extension.hostSelection().chosen()
}

func (extension OfficialExtension) SaveChosenFolder(ctx context.Context, folder string) error {
	if extension.spec.ID == "" {
		return errNoOfficialExtension
	}
	return extension.hostSelection().save(ctx, folder, errors.New(packageCopy(extension.legacy.Copy.FolderInvalid, "Choose a folder containing a portable installation.")))
}

func (extension OfficialExtension) pin(c Catalog) (*ConnectorPackagePin, error) {
	if extension.spec.ID == "" {
		return nil, errNoOfficialExtension
	}
	return c.connectorPin(extension.spec.ID)
}

func (extension OfficialExtension) Revision(c Catalog) (revision string, available bool, err error) {
	pin, err := extension.pin(c)
	if err != nil {
		return "", false, err
	}
	return pin.MaintenanceRevision(), pin != nil, nil
}

func (extension OfficialExtension) PackageDigest(c Catalog) (string, error) {
	pin, err := extension.pin(c)
	if err != nil || pin == nil {
		return "", err
	}
	return pin.SHA256, nil
}

type extensionPackageUnavailable string

func (reason extensionPackageUnavailable) Error() string { return string(reason) }
func (reason extensionPackageUnavailable) Unwrap() error { return errPackageUnavailable }

func (extension OfficialExtension) unavailable() error {
	return extensionPackageUnavailable(packageCopy(extension.legacy.Copy.PackageUnavailable, errPackageUnavailable.Error()))
}

func (extension OfficialExtension) PackageDigestForConnector(c Catalog, image string, size int64) (string, error) {
	pin, err := extension.pin(c)
	if err != nil {
		return "", err
	}
	return pin.packageDigestForConnector(image, size)
}

// Reconnect is the historical already-installed path. New installer effects
// run through Connect and its durable package receipt.
func (extension OfficialExtension) Reconnect(ctx context.Context, c Catalog, found ExtensionHost) error {
	if extension.spec.ID == "" {
		return errNoOfficialExtension
	}
	if _, ok := c.integrationPackage(extension.spec.ID); ok {
		return errors.New("the compiled integration needs a native registration proof")
	}
	if found.Connected && extension.Pinned(ctx, c, found.Base) {
		return extension.Register(ctx, extension.Connector(found.Base))
	}
	if !packageHostPresent(extension.spec, found.Base) {
		return errors.New(packageCopy(extension.legacy.Copy.FolderGone, "The application folder is gone. Open the Catalog again."))
	}
	return extension.unavailable()
}

var registerCompatibilityConnector = RegisterExtensionConnector

func (extension OfficialExtension) Connect(ctx context.Context, c Catalog, found ExtensionHost, ops PackageConnection) (bool, error) {
	if binding, ok := c.integrationPackage(extension.spec.ID); ok {
		ops.checkFreshProduct = func(ctx context.Context) error {
			return checkFreshExtensionPackage(ctx, binding.spec.UpgradeCode)
		}
	}
	return extension.connect(ctx, c, found, ops, extension.installAtHost)
}

func (extension OfficialExtension) connect(ctx context.Context, c Catalog, found ExtensionHost, ops PackageConnection,
	install func(context.Context, string, string, product.PackageSpec, extensionPackageMode, func() error) error) (bool, error) {
	if extension.spec.ID == "" {
		return true, errNoOfficialExtension
	}
	target := extension.target(found.Base)
	handled, err := c.connectSelectedExtensionPackage(ctx, found.Base, found.Connected, target, extension.packageFiles(), ops, install)
	if errors.Is(err, errPackageUnavailable) {
		if _, declared := c.integrationPackage(target.catalogID); !declared {
			err = extension.unavailable()
		}
	}
	return handled, err
}

func (extension OfficialExtension) Register(ctx context.Context, connector string) error {
	if extension.spec.ID == "" {
		return errNoOfficialExtension
	}
	return registerCompatibilityConnector(ctx, connector, extension.spec.Title)
}

func (extension OfficialExtension) RegistrationOutcome(outcome, reason string) error {
	if extension.spec.ID == "" {
		return errNoOfficialExtension
	}
	return extensionRegistrationOutcome(extension.spec.Title, outcome, reason)
}

func (extension OfficialExtension) Pinned(ctx context.Context, c Catalog, base string) bool {
	pin, err := extension.pin(c)
	if err != nil {
		return false
	}
	if pin == nil || pin.ConnectorSHA256 == "" {
		return true
	}
	held, err := c.holdCurrentConnector(ctx, base, extension.target(base), extension.spec, pin)
	return err == nil && held.Close() == nil
}

func (extension OfficialExtension) Outdated(c Catalog, base string) (bool, error) {
	pin, err := extension.pin(c)
	if err != nil {
		return false, err
	}
	return pin.connectorOutdated(extension.Connector(base))
}

func (extension OfficialExtension) PackageMissing(c Catalog) string {
	pin, err := extension.pin(c)
	if err != nil {
		return err.Error()
	}
	if pin == nil || localPackage(pin.download()) == "" {
		return "this OpenAbstractions build doesn't include the integration update"
	}
	return ""
}

// StagePackage saves the exact admitted source for existing maintenance,
// including updates that resume after the app closes.
func (extension OfficialExtension) StagePackage(ctx context.Context, c Catalog, transfer Transfer) error {
	pin, err := c.requiredConnectorPin(extension.spec.ID)
	if err != nil {
		return err
	}
	current, err := extension.packageFiles().current()
	if err != nil {
		return err
	}
	download := pin.download()
	download.Label = "Download the " + extension.spec.Title + " integration"
	return acquireConnectorPackage(ctx, download, current, transfer)
}

func (extension OfficialExtension) Processes(found ExtensionHost) ([]uint32, error) {
	if extension.spec.ID == "" {
		return nil, errNoOfficialExtension
	}
	return hostProcesses(found.Base, extension.processFolders)
}

func (extension OfficialExtension) Image(base string) (sha string, size int64, err error) {
	if extension.spec.ID == "" {
		return "", 0, errNoOfficialExtension
	}
	return connectorFingerprint(extension.Connector(base))
}

func (extension OfficialExtension) HoldImage(ctx context.Context, base, sha string, size int64) (HeldProgram, error) {
	if extension.spec.ID == "" {
		return HeldProgram{}, errNoOfficialExtension
	}
	return holdConnectorProof(ctx, extension.Connector(base), sha, size, extension.spec.Title)
}

// CheckMissingCompanion confirms the exact earlier program image before
// repair of a missing declared integration companion.
func (extension OfficialExtension) CheckMissingCompanion(ctx context.Context, base, digest string, size int64) error {
	if extension.spec.ID == "" {
		return errNoOfficialExtension
	}
	companion := filepath.Join(filepath.Dir(extension.Connector(base)), extension.locator.integrationMarker)
	return CheckMissingPackageCompanion(ctx, base, companion, func(ctx context.Context) (HeldProgram, error) {
		return extension.HoldImage(ctx, base, digest, size)
	})
}

func (extension OfficialExtension) packageFiles() extensionPackageFiles {
	return extensionPackageFiles{compatibility: extension.spec, fallbackFolder: extension.fallbackFolder}
}

func (extension OfficialExtension) KeepPreviousPackage(ctx context.Context) (path, digest string, err error) {
	return extension.packageFiles().keepPrevious(ctx)
}

func (extension OfficialExtension) InstallPackage(ctx context.Context, msi, base string) error {
	return extension.InstallSavedPackage(ctx, msi, base, func() error { return nil })
}

func (extension OfficialExtension) InstallSavedPackage(ctx context.Context, msi, base string, beforeEffect func() error) error {
	if extension.spec.ID == "" {
		return errNoOfficialExtension
	}
	return extension.installAtHost(ctx, msi, base, extension.spec, extensionReplacePackage, beforeEffect)
}

func (extension OfficialExtension) installAtHost(ctx context.Context, msi, base string, spec product.PackageSpec, mode extensionPackageMode, beforeEffect func() error) error {
	return installExtensionAtHost(ctx, msi, base, spec, extension.locator.integrationMarker, mode, beforeEffect, connectExtensionPackageWithEffect)
}

// InstallPinnedPackage returns publication to the maintenance caller after
// the installer finishes. The caller owns the native Registry proof.
func (extension OfficialExtension) InstallPinnedPackage(ctx context.Context, c Catalog, base string, markEffect func() error) (func(context.Context) error, error) {
	return extension.runPackage(ctx, c, base, func(ctx context.Context, msi string, spec product.PackageSpec, _ func(context.Context) error) error {
		return extension.installAtHost(ctx, msi, base, spec, extensionReplacePackage, markEffect)
	})
}

func (extension OfficialExtension) runPackage(ctx context.Context, c Catalog, base string, install stagedConnectorInstaller) (func(context.Context) error, error) {
	if extension.spec.ID == "" {
		return nil, errNoOfficialExtension
	}
	publish, err := c.stageConnectorPackage(ctx, base, extension.target(base), extension.packageFiles(), install)
	if errors.Is(err, errPackageUnavailable) {
		return nil, extension.unavailable()
	}
	return publish, err
}

func (extension OfficialExtension) target(base string) packageTarget {
	return packageTarget{catalogID: extension.spec.ID, registration: extension.spec.Registration, connector: extension.Connector(base)}
}

func (extension OfficialExtension) ReplacePackage(ctx context.Context, c Catalog, base string, tx KeptPackageTransaction) error {
	if extension.spec.ID == "" {
		return errNoOfficialExtension
	}
	return c.replaceConnectorPackage(ctx, base, extension.target(base), extension.spec, tx)
}

func (extension OfficialExtension) PublishPinnedPackage(ctx context.Context, c Catalog) error {
	if extension.spec.ID == "" {
		return errNoOfficialExtension
	}
	err := extension.packageFiles().publishPinned(ctx, c, extension.spec.ID)
	if errors.Is(err, errPackageUnavailable) {
		return extension.unavailable()
	}
	return err
}

func (extension OfficialExtension) HoldUndoPackage(ctx context.Context, path, digest string) (release func() error, publish func(context.Context) error, err error) {
	return extension.packageFiles().holdPredecessor(ctx, path, digest)
}

func (extension OfficialExtension) HoldConnector(ctx context.Context, c Catalog, base string) (HeldProgram, error) {
	pin, err := extension.pin(c)
	if err != nil {
		return HeldProgram{}, err
	}
	if pin == nil || pin.ConnectorSHA256 == "" {
		return HeldProgram{}, errors.New("this build pins no " + extension.spec.Title + " connector")
	}
	return c.holdCurrentConnector(ctx, base, extension.target(base), extension.spec, pin)
}

func isLegacyConnectorExtension(id string) bool {
	facts := product.ExtensionSetupFacts()
	if id == "" || facts.PackageID != id {
		return false
	}
	_, ok := product.LegacyPackageDeclaration(id)
	return ok
}
