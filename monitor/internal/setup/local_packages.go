package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/openabstractions/abstraction-facade/go/client"
)

// LocalPackageFolder names a folder of OpenAbstractions release assets on this
// computer. A private preview build sets it at link time
// (-X .../monitor/internal/setup.LocalPackageFolder=<folder>), so Setup
// installs applications and their components from packages built beside
// the installer before publication. The folder's adopter_assets.json
// pins those packages in the catalogue. An asset is taken from the folder only
// under its release name and exact pinned size; the runner rehashes the copied
// bytes against the catalogue pin like any download. A missing file falls
// through to the public download service.
var LocalPackageFolder string

// ReleaseFolderVariable names the environment variable a pre-release check of
// a signed build sets before starting the Panel. It names the folder holding
// the release's assets, and Setup takes packages from it as from
// LocalPackageFolder. The pin document stays the build's own.
const ReleaseFolderVariable = "OA_RELEASE_FOLDER"

// packageFolder is the linked preview folder, else the release folder the
// environment names; "" when neither is an absolute path.
func packageFolder() string {
	folder := LocalPackageFolder
	if folder == "" {
		folder = os.Getenv(ReleaseFolderVariable)
	}
	if folder == "" || !filepath.IsAbs(folder) {
		return ""
	}
	return filepath.Clean(folder)
}

// LocalPackageAssetsSHA256 is the SHA-256 the preview build linked for the
// folder's adopter_assets.json. Setup ignores a document with other bytes.
var LocalPackageAssetsSHA256 string

// localPackageAssetsName is the adopter pin document a package folder carries
// for its packages. Setup's catalogue projects it over the embedded pins.
const localPackageAssetsName = "adopter_assets.json"

func localPackageAssets() []byte {
	if LocalPackageFolder == "" || !filepath.IsAbs(LocalPackageFolder) {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(LocalPackageFolder, localPackageAssetsName))
	if err != nil || len(raw) > 1<<20 {
		return nil
	}
	sum := sha256.Sum256(raw)
	if LocalPackageAssetsSHA256 == "" || hex.EncodeToString(sum[:]) != LocalPackageAssetsSHA256 {
		return nil
	}
	return raw
}

// localPackageOwner marks a journalled transfer served from LocalPackageFolder.
const localPackageOwner = "oa-local-package"

// localPackage returns the folder file for an OpenAbstractions release asset
// with the pin's exact size, or "" when the folder does not hold it.
func localPackage(p PinnedDownload) string {
	folder := packageFolder()
	if folder == "" {
		return ""
	}
	name, ok := releaseAssetName(p.URL)
	if !ok {
		return ""
	}
	file := filepath.Join(folder, name)
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() != p.Bytes {
		return ""
	}
	return file
}

// releaseAssetName is the file name of an OpenAbstractions release asset
// URL, the only packages a preview build's folder supplies.
func releaseAssetName(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || !strings.HasPrefix(u.Path, "/openabstractions/") || !strings.Contains(u.Path, "/releases/download/") {
		return "", false
	}
	name := path.Base(u.Path)
	return name, validArchiveMember(name)
}

func localPackageWork(key, file string) WorkIdentity {
	binding := client.JobsBinding{Provider: localPackageOwner, Endpoint: file, LogicalOwner: localPackageOwner}
	return WorkIdentity{Key: key, HistoryEpoch: localPackageOwner, Binding: binding, OperationsBinding: binding}
}

func isLocalPackageWork(work WorkIdentity) bool {
	return work.Binding.LogicalOwner == localPackageOwner
}

func localPackageFile(work WorkIdentity) (*os.File, os.FileInfo, error) {
	file := work.OperationsBinding.Endpoint
	if folder := packageFolder(); folder == "" || !filepath.IsAbs(file) || filepath.Dir(file) != folder {
		return nil, nil, fmt.Errorf("local package %s is outside this build's package folder", file)
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, errors.Join(err, errors.New("local package is not a regular file"))
	}
	return f, info, nil
}

func observeLocalPackage(work WorkIdentity) TransferObservation {
	f, info, err := localPackageFile(work)
	if err != nil {
		return TransferObservation{State: "failed", Failure: "local package: " + err.Error()}
	}
	f.Close()
	return TransferObservation{State: "complete", Done: info.Size(), Total: info.Size(), Operation: localPackageOwner}
}

func copyLocalPackage(ctx context.Context, work WorkIdentity, dst io.Writer) (int64, error) {
	f, _, err := localPackageFile(work)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(dst, readerContext{ctx, f})
}

type readerContext struct {
	ctx context.Context
	r   io.Reader
}

func (r readerContext) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
