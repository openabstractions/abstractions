// Package durabletemp puts a test binary's temporary folder on a file system
// that keeps file IDs. Only tests import it.
//
// monitor, mcp-gateway and serve each keep an identical copy of this file at
// internal/durabletemp: a module publishes on its own, so it cannot import
// another module's internal package.
package durabletemp

import (
	"context"
	"os"
	"runtime"

	"github.com/openabstractions/abstraction-identity/fileid"
)

// Enter points TMPDIR at a new folder under /var/tmp when the temporary
// folder lies on a file system whose file IDs fileid does not store
// (fileid.Volatile). /tmp is tmpfs on WSL, Fedora, Arch and Debian 13, and a
// tmpfs file ID may name another file after a reboot, so fileid refuses to
// store it. Fixtures that pin a program or folder by its stored file ID then
// find a file system that keeps file IDs in t.TempDir, as NTFS, APFS and ext4
// temporary folders already do. /var/tmp lies on the root file system.
//
// Enter returns the folder it made, or "" when it changed nothing: on
// Windows, when the temporary folder keeps file IDs, and when /var/tmp keeps
// none either. Child processes the tests start inherit TMPDIR.
func Enter() (string, error) {
	if runtime.GOOS == "windows" || persistent(os.TempDir()) {
		return "", nil
	}
	dir, err := os.MkdirTemp("/var/tmp", "oa-test-")
	if err != nil {
		return "", nil
	}
	if !persistent(dir) {
		return "", os.RemoveAll(dir)
	}
	if err := os.Setenv("TMPDIR", dir); err != nil {
		return "", err
	}
	return dir, nil
}

// Leave removes the folder Enter made.
func Leave(dir string) {
	if dir != "" {
		//unchecked: test cleanup; a folder left behind under /var/tmp is the only effect
		os.RemoveAll(dir)
	}
}

func persistent(dir string) bool {
	id, err := fileid.Resolve(context.Background(), fileid.Self, dir, fileid.Options{})
	return err == nil && id.Durability() == fileid.Persistent
}
