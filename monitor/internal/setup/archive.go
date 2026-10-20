package setup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

const archiveChunk = 64 << 10
const archiveMarker = ".oa-install.json"

type archiveFileRecord struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type archiveReceipt struct {
	Format          int                 `json:"format"`
	ArchivePath     string              `json:"archive_path,omitempty"`
	ArtifactSHA256  string              `json:"artifact_sha256"`
	Version         string              `json:"version"`
	InstallIdentity string              `json:"install_identity"`
	Dirs            []string            `json:"dirs"`
	Files           []archiveFileRecord `json:"files"`
}

// InstallArchive installs a catalog-pinned portable ZIP into a new per-user
// destination. The caller selects the fixed catalog artifact and destination;
// archive members supply neither an executable command nor an install path.
func InstallArchive(ctx context.Context, a Artifact, zipPath, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.validate(); err != nil {
		return fmt.Errorf("setup archive: catalog artifact: %w", err)
	}
	if a.Action != "archive_per_user" {
		return errors.New("setup archive: artifact is not a portable archive")
	}
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination || filepath.Base(destination) == "." {
		return errors.New("setup archive: absolute clean destination required")
	}
	// The destination is checked by the path owner before anything is
	// opened or created there: the staging check below asks identity, which
	// opens nothing off a local drive, so a share would be written first and
	// refused after.
	if err := identity.LocalPath(destination); err != nil {
		return fmt.Errorf("setup archive: the destination must be on a local drive: %w", err)
	}
	if err := absent(destination); err != nil {
		return err
	}
	input, err := openArchiveSource(ctx, zipPath)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != a.Bytes {
		return errors.New("setup archive: compressed size differs from catalog")
	}
	if err := verifyArchive(ctx, input, a); err != nil {
		return err
	}
	reader, err := zip.NewReader(input, info.Size())
	if err != nil {
		return fmt.Errorf("setup archive: ZIP: %w", err)
	}
	if a.InstallIdentity == "tn" {
		// The pinned upstream archive uses ./ for its root. Normalize only
		// this fixed package before the ordinary member checks.
		reader.File = normalizedArchiveMembers(a, reader.File)
		if err := theNoiseEnginePath(filepath.Join(destination, "e")); err != nil {
			return err
		}
	}
	if err := validateMembers(reader.File, a.PeakBytes); err != nil {
		return err
	}
	if home, err := CurrentHome(); err == nil {
		if err := markNoBackupClass(home, destination); err != nil {
			return err
		}
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	prefix := "." + filepath.Base(destination) + ".stage-"
	if a.InstallIdentity == "tn" {
		prefix = ".s-"
	}
	stage, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		return err
	}
	stageFile, err := fileid.Open(ctx, fileid.Self, stage, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return err
	}
	stageKey, keyErr := fileid.FileKeyOf(stageFile.File())
	if err := errors.Join(keyErr, stageFile.Close()); err != nil {
		return err
	}
	defer func() {
		if stage != "" {
			cleanArchiveStage(stage, stageKey)
		}
	}()
	// The staging folder must sit directly in parent: its own parent is parent
	// by file identity (identity.SameFile).
	if same, err := identity.SameFile(filepath.Dir(stage), parent); err != nil || !same {
		return errors.New("setup archive: staging directory escaped destination parent")
	}
	if a.NativeResources != nil {
		if err := privatedir.Tighten(stage); err != nil {
			return err
		}
	}
	extractRoot := stage
	if a.InstallIdentity == "tn" {
		extractRoot = filepath.Join(stage, "e")
		if err := theNoiseEnginePath(extractRoot); err != nil {
			return err
		}
	}
	files, dirs, err := extractMembers(ctx, reader.File, extractRoot, a.PeakBytes)
	if err != nil {
		return err
	}
	if a.InstallIdentity == "tn" {
		for i := range files {
			files[i].Path = "e/" + files[i].Path
		}
		for i := range dirs {
			dirs[i] = "e/" + dirs[i]
		}
		dirs = append(dirs, "e")
	}
	if err := writeArchiveReceipt(stage, a, input.Name(), files, dirs); err != nil {
		return err
	}
	// The source may have changed after the first hash. Recheck the same open
	// file before publishing the staged tree.
	if err := verifyArchive(ctx, input, a); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := absent(destination); err != nil {
		return err
	}
	if err := publishArchiveDirectory(ctx, stage, destination, stageKey); err != nil {
		return fmt.Errorf("setup archive: publish staged app: %w", err)
	}
	stage = ""
	return nil
}

// ArchiveInstalled verifies the pinned receipt and every installed archive
// file. An unmarked directory is never treated as an installed app. This is a
// recovery check; large archives can take time to rehash.
func ArchiveInstalled(a Artifact, destination string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return archiveInstalledContext(ctx, a, destination)
}

// ArchiveReceiptMatches reads only the installed receipt: whether the folder
// holds the pinned artifact's receipt, by its SHA-256, version and install
// identity. It reads no package file and runs nothing from the package; a
// display that needs no authority uses it in place of ArchiveInstalled.
func ArchiveReceiptMatches(a Artifact, destination string) (bool, error) {
	_, ok, err := archiveReceiptFor(a, destination)
	return ok, err
}

// treeSampleFiles is how many of the receipt's first files, and how many of
// its programs, a status read checks by size.
const treeSampleFiles = 16

// archiveReceiptAndSizes reads the installed receipt and checks that a small
// fixed sample of the files it lists, its first files and its programs, are
// still regular files of their recorded sizes. It walks no tree and hashes
// nothing, so a status read can afford it for a package of many thousand
// files; ArchiveInstalled hashes on demand. Reason names the first missing or
// changed file.
func archiveReceiptAndSizes(a Artifact, destination string) (ok bool, reason string, err error) {
	receipt, ok, err := archiveReceiptFor(a, destination)
	if err != nil {
		return false, "", err
	}
	if !ok {
		return false, "its install receipt is missing or names another version", nil
	}
	sample := receipt.Files[:min(len(receipt.Files), treeSampleFiles)]
	programs := 0
	for _, file := range receipt.Files[len(sample):] {
		if programs < treeSampleFiles && strings.EqualFold(path.Ext(file.Path), ".exe") {
			sample = append(sample[:len(sample):len(sample)], file)
			programs++
		}
	}
	for _, file := range sample {
		if !safeMemberPath(file.Path) {
			return false, "", errors.New("setup archive: unsafe install receipt")
		}
		if err := verifyInstalledPath(destination, file.Path); errors.Is(err, os.ErrNotExist) {
			return false, file.Path + " is missing", nil
		} else if err != nil {
			return false, "", err
		}
		info, err := os.Lstat(filepath.Join(destination, filepath.FromSlash(file.Path)))
		if err != nil {
			return false, "", err
		}
		if info.Size() != file.Size {
			return false, file.Path + " changed size", nil
		}
	}
	return true, "", nil
}

// archiveReceiptFor reads destination's receipt and reports whether it names
// artifact a.
func archiveReceiptFor(a Artifact, destination string) (archiveReceipt, bool, error) {
	if err := a.validate(); err != nil {
		return archiveReceipt{}, false, err
	}
	if a.Action != "archive_per_user" || !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return archiveReceipt{}, false, errors.New("setup archive: invalid installed archive target")
	}
	if err := identity.LocalPath(destination); err != nil {
		return archiveReceipt{}, false, fmt.Errorf("setup archive: the installed target must be on a local drive: %w", err)
	}
	root, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return archiveReceipt{}, false, nil
	}
	if err != nil {
		return archiveReceipt{}, false, err
	}
	if !root.IsDir() || root.Mode()&os.ModeSymlink != 0 {
		return archiveReceipt{}, false, errors.New("setup archive: installed target is not a directory")
	}
	markerPath := filepath.Join(destination, archiveMarker)
	markerInfo, err := os.Lstat(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		return archiveReceipt{}, false, nil
	}
	if err != nil {
		return archiveReceipt{}, false, err
	}
	if !markerInfo.Mode().IsRegular() || markerInfo.Size() <= 0 || markerInfo.Size() > 16<<20 {
		return archiveReceipt{}, false, errors.New("setup archive: invalid install receipt")
	}
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return archiveReceipt{}, false, err
	}
	var receipt archiveReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return archiveReceipt{}, false, fmt.Errorf("setup archive: invalid receipt: %w", err)
	}
	if receipt.Format != 2 || !strings.EqualFold(receipt.ArtifactSHA256, a.SHA256) ||
		receipt.Version != a.Version || receipt.InstallIdentity != a.InstallIdentity || len(receipt.Files) == 0 || len(receipt.Files) > 100000 {
		return archiveReceipt{}, false, nil
	}
	return receipt, true, nil
}

func archiveInstalledContext(ctx context.Context, a Artifact, destination string) (bool, error) {
	receipt, ok, err := archiveReceiptFor(a, destination)
	if err != nil || !ok {
		return false, err
	}
	if err := receiptHasExpectedRoster(ctx, a, receipt); err != nil {
		return false, err
	}
	seen := make(map[string]bool, len(receipt.Files)+len(receipt.Dirs)+1)
	expected := map[string]bool{archiveMarker: false}
	if len(receipt.Dirs) > 100000 {
		return false, errors.New("setup archive: install receipt has too many directories")
	}
	for _, dir := range receipt.Dirs {
		if !safeMemberPath(dir) || foldName(dir) == foldName(archiveMarker) || seen[foldName(dir)] {
			return false, errors.New("setup archive: unsafe install receipt directory")
		}
		seen[foldName(dir)] = true
		expected[dir] = true
	}
	var total int64
	for _, file := range receipt.Files {
		if !safeMemberPath(file.Path) || foldName(file.Path) == foldName(archiveMarker) || file.Size < 0 || file.Size > a.PeakBytes-total ||
			len(file.SHA256) != 64 || seen[foldName(file.Path)] {
			return false, errors.New("setup archive: unsafe install receipt")
		}
		seen[foldName(file.Path)] = true
		expected[file.Path] = false
		total += file.Size
		target := filepath.Join(destination, filepath.FromSlash(file.Path))
		if err := verifyInstalledPath(destination, file.Path); errors.Is(err, os.ErrNotExist) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		input, err := os.Open(target)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		info, statErr := input.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() != file.Size {
			input.Close()
			return false, nil
		}
		h := sha256.New()
		_, hashErr := io.Copy(h, input)
		closeErr := input.Close()
		if hashErr != nil {
			return false, hashErr
		}
		if closeErr != nil {
			return false, closeErr
		}
		if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), file.SHA256) {
			return false, nil
		}
	}
	if a.NativeResources != nil {
		if err := nativeInstalledInventory(ctx, a, destination, receipt.ArchivePath, expected); err != nil {
			return false, err
		}
	}
	if err := exactArchiveTree(destination, expected); errors.Is(err, errArchiveTreeMismatch) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

var errArchiveTreeMismatch = errors.New("setup archive: installed tree differs from receipt")

func exactArchiveTree(root string, expected map[string]bool) error {
	visited := make(map[string]bool, len(expected))
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		wantDir, ok := expected[rel]
		if !ok || visited[rel] {
			return errArchiveTreeMismatch
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || info.IsDir() != wantDir || !wantDir && !info.Mode().IsRegular() {
			return errArchiveTreeMismatch
		}
		visited[rel] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(visited) != len(expected) {
		return errArchiveTreeMismatch
	}
	return nil
}

func verifyInstalledPath(root, name string) error {
	current := root
	parts := strings.Split(name, "/")
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || i < len(parts)-1 && !info.IsDir() || i == len(parts)-1 && !info.Mode().IsRegular() {
			return errors.New("setup archive: installed path changed type")
		}
	}
	return nil
}

func writeArchiveReceipt(stage string, a Artifact, source string, files []archiveFileRecord, dirs []string) error {
	if len(files) == 0 {
		return errors.New("setup archive: no installed files")
	}
	if a.NativeResources != nil {
		if err := validateNativeMarkerBudget(a, files, dirs); err != nil {
			return err
		}
		if a.NativeResources.MarkerBytes+nativeArchivePathBytes(source) > 16<<20 {
			return errors.New("native install receipt exceeds writer bound")
		}
	}
	marker, err := os.OpenFile(filepath.Join(stage, archiveMarker), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	receipt := archiveReceipt{Format: 2, ArchivePath: source, ArtifactSHA256: a.SHA256, Version: a.Version, InstallIdentity: a.InstallIdentity, Dirs: dirs, Files: files}
	encodeErr := json.NewEncoder(marker).Encode(receipt)
	if encodeErr == nil {
		var info os.FileInfo
		info, encodeErr = marker.Stat()
		if encodeErr == nil && info.Size() > 16<<20 {
			encodeErr = errors.New("setup archive: install receipt exceeds limit")
		}
	}
	if encodeErr == nil {
		encodeErr = marker.Sync()
	}
	closeErr := marker.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}

func absent(destination string) error {
	_, err := os.Lstat(destination)
	if err == nil {
		return errors.New("setup archive: destination already exists")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func verifyArchive(ctx context.Context, input *os.File, a Artifact) error {
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	buf := make([]byte, archiveChunk)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := input.Read(buf)
		if n > 0 {
			size += int64(n)
			if size > a.Bytes {
				return errors.New("setup archive: compressed size exceeds catalog")
			}
			_, _ = h.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if size != a.Bytes || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), a.SHA256) {
		return errors.New("setup archive: compressed size or SHA-256 differs from catalog")
	}
	return nil
}

func validateMembers(files []*zip.File, limit int64) error {
	if len(files) == 0 || len(files) > 100000 || limit < 1 {
		return errors.New("setup archive: empty archive or invalid expansion limit")
	}
	type node struct{ directory, explicit bool }
	nodes := make(map[string]node, len(files))
	var declared int64
	for _, f := range files {
		name, directory, err := memberName(f)
		if err != nil {
			return err
		}
		parts := strings.Split(name, "/")
		for i := range parts {
			key := foldName(strings.Join(parts[:i+1], "/"))
			wantDir := i < len(parts)-1 || directory
			prior, exists := nodes[key]
			if exists && (prior.directory != wantDir || i == len(parts)-1 && prior.explicit) {
				return fmt.Errorf("setup archive: duplicate or file/directory collision at %q", f.Name)
			}
			prior.directory = wantDir
			if i == len(parts)-1 {
				prior.explicit = true
			}
			nodes[key] = prior
		}
		if !directory {
			if f.UncompressedSize64 > uint64(limit-declared) {
				return errors.New("setup archive: declared expansion exceeds catalog")
			}
			declared += int64(f.UncompressedSize64)
		}
	}
	return nil
}

func memberName(f *zip.File) (name string, directory bool, err error) {
	name = f.Name
	directory = strings.HasSuffix(name, "/")
	name = strings.TrimSuffix(name, "/")
	mode := f.Mode()
	if !safeMemberPath(name) || foldName(name) == foldName(archiveMarker) || mode&os.ModeType != 0 && !mode.IsDir() ||
		f.ExternalAttrs&(0x400<<16) != 0 || f.ExternalAttrs&0x400 != 0 || f.Flags&1 != 0 ||
		mode.IsDir() != directory {
		return "", false, fmt.Errorf("setup archive: unsafe member %q", f.Name)
	}
	return name, directory, nil
}

func safeMemberPath(name string) bool {
	if !utf8.ValidString(name) || name == "" || path.IsAbs(name) || filepath.IsAbs(name) || strings.ContainsAny(name, "\\:\x00") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || invalidWindowsPart(part) {
			return false
		}
	}
	return true
}

func invalidWindowsPart(part string) bool {
	for _, r := range part {
		if r < 32 || strings.ContainsRune(`<>"|?*`, r) {
			return true
		}
	}
	base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" {
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9'
}

// foldName keys an archive member or version name for the collision checks
// before placement, as the file system compares names
// (identity.PathNameKey); Unicode case folding is looser than NTFS.
func foldName(name string) string { return identity.PathNameKey(name) }

func extractMembers(ctx context.Context, files []*zip.File, stage string, limit int64) ([]archiveFileRecord, []string, error) {
	var total int64
	records := make([]archiveFileRecord, 0, len(files))
	directorySet := map[string]bool{}
	buf := make([]byte, archiveChunk)
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		name := strings.TrimSuffix(f.Name, "/")
		parts := strings.Split(name, "/")
		for i := 0; i < len(parts)-1; i++ {
			directorySet[strings.Join(parts[:i+1], "/")] = true
		}
		target := filepath.Join(stage, filepath.FromSlash(name))
		if strings.HasSuffix(f.Name, "/") {
			directorySet[name] = true
			if err := os.MkdirAll(target, 0o700); err != nil {
				return nil, nil, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, nil, err
		}
		member, err := f.Open()
		if err != nil {
			return nil, nil, err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			member.Close()
			return nil, nil, err
		}
		start := total
		h := sha256.New()
		copyErr := copyBounded(ctx, io.MultiWriter(output, h), member, buf, &total, limit)
		closeErr := output.Close()
		memberErr := member.Close()
		if copyErr != nil {
			return nil, nil, copyErr
		}
		if closeErr != nil {
			return nil, nil, closeErr
		}
		if memberErr != nil {
			return nil, nil, memberErr
		}
		records = append(records, archiveFileRecord{Path: name, Size: total - start, SHA256: hex.EncodeToString(h.Sum(nil))})
	}
	dirs := make([]string, 0, len(directorySet))
	for dir := range directorySet {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return records, dirs, nil
}

func copyBounded(ctx context.Context, output io.Writer, input io.Reader, buf []byte, total *int64, limit int64) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := input.Read(buf)
		if n > 0 {
			if int64(n) > limit-*total {
				return errors.New("setup archive: expanded bytes exceed catalog")
			}
			written, err := output.Write(buf[:n])
			*total += int64(written)
			if err != nil {
				return err
			}
			if written != n {
				return io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
