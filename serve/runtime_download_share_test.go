//go:build windows || darwin || linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
)

// A NAS download store on a network share is refused when the runtime is
// configured: its job records take the cas lock, which holds within one host
// (cas CAS-L2). Nothing is created on the share and no provider is pinned, so
// the runtime starts again once the setting names a store it accepts.
func TestManagedDownloadExecutorRefusesANASStoreOnANetworkShare(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	share := filepath.Join(nasShareSpelling(t, dir), "shared-jobs")
	if _, err := managedDownloadExecutor(root, nil, "nas", share); !errors.Is(err, identity.ErrNotLocalPath) {
		t.Fatalf("managedDownloadExecutor on %s: %v; want identity.ErrNotLocalPath", share, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("the refused store left %v in %s (%v)", entries, dir, err)
	}
	if _, found, err := readDownloadProviderPin(root); err != nil || found {
		t.Fatalf("a refused store was pinned: found=%v err=%v", found, err)
	}
	if _, err := managedDownloadExecutor(root, nil, "native", ""); err != nil {
		t.Fatalf("the native provider after a refused store: %v", err)
	}
}
