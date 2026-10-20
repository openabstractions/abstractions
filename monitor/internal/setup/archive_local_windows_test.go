package setup

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
)

// A destination off a local drive is refused by the path owner before the
// archive is opened or anything is created: the zip path named here does
// not exist, so a later step would have failed differently.
func TestInstallArchiveRefusesAShareDestinationFirst(t *testing.T) {
	_, pin := archiveFixture(t, archiveEntry{name: "app.exe", data: strings.Repeat("x", 64)})
	missingZip := filepath.Join(t.TempDir(), "never-opened.zip")
	for _, destination := range []string{`\\192.0.2.1\share\apps\installed`, `\\?\UNC\192.0.2.1\share\apps\installed`} {
		err := InstallArchive(context.Background(), pin, missingZip, destination)
		if !errors.Is(err, identity.ErrNotLocalPath) {
			t.Errorf("destination %q: %v; want identity's refusal", destination, err)
		}
	}
}
