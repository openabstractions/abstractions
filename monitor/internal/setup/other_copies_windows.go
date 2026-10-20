package setup

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// readUninstallEntries reads the per-user and per-machine uninstall entries,
// the 32-bit view included, with read access only.
func readUninstallEntries() ([]UninstallEntry, error) {
	const uninstall = `Software\Microsoft\Windows\CurrentVersion\Uninstall`
	var entries []UninstallEntry
	var failures []error
	for _, view := range []struct {
		root   registry.Key
		access uint32
	}{
		{registry.CURRENT_USER, 0},
		{registry.LOCAL_MACHINE, registry.WOW64_64KEY},
		{registry.LOCAL_MACHINE, registry.WOW64_32KEY},
	} {
		parent, err := registry.OpenKey(view.root, uninstall, registry.ENUMERATE_SUB_KEYS|view.access)
		if errors.Is(err, registry.ErrNotExist) {
			continue
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		names, err := parent.ReadSubKeyNames(-1)
		parent.Close()
		if err != nil {
			failures = append(failures, err)
		}
		for _, name := range names {
			key, err := registry.OpenKey(view.root, uninstall+`\`+name, registry.QUERY_VALUE|view.access)
			if err != nil {
				continue
			}
			var entry UninstallEntry
			entry.DisplayName, _, _ = key.GetStringValue("DisplayName")
			entry.InstallLocation, _, _ = key.GetStringValue("InstallLocation")
			entry.DisplayIcon, _, _ = key.GetStringValue("DisplayIcon")
			key.Close()
			if entry.DisplayName != "" {
				entries = append(entries, entry)
			}
		}
	}
	return entries, errors.Join(failures...)
}
