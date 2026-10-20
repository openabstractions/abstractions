package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-provider-lemonade/go/server"
	lemonade "github.com/openabstractions/abstraction-provider-lemonade/go/setup"
	"golang.org/x/sys/windows"
)

// The provider's real package check runs behind the fake CLI, with the exit
// codes lemonaded gives it: 2 for a definitive refusal, 1 otherwise. A
// staged lemond.exe an antivirus scan holds open is no refusal: the health
// check does not finish, the update stays staged, and it passes once the
// file is free.
func TestLockedLemonadeProgramKeepsTheUpdateStaged(t *testing.T) {
	f, l := newLemonadeUpdate(t)
	data := "lemond " + f.v2.Version
	sum := sha256.Sum256([]byte(data))
	entry := server.Packages[0]
	entry.ID, entry.Version = "fixture-"+f.v2.Version, f.v2.Version
	entry.Manifest = server.PackageManifest{Name: "lemonade", Files: []server.ManifestFile{{Name: "lemond.exe", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}}}
	entry.ProgramSHA256 = hex.EncodeToString(sum[:])
	saved := server.Packages
	server.Packages = []server.Package{entry}
	t.Cleanup(func() { server.Packages = saved })
	lemonadeCLI = func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "package" && args[1] == "check" {
			p, err := lemonade.CheckPackage(ctx, flagValue(args, "--version"), flagValue(args, "--root"))
			if err != nil {
				code := 1
				if errors.Is(err, lemonade.ErrPackageRefused) {
					code = 2
				}
				return nil, &lemonadeExit{Code: code, Reason: err.Error()}
			}
			return json.Marshal(map[string]any{"version": 1, "ok": true, "backend_archive_sha256": p.BackendArchiveSHA256, "model_revision": p.ModelRevision})
		}
		return l.run(ctx, args...)
	}
	r := f.runner()
	for _, phase := range []Phase{Requested, Verified, Staged} {
		f.advance(r, phase)
	}
	name, err := windows.UTF16PtrFromString(filepath.Join(f.root2, "lemonade", "lemond.exe"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Advance(f.ctx, f.component)
	windows.CloseHandle(h)
	if err == nil || f.effect().Phase != Staged || !f.installed(f.v2, f.root2) {
		t.Fatalf("a locked program retired the staged version: %v %+v", err, f.effect())
	}
	f.advance(r, Checked)
}
