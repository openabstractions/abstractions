package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	credentials "github.com/openabstractions/abstraction-credentials/go"
)

// credentialsBackendFile is the Linux store choice older runtimes wrote inside
// the credentials directory. The provider still reads it there; new choices
// are written beside the directory (credentials.LinuxStoreMarker).
const credentialsBackendFile = "backend"

// TestCredentialsBackendChoiceIsLinuxOnly: off Linux, "credentials backend"
// refuses as a usage mistake and creates nothing, since a credentials
// directory alone would bind a fresh Windows state to the Credential Manager.
// On Linux it writes the choice beside the credentials directory and creates
// no credentials directory.
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
	state := privateTestState(t)
	if err := credentialsBackend(io.Discard, io.Discard, "file", state); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "credentials")
	if data, err := os.ReadFile(credentials.LinuxStoreMarker(dir)); err != nil || string(data) != credentials.StoreFile+"\n" {
		t.Fatalf("linux choice: %q %v", data, err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the choice created the credentials directory (%v)", err)
	}
}
