package setup

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
)

// Facts the Setup page shows beside its ticks that no plan carries: the
// release's version, an installed package's size and folder, and whether
// the download archive already holds a download.

// ReleaseVersion is this release's version, such as 0.8.0: the tag the
// release's adopter pins name.
func ReleaseVersion() (string, error) {
	pins, err := parseAdopterAssets(adopterAssetsJSON)
	return strings.TrimPrefix(pins.ReleaseTag, "v"), err
}

// InstalledSize is the folder an installed package a occupies and the bytes
// its install receipt records. It reads the receipt alone: it hashes no
// file, so the Setup page's read stays quick. A package without a portable
// archive receipt reports an error.
func InstalledSize(a Artifact) (folder string, bytes int64, err error) {
	if a.NativeResources != nil {
		// A native application's receipt names its front files; its backend
		// expands beside them unrecorded, so the receipt's sum is no size.
		return "", 0, errors.New("a native application's receipt records no size")
	}
	folder, err = installPath(a)
	if err != nil {
		return "", 0, err
	}
	receipt, ok, err := archiveReceiptFor(a, folder)
	if err != nil {
		return "", 0, err
	}
	if !ok {
		return "", 0, errors.New("the installed package has no receipt for this version")
	}
	for _, file := range receipt.Files {
		bytes += file.Size
	}
	return filepath.Clean(folder), bytes, nil
}

// InstalledEngineOverview reports the core provider fallback and its starter
// model readiness for a choice the Catalog does not offer as a package.
func InstalledEngineOverview(ctx context.Context, journal Journal, engine AppID) (installed, ready bool) {
	recipe, ok := pictureRecipeFor(engine)
	if !ok || !recipe.installedFallback {
		return false, false
	}
	feature, err := EngineFeatureStatus(engine)
	if err != nil || !feature.Installed {
		return false, false
	}
	ready, err = starterPictureConfigured(ctx, journal, engine)
	if err != nil {
		log.Printf("setup: %s picture model journal: %v", ApplicationName(engine), err)
	}
	return true, ready
}

// Downloads are the pinned downloads of plan's assets.
func (p ModelPlan) Downloads() []PinnedDownload {
	downloads := make([]PinnedDownload, 0, len(p.Assets))
	for _, asset := range p.Assets {
		downloads = append(downloads, asset.Download)
	}
	return downloads
}
