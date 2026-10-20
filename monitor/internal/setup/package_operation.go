package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

func packageCopy(declared, fallback string) string {
	if declared != "" {
		return declared
	}
	return fallback
}

type extensionPackageMode bool

const (
	extensionReplacePackage extensionPackageMode = false
	extensionFreshPackage   extensionPackageMode = true
)

func packageConnector(spec product.PackageSpec, base string) string {
	return filepath.Join(base, spec.HostSubfolder, spec.InstallFolder, spec.Connector)
}

func packageHostPresent(spec product.PackageSpec, base string) bool {
	if !filepath.IsAbs(base) || spec.HostSubfolder == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(base, spec.HostSubfolder))
	return err == nil && info.IsDir()
}

// extensionConnectorPresent is a layout readback. Operation admission uses
// the held connector's exact size and digest separately.
func extensionConnectorPresent(connector, marker string) bool {
	return isRegularFile(connector) && (marker == "" || isRegularFile(filepath.Join(filepath.Dir(connector), marker)))
}

func installExtensionAtHost(ctx context.Context, msi, base string, spec product.PackageSpec, marker string,
	mode extensionPackageMode, markEffect func() error,
	dispatch func(context.Context, string, string, product.PackageSpec, extensionPackageMode, func() error) error) error {
	if !packageHostPresent(spec, base) {
		return fmt.Errorf("%s's folder is gone. Open the Catalog again.", spec.Title)
	}
	if err := dispatch(ctx, msi, base, spec, mode, markEffect); err != nil {
		return err
	}
	if !extensionConnectorPresent(packageConnector(spec, base), marker) {
		return fmt.Errorf("the %s integration installed but is not in %s's %s folder", spec.Title, spec.Title, spec.HostSubfolder)
	}
	return nil
}

func extensionHostConfig(ctx context.Context, spec product.PackageSpec, base string) (string, error) {
	home, err := CurrentHome()
	if err != nil {
		return "", err
	}
	config := filepath.Join(home.Config, spec.ID+"-host.json")
	return config, writeSetupFile(ctx, config, map[string]string{spec.HostKey: base})
}

// extensionRegistrationAnswer reads the connector's register-self result.
func extensionRegistrationAnswer(output []byte) (outcome, reason string) {
	line, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	outcome, reason, _ = registrationAnswerFields([]byte(line))
	return outcome, reason
}

// registrationAnswerFields reads the two display fields of the connector
// CLI's reply. Its registration and Keep acknowledgements belong to Registry.
func registrationAnswerFields(raw []byte) (outcome, reason string, valid bool) {
	var answer map[string]json.RawMessage
	if json.Unmarshal(raw, &answer) != nil {
		return "", "", false
	}
	read := func(name string, target *string) error {
		if field, present := answer[name]; present {
			return json.Unmarshal(field, target)
		}
		return nil
	}
	err := errors.Join(read("outcome", &outcome), read("reason", &reason))
	return outcome, reason, err == nil
}

// ExtensionRegistrationConflict carries the native Registry's conflict reason
// for the selected package connector.
type ExtensionRegistrationConflict struct {
	Reason string
	title  string
}

func (c *ExtensionRegistrationConflict) Error() string {
	switch c.Reason {
	case "identity":
		return fmt.Sprintf("An earlier %s connector is in Apps, so this one was not added", c.title)
	case "unconfirmed", "removing", "state":
		return fmt.Sprintf("A %s connector is waiting in Apps. Keep or remove it there, then Connect again", c.title)
	default:
		return fmt.Sprintf("Another program is registered as %s in Apps. Remove it there, then Connect again", c.title)
	}
}

// RegisterExtensionConnector dispatches register-self for the program selected
// by Setup. Declared paths are supplied from the held-image confirmation.
func RegisterExtensionConnector(ctx context.Context, program, title string) error {
	outcome, reason, err := runExtensionRegistration(ctx, program)
	if err != nil {
		return fmt.Errorf("the %s connector could not register: %w", title, err)
	}
	return extensionRegistrationOutcome(title, outcome, reason)
}

func extensionRegistrationOutcome(title, outcome, reason string) error {
	switch outcome {
	case "applied":
		return nil
	case "conflict":
		return &ExtensionRegistrationConflict{Reason: reason, title: title}
	default:
		return fmt.Errorf("OpenAbstractions did not add the %s connector (%s %s)", title, outcome, reason)
	}
}

// connectorImage joins the exact admitted release pin to its declared host
// path. Both Connect and Registry re-pin hold this image before proof.
func (binding admittedPackage) connectorImage(base string) sevenZipPin {
	return sevenZipPin{Path: packageConnector(binding.spec, base), SHA256: binding.pin.ConnectorSHA256, Bytes: binding.pin.ConnectorBytes}
}

func (binding admittedPackage) matchHostTarget(base string, target packageTarget, compatibility product.PackageSpec) error {
	if err := target.match(binding.spec); err != nil {
		return err
	}
	if packageConnector(binding.spec, base) != target.connector || binding.spec.HostSubfolder != compatibility.HostSubfolder ||
		binding.spec.InstallFolder != compatibility.InstallFolder || binding.spec.HostKey != compatibility.HostKey ||
		binding.spec.UpgradeCode != compatibility.UpgradeCode {
		return errors.New("the integration target differs from the selected host layout")
	}
	return nil
}

// connectDeclaredPackage selects the paths and pins from the same admitted
// release row. Discovery supplies the chosen host and its installed state.
func (binding admittedPackage) connectDeclaredPackage(ctx context.Context, base string, discoveredInstalled bool,
	ops PackageConnection, install func(context.Context, string, string, product.PackageSpec, func() error) error) error {
	spec := binding.spec
	if !packageHostPresent(spec, base) {
		return fmt.Errorf("%s's folder is gone. Open the Catalog again.", spec.Title)
	}
	if spec.InstallFolder == "" || spec.HostKey == "" || spec.UpgradeCode == "" {
		return errors.New("the admitted package is missing its installer layout")
	}
	image := binding.connectorImage(base)
	target := packageTarget{catalogID: spec.ID, registration: spec.Registration, connector: image.Path}
	alreadyInstalled := false
	if discoveredInstalled {
		held, err := holdPinnedProgram(ctx, image)
		alreadyInstalled = err == nil && held.Close() == nil
	}
	current := ""
	if !alreadyInstalled || ops.ExistingReceipt != nil {
		var err error
		current, err = currentPackageCopy(spec)
		if err != nil {
			return err
		}
	}
	pin := PinnedDownload{URL: releaseAssetURL(binding.releaseTag, binding.pin.File), SHA256: binding.pin.SHA256, Bytes: binding.pin.Bytes}
	return runPackageConnection(ctx, spec, target, pin, image, current, alreadyInstalled,
		func(ctx context.Context, staged string, markEffect func() error) error {
			return install(ctx, staged, base, spec, markEffect)
		}, ops, "")
}

// connectSelectedExtensionPackage binds both installation orders to the
// same admitted target and durable package connection.
func (c Catalog) connectSelectedExtensionPackage(ctx context.Context, base string, connected bool, target packageTarget, files extensionPackageFiles,
	ops PackageConnection, install func(context.Context, string, string, product.PackageSpec, extensionPackageMode, func() error) error) (bool, error) {
	if binding, ok := c.integrationPackage(target.catalogID); ok {
		if err := binding.matchHostTarget(base, target, files.compatibility); err != nil {
			return true, err
		}
		if _, err := c.connectorPin(target.catalogID); err != nil {
			return true, err
		}
		return true, binding.connectDeclaredPackage(ctx, base, connected, ops,
			func(ctx context.Context, msi, base string, spec product.PackageSpec, markEffect func() error) error {
				return install(ctx, msi, base, spec, extensionFreshPackage, markEffect)
			})
	}
	pin, err := c.connectorPin(target.catalogID)
	if err != nil {
		return true, err
	}
	if ops.ExistingReceipt == nil {
		if pin == nil || connected && pin.ConnectorSHA256 == "" {
			return false, nil
		}
		if connected {
			held, err := c.holdCurrentConnector(ctx, base, target, files.compatibility, pin)
			if err == nil && held.Close() == nil {
				return false, nil
			}
		}
	}
	legacy, present := product.LegacyPackageDeclaration(target.catalogID)
	if !present || legacy.Order == "" || pin == nil {
		if ops.ExistingReceipt != nil {
			return true, errors.New("the pending integration installation requires its original release")
		}
		return true, errPackageUnavailable
	}
	spec := files.compatibility
	if !packageHostPresent(spec, base) {
		return true, errors.New(packageCopy(legacy.Copy.FolderGone, fmt.Sprintf("%s's folder is gone. Open the Catalog again.", spec.Title)))
	}
	current, err := files.current()
	if err != nil {
		return true, err
	}
	image := sevenZipPin{Path: target.connector, SHA256: pin.ConnectorSHA256, Bytes: pin.ConnectorBytes}
	return true, runPackageConnection(ctx, spec, target, pin.download(), image, current, false,
		func(ctx context.Context, msi string, markEffect func() error) error {
			return install(ctx, msi, base, spec, extensionReplacePackage, markEffect)
		}, ops, legacy.Order)
}

// extensionPackageFiles owns the private saved-current MSI and its predecessors.
type extensionPackageFiles struct {
	compatibility  product.PackageSpec
	fallbackFolder string
}

func (files extensionPackageFiles) current() (string, error) {
	spec := files.compatibility
	if spec.InstallFolder == "" {
		spec.InstallFolder = files.fallbackFolder
	}
	return currentPackageCopy(spec)
}

func (files extensionPackageFiles) keepPrevious(ctx context.Context) (string, string, error) {
	current, err := files.current()
	if err != nil {
		return "", "", err
	}
	return keepPreviousPackage(ctx, current)
}

func (files extensionPackageFiles) holdPredecessor(ctx context.Context, path, digest string) (release func() error, publish func(context.Context) error, err error) {
	current, err := files.current()
	if err != nil {
		return nil, nil, err
	}
	var raw bytes.Buffer
	release, err = holdSavedPackage(ctx, path, digest, &raw)
	if err != nil {
		return nil, nil, err
	}
	return release, func(ctx context.Context) error { return writePinnedPackage(ctx, current, raw.Bytes(), 64<<20) }, nil
}

func (files extensionPackageFiles) publishPinned(ctx context.Context, c Catalog, id string) error {
	pin, err := c.requiredConnectorPin(id)
	if err != nil {
		return err
	}
	current, err := files.current()
	if err != nil {
		return err
	}
	raw, err := readPinnedOrStagedPackage(ctx, pin.download(), current)
	if err != nil {
		return err
	}
	return writePinnedPackage(ctx, current, raw, 64<<20)
}

func (c Catalog) requiredConnectorPin(id string) (*ConnectorPackagePin, error) {
	pin, err := c.connectorPin(id)
	if err != nil {
		return nil, err
	}
	if pin == nil {
		return nil, errPackageUnavailable
	}
	return pin, nil
}

type stagedConnectorInstaller func(ctx context.Context, msi string, spec product.PackageSpec, publish func(context.Context) error) error

// stageConnectorPackage binds the admitted MSI to the selected host before
// passing the held staging file and its exact publication to the caller.
func (c Catalog) stageConnectorPackage(ctx context.Context, base string, target packageTarget, files extensionPackageFiles,
	install stagedConnectorInstaller) (func(context.Context) error, error) {
	pin, err := c.requiredConnectorPin(target.catalogID)
	if err != nil {
		return nil, err
	}
	spec := files.compatibility
	if binding, ok := c.integrationPackage(target.catalogID); ok {
		if err := binding.matchHostTarget(base, target, spec); err != nil {
			return nil, err
		}
		spec = binding.spec
	}
	current, err := files.current()
	if err != nil {
		return nil, err
	}
	return runStagedPackage(ctx, pin.download(), current, func(ctx context.Context, staged string, publish func(context.Context) error) error {
		return install(ctx, staged, spec, publish)
	})
}

func (c Catalog) replaceConnectorPackage(ctx context.Context, base string, target packageTarget, compatibility product.PackageSpec, tx KeptPackageTransaction) error {
	pin, err := c.connectorPin(target.catalogID)
	if err != nil {
		return err
	}
	if pin == nil || pin.ConnectorSHA256 == "" || tx.Program != target.connector {
		return errors.New("the integration replacement has no matching package and target")
	}
	if _, err := c.matchConnectorTarget(base, target, compatibility); err != nil {
		return err
	}
	tx.PackageSHA256, tx.ConnectorSHA256, tx.ConnectorBytes = pin.SHA256, pin.ConnectorSHA256, pin.ConnectorBytes
	return runKeptPackageTransaction(ctx, tx)
}

// matchConnectorTarget validates the selected host against its admitted row.
// Older releases have no declared row and keep their existing pinned path.
func (c Catalog) matchConnectorTarget(base string, target packageTarget, compatibility product.PackageSpec) (sevenZipPin, error) {
	binding, ok := c.integrationPackage(target.catalogID)
	if !ok {
		return sevenZipPin{}, nil
	}
	if err := binding.matchHostTarget(base, target, compatibility); err != nil {
		return sevenZipPin{}, err
	}
	return binding.connectorImage(base), nil
}

func (c Catalog) holdCurrentConnector(ctx context.Context, base string, target packageTarget, compatibility product.PackageSpec, pin *ConnectorPackagePin) (HeldProgram, error) {
	image, err := c.matchConnectorTarget(base, target, compatibility)
	if err != nil {
		return HeldProgram{}, err
	}
	if image.Path == "" {
		image = sevenZipPin{Path: target.connector, Bytes: pin.ConnectorBytes, SHA256: pin.ConnectorSHA256}
	}
	return holdPinnedProgram(ctx, image)
}

func (pin ConnectorPackagePin) download() PinnedDownload {
	return PinnedDownload{URL: pin.URL, SHA256: pin.SHA256, Bytes: pin.Bytes}
}

func (pin *ConnectorPackagePin) packageDigestForConnector(image string, size int64) (string, error) {
	if pin == nil || pin.ConnectorSHA256 != image || pin.ConnectorBytes != size {
		return "", errors.New("the earlier integration has no verified current package pin")
	}
	return pin.SHA256, nil
}

func (pin *ConnectorPackagePin) connectorOutdated(path string) (bool, error) {
	if pin == nil || pin.ConnectorSHA256 == "" {
		return false, nil
	}
	sum, err := connectorSum(path)
	if err != nil {
		return false, err
	}
	return !strings.EqualFold(sum, pin.ConnectorSHA256), nil
}

func connectorFingerprint(path string) (string, int64, error) {
	sum, err := connectorSum(path)
	if err != nil {
		return "", 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	return sum, info.Size(), nil
}

func holdConnectorProof(ctx context.Context, path, digest string, bytes int64, title string) (HeldProgram, error) {
	if !assetSHA.MatchString(digest) || bytes <= 0 {
		return HeldProgram{}, errors.New("no " + title + " connector image to hold")
	}
	return holdPinnedProgram(ctx, sevenZipPin{Path: path, Bytes: bytes, SHA256: digest})
}

// connectorSums remembers a connector's SHA-256 by its path, size and
// change time, so reading the Catalog hashes it once per change.
var connectorSums sync.Map

type connectorStamp struct {
	size     int64
	modified time.Time
	sum      string
}

func connectorSum(file string) (string, error) {
	info, err := os.Stat(file)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a file", file)
	}
	if known, ok := connectorSums.Load(file); ok {
		if stamp := known.(connectorStamp); stamp.size == info.Size() && stamp.modified.Equal(info.ModTime()) {
			return stamp.sum, nil
		}
	}
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(f, 64<<20)); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	connectorSums.Store(file, connectorStamp{size: info.Size(), modified: info.ModTime(), sum: sum})
	return sum, nil
}
