package setup

import (
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
)

// NativeArchiveProgram checks the exact source-owned image before a confirmed
// uninstall removes its registration. Changed backend/user files carry no
// registration authority and are preserved by the ordinary removal owner.
func NativeArchiveProgram(ctx context.Context, a Artifact, root string) (string, error) {
	if a.NativeResources == nil {
		return "", errors.New("native archive source required")
	}
	if err := verifyInstalledPath(root, archiveMarker); err != nil {
		return "", err
	}
	f, err := os.Open(filepath.Join(root, archiveMarker))
	if err != nil {
		return "", err
	}
	raw, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	err = errors.Join(err, f.Close())
	if err != nil {
		return "", err
	}
	if len(raw) > 16<<20 {
		return "", errors.New("native archive receipt exceeds bound")
	}
	var receipt archiveReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return "", err
	}
	if receipt.ArtifactSHA256 != a.SHA256 || receipt.Version != a.Version || receipt.InstallIdentity != a.InstallIdentity {
		return "", errors.New("native archive receipt source differs")
	}
	if err := receiptHasExpectedRoster(ctx, a, receipt); err != nil {
		return "", err
	}
	image, _, _, err := nativeImagePin(ctx, a, root, receipt.ArchivePath)
	if err != nil {
		return "", err
	}
	if err := verifyInstalledPath(root, a.ApplicationProgram); err != nil {
		return "", err
	}
	if err := verifyFileContext(ctx, image.Path, Artifact{Bytes: image.Bytes, SHA256: image.SHA256}); err != nil {
		return "", err
	}
	return image.Path, nil
}

type installedNativeArchiveHost struct {
	artifact Artifact
	root     string
	program  sevenZipPin
	files    []archiveFileRecord
}

func (archive retainedNativeArchive) approvedArtifact(ctx context.Context, journal Journal) (Artifact, error) {
	state, err := journal.Load(ctx)
	if err != nil {
		return Artifact{}, err
	}
	return journal.ComponentArtifact(state, archive.component)
}

func (archive retainedNativeArchive) usesNativeResources(ctx context.Context, j Journal) (bool, error) {
	artifact, err := archive.approvedArtifact(ctx, j)
	if err != nil {
		return false, err
	}
	return artifact.NativeResources != nil, nil
}

// installedHost binds the journal's reviewed component
// to a complete installation and the exact program from its pinned ZIP.
func (archive retainedNativeArchive) installedHost(ctx context.Context, j Journal) (installedNativeArchiveHost, error) {
	a, err := archive.approvedArtifact(ctx, j)
	if err != nil {
		return installedNativeArchiveHost{}, err
	}
	if a.NativeResources == nil {
		return installedNativeArchiveHost{}, errors.New("this " + archive.title + " package registers through its descriptor")
	}
	root, err := installPath(a)
	if err != nil {
		return installedNativeArchiveHost{}, err
	}
	installed, err := archiveInstalledContext(ctx, a, root)
	if err != nil {
		return installedNativeArchiveHost{}, err
	}
	if !installed {
		return installedNativeArchiveHost{}, errors.New(archive.title + " is not completely installed; check and recover it in Catalog first")
	}
	return resolveArchiveHostImage(ctx, a, root)
}

func resolveArchiveHostImage(ctx context.Context, a Artifact, root string) (installedNativeArchiveHost, error) {
	receipt, err := nativeInstalledReceipt(ctx, a, root)
	if err != nil {
		return installedNativeArchiveHost{}, err
	}
	program, files, _, err := nativeImagePin(ctx, a, root, receipt.ArchivePath)
	if err != nil {
		return installedNativeArchiveHost{}, err
	}
	return installedNativeArchiveHost{artifact: a, root: root, program: program, files: files}, nil
}

// resealNativeArchiveRuntimeSite revalidates the installed host image and
// its ZIP roster before the native helper changes its runtime site.
func resealNativeArchiveRuntimeSite(ctx context.Context, a Artifact, site string) error {
	if a.NativeResources == nil {
		return nil
	}
	root, err := installPath(a)
	if err != nil {
		return err
	}
	host, err := resolveArchiveHostImage(ctx, a, root)
	if err != nil {
		return err
	}
	if err := verifyNativeHostFiles(ctx, a, root, host.files); err != nil {
		return err
	}
	_, err = nativeArchiveSealCommand(ctx, host.program, site)
	return err
}

func (archive retainedNativeArchive) registerSelf(ctx context.Context, j Journal) error {
	host, err := archive.installedHost(ctx, j)
	if err != nil {
		return err
	}
	if err := verifyNativeHostFiles(ctx, host.artifact, host.root, host.files); err != nil {
		return err
	}
	_, err = nativeArchiveCommand(ctx, host.program, nativeArchiveRegister)
	return err
}

// HeldProgram is an installed program Setup holds open while it re-pins the
// app's entry to it: no process can write the file until Close, and its
// bytes were checked against the package's receipt through this handle.
type HeldProgram struct {
	Path   string
	SHA256 string
	file   *os.File
}

// Close releases the program.
func (h HeldProgram) Close() error {
	if h.file == nil {
		return nil
	}
	return h.file.Close()
}

func (archive retainedNativeArchive) holdProgram(ctx context.Context, j Journal) (HeldProgram, error) {
	host, err := archive.installedHost(ctx, j)
	if err != nil {
		return HeldProgram{}, err
	}
	if err := verifyInstalledPath(host.root, host.artifact.ApplicationProgram); err != nil {
		return HeldProgram{}, err
	}
	return holdPinnedProgram(ctx, host.program)
}

// holdPinnedProgram opens pin's file excluding writers and checks its size
// and SHA-256 through that handle.
func holdPinnedProgram(ctx context.Context, pin sevenZipPin) (HeldProgram, error) {
	file, err := openWriteExcluded(pin.Path)
	if err != nil {
		return HeldProgram{}, err
	}
	held := HeldProgram{Path: pin.Path, SHA256: strings.ToLower(pin.SHA256), file: file}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("the installed program is not a regular file")
	}
	if err == nil && info.Size() != pin.Bytes {
		err = errPinnedSize
	}
	if err == nil {
		sum := sha256.New()
		var total int64
		if err = copyBounded(ctx, sum, file, make([]byte, archiveChunk), &total, pin.Bytes); err == nil && !strings.EqualFold(hex.EncodeToString(sum.Sum(nil)), pin.SHA256) {
			err = errPinnedDigest
		}
	}
	if err != nil {
		return HeldProgram{}, errors.Join(fmt.Errorf("the installed program does not match its package: %w", err), held.Close())
	}
	return held, nil
}
