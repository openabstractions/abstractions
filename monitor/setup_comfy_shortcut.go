package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// startShortcut is one Start-menu shortcut: the program it starts, its
// arguments and its tooltip.
type startShortcut struct {
	Target      string
	Arguments   string
	Description string
}

// The platform's Start-menu owner (the Windows shell adapter). Off
// Windows startMenuPrograms answers errors.ErrUnsupported and Setup makes no
// shortcut. Tests replace all three.
var (
	startMenuPrograms  func() (string, error)
	readStartShortcut  func(path string) (startShortcut, error)
	writeStartShortcut func(path string, s startShortcut) error
)

// panelExecutable is the Panel program a shortcut starts. Tests replace it.
var panelExecutable = panelProgramPath

// extensionShortcut describes a registered extension's Start-menu entry.
type extensionShortcut struct {
	application string
	title       string
	description string
}

func declaredExtensionShortcut(id, descriptionSuffix string) (extensionShortcut, bool) {
	spec, ok := product.PackageDeclaration(id)
	if !ok {
		return extensionShortcut{}, false
	}
	return extensionShortcut{application: spec.Registration, title: spec.Title, description: spec.Title + descriptionSuffix}, true
}

func (s extensionShortcut) names() [2]string {
	return [2]string{s.title + ".lnk", s.title + " (OpenAbstractions).lnk"}
}

// shortcutMenu holds the account's Start menu, or a private stand-in for
// a Panel serving an isolated runtime.
func shortcutMenu() (string, error) {
	home, err := setup.CurrentHome()
	if err != nil {
		return "", err
	}
	if home.Isolated {
		return filepath.Join(home.Config, "Start Menu", "Programs"), nil
	}
	return startMenuPrograms()
}

func (s extensionShortcut) want() (startShortcut, error) {
	if !validApplicationName(s.application) || strings.TrimSpace(s.title) == "" ||
		strings.ContainsAny(s.title, ":/\\<>|\"?*") || strings.IndexFunc(s.title, unicode.IsControl) >= 0 {
		return startShortcut{}, errors.New("the extension has no safe Start menu shortcut identity")
	}
	for _, name := range s.names() {
		if err := fileid.SafeName(name); err != nil {
			return startShortcut{}, errors.New("the extension has no safe Start menu shortcut identity")
		}
	}
	panel, err := panelExecutable()
	if err != nil {
		return startShortcut{}, err
	}
	return startShortcut{Target: panel, Arguments: "-open-app " + s.application, Description: s.description}, nil
}

func sameShortcut(a, b startShortcut) bool {
	return identity.SameSubjectProgram(a.Target, b.Target) && a.Arguments == b.Arguments && a.Description == b.Description
}

// ownStartShortcut requires exact launch arguments and the existing Panel name or signer check.
func ownStartShortcut(have, want startShortcut) bool {
	if have.Arguments != want.Arguments || have.Target == "" {
		return false
	}
	return identity.SamePathName(filepath.Base(have.Target), filepath.Base(want.Target)) || oaSameSigner(have.Target)
}

// shortcutState finds the exact shortcut and the first name available to Setup.
func (s extensionShortcut) shortcutState(dir string, want startShortcut) (current, free string) {
	for _, name := range s.names() {
		path := filepath.Join(dir, name)
		have, err := readStartShortcut(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if free == "" {
				free = path
			}
		case err != nil:
			// An unreadable file keeps its name.
		case sameShortcut(have, want):
			return path, ""
		case ownStartShortcut(have, want):
			// Setup's own shortcut for an earlier Panel path or wording.
			if free == "" {
				free = path
			}
		}
	}
	return "", free
}

// current reports whether the Start menu has the exact owned shortcut.
func (s extensionShortcut) current() bool {
	dir, err := shortcutMenu()
	if errors.Is(err, errors.ErrUnsupported) {
		return true
	}
	want, wantErr := s.want()
	if err != nil || wantErr != nil {
		return false
	}
	current, _ := s.shortcutState(dir, want)
	return current != ""
}

// ensure keeps a competing shortcut and uses the next available name.
func (s extensionShortcut) ensure() error {
	dir, err := shortcutMenu()
	if errors.Is(err, errors.ErrUnsupported) {
		return nil
	}
	if err != nil {
		return err
	}
	want, err := s.want()
	if err != nil {
		return err
	}
	current, free := s.shortcutState(dir, want)
	if current != "" {
		return nil
	}
	if free == "" {
		return errors.New("the Start menu's " + s.title + " shortcuts belong to other programs")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeStartShortcut(free, want)
}

// remove deletes only shortcuts owned by this registration.
func (s extensionShortcut) remove() error {
	dir, err := shortcutMenu()
	if errors.Is(err, errors.ErrUnsupported) {
		return nil
	}
	if err != nil {
		return err
	}
	want, err := s.want()
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range s.names() {
		path := filepath.Join(dir, name)
		if have, err := readStartShortcut(path); err == nil && ownStartShortcut(have, want) {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
