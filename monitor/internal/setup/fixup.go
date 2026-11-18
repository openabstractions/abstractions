package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

func installerFixup(component string) *Fixup {
	declaration, found := product.DesktopInstallerFixup(component)
	if !found {
		return nil
	}
	return &declaration
}

// Fixup is an adopter fix-up a component's installed app carries: one file
// in the official app's folder that the adopter's own installer replaces
// over the app's stock bytes. See Adopter fix-ups and Repair in README.md.
// The app's own updater can bring the stock file back. Setup only reads files; the
// adopter's installer stays the only writer, and Repair runs that
// installer's Windows Installer repair. The data names the app's files and
// hashes; Setup's code names no app.
type Fixup = product.DesktopFixup

// FixupState is what a fix-up's file reads.
type FixupState string

const (
	// FixupCurrent: the adopter's file is in place.
	FixupCurrent FixupState = "current"
	// FixupNeedsRepair: the app's stock file is back in a supported
	// release; Repair re-applies the fix.
	FixupNeedsRepair FixupState = "needs_repair"
	// FixupUnknown: a release or file Setup does not know; no Repair.
	FixupUnknown FixupState = "unknown"
)

// FixupStatus is one installed app's fix-up as the Setup page shows it.
type FixupStatus struct {
	App       AppID      `json:"app"`
	Name      string     `json:"name"`
	Component string     `json:"component"`
	State     FixupState `json:"state"`
	// Sentence is the first part of the row's state line; empty when the
	// fix is in place.
	Sentence string `json:"sentence,omitempty"`
	// Versions are the supported app versions.
	Versions []string `json:"versions,omitempty"`
	// Supported is an unknown file in a supported release of the app.
	Supported bool `json:"supported,omitempty"`
	// Detail is the sentence the row's details add.
	Detail string `json:"detail,omitempty"`
	// Repair is whether the row offers Repair.
	Repair bool `json:"repair,omitempty"`
	// Running is whether the app is open now; Repair waits until it closes.
	Running bool `json:"running,omitempty"`
	// Close is the sentence Repair answers while the app is open.
	Close string `json:"close,omitempty"`
}

// ErrFixupAppRunning refuses a Repair while the app is open: Windows cannot
// replace a file the app has loaded. ErrFixupInstallerBusy is Windows
// Installer running another installation.
var (
	ErrFixupAppRunning    = errors.New("the app is open")
	ErrFixupInstallerBusy = errors.New("another installation is running")
)

var (
	fixupUpgradeCode = regexp.MustCompile(`^\{[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}\}$`)
	fixupDigest      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	fixupVersion     = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

// fixupPackageInstalled reports whether the adopter's integration package is
// installed for this account; fixupRunning whether a program in folder runs;
// fixupRepairPackage runs that package's repair. Tests replace them.
var (
	fixupPackageInstalled = installedPackage
	fixupRunning          = folderProgramRunning
	fixupRepairPackage    = repairPackage
)

// fixupLocalAppData is the account's local application data folder, where
// an official program and the adopter package live; tests replace it.
var fixupLocalAppData = layout.LocalAppData

func relativeSlashPath(name string) error {
	for _, part := range strings.Split(name, "/") {
		if err := fileid.SafeName(part); err != nil {
			return fmt.Errorf("fix-up path %q is not a relative path: %w", name, err)
		}
	}
	return nil
}

func validateFixup(f *Fixup) error {
	for _, name := range []string{f.File, f.Package, f.PackageFile} {
		if err := relativeSlashPath(name); err != nil {
			return err
		}
	}
	if len(f.Stock) == 0 || len(f.Fixed) == 0 || len(f.ProgramVersions) == 0 || len(f.ProgramSHA256) != len(f.ProgramVersions) || f.Purpose == "" {
		return errors.New("a fix-up names its stock and fixed digests, its versions with their program digests and its purpose")
	}
	for _, digest := range append(slices.Clone(f.Stock), f.Fixed...) {
		if !fixupDigest.MatchString(digest) {
			return fmt.Errorf("fix-up digest %q is not a SHA-256", digest)
		}
	}
	for _, digest := range f.Stock {
		if slices.Contains(f.Fixed, digest) {
			return fmt.Errorf("fix-up digest %q is both stock and fixed", digest)
		}
	}
	for _, version := range f.ProgramVersions {
		if !fixupVersion.MatchString(version) || !fixupDigest.MatchString(f.ProgramSHA256[version]) {
			return fmt.Errorf("fix-up version %q needs its program's SHA-256", version)
		}
	}
	if !fixupUpgradeCode.MatchString(f.RepairUpgradeCode) {
		return fmt.Errorf("fix-up repair package %q is not an upgrade code", f.RepairUpgradeCode)
	}
	return nil
}

// fixupTarget is one fix-up with its app, the folder its file is read in
// and the adopter package's folder.
type fixupTarget struct {
	choice    Choice
	component string
	fixup     Fixup
	folder    string
	package_  string
}

// fixupTargets are the catalogue's fix-ups whose app names its official
// program, with that program's folder.
func (c Catalog) fixupTargets() ([]fixupTarget, error) {
	var targets []fixupTarget
	for _, choice := range c.Choices {
		if choice.Official == nil {
			continue
		}
		for _, id := range choice.Components {
			for _, component := range c.Components {
				if component.ID != id || component.Fixup == nil {
					continue
				}
				local, err := fixupLocalAppData()
				if err != nil {
					return nil, err
				}
				targets = append(targets, fixupTarget{choice: choice, component: id, fixup: *component.Fixup,
					folder:   filepath.Join(local, filepath.FromSlash(path.Dir(choice.Official.LocalAppData))),
					package_: filepath.Join(local, filepath.FromSlash(component.Fixup.Package))})
			}
		}
	}
	return targets, nil
}

// UnsupportedOfficialRelease names the releases the Catalog integrates when
// app's official program is a release its fix-up does not list (ruling
// OPENCODE-WATCHER-PIN 2). It answers "" for a listed release, an app
// without a fix-up, and a program it cannot read; the integration's own
// installer guard still decides those.
func (c Catalog) UnsupportedOfficialRelease(app AppID) string {
	targets, err := c.fixupTargets()
	if err != nil {
		return ""
	}
	for _, t := range targets {
		if t.choice.ID != app {
			continue
		}
		digest, err := cachedDigest(filepath.Join(t.folder, filepath.Base(filepath.FromSlash(t.choice.Official.LocalAppData))))
		if err != nil {
			return ""
		}
		for _, version := range t.fixup.ProgramVersions {
			if t.fixup.ProgramSHA256[version] == digest {
				return ""
			}
		}
		name := t.choice.Name()
		return "This release of OpenAbstractions supports " + name + " " + joinWords(t.fixup.ProgramVersions, "and") + "."
	}
	return ""
}

// CheckFixups reads every installed app's fix-up. It hashes the named files
// read-only and changes nothing. An app whose official program is absent,
// or whose adopter package is not installed or carries no fix, has no
// fix-up to show.
func (c Catalog) CheckFixups(ctx context.Context) ([]FixupStatus, error) {
	targets, err := c.fixupTargets()
	if err != nil {
		return nil, err
	}
	out := []FixupStatus{}
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		status, applies, err := target.check()
		if err != nil {
			return nil, err
		}
		if applies {
			out = append(out, status)
		}
	}
	return out, nil
}

// check reads one target. applies is false when its app or its adopter
// package is not installed, or the package carries no fix.
func (t fixupTarget) check() (status FixupStatus, applies bool, err error) {
	program := filepath.Join(t.folder, filepath.Base(filepath.FromSlash(t.choice.Official.LocalAppData)))
	if info, err := os.Stat(program); err != nil || !info.Mode().IsRegular() {
		return FixupStatus{}, false, nil
	}
	installed, err := fixupPackageInstalled(t.fixup.RepairUpgradeCode)
	if err != nil || !installed {
		return FixupStatus{}, false, err
	}
	carried, err := fileDigest(filepath.Join(t.package_, filepath.FromSlash(t.fixup.PackageFile)))
	if errors.Is(err, os.ErrNotExist) || err == nil && !slices.Contains(t.fixup.Fixed, carried) {
		return FixupStatus{}, false, nil
	}
	if err != nil {
		return FixupStatus{}, false, err
	}
	name := t.choice.Name()
	versions := t.fixup.ProgramVersions
	status = FixupStatus{App: t.choice.ID, Name: name, Component: t.component, State: FixupUnknown, Versions: versions,
		Close: "Close " + name + ", then press Repair."}
	digest, err := fileDigest(filepath.Join(t.folder, filepath.FromSlash(t.fixup.File)))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return FixupStatus{}, false, err
	}
	if err == nil && slices.Contains(t.fixup.Fixed, digest) {
		status.State = FixupCurrent
		return status, true, nil
	}
	// The stock file is the same in every release: the release is the
	// official program's own bytes.
	programDigest, err := cachedDigest(program)
	if err != nil {
		return FixupStatus{}, false, err
	}
	version := ""
	for _, v := range versions {
		if t.fixup.ProgramSHA256[v] == programDigest {
			version = v
		}
	}
	stock := digest != "" && slices.Contains(t.fixup.Stock, digest)
	switch {
	case version != "" && stock:
		status.State, status.Repair = FixupNeedsRepair, true
		status.Sentence = "Needs its OpenAbstractions fix again"
		status.Detail = "An update to " + name + " removed it. " + t.fixup.Purpose
	case version != "":
		// A supported release whose file is missing or one Setup does not
		// know: Repair replaces only the stock file.
		status.Supported = true
		status.Sentence = "The OpenAbstractions fix can't be checked"
		status.Detail = name + " " + version + " is a supported version. The file the fix replaces is missing or changed. Reinstall " +
			name + " with OpenAbstractions to put the fix back."
	default:
		status.Sentence = "Not a version this release supports"
		status.Detail = "This release of OpenAbstractions supports " + name + " " + joinWords(versions, "and") +
			". Setup leaves this " + name + " as it is."
	}
	running, err := fixupRunning(t.folder)
	if err != nil {
		return FixupStatus{}, false, err
	}
	status.Running = running
	return status, true, nil
}

// RepairFixup re-applies component's fix-up through its adopter's
// installer: the Windows Installer repair of the adopter's integration
// package, which replaces the file only over its stock bytes. It refuses
// with ErrFixupAppRunning while the app is open, and refuses a release or
// file Setup does not know. It returns the status read again after the
// repair.
func (c Catalog) RepairFixup(ctx context.Context, componentID string) (FixupStatus, error) {
	targets, err := c.fixupTargets()
	if err != nil {
		return FixupStatus{}, err
	}
	for _, target := range targets {
		if target.component != componentID {
			continue
		}
		before, applies, err := target.check()
		if err != nil {
			return FixupStatus{}, err
		}
		if !applies {
			return FixupStatus{}, errors.New(target.choice.Name() + " with its OpenAbstractions fix is not installed.")
		}
		if before.State == FixupCurrent {
			return before, nil
		}
		if !before.Repair {
			return before, errors.New(before.Sentence + ".")
		}
		if before.Running {
			return before, fmt.Errorf("%w: %s", ErrFixupAppRunning, before.Close)
		}
		if err := ctx.Err(); err != nil {
			return before, err
		}
		repairErr := fixupRepairPackage(ctx, target.fixup.RepairUpgradeCode)
		after, _, err := target.check()
		if err != nil {
			return before, errors.Join(repairErr, err)
		}
		if after.State == FixupCurrent {
			return after, nil
		}
		if after.Running {
			return after, errors.Join(fmt.Errorf("%w: %s", ErrFixupAppRunning, after.Close), repairErr)
		}
		if repairErr != nil {
			return after, repairErr
		}
		return after, errors.New(target.choice.Name() + " still needs its OpenAbstractions fix.")
	}
	return FixupStatus{}, fmt.Errorf("component %q has no fix-up", componentID)
}

// fileDigest is the SHA-256 of the file at name, read without changing it.
func fileDigest(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// digestCache keeps a large program's digest while its size and time stay
// the same, so each overview read does not hash it again.
var digestCache = struct {
	sync.Mutex
	entries map[string]cachedFileDigest
}{entries: map[string]cachedFileDigest{}}

type cachedFileDigest struct {
	size   int64
	mod    time.Time
	digest string
}

func cachedDigest(name string) (string, error) {
	info, err := os.Stat(name)
	if err != nil {
		return "", err
	}
	digestCache.Lock()
	entry, ok := digestCache.entries[name]
	digestCache.Unlock()
	if ok && entry.size == info.Size() && entry.mod.Equal(info.ModTime()) {
		return entry.digest, nil
	}
	digest, err := fileDigest(name)
	if err != nil {
		return "", err
	}
	digestCache.Lock()
	digestCache.entries[name] = cachedFileDigest{size: info.Size(), mod: info.ModTime(), digest: digest}
	digestCache.Unlock()
	return digest, nil
}
