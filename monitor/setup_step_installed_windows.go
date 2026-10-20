package main

import (
	"path/filepath"

	"github.com/openabstractions/abstraction-storage/go/layout"
	"golang.org/x/sys/windows/registry"
)

// installedApplicationManifest reads the application manifest an
// OpenAbstractions Setup package recorded for name at install
// (installer/apps/app.wxs, HKCU\Software\OpenAbstractions\Applications\<name>,
// value manifest). The record says where the manifest lies; the manifest is
// read and checked as the registration page reads it. Any process of this
// account can write the record, so stepInstalledApps uses it only inside the
// package's own folder (setupAppFolder) or for Setup's recorded program.
func installedApplicationManifest(name string) (string, bool) {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\OpenAbstractions\Applications\`+name, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer key.Close()
	manifest, _, err := key.GetStringValue("manifest")
	if err != nil || manifest == "" {
		return "", false
	}
	return manifest, true
}

// installedApplicationRecords reads every manifest record Setup packages
// wrote for this account, name to manifest file, for the registrations
// waiting in Apps (application_pending.go). Each manifest is read and checked
// again before it is shown or registered.
func installedApplicationRecords() map[string]string {
	found := map[string]string{}
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\OpenAbstractions\Applications`, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return found
	}
	defer key.Close()
	names, err := key.ReadSubKeyNames(256)
	if err != nil && len(names) == 0 {
		return found
	}
	for _, name := range names {
		if manifest, ok := installedApplicationManifest(name); ok {
			found[name] = manifest
		}
	}
	return found
}

// setupAppFolder is the folder an OpenAbstractions Setup application package
// installs name into (installer/apps/app.wxs INSTALLFOLDER:
// LocalAppData\Programs\OpenAbstractions\apps\<name>), "" when the known
// folder cannot be read. Storage's layout package reads LocalAppData (STO-L2).
func setupAppFolder(name string) string {
	local, _, err := layout.WindowsAccountFolders(layout.Current())
	if err != nil || local == "" || name == "" || filepath.Base(name) != name {
		return ""
	}
	return filepath.Join(local, "Programs", "OpenAbstractions", "apps", name)
}
