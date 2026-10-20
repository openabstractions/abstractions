//go:build windows

package main

import (
	"testing"

	credentials "github.com/openabstractions/abstraction-credentials/go"
)

// credentialManagerTargets is the Credential Manager backend's listing, which
// takes the same store lock as every other OA call on the store: overlapping
// unlocked calls lose and restore items (abstraction-credentials
// credman_windows.go).
type credentialManagerTargets interface {
	credentials.Backend
	Targets(prefix string) ([]string, error)
}

func credentialManager(t *testing.T) credentialManagerTargets {
	t.Helper()
	backend, err := credentials.NewCredentialManager()
	if err != nil {
		t.Fatal(err)
	}
	store, ok := backend.(credentialManagerTargets)
	if !ok {
		t.Fatal("the Credential Manager backend lists no targets")
	}
	return store
}

// storeItems lists the generic credential target names under prefix.
func storeItems(t *testing.T, prefix string) []string {
	t.Helper()
	names, err := credentialManager(t).Targets(prefix)
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// removeStoreItems deletes every generic credential under the test namespace.
func removeStoreItems(t *testing.T, namespace string) {
	t.Helper()
	store := credentialManager(t)
	for _, target := range storeItems(t, namespace+"/") {
		if err := store.Delete(target); err != nil {
			t.Errorf("delete %s: %v", target, err)
		}
	}
	if left := storeItems(t, namespace+"/"); len(left) != 0 {
		t.Errorf("test credentials left under %s: %d", namespace, len(left))
	}
}
