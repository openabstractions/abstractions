package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

// Installation declarations: the declaration files the installation places in
// a directory named `declarations` beside the installed runtime executable,
// read at start and listed as declared by the installation (facade
// CONTRACT.md FAC-R7, VISION 2026-09-22 on optional features). The rule is the
// operator siblings' rule: whatever directory holds `openabstractions` holds
// them, so both layouts work with no post-install step.
//
//	Windows, per-user or machine   <install>\tools\declarations\<name>.json
//	Linux and macOS, per-user      ~/.local/bin/declarations/<name>.json
//
// An operator never withdraws one: `Withdraw` and `RemoveHost` disable it by
// name in <state>/declarations-disabled.json, so a reinstall that writes the
// file again does not resurrect the operator's removal. A later `Declare` or
// `AddHost` of that name enables it again.
//
// A shipped file cannot carry an absolute program path, because the install
// location is chosen when the package is installed. A provider declaration
// read from this directory may instead name its program as a bare file name
// with no path separator, which resolves against the tools directory the
// `declarations` directory sits beside: the same directory `operatorSiblings`
// resolves the runtime's own operator programs against
// (`serve/runtime_credentials.go`). The resolved absolute path replaces the
// shipped name in the declaration the runtime reads, launches and names to
// its rights rules; a name that resolves to no file is invalid, and the
// report names the shipped name and the resolved path. An operator's own
// declaration keeps the absolute-path rule; only a declaration read from
// this directory resolves a bare name.
const (
	installationDeclarationsDir = "declarations"
	// declaredByInstallation is the declared_by the runtime asserts for every
	// declaration it read from that directory, whatever the file says.
	declaredByInstallation = "installation"
	// disabledDeclarationsFile records the names an operator withdrew.
	disabledDeclarationsFile = "declarations-disabled.json"
)

// Where a declaration came from. The order is its precedence, lowest first:
// an operator's declaration shadows a product's, which shadows the
// installation's.
const (
	declarationSourceInstallation = "installation"
	declarationSourceProduct      = "product"
	declarationSourceOperator     = "operator"
)

// installationDeclarationPath is the directory the installation's declaration
// files live in. Tests replace it so no test reads the owner's installation.
var installationDeclarationPath = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(filepath.Clean(exe)), installationDeclarationsDir), nil
}

// disabledDeclarations is the names an operator withdrew, and the file's bytes
// so the revision changes with them.
func (p *runtimeProviders) disabledDeclarations() (map[string]bool, []byte) {
	raw, err := os.ReadFile(p.disabledPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			p.report(err)
		}
		return map[string]bool{}, nil
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		p.report(err)
		return map[string]bool{}, raw
	}
	out := map[string]bool{}
	for _, n := range names {
		out[n] = true
	}
	return out, raw
}

// disable records name as withdrawn, so no product probe and no reinstall
// brings it back.
func (p *runtimeProviders) disable(name string) error {
	disabled, _ := p.disabledDeclarations()
	if disabled[name] {
		return nil
	}
	names := make([]string, 0, len(disabled)+1)
	for n := range disabled {
		names = append(names, n)
	}
	names = append(names, name)
	sort.Strings(names)
	return writeAtomically(p.disabledPath, names)
}

// enable forgets an operator's withdrawal of name.
func (p *runtimeProviders) enable(name string) error {
	disabled, _ := p.disabledDeclarations()
	if !disabled[name] {
		return nil
	}
	names := make([]string, 0, len(disabled))
	for n := range disabled {
		if n != name {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		if err := os.Remove(p.disabledPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeAtomically(p.disabledPath, names)
}

// installedDeclarations is every valid declaration file the installation
// placed, with declared_by asserted as installation and a bare program name
// resolved against the tools directory.
func (p *runtimeProviders) installedDeclarations(digest io.Writer) []providerFile {
	if p.installation == "" {
		return nil
	}
	files, err := p.readDeclarationDir(p.installation, digest, true)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			p.report(err)
		}
		return nil
	}
	for i := range files {
		files[i].DeclaredBy, files[i].Source = declaredByInstallation, declarationSourceInstallation
	}
	return files
}

// resolveBundledProgram resolves a bare program name shipped in a
// declaration read from declarationsDir against the tools directory that
// directory sits beside — the directory `operatorSiblings` resolves the
// runtime's own operator programs against. A name that already carries a
// path separator, or a declaration with no program (a host or a remote
// runtime), is left unchanged and bundled reads false. A resolved name that
// names no file on disk is an error naming both the shipped name and the
// resolved path.
func resolveBundledProgram(declarationsDir string, d providerDeclaration) (resolved string, bundled bool, err error) {
	program := d.Program
	if program == "" || d.remote() || d.host() || program != filepath.Base(program) {
		return program, false, nil
	}
	resolved = filepath.Join(filepath.Dir(declarationsDir), program)
	if _, err := os.Stat(resolved); err != nil {
		return resolved, true, fmt.Errorf("%s not found at %s", program, resolved)
	}
	return resolved, true, nil
}

// declaredNames is the names of a file list, for shadowing.
func declaredNames(files []providerFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Declaration.Name)
	}
	return out
}

// shadow keeps every file of higher whose name no file of lower already has.
func shadow(higher, lower []providerFile) []providerFile {
	names := declaredNames(higher)
	for _, f := range lower {
		if !slices.Contains(names, f.Declaration.Name) {
			higher = append(higher, f)
		}
	}
	return higher
}
