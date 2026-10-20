package setup

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	identity "github.com/openabstractions/abstraction-identity"
)

// OtherCopy is an installation of an application Setup offers that carries no
// OpenAbstractions mark (VISION 2026-10-02, OA's builds of third-party
// applications). Setup reads it and never modifies or removes it.
type OtherCopy struct {
	App      AppID  `json:"app"`
	Title    string `json:"title"`
	Location string `json:"location"`
	// Source names where Setup found it: "folder" for a known install folder,
	// "uninstall entry" for the Windows uninstall registry.
	Source string `json:"source"`
}

// UninstallEntry is one read-only row of the Windows uninstall registry.
type UninstallEntry struct {
	DisplayName     string
	InstallLocation string
	DisplayIcon     string
}

// CopyFinder looks for other copies of ComfyUI in the usual
// per-user and per-machine places. Every folder is a field so a test names
// fixture folders; an empty field is skipped.
type CopyFinder struct {
	UserProfile  string // %USERPROFILE%
	LocalAppData string // %LOCALAPPDATA%
	AppData      string // %APPDATA%
	ProgramFiles []string
	SystemDrive  string // the drive root, such as C:\
	// Uninstall reads the per-user and per-machine uninstall entries. Nil
	// reads none.
	Uninstall func() ([]UninstallEntry, error)
}

// DefaultCopyFinder names this account's folders and the machine's uninstall
// registry.
func DefaultCopyFinder() CopyFinder {
	finder := CopyFinder{UserProfile: os.Getenv("USERPROFILE"), LocalAppData: os.Getenv("LOCALAPPDATA"),
		AppData: os.Getenv("APPDATA"), Uninstall: readUninstallEntries}
	if home, err := os.UserHomeDir(); err == nil && finder.UserProfile == "" {
		finder.UserProfile = home
	}
	for _, name := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
		if dir := os.Getenv(name); dir != "" && !slices.Contains(finder.ProgramFiles, dir) {
			finder.ProgramFiles = append(finder.ProgramFiles, dir)
		}
	}
	if drive := os.Getenv("SystemDrive"); drive != "" {
		finder.SystemDrive = drive + string(filepath.Separator)
	}
	return finder
}

// Find returns every unmarked copy, ComfyUI first, each location once. A
// copy that carries the OA mark is Setup's own build and is left out. An
// unreadable uninstall registry leaves the folder results and reports its
// error beside them.
func (f CopyFinder) Find() ([]OtherCopy, error) {
	var found []OtherCopy
	add := func(c OtherCopy) {
		c.Location = filepath.Clean(c.Location)
		for _, prior := range found {
			if prior.App != c.App {
				continue
			}
			// One location found twice: by file identity when it exists,
			// else by its exact spelling.
			if same, err := identity.SameFile(prior.Location, c.Location); err == nil && same || err != nil && prior.Location == c.Location {
				return
			}
		}
		found = append(found, c)
	}
	for _, dir := range f.comfyFolderCandidates() {
		if (comfyPortable(dir) || comfyClone(dir)) && !oaMarkedComfy(dir) {
			add(OtherCopy{App: ComfyUI, Title: "ComfyUI", Location: dir, Source: "folder"})
		}
	}
	for _, dir := range f.comfyDesktopCandidates() {
		if regularFile(filepath.Join(dir, "ComfyUI.exe")) == nil {
			add(OtherCopy{App: ComfyUI, Title: "ComfyUI Desktop", Location: dir, Source: "folder"})
		}
	}
	var registryErr error
	if f.Uninstall != nil {
		entries, err := f.Uninstall()
		if err != nil {
			registryErr = err
		}
		for _, entry := range entries {
			app, title := uninstallApp(entry.DisplayName)
			if app == "" {
				continue
			}
			location := uninstallLocation(entry)
			if location == "" || app == ComfyUI && oaMarkedComfy(location) {
				continue
			}
			add(OtherCopy{App: app, Title: title, Location: location, Source: "uninstall entry"})
		}
	}
	slices.SortStableFunc(found, func(a, b OtherCopy) int { return strings.Compare(string(a.App), string(b.App)) })
	return found, registryErr
}

// comfyFolderCandidates are the folders a portable ComfyUI is usually
// unpacked to, or a ComfyUI clone checked out in: the account's home,
// Desktop, Downloads and Documents, and the system drive's root.
func (f CopyFinder) comfyFolderCandidates() []string {
	var parents []string
	if f.UserProfile != "" {
		parents = append(parents, f.UserProfile, filepath.Join(f.UserProfile, "Desktop"),
			filepath.Join(f.UserProfile, "Downloads"), filepath.Join(f.UserProfile, "Documents"))
	}
	if f.SystemDrive != "" {
		parents = append(parents, f.SystemDrive)
	}
	var dirs []string
	for _, parent := range parents {
		dirs = append(dirs, filepath.Join(parent, comfyPortableDirectory), filepath.Join(parent, "ComfyUI"))
	}
	return dirs
}

func (f CopyFinder) comfyDesktopCandidates() []string {
	if f.LocalAppData == "" {
		return nil
	}
	return []string{filepath.Join(f.LocalAppData, "Programs", "@comfyorgcomfyui-electron"),
		filepath.Join(f.LocalAppData, "Programs", "ComfyUI")}
}

// comfyPortable recognizes a ComfyUI portable folder by its server entry and
// embedded Python.
func comfyPortable(dir string) bool {
	return regularFile(filepath.Join(dir, "ComfyUI", "main.py")) == nil &&
		regularFile(filepath.Join(dir, "python_embeded", "python.exe")) == nil
}

// comfyClone recognizes a ComfyUI source checkout by its server entry and
// its comfy package.
func comfyClone(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "comfy"))
	return regularFile(filepath.Join(dir, "main.py")) == nil && err == nil && info.IsDir()
}

// oaMarkedComfy reports OA's ComfyUI package: its root, which holds the
// portable folder, carries OA-PROVENANCE.json and Setup's install receipt.
func oaMarkedComfy(dir string) bool {
	for _, root := range []string{dir, filepath.Dir(dir)} {
		if regularFile(filepath.Join(root, "OA-PROVENANCE.json")) == nil && regularFile(filepath.Join(root, archiveMarker)) == nil {
			return true
		}
	}
	return false
}

func uninstallApp(name string) (AppID, string) {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "openabstractions") || strings.Contains(lower, "open abstractions"):
		return "", ""
	case strings.Contains(lower, "comfyui"):
		return ComfyUI, name
	}
	return "", ""
}

// uninstallLocation is the entry's install folder, or the folder of its icon
// when the entry names no folder.
func uninstallLocation(entry UninstallEntry) string {
	if entry.InstallLocation != "" {
		return strings.Trim(entry.InstallLocation, `"`)
	}
	icon := strings.Trim(entry.DisplayIcon, `"`)
	if i := strings.LastIndex(icon, ","); i > strings.LastIndexAny(icon, `\/`) {
		icon = icon[:i]
	}
	if icon == "" || !filepath.IsAbs(icon) {
		return ""
	}
	return filepath.Dir(icon)
}

// ErrOtherCopy refuses a plan that would install OA's build of an
// application already installed without the OA mark, unless the person chose
// to install it beside that copy.
var ErrOtherCopy = errors.New("another copy is installed")

// CheckBeside refuses selected apps that have another copy and that the
// person did not choose to install beside it.
func CheckBeside(selected, beside []AppID, copies []OtherCopy) error {
	for _, c := range copies {
		if slices.Contains(selected, c.App) && !slices.Contains(beside, c.App) {
			return &OtherCopyError{Copy: c}
		}
	}
	return nil
}

// OtherCopyError names the copy a refused plan would install beside.
type OtherCopyError struct{ Copy OtherCopy }

func (e *OtherCopyError) Error() string {
	return e.Copy.Title + " is already installed at " + e.Copy.Location +
		". Setup leaves it as it is. Choose to install the OpenAbstractions build as well, then review again."
}

func (e *OtherCopyError) Unwrap() error { return ErrOtherCopy }
