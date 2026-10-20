package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

type signedHostSearch struct {
	preferred string
	firstScan string
	name      string
	exclude   string
}

// findSignedHostProgram checks the declared path first, then the declared
// fallback folder, then bounded Programs entries. All candidates use the
// same trusted-signature check.
func findSignedHostProgram(local string, rule signedHostSearch, signed func(string) bool) string {
	if local == "" {
		return ""
	}
	preferred := filepath.Join(local, filepath.FromSlash(rule.preferred))
	if isRegularFile(preferred) && signed(preferred) {
		return preferred
	}
	programs := filepath.Join(local, "Programs")
	folders := []string{filepath.Join(local, rule.firstScan)}
	excludedPath := filepath.Join(local, filepath.FromSlash(rule.exclude))
	excluded := hostDirectoryKey(excludedPath)
	for _, entry := range hostDirectoryEntries(programs) {
		if !entry.IsDir() || !strings.Contains(strings.ToLower(entry.Name()), rule.name) {
			continue
		}
		folder := filepath.Join(programs, entry.Name())
		if folder != excludedPath && (excluded == "" || hostDirectoryKey(folder) != excluded) {
			folders = append(folders, folder)
		}
	}
	for _, folder := range folders {
		for _, entry := range hostDirectoryEntries(folder) {
			name := strings.ToLower(entry.Name())
			program := filepath.Join(folder, entry.Name())
			if strings.HasSuffix(name, ".exe") && strings.Contains(name, rule.name) && !strings.Contains(name, "uninstall") &&
				isRegularFile(program) && signed(program) {
				return program
			}
		}
	}
	return ""
}

func hostDirectoryEntries(folder string) []os.DirEntry {
	f, err := os.Open(folder)
	if err != nil {
		return nil
	}
	defer f.Close()
	entries, _ := f.ReadDir(64)
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries
}

// ExtensionHostFolders holds the known account folders and portable
// candidates supplied to extension discovery.
type ExtensionHostFolders struct {
	Roaming   string
	Local     string
	Chosen    string
	Portables []string
}

// portableHostSelection stores the current choice under the declared
// package ID. A historical relative record is recognition-only.
type portableHostSelection struct {
	packageID    string
	legacyRecord string
	locator      extensionHostLocator
}

func (selection portableHostSelection) currentRecord(config string) string {
	if !validArchiveMember(selection.packageID) {
		return ""
	}
	return filepath.Join(config, "extension-host-"+selection.packageID+".json")
}

func (selection portableHostSelection) accountFolders(app AppID) ExtensionHostFolders {
	var folders ExtensionHostFolders
	if local, roaming, err := layout.WindowsAccountFolders(layout.Current()); err == nil {
		folders.Local, folders.Roaming = local, roaming
	}
	folders.Chosen = selection.chosen()
	// Only folder observations are candidates for portable discovery.
	usual := DefaultCopyFinder()
	copies, _ := CopyFinder{UserProfile: usual.UserProfile, SystemDrive: usual.SystemDrive}.Find()
	for _, copy := range copies {
		if copy.App == app && copy.Source == "folder" {
			folders.Portables = append(folders.Portables, copy.Location)
		}
	}
	return folders
}

func (selection portableHostSelection) chosen() string {
	home, err := CurrentHome()
	if err != nil {
		return ""
	}
	record := selection.currentRecord(home.Config)
	if record == "" {
		return ""
	}
	if info, err := os.Lstat(record); errors.Is(err, os.ErrNotExist) && safeMemberPath(selection.legacyRecord) {
		record = filepath.Join(home.Config, filepath.FromSlash(selection.legacyRecord))
	} else if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	raw, err := readBounded(record, 64<<10)
	if err != nil {
		return ""
	}
	var choice struct {
		Folder string `json:"folder"`
	}
	if json.Unmarshal(raw, &choice) != nil || !filepath.IsAbs(choice.Folder) {
		return ""
	}
	if _, _, ok := selection.locator.portable(choice.Folder); !ok {
		return ""
	}
	return choice.Folder
}

func (selection portableHostSelection) save(ctx context.Context, folder string, invalid error) error {
	if !filepath.IsAbs(folder) {
		return invalid
	}
	if _, _, ok := selection.locator.portable(folder); !ok {
		return invalid
	}
	home, err := CurrentHome()
	if err != nil {
		return err
	}
	record := selection.currentRecord(home.Config)
	if record == "" {
		return errors.New("the extension has no declared portable folder record")
	}
	return writeSetupFile(ctx, record, map[string]string{"folder": filepath.Clean(folder)})
}

func readBounded(file string, limit int64) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("settings file too large")
	}
	return raw, nil
}

func writeSetupFile(ctx context.Context, file string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	return cas.ChangeLimitContext(ctx, file, 1<<20, func([]byte) ([]byte, error) { return raw, nil })
}

// ExtensionHost is one observed installation. Its program is available to
// the local launch owner and omitted from the observed host's JSON.
type ExtensionHost struct {
	Kind      string `json:"kind"`
	Base      string `json:"base"`
	Program   string `json:"-"`
	Connected bool   `json:"connected"`
}

type extensionHostKinds struct {
	portable, singleton, installed string
}

type extensionHostLocator struct {
	spec                        product.PackageSpec
	kinds                       extensionHostKinds
	installBase, localInstalls  string
	settingsPaths, settingsKeys []string
	desktopProgram              string
	portableLaunchers           []string
	integrationMarker           string
	signature                   func(string) bool
	search                      signedHostSearch
}

const extensionHostSettingsLimit = 1 << 20

func (locator extensionHostLocator) present(base string) bool {
	return packageHostPresent(locator.spec, base)
}

func (locator extensionHostLocator) connected(base string) bool {
	return extensionConnectorPresent(packageConnector(locator.spec, base), locator.integrationMarker)
}

func (locator extensionHostLocator) signedProgram(local string) string {
	return findSignedHostProgram(local, locator.search, locator.signature)
}

func (locator extensionHostLocator) find(folders ExtensionHostFolders, declaration json.RawMessage) []ExtensionHost {
	roots := discoveryRoots{local: folders.Local, roaming: folders.Roaming}
	var found []ExtensionHost
	admitted := make(map[string]bool)
	add := func(kind, base, program string) {
		base = filepath.Clean(base)
		if key := hostDirectoryKey(base); key != "" {
			if admitted[key] {
				return
			}
			admitted[key] = true
		}
		found = append(found, ExtensionHost{Kind: kind, Base: base, Program: program, Connected: locator.connected(base)})
	}
	for _, folder := range append([]string{folders.Chosen}, folders.Portables...) {
		if folder == "" {
			continue
		}
		if base, program, ok := locator.portable(folder); ok {
			add(locator.kinds.portable, base, program)
		}
	}
	descriptor, err := parseDiscoveryDescriptor(declaration)
	if err != nil && len(declaration) != 0 {
		logDiscoveryOnce("setup: desktop installation discovery is unavailable: %v", err)
	}
	if err == nil {
		err := readDiscoverySingleton(context.Background(), descriptor, roots, func(base string) bool {
			if !locator.present(base) {
				return false
			}
			add(locator.kinds.singleton, base, locator.singletonProgram(roots.local))
			return true
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			logDiscoveryOnce("setup: desktop settings discovery is unavailable")
		}
	}
	program := locator.signedProgram(roots.local)
	var installed []string
	var pending pendingExtensionHosts
	if err == nil {
		installed, pending = locator.installRecords(roots, descriptor)
	}
	for _, base := range installed {
		add(locator.kinds.installed, base, program)
	}
	if roots.roaming != "" {
		for _, name := range locator.settingsPaths {
			if base, ok := locator.settingsBase(filepath.Join(roots.roaming, filepath.FromSlash(name))); ok && !pending.contains(base) {
				add(locator.kinds.installed, base, program)
			}
		}
	}
	if roots.local != "" {
		installs := filepath.Join(roots.local, filepath.FromSlash(locator.localInstalls))
		for _, entry := range hostDirectoryEntries(installs) {
			if !entry.IsDir() {
				continue
			}
			for _, base := range []string{filepath.Join(installs, entry.Name()), filepath.Join(installs, entry.Name(), locator.installBase)} {
				if locator.present(base) {
					if !pending.contains(base) {
						add(locator.kinds.installed, base, program)
					}
					break
				}
			}
		}
	}
	return found
}

// The descriptor supplies candidate paths and closed status. Setup still
// decides which folders to list and whether a pending row hides a fallback.
func (locator extensionHostLocator) installRecords(roots discoveryRoots, descriptor discoveryDescriptor) (installed []string, pending pendingExtensionHosts) {
	pending = pendingExtensionHosts{names: make(map[string]bool), identities: make(map[string]bool)}
	err := readDiscovery(context.Background(), descriptor, roots, func(i int, entry discoveryRecord, err error) {
		if err != nil {
			logDiscoveryOnce("setup: desktop install %d is skipped: its entry has another shape", i)
			return
		}
		if entry.cloud || entry.path == "" {
			return
		}
		if !entry.installed {
			if filepath.IsAbs(entry.path) {
				for _, path := range []string{entry.path, filepath.Join(entry.path, locator.installBase)} {
					pending.names[filepath.Clean(path)] = true
					if key := hostDirectoryKey(path); key != "" {
						pending.identities[key] = true
					}
				}
			}
			return
		}
		base, reason := locator.installedBase(entry.path)
		if reason != "" {
			logDiscoveryOnce("setup: desktop install %d is skipped: %s", i, reason)
			return
		}
		installed = append(installed, base)
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		logDiscoveryOnce("setup: desktop installation discovery is unavailable: %v", err)
	}
	return installed, pending
}

// installedBase checks a recorded local installation before deriving its host folder.
func (locator extensionHostLocator) installedBase(installPath string) (string, string) {
	if !localAbsolutePath(installPath) {
		return "", "its installPath is not an absolute local path"
	}
	installPath = filepath.Clean(installPath)
	info, err := os.Lstat(installPath)
	if err != nil || !info.IsDir() || info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return "", "its installPath is not a folder"
	}
	for _, base := range []string{filepath.Join(installPath, locator.installBase), installPath} {
		if locator.present(base) {
			return base, ""
		}
	}
	return "", "its installPath holds no " + locator.spec.Title + " folder"
}

// localAbsolutePath is whether path is absolute on a drive letter: a UNC
// share or a device path is refused.
func localAbsolutePath(path string) bool {
	volume := filepath.VolumeName(path)
	return filepath.IsAbs(path) && len(volume) == 2 && volume[1] == ':'
}

type pendingExtensionHosts struct {
	names      map[string]bool
	identities map[string]bool
}

func (pending pendingExtensionHosts) contains(path string) bool {
	// The exact declaration spelling still suppresses a fallback if its
	// folder was absent during observation or its directory could not be read.
	// Other spellings suppress only when the volume proves they name it.
	return pending.names[filepath.Clean(path)] ||
		(len(pending.identities) != 0 && pending.identities[hostDirectoryKey(path)])
}

// hostDirectoryKey identifies an observed folder without treating its spelling as a key.
func hostDirectoryKey(path string) string {
	id, err := fileid.Resolve(context.Background(), fileid.Self, path, fileid.Options{})
	if err != nil || !id.IsDir() {
		return ""
	}
	return id.Key()
}

// settingsBase reads the first configured host path that still names a host folder.
func (locator extensionHostLocator) settingsBase(file string) (string, bool) {
	raw, err := readBounded(file, extensionHostSettingsLimit)
	if err != nil {
		return "", false
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal(raw, &settings) != nil {
		return "", false
	}
	for _, key := range locator.settingsKeys {
		var base string
		if json.Unmarshal(settings[key], &base) == nil && base != "" && locator.present(base) {
			return filepath.Clean(base), true
		}
	}
	return "", false
}

// singletonProgram returns the exact known launcher for the singleton host.
func (locator extensionHostLocator) singletonProgram(local string) string {
	if local == "" {
		return ""
	}
	program := filepath.Join(local, filepath.FromSlash(locator.desktopProgram))
	if isRegularFile(program) {
		return program
	}
	return ""
}

// portable recognizes the declared host folder and its ordered launchers.
func (locator extensionHostLocator) portable(chosen string) (base, program string, ok bool) {
	chosen = filepath.Clean(chosen)
	root := ""
	switch {
	case locator.present(filepath.Join(chosen, locator.installBase)):
		base, root = filepath.Join(chosen, locator.installBase), chosen
	case locator.present(chosen):
		base, root = chosen, filepath.Dir(chosen)
	default:
		return "", "", false
	}
	for _, name := range locator.portableLaunchers {
		if isRegularFile(filepath.Join(root, name)) {
			return base, filepath.Join(root, name), true
		}
	}
	return base, "", true
}

// OpenExtensionHost starts the observed program using the platform launch policy.
func OpenExtensionHost(host ExtensionHost, title string) error {
	if host.Program == "" {
		return fmt.Errorf("Start %s from the Start menu.", title)
	}
	return startDesktopProgram(host.Program)
}

func hostProcessFolders(base string, siblingNames []string) []string {
	if !filepath.IsAbs(base) {
		return nil
	}
	folders := []string{base}
	parent := filepath.Dir(base)
	for _, name := range siblingNames {
		folders = append(folders, filepath.Join(parent, name))
	}
	return folders
}

func hostProcesses(base string, siblingNames []string) ([]uint32, error) {
	folders := hostProcessFolders(base, siblingNames)
	if len(folders) == 0 {
		return nil, nil
	}
	return folderProcesses(folders)
}
