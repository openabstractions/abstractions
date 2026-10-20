package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/openabstractions/abstraction-provider-kit/go/sandbox"
)

// removeLeftoverSandboxes takes back every application sandbox a session left
// in this account's runtime state, <state>/apps/<app>/sandbox.json, with the
// kit's Remove: it revokes each recorded grant, the ancestors' attribute
// entries included (provider kit KIT-S7), compares each access list with the
// snapshot the manifest recorded, deletes the profile and the manifest. A
// session normally does this itself at exit; a session that crashed and was
// never opened again leaves its manifest, and the installer's removal runs
// `service remove-sandboxes` as the removing account before the program goes.
// A sandbox a session still runs in (ErrInUse) is left to that session.
//
// An elevated token is refused before any manifest is read. A manifest is
// data in the account's own folder: a forged folder ("tree") entry would make
// Remove's revoke re-propagate inheritance through a tree the forger chose,
// with an administrator's rights. The account's own unelevated token can
// remove every sandbox its sessions made.
func removeLeftoverSandboxes(state string, output io.Writer, elevated func() bool, remove func(string, bool) (*sandbox.Removal, error)) error {
	if elevated() {
		return printLine(output, "remove-sandboxes: refused with an elevated token; run it unelevated as the account whose sessions left them")
	}
	manifests, err := filepath.Glob(filepath.Join(state, "apps", "*", "sandbox.json"))
	if err != nil {
		return err
	}
	sort.Strings(manifests)
	var problems []error
	for _, manifest := range manifests {
		if info, err := os.Lstat(manifest); err != nil || !info.Mode().IsRegular() {
			problems = append(problems, fmt.Errorf("%s: not a regular manifest file: %v", manifest, err))
			continue
		}
		removal, err := remove(manifest, false)
		switch {
		case errors.Is(err, sandbox.ErrInUse):
			if err := printLine(output, manifest+": a session still runs in this sandbox; it removes the sandbox at exit"); err != nil {
				return err
			}
			continue
		case err != nil:
			problems = append(problems, fmt.Errorf("%s: %w", manifest, err))
			continue
		}
		line := fmt.Sprintf("%s: %d grants revoked, profile %s deleted", manifest, len(removal.Revoked), removal.Profile)
		if len(removal.Changed) > 0 {
			line += fmt.Sprintf("; changed by someone else since: %v", removal.Changed)
		}
		if err := printLine(output, line); err != nil {
			return err
		}
	}
	if len(manifests) == 0 {
		if err := printLine(output, "no sandbox left in "+filepath.Join(state, "apps")); err != nil {
			return err
		}
	}
	return errors.Join(problems...)
}
