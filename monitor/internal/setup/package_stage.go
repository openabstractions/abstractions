package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

type packageTarget struct {
	catalogID    string
	registration string
	connector    string
}

func (target packageTarget) match(spec product.PackageSpec) error {
	if target.catalogID != spec.ID || target.registration != spec.Registration ||
		filepath.Base(target.connector) != spec.Connector {
		return errors.New("the integration target differs from the compiled package")
	}
	return nil
}

var errPackageUnavailable = errors.New("this build does not include the integration package")

func readPinnedPackage(pin PinnedDownload) ([]byte, error) {
	source := localPackage(pin)
	if source == "" {
		return nil, errPackageUnavailable
	}
	raw, err := readBounded(source, pin.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the integration package: %w", err)
	}
	return checkPinnedPackage(raw, pin)
}

func checkPinnedPackage(raw []byte, pin PinnedDownload) ([]byte, error) {
	sum := sha256.Sum256(raw)
	if int64(len(raw)) != pin.Bytes || hex.EncodeToString(sum[:]) != pin.SHA256 {
		return nil, errors.New("the integration package differs from its pin")
	}
	return raw, nil
}

func writePinnedPackage(ctx context.Context, target string, raw []byte, limit int64) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	return cas.ChangeLimitContext(ctx, target, limit, func([]byte) ([]byte, error) { return raw, nil })
}

func currentPackageCopy(spec product.PackageSpec) (string, error) {
	if !validArchiveMember(spec.InstallFolder) {
		return "", errors.New("the declared package needs an install folder name")
	}
	home, err := CurrentHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home.Config, spec.InstallFolder+".msi"), nil
}

// keepPreviousPackage saves the current MSI under its digest before update.
func keepPreviousPackage(ctx context.Context, current string) (path, digest string, err error) {
	raw, err := readBounded(current, 64<<20)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	digest = hex.EncodeToString(sum[:])
	path = strings.TrimSuffix(current, ".msi") + "-previous-" + digest + ".msi"
	if err := writePinnedPackage(ctx, path, raw, 64<<20); err != nil {
		return "", "", err
	}
	return path, digest, nil
}

// Different candidates may reach staging before one claims the receipt.
func packageStagePath(current, digest string) string {
	return filepath.Join(filepath.Dir(current), "integration-package-stage", digest, filepath.Base(current))
}

func readPinnedOrStagedPackage(ctx context.Context, pin PinnedDownload, current string) ([]byte, error) {
	raw, err := readPinnedPackage(pin)
	if !errors.Is(err, errPackageUnavailable) {
		return raw, err
	}
	staged := packageStagePath(current, pin.SHA256)
	if _, err := os.Lstat(staged); errors.Is(err, os.ErrNotExist) {
		return nil, errPackageUnavailable
	} else if err != nil {
		return nil, err
	}
	var captured bytes.Buffer
	release, err := holdSavedPackage(ctx, staged, pin.SHA256, &captured)
	if err != nil {
		return nil, err
	}
	if err := release(); err != nil {
		return nil, err
	}
	if int64(captured.Len()) != pin.Bytes {
		return nil, errors.New("the staged integration package differs from its pinned size")
	}
	return captured.Bytes(), nil
}

// acquireConnectorPackage uses the Catalog's transfer owner when the signed
// Panel carries the exact release pin but has no development package folder.
func acquireConnectorPackage(ctx context.Context, pin PinnedDownload, current string, transfer Transfer) error {
	if pin.Bytes <= 0 || pin.Bytes > 64<<20 {
		return errors.New("the connector package exceeds its staging limit")
	}
	if raw, err := readPinnedOrStagedPackage(ctx, pin, current); err == nil {
		if localPackage(pin) == "" {
			return nil
		}
		return writePinnedPackage(ctx, packageStagePath(current, pin.SHA256), raw, 64<<20)
	} else if !errors.Is(err, errPackageUnavailable) || transfer == nil {
		return err
	}
	if err := pin.validate(); err != nil {
		return err
	}
	key := workKey("connector-integration", Artifact{SHA256: pin.SHA256})
	work, err := downloadPinnedIntegration(ctx, pin, transfer, key)
	if err != nil {
		return err
	}
	var body bytes.Buffer
	if _, err := transfer.Copy(ctx, work, &boundedWriter{writer: &body, remaining: pin.Bytes}); err != nil {
		return err
	}
	raw, err := checkPinnedPackage(body.Bytes(), pin)
	if err != nil {
		return err
	}
	staged := packageStagePath(current, pin.SHA256)
	dir := filepath.Dir(staged)
	stageRoot := filepath.Dir(dir)
	config, err := programfiles.HoldDirectory(filepath.Dir(current))
	if err != nil {
		return err
	}
	defer config.Release()
	if err := os.Mkdir(stageRoot, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	root, err := programfiles.HoldDirectory(stageRoot)
	if err != nil {
		return err
	}
	defer root.Release()
	if err := privatedir.Ensure(dir); err != nil {
		return err
	}
	parent, err := programfiles.HoldDirectory(dir)
	if err != nil {
		return err
	}
	defer parent.Release()
	if err := privatedir.CheckDir(dir); err != nil {
		return err
	}
	return writePinnedPackage(ctx, staged, raw, 64<<20)
}

// runStagedPackage holds a verified, private MSI over the installer call.
// Its caller publishes the same bytes at the point its registration path permits.
func runStagedPackage(ctx context.Context, pin PinnedDownload, current string, install func(context.Context, string, func(context.Context) error) error) (publish func(context.Context) error, err error) {
	raw, err := readPinnedOrStagedPackage(ctx, pin, current)
	if err != nil {
		return nil, err
	}
	staged := packageStagePath(current, pin.SHA256)
	if err := writePinnedPackage(ctx, staged, raw, 64<<20); err != nil {
		return nil, err
	}
	release, err := HoldSavedPackage(ctx, staged, pin.SHA256)
	if err != nil {
		return nil, err
	}
	defer func() {
		if releaseErr := release(); releaseErr != nil {
			publish, err = nil, errors.Join(err, releaseErr)
		}
	}()
	publish = func(ctx context.Context) error {
		return writePinnedPackage(ctx, current, raw, 64<<20)
	}
	if err := install(ctx, staged, publish); err != nil {
		return nil, err
	}
	return publish, nil
}
