package setup

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

// Removal derives configured files from the same authenticated native image
// even after an interrupted deletion removed that image from the version.
// The selected producer builds this metadata owner as a CGO-disabled Go image;
// its resources are embedded and its native imports use system DLL owners.
func nativeRemovalReceipt(ctx context.Context, a Artifact, root string, receipt archiveReceipt) (result archiveReceipt, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	original, err := installPath(a)
	if err != nil {
		return result, err
	}
	pending, err := fileid.Pending(ctx, fileid.Self, original)
	if err != nil {
		return result, err
	}
	original = pending.Path()
	var image sevenZipPin
	for _, f := range receipt.Files {
		if f.Path == a.ApplicationProgram {
			image = sevenZipPin{Path: filepath.Join(root, filepath.FromSlash(f.Path)), Bytes: f.Size, SHA256: f.SHA256}
		}
	}
	if image.Path == "" {
		return result, errors.New("expected native image is absent")
	}
	// Prefer the existing exact source image. Recovery material is needed
	// only when an interrupted removal deleted it or a person changed it.
	site, _ := installedPyTorchSite(ctx, root, a)
	installedPlan := func(ctx context.Context, image sevenZipPin, original string) ([]byte, error) {
		if site == "" {
			return nativeArchiveInstalledCommand(ctx, image, original)
		}
		return nativeArchiveInstalledSiteCommand(ctx, image, original, site)
	}
	if verifyInstalledPath(root, a.ApplicationProgram) == nil && verifyFileContext(ctx, image.Path, Artifact{Bytes: image.Bytes, SHA256: image.SHA256}) == nil {
		raw, e := installedPlan(ctx, image, original)
		if e != nil {
			return result, e
		}
		return nativeRemovalPlan(a, receipt, original, raw)
	}
	input, err := openArchiveSource(ctx, receipt.ArchivePath)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, input.Close()) }()
	if err = verifyArchive(ctx, input, a); err != nil {
		return result, err
	}
	reader, err := zip.NewReader(input, a.Bytes)
	if err != nil {
		return result, err
	}
	work, err := os.MkdirTemp(filepath.Dir(receipt.ArchivePath), ".native-removal-")
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(work)) }()
	if err = privatedir.Tighten(work); err != nil {
		return result, err
	}
	image.Path = filepath.Join(work, "owner.exe")
	found := false
	for _, member := range reader.File {
		if member.Name != a.ApplicationProgram {
			continue
		}
		if found {
			return result, errors.New("duplicate native image")
		}
		found = true
		in, e := member.Open()
		if e != nil {
			return result, e
		}
		out, e := os.OpenFile(image.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return result, errors.Join(e, in.Close())
		}
		var n int64
		n, e = io.Copy(out, io.LimitReader(in, image.Bytes+1))
		e = errors.Join(e, in.Close(), out.Close())
		if e != nil {
			return result, e
		}
		if n != image.Bytes {
			return result, errors.New("native image size differs")
		}
	}
	if !found {
		return result, errors.New("native image missing from pinned archive")
	}
	if err = verifyFileContext(ctx, image.Path, Artifact{Bytes: image.Bytes, SHA256: image.SHA256}); err != nil {
		return result, err
	}
	if err = verifyArchive(ctx, input, a); err != nil {
		return result, err
	}
	raw, err := installedPlan(ctx, image, original)
	if err != nil {
		return result, err
	}
	return nativeRemovalPlan(a, receipt, original, raw)
}

// installedPyTorchSite reads the dependency recorded by the native archive.
// An absent file or its unconfigured template names no component.
func installedPyTorchSite(ctx context.Context, root string, a Artifact) (string, error) {
	path := filepath.Join(root, nativeResourceDirectory(a), "standalone-env", "Lib", "site-packages", "oa-pytorch.pth")
	h, err := fileid.Open(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if h.FileID().IsDir() || h.FileID().Links() != 1 {
		return "", errors.Join(errors.New("the installed archive dependency is not an ordinary file"), h.Close())
	}
	const limit = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(h.File(), limit+1))
	if err := errors.Join(err, h.Close()); err != nil {
		return "", err
	}
	if len(raw) > limit {
		return "", errors.New("the installed archive dependency is too large")
	}
	site := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if site != "" || !filepath.IsAbs(line) {
			return "", errors.New("the installed archive dependency is not one absolute path")
		}
		site = line
	}
	return site, nil
}

func nativeRemovalPlan(a Artifact, receipt archiveReceipt, original string, raw []byte) (result archiveReceipt, err error) {
	plan, err := decodeNativeInstallPlanAt(raw, a, original)
	if err != nil {
		return result, err
	}
	result = receipt
	result.Files = append([]archiveFileRecord{}, receipt.Files...)
	dirs := map[string]bool{}
	for _, d := range receipt.Dirs {
		dirs[d] = true
	}
	for _, f := range receipt.Files {
		if identity.SamePathName(f.Path, plan.Directory) || identity.PathNameWithin(f.Path, plan.Directory) {
			return result, errors.New("native archive overlaps external engine")
		}
	}
	dirs[plan.Directory] = true
	for _, f := range plan.Files {
		f.Path = path.Join(plan.Directory, f.Path)
		result.Files = append(result.Files, f)
		for d := path.Dir(f.Path); d != "."; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	result.Dirs = nil
	for d := range dirs {
		result.Dirs = append(result.Dirs, d)
	}
	sort.Strings(result.Dirs)
	return result, nil
}
