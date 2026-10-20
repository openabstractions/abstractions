package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	jobwire "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

// DesktopIntegration is this release's exact per-user MSI for an existing
// official desktop app.
type DesktopIntegration struct {
	App                                                             AppID
	Title, File, URL, SHA256, ProductCode, PackageCode, UpgradeCode string
	Bytes                                                           int64
}

// DesktopInstallerFailure carries safe Panel copy and a separate diagnostic
// for logs. Native installer statuses are for the diagnostic.
type DesktopInstallerFailure struct {
	Reason     string
	diagnostic error
}

var ErrDesktopIntegrationRunning = errors.New("the desktop application is running")

func (e *DesktopInstallerFailure) Error() string { return e.diagnostic.Error() }
func (e *DesktopInstallerFailure) Unwrap() error { return e.diagnostic }

func (p adopterExtensionPin) hasProductIdentity() bool {
	return p.ProductCode != "" || p.PackageCode != "" || p.UpgradeCode != ""
}

func (p adopterExtensionPin) desktopPackage(schema int, tag string) (*DesktopIntegration, error) {
	if schema != 2 || tag != "v"+p.Version || !fixupVersion.MatchString(p.Version) || p.File != p.ID+"-"+p.Version+".msi" ||
		!assetSHA.MatchString(p.SHA256) || p.Bytes <= 0 || p.Bytes >= 128<<20 || !fixupUpgradeCode.MatchString(p.ProductCode) ||
		(p.PackageCode != "" && !fixupUpgradeCode.MatchString(p.PackageCode)) ||
		p.ConnectorSHA256 != "" || p.ConnectorBytes != 0 {
		return nil, fmt.Errorf("invalid desktop extension pin %q", p.ID)
	}
	app := AppID(p.ID)
	catalog := compiledCatalog()
	for _, choice := range catalog.Choices {
		if choice.ID != app || !choice.Application || choice.Official == nil {
			continue
		}
		for _, component := range catalog.Components {
			if !slices.Contains(choice.Components, component.ID) || component.Fixup == nil {
				continue
			}
			upgrade := component.Fixup.RepairUpgradeCode
			if !fixupUpgradeCode.MatchString(upgrade) || (p.UpgradeCode != "" && p.UpgradeCode != upgrade) {
				return nil, fmt.Errorf("desktop extension pin %q differs from the compiled package family", p.ID)
			}
			return &DesktopIntegration{App: app, Title: choice.Name(), File: p.File, URL: releaseAssetURL(tag, p.File),
				SHA256: p.SHA256, ProductCode: p.ProductCode, PackageCode: p.PackageCode, UpgradeCode: upgrade, Bytes: p.Bytes}, nil
		}
	}
	return nil, fmt.Errorf("desktop extension pin %q has no compiled package owner", p.ID)
}

func (p DesktopIntegration) downloadPin() PinnedDownload {
	return PinnedDownload{URL: p.URL, SHA256: p.SHA256, Bytes: p.Bytes, Label: "Download the " + p.Title + " integration"}
}

// Action reads the Windows Installer registration for the
// Catalog. It makes no claim about the integration's live connection.
func (p DesktopIntegration) Action() (string, error) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return "", errors.New("This integration requires Windows x64.")
	}
	home, err := CurrentHome()
	if err != nil {
		return "", err
	}
	if home.Isolated {
		return "", errors.New("Open the installed Panel to change this account's desktop integration.")
	}
	action, _, err := p.packageState()
	return action, err
}

// Apply verifies and holds the package until Windows
// Installer finishes. The installed Desktop is verified by the Panel before
// calling this owner and again by the MSI's existing custom action.
func (p DesktopIntegration) Apply(ctx context.Context, action string, transfer Transfer) error {
	current, err := p.Action()
	if err != nil {
		return err
	}
	if current != action {
		return errors.New("The desktop integration changed. Check the Catalog again.")
	}
	return p.applySource(ctx, action, transfer, p.runPackage)
}

// Stage keeps the verified source for a deferred update in Setup's private
// directory. No installer runs until ApplyStaged verifies the held copy again.
func (p DesktopIntegration) Stage(ctx context.Context, dir string, transfer Transfer) error {
	config := filepath.Dir(dir)
	base, err := programfiles.HoldDirectory(config)
	if errors.Is(err, os.ErrNotExist) {
		parent, holdErr := programfiles.HoldDirectory(filepath.Dir(config))
		if holdErr != nil {
			return holdErr
		}
		defer parent.Release()
		if err := os.Mkdir(config, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		base, err = programfiles.HoldDirectory(config)
		if err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}
	defer base.Release()
	if err := privatedir.Ensure(dir); err != nil {
		return err
	}
	return p.applySource(ctx, "update", transfer, func(ctx context.Context, source, _ string) (err error) {
		parent, err := programfiles.HoldDirectory(dir)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, parent.Release()) }()
		if err := privatedir.CheckDir(dir); err != nil {
			return err
		}
		return p.publishStaged(ctx, dir, source)
	})
}

func (p DesktopIntegration) ApplyKeptUpdate(ctx context.Context, transfer Transfer, keep func(context.Context) error) error {
	return p.applySource(ctx, "update", transfer, p.runKeptUpdate(keep))
}

func (p DesktopIntegration) ApplyStaged(ctx context.Context, dir string, keep func(context.Context) error) error {
	action, err := p.Action()
	if err != nil || action != "update" {
		return errors.Join(err, errors.New("The desktop integration changed. Check the Catalog again."))
	}
	parent, err := programfiles.HoldDirectory(dir)
	if err != nil {
		return err
	}
	defer parent.Release()
	if err := privatedir.CheckDir(dir); err != nil {
		return err
	}
	return p.applyPackage(ctx, filepath.Join(dir, p.File), "update", p.runKeptUpdate(keep))
}

func (p DesktopIntegration) runKeptUpdate(keep func(context.Context) error) func(context.Context, string, string) error {
	return func(ctx context.Context, path, action string) error {
		return p.runPackageWithAdmission(ctx, path, action, keep)
	}
}

func (p DesktopIntegration) ClearStaged(dir string) error {
	parent, err := programfiles.HoldDirectory(dir)
	if err != nil {
		return err
	}
	defer parent.Release()
	if err := privatedir.CheckDir(dir); err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, p.File))
}

func (p DesktopIntegration) applySource(ctx context.Context, action string, transfer Transfer, run func(context.Context, string, string) error) (err error) {
	pin := p.downloadPin()
	if path := localPackage(pin); path != "" {
		return p.applyPackage(ctx, path, action, run)
	}
	if leaf, ok := releaseAssetName(pin.URL); !ok || leaf != p.File || transfer == nil {
		return errors.New("This build's desktop integration package is unavailable.")
	}
	if err := pin.validate(); err != nil {
		return err
	}
	work, err := p.downloadPackage(ctx, pin, transfer)
	if err != nil {
		return err
	}
	platform := layout.Current()
	roots, err := layout.Resolve(platform)
	if err != nil {
		return err
	}
	tmp, err := layout.EnsureTmp(roots, platform)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp(tmp, "oa-desktop-integration-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.Remove(dir)) }()
	directory, err := programfiles.HoldDirectory(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directory.Release()) }()
	if err := privatedir.Tighten(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, p.File)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.Remove(path)) }()
	written, copyErr := transfer.Copy(ctx, work, &boundedWriter{writer: file, remaining: pin.Bytes})
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return err
	}
	if written != pin.Bytes {
		return fmt.Errorf("desktop integration download yielded %d bytes, expected %d", written, pin.Bytes)
	}
	return p.applyPackage(ctx, path, action, run)
}

// A click can pick up the exact prior download. It advances past attempts the
// service confirms failed or sealed and never starts another attempt on its own.
func (p DesktopIntegration) downloadPackage(ctx context.Context, pin PinnedDownload, transfer Transfer) (WorkIdentity, error) {
	key := workKey("desktop-integration-"+string(p.App), Artifact{SHA256: pin.SHA256})
	return downloadPinnedIntegration(ctx, pin, transfer, key)
}

func downloadPinnedIntegration(ctx context.Context, pin PinnedDownload, transfer Transfer, key string) (WorkIdentity, error) {
	work, err := transfer.Reserve(ctx, pin, key)
	if err != nil {
		return WorkIdentity{}, err
	}
	observation, err := transfer.Observe(ctx, work)
	for err == nil && (observation.State == jobwire.WorkStateFailed.String() || observation.State == jobwire.ObservationOutcomeDefinitelyNotAccepted.String()) {
		if err := ctx.Err(); err != nil {
			return WorkIdentity{}, err
		}
		work.Attempt++
		observation, err = transfer.Observe(ctx, work)
	}
	if err != nil {
		return WorkIdentity{}, err
	}
	if observation.State == jobwire.ObservationOutcomeUnknown.String() {
		if err := transfer.Submit(ctx, pin, work); err != nil {
			return WorkIdentity{}, err
		}
		observation, err = transfer.Observe(ctx, work)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for err == nil {
		switch observation.State {
		case jobwire.WorkStateComplete.String():
			return work, nil
		case jobwire.WorkStatePending.String(), jobwire.WorkStateRunning.String(), jobwire.WorkStateTransferred.String(), jobwire.ObservationOutcomeUnknown.String():
		case jobwire.WorkStateFailed.String():
			return WorkIdentity{}, fmt.Errorf("desktop integration download failed: %s", observation.Failure)
		default:
			return WorkIdentity{}, fmt.Errorf("desktop integration download status: %s", observation.State)
		}
		select {
		case <-ctx.Done():
			return WorkIdentity{}, ctx.Err()
		case <-ticker.C:
			observation, err = transfer.Observe(ctx, work)
		}
	}
	return WorkIdentity{}, err
}

func (p DesktopIntegration) applyPackage(ctx context.Context, path, action string, run func(context.Context, string, string) error) (err error) {
	if !filepath.IsAbs(path) || filepath.Base(path) != p.File {
		return errors.New("This build's desktop integration package is unavailable.")
	}
	parent, err := programfiles.HoldDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Release()) }()
	held, err := holdPinnedProgram(ctx, sevenZipPin{Path: path, SHA256: p.SHA256, Bytes: p.Bytes})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, held.Close()) }()
	return run(ctx, held.Path, action)
}
