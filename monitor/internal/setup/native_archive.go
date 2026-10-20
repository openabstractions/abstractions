package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openabstractions/abstraction-facade/go/supervision"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// These are the existing release-owned resource pins carried with the host.
// The host's signed resource owns the decoded installation roster.
type nativeArchiveResources struct {
	Backend     adopterBackendResource `json:"backend"`
	Source      adopterSourceOffer     `json:"source"`
	RecordBytes int64                  `json:"record_bytes"`
	MarkerBytes int64                  `json:"marker_bytes"`
	RecordRoots []string               `json:"record_roots"`
}

func (a Artifact) validateNativeResources() error {
	if a.NativeResources == nil {
		if a.ApplicationProgram != "" || a.ServiceProgram != "" || a.ArchiveToolProgram != "" {
			return errors.New("native programs require their resource pins")
		}
		return nil
	}
	if a.Action != "archive_per_user" || a.Platform != "windows/amd64" || !safeMemberPath(a.ApplicationProgram) || !safeMemberPath(a.ServiceProgram) || !safeMemberPath(a.ArchiveToolProgram) || a.ApplicationProgram == a.ServiceProgram || a.ApplicationProgram == a.ArchiveToolProgram || a.RecordsSHA256 != "" {
		return errors.New("invalid native archive roles")
	}
	r := a.NativeResources
	if r.RecordBytes <= 0 || r.RecordBytes > a.PeakBytes || r.MarkerBytes <= 0 || r.MarkerBytes > 16<<20 || len(r.RecordRoots) != 2 {
		return errors.New("native archive lacks its bounded record budget")
	}
	seenRoots := map[string]bool{}
	for _, root := range r.RecordRoots {
		if !safeMemberPath(root) || strings.ContainsAny(root, "/\\") || seenRoots[foldName(root)] {
			return errors.New("invalid native record root")
		}
		seenRoots[foldName(root)] = true
	}
	if !seenRoots[foldName(nativeResourceDirectory(a))] {
		return errors.New("native engine record root differs from the declared relay program")
	}
	if nativeResourceDirectory(a) == "" {
		return errors.New("relay program must be inside a separate resource root")
	}
	if !r.Source.DataRelease.valid() || r.Source.DataRelease != r.Backend.DataRelease {
		return errors.New("native resource source release differs")
	}
	if err := r.Source.Archive.validate("zip", map[string]bool{}); err != nil {
		return err
	}
	if err := r.Backend.Archive.validate("7z", map[string]bool{}); err != nil {
		return err
	}
	if !r.Backend.Provenance.Manifest.valid() || r.Backend.Provenance.Manifest.Bytes > 16<<20 {
		return errors.New("invalid native manifest pin")
	}
	if err := validateBackendResource(r.Backend); err != nil {
		return err
	}
	return nil
}

type nativeInstallPlan struct {
	InstalledRoot string `json:"installed_root,omitempty"`
	Directory     string `json:"directory"`
	Manifest      struct {
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	} `json:"manifest"`
	Files []archiveFileRecord `json:"files"`
	Dirs  []string            `json:"dirs"`
}

func decodeNativeInstallPlan(raw []byte, a Artifact) (nativeInstallPlan, error) {
	return decodeNativeInstallPlanAt(raw, a, "")
}

func decodeNativeInstallPlanAt(raw []byte, a Artifact, originalRoot string) (nativeInstallPlan, error) {
	var plan nativeInstallPlan
	if len(raw) == 0 || len(raw) > 16<<20 || a.NativeResources == nil {
		return plan, errors.New("invalid native installation plan bound")
	}
	keys := json.NewDecoder(bytes.NewReader(raw))
	if err := checkJSONKeys(keys, nil); err != nil {
		return plan, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		return plan, err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return plan, errors.New("native plan has trailing content")
	}
	if plan.InstalledRoot != originalRoot {
		return plan, errors.New("native plan differs from the bound original install root")
	}
	if !safeMemberPath(plan.Directory) || strings.ContainsAny(plan.Directory, "/\\") || len(plan.Files) == 0 || len(plan.Files) > 100000 {
		return plan, errors.New("invalid native plan paths or inventory")
	}
	if plan.Directory != nativeResourceDirectory(a) {
		return plan, errors.New("native resource root differs from the catalogue relay program")
	}
	pin := a.NativeResources.Backend.Provenance.Manifest
	if plan.Manifest.Bytes != pin.Bytes || plan.Manifest.SHA256 != pin.SHA256 {
		return plan, errors.New("native own manifest differs from catalog resource pin")
	}
	seen := map[string]bool{}
	manifest := false
	var total int64
	for _, f := range plan.Files {
		key := foldName(f.Path)
		if !safeMemberPath(f.Path) || seen[key] || f.Size < 0 || !sha256Hex.MatchString(f.SHA256) || f.Size > a.PeakBytes-total {
			return plan, errors.New("invalid native file inventory")
		}
		seen[key] = true
		total += f.Size
		if f.Path == "manifest.json" {
			manifest = f.Size == pin.Bytes && f.SHA256 == pin.SHA256
		}
	}
	if !manifest {
		return plan, errors.New("native plan lacks exact manifest member")
	}
	return plan, nil
}

func nativeImagePin(ctx context.Context, a Artifact, root, source string) (sevenZipPin, []archiveFileRecord, []string, error) {
	files, dirs, err := expectedArchiveRoster(ctx, a, source)
	if err != nil {
		return sevenZipPin{}, nil, nil, err
	}
	for _, f := range files {
		if f.Path == a.ApplicationProgram {
			return sevenZipPin{Path: filepath.Join(root, filepath.FromSlash(f.Path)), Bytes: f.Size, SHA256: f.SHA256}, files, dirs, nil
		}
	}
	return sevenZipPin{}, nil, nil, errors.New("application role is absent from the expected host ZIP")
}

// Native dependencies include foreign loaders with MAX_PATH-sized inputs.
// Count the complete authenticated roster at its actual future spelling,
// before extraction. This creates no path or alternate installation root.
func nativeArchivePathBudget(ctx context.Context, root string, files []archiveFileRecord) error {
	if err := identity.LocalPath(root); err != nil {
		return err
	}
	pending, err := fileid.Pending(ctx, fileid.Self, root)
	if err != nil {
		return err
	}
	for _, f := range files {
		if !safeMemberPath(f.Path) {
			return errors.New("native resource has an unsafe member path")
		}
		length := supervision.UTF16Len(filepath.Join(pending.Path(), filepath.FromSlash(f.Path)))
		if length > supervision.MaxLibraryPath {
			return fmt.Errorf("native installed resource path exceeds %d UTF-16 units: %s (%d)", supervision.MaxLibraryPath, f.Path, length)
		}
	}
	return nil
}

// installNativeResources runs only after the pinned host ZIP has been installed.
// A failure leaves the existing journal's Installing/Uncertain outcome intact.
func installNativeResources(ctx context.Context, a Artifact, root, source string, parts []sevenZipPin) error {
	return installNativeResourcesFor(ctx, a, root, source, parts, "")
}

// installNativeResourcesFor seals with oa-pytorch.pth naming site, the
// installed PyTorch component's site-packages, when site is given.
func installNativeResourcesFor(ctx context.Context, a Artifact, root, source string, parts []sevenZipPin, site string) error {
	host, files, _, err := nativeImagePin(ctx, a, root, source)
	if err != nil {
		return err
	}
	if err := verifyNativeHostFiles(ctx, a, root, files); err != nil {
		return err
	}
	raw, err := nativeArchiveCommand(ctx, host, nativeArchivePlan)
	if err != nil {
		return err
	}
	plan, err := decodeNativeInstallPlan(raw, a)
	if err != nil {
		return err
	}
	if err := nativeArchivePathBudget(ctx, filepath.Join(root, plan.Directory), plan.Files); err != nil {
		return err
	}
	if err := validateNativeRecordBudget(a, files, plan); err != nil {
		return err
	}
	var tool sevenZipPin
	for _, f := range files {
		if identity.SamePathName(f.Path, plan.Directory) || identity.PathNameWithin(f.Path, plan.Directory) {
			return errors.New("host ZIP overlaps external native resource")
		}
		if f.Path == a.ArchiveToolProgram {
			tool = sevenZipPin{Path: filepath.Join(root, filepath.FromSlash(f.Path)), Bytes: f.Size, SHA256: f.SHA256}
		}
	}
	if tool.Path == "" {
		return errors.New("native decoder role is absent from expected host ZIP")
	}
	backend := a.NativeResources.Backend.Archive
	if err := nativeArchiveExtract(ctx, sevenZipArchive{Tool: tool, Parts: parts, ArchiveBytes: backend.Bytes, ArchiveSHA256: backend.SHA256, Files: plan.Files, Destination: filepath.Join(root, plan.Directory), MaxExpanded: a.PeakBytes, MaxMembers: 100000, Timeout: 10 * time.Minute}); err != nil {
		return err
	}
	// The tree was just extracted and is not sealed yet. The host skips the
	// seal when a seal record already exists, so Setup deletes any old record
	// first. An old record comes from a partial install that Try again
	// removed, or from an earlier install of the same backend.
	if err := dropNativeSealRecord(a); err != nil {
		return err
	}
	if site == "" {
		_, err = nativeArchiveCommand(ctx, host, nativeArchiveSeal)
	} else {
		_, err = nativeArchiveSealCommand(ctx, host, site)
	}
	if err != nil {
		return err
	}
	_, err = nativeArchiveCommand(ctx, host, nativeArchiveCheck)
	return err
}

var nativeArchiveExtract = extractSevenZip // focused inert archive-effect seam

// dropNativeSealRecord removes the host's seal record before Setup seals a
// freshly extracted tree. An installed tree's next start hashes it whole and
// writes the record again.
func dropNativeSealRecord(a Artifact) error {
	name, err := nativeSealRecord(a)
	if err != nil {
		return err
	}
	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("the earlier %s seal record could not be removed: %w", nativeArchiveAppTitle, err)
	}
	return nil
}

// The sealed native owner supplies its actual mutable user directory. Setup
// writes the existing image-policy record there, after complete verification.
func nativeArchiveUserDirectory(ctx context.Context, a Artifact, root string) (string, error) {
	receipt, err := nativeInstalledReceipt(ctx, a, root)
	if err != nil {
		return "", err
	}
	host, files, _, err := nativeImagePin(ctx, a, root, receipt.ArchivePath)
	if err != nil {
		return "", err
	}
	if err := verifyNativeHostFiles(ctx, a, root, files); err != nil {
		return "", err
	}
	raw, err := nativeArchiveCommand(ctx, host, nativeArchiveCheck)
	if err != nil {
		return "", err
	}
	var metadata struct {
		UserDirectory string `json:"user_directory"`
	}
	keys := json.NewDecoder(bytes.NewReader(raw))
	if err := checkJSONKeys(keys, nil); err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&metadata); err != nil {
		return "", err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return "", errors.New("native check metadata has trailing content")
	}
	user := metadata.UserDirectory
	if err := nativeUserDirectorySeparation(user, root); err != nil {
		return "", err
	}
	if err := privatedir.CheckDir(user); err != nil {
		return "", err
	}
	return user, nil
}

// nativeInstalledReceipt reads the installed native package's bounded
// receipt and requires its roster to be the catalogue artifact's.
func nativeInstalledReceipt(ctx context.Context, a Artifact, root string) (archiveReceipt, error) {
	var receipt archiveReceipt
	if err := verifyInstalledPath(root, archiveMarker); err != nil {
		return receipt, err
	}
	f, err := os.Open(filepath.Join(root, archiveMarker))
	if err != nil {
		return receipt, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err = errors.Join(readErr, f.Close()); err != nil {
		return receipt, err
	}
	if len(raw) > 16<<20 {
		return receipt, errors.New("native install receipt exceeds bound")
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return receipt, err
	}
	return receipt, receiptHasExpectedRoster(ctx, a, receipt)
}

func nativeUserDirectorySeparation(user, root string) error {
	if !filepath.IsAbs(user) || filepath.Clean(user) != user {
		return errors.New("native user directory is not a separate local path")
	}
	_, inside, err := identity.InsideRel(user, root)
	if err != nil {
		return fmt.Errorf("native user directory is not a separate local path: %w", err)
	}
	if inside {
		return errors.New("native user directory is not a separate local path")
	}
	return nil
}

func verifyNativeHostFiles(ctx context.Context, a Artifact, root string, files []archiveFileRecord) error {
	expected := map[string]bool{archiveMarker: false}
	for _, f := range files {
		expected[f.Path] = false
		for dir := path.Dir(f.Path); dir != "."; dir = path.Dir(dir) {
			expected[dir] = true
		}
		if err := verifyInstalledPath(root, f.Path); err != nil {
			return err
		}
		if err := verifyFileContext(ctx, filepath.Join(root, filepath.FromSlash(f.Path)), Artifact{Bytes: f.Size, SHA256: f.SHA256}); err != nil {
			return fmt.Errorf("native host member %s: %w", f.Path, err)
		}
	}
	resource := nativeResourceDirectory(a)
	return filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if current == root {
			return nil
		}
		rel, inside, err := identity.InsideRel(current, root)
		if err != nil {
			return err
		}
		if !inside {
			return errArchiveTreeMismatch
		}
		rel = filepath.ToSlash(rel)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return errArchiveTreeMismatch
		}
		if identity.SamePathName(rel, resource) {
			if !info.IsDir() {
				return errArchiveTreeMismatch
			}
			return filepath.SkipDir
		}
		directory, ok := expected[rel]
		if !ok || directory != info.IsDir() {
			return errArchiveTreeMismatch
		}
		return nil
	})
}

func nativeResourceDirectory(a Artifact) string {
	root, rest, ok := strings.Cut(a.ServiceProgram, "/")
	if !ok || rest == "" || !safeMemberPath(root) || strings.ContainsAny(root, "/\\") || identity.SamePathName(a.ApplicationProgram, root) || identity.PathNameWithin(a.ApplicationProgram, root) || identity.SamePathName(a.ArchiveToolProgram, root) || identity.PathNameWithin(a.ArchiveToolProgram, root) {
		return ""
	}
	return root
}

func nativeInstalledInventory(ctx context.Context, a Artifact, root, source string, expected map[string]bool) error {
	host, files, _, err := nativeImagePin(ctx, a, root, source)
	if err != nil {
		return err
	}
	if err := verifyNativeHostFiles(ctx, a, root, files); err != nil {
		return err
	}
	raw, err := nativeArchiveCommand(ctx, host, nativeArchivePlan)
	if err != nil {
		return err
	}
	plan, err := decodeNativeInstallPlan(raw, a)
	if err != nil {
		return err
	}
	if err := validateNativeRecordBudget(a, files, plan); err != nil {
		return err
	}
	for name := range expected {
		if identity.SamePathName(name, plan.Directory) || identity.PathNameWithin(name, plan.Directory) {
			return errors.New("native resource overlaps host ZIP")
		}
	}
	// The native verifier checks configured backend bytes, ACLs and the sealed
	// control root. It may refresh its private metadata; it launches no target.
	if _, err := nativeArchiveCommand(ctx, host, nativeArchiveCheck); err != nil {
		return err
	}
	expected[plan.Directory] = true
	for _, f := range plan.Files {
		name := path.Join(plan.Directory, f.Path)
		expected[name] = false
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			expected[dir] = true
		}
	}
	return nil
}
