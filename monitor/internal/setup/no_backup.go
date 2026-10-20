package setup

import (
	"path/filepath"

	"github.com/openabstractions/abstraction-storage/go/layout"
)

// noBackupClasses are the folders below Setup's data root whose contents
// Setup downloads again by digest: engines, the native application copy and
// model stages (ST-2 ruling 4, storage STO-L7).
var noBackupClasses = [][]string{{"OpenAbstractions", "engines"}, {"OA", "e"}, {"OA", "a"}, {"OA", "m"}}

// markNoBackupClass makes the class folder above path and gives it the
// no-backup mark on macOS and Linux before anything is created below it. A
// path in no class is left alone.
func markNoBackupClass(home Home, path string) error {
	for _, class := range noBackupClasses {
		root := filepath.Join(append([]string{home.Data}, class...)...)
		if path == root || withinPath(root, path) {
			return layout.EnsureNoBackup(root)
		}
	}
	return nil
}
