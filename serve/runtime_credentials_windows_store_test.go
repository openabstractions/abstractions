package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	credentials "github.com/openabstractions/abstraction-credentials/go"
)

// windowsSelection records which Windows store constructor ran.
type windowsSelection struct {
	t       *testing.T
	opened  []string
	failing bool
}

func (w *windowsSelection) constructors() windowsCredentialsConstructors {
	return windowsCredentialsConstructors{
		credentialManager: func() (credentials.Backend, error) {
			w.opened = append(w.opened, credentials.StoreWindowsCredentialManager)
			if w.failing {
				return nil, errors.New("constructor failed")
			}
			return keychainSelectionBackend(credentials.StoreWindowsCredentialManager), nil
		},
		dpapiFile: func(dir string) (credentials.Backend, error) {
			w.opened = append(w.opened, credentials.StoreWindowsDPAPIFile+" "+filepath.Base(dir))
			if w.failing {
				return nil, errors.New("constructor failed")
			}
			return keychainSelectionBackend(credentials.StoreWindowsDPAPIFile), nil
		},
	}
}

func markerWord(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(windowsCredentialsMarker(dir))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestFreshWindowsStateBindsToTheDPAPIFileStore: a state with no credentials
// directory binds to windows-dpapi-file with a durable marker, and keeps it
// once the directory exists.
func TestFreshWindowsStateBindsToTheDPAPIFileStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	w := &windowsSelection{t: t}
	if b := newWindowsCredentialsBackend(dir, w.constructors()); b.Name() != credentials.StoreWindowsDPAPIFile {
		t.Fatalf("fresh state opened %s", b.Name())
	}
	if got := markerWord(t, dir); got != credentials.StoreWindowsDPAPIFile+"\n" {
		t.Fatalf("marker %q", got)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if b := newWindowsCredentialsBackend(dir, w.constructors()); b.Name() != credentials.StoreWindowsDPAPIFile {
		t.Fatalf("restart opened %s", b.Name())
	}
	if len(w.opened) != 2 || w.opened[0] != "windows-dpapi-file items" || w.opened[1] != w.opened[0] {
		t.Fatalf("opened %v", w.opened)
	}
}

// TestExistingWindowsStateKeepsCredentialManager: an unmarked state that
// already has a credentials directory keeps its Credential Manager items and
// binds to that store; an empty new store never turns them lost.
func TestExistingWindowsStateKeepsCredentialManager(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w := &windowsSelection{t: t}
	for i := 0; i < 2; i++ {
		if b := newWindowsCredentialsBackend(dir, w.constructors()); b.Name() != credentials.StoreWindowsCredentialManager {
			t.Fatalf("existing state opened %s", b.Name())
		}
	}
	if got := markerWord(t, dir); got != credentials.StoreWindowsCredentialManager+"\n" {
		t.Fatalf("marker %q", got)
	}
	// A marker binds even when the directory is gone.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if b := newWindowsCredentialsBackend(dir, w.constructors()); b.Name() != credentials.StoreWindowsCredentialManager {
		t.Fatalf("marked state without its directory opened %s", b.Name())
	}
}

// TestCredentialsBackendChoiceIsLinuxOnly: off Linux, "credentials backend"
// refuses as a usage mistake and creates nothing, since a credentials
// directory alone would bind a fresh Windows state to the Credential Manager.
// On Linux it writes the choice.
func TestCredentialsBackendChoiceIsLinuxOnly(t *testing.T) {
	saved := credentialsBackendGOOS
	t.Cleanup(func() { credentialsBackendGOOS = saved })
	for _, goos := range []string{"windows", "darwin"} {
		credentialsBackendGOOS = goos
		state := t.TempDir()
		err := credentialsBackend(io.Discard, io.Discard, "file", state)
		assertExit(t, err, exitUsage, "credentials backend on "+goos)
		if _, err := os.Lstat(filepath.Join(state, "credentials")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: the refused choice created the credentials directory (%v)", goos, err)
		}
	}
	credentialsBackendGOOS = "linux"
	state := t.TempDir()
	if err := credentialsBackend(io.Discard, io.Discard, "file", state); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(state, "credentials", credentialsBackendFile)); err != nil || string(data) != credentials.StoreFile+"\n" {
		t.Fatalf("linux choice: %q %v", data, err)
	}
}

// TestUnusableWindowsStoreMarkerSelectsNoStore: an unknown marker word, a
// marker that is not a file and a store that cannot open each read
// unavailable, and no other store is opened in its place.
func TestUnusableWindowsStoreMarkerSelectsNoStore(t *testing.T) {
	for _, c := range []struct {
		name, marker string
		directory    bool
		failing      bool
		want         string
	}{
		{name: "unknown word", marker: "secret-service\n", want: unavailableWindowsStore},
		{name: "marker is a directory", directory: true, want: unavailableWindowsStore},
		{name: "store fails to open", marker: "windows-dpapi-file\n", failing: true, want: credentials.StoreWindowsDPAPIFile},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "credentials")
			marker := windowsCredentialsMarker(dir)
			var err error
			if c.directory {
				err = os.MkdirAll(marker, 0o700)
			} else {
				err = os.WriteFile(marker, []byte(c.marker), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			w := &windowsSelection{t: t, failing: c.failing}
			b := newWindowsCredentialsBackend(dir, w.constructors())
			if b.Name() != c.want {
				t.Fatalf("store %s, want %s", b.Name(), c.want)
			}
			if err := b.Put("k", []byte("v")); !errors.Is(err, credentials.ErrUnavailable) {
				t.Fatalf("put: %v", err)
			}
			if !c.failing && len(w.opened) != 0 {
				t.Fatalf("opened %v", w.opened)
			}
		})
	}
}
