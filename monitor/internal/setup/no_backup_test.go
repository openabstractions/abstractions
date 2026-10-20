package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/openabstractions/abstraction-storage/go/layout"
)

// ST-2 ruling 4 (STO-L7): before Setup extracts an engine, the native ComfyUI
// copy or a model stage on macOS and Linux, the class folder above it carries
// CACHEDIR.TAG; an app folder outside these classes gets no mark, and Windows
// folders keep their state.
func TestSetupMarksDownloadedAgainFoldersNoBackup(t *testing.T) {
	home := Home{Data: t.TempDir()}
	for _, c := range []struct {
		path  []string
		class []string
	}{
		{[]string{"OA", "e", "pt", "1", "i"}, []string{"OA", "e"}},
		{[]string{"OA", "a", "cu", "1"}, []string{"OA", "a"}},
		{[]string{"OA", "m", "pt", "flux"}, []string{"OA", "m"}},
		{[]string{"OpenAbstractions", "engines", "llama.cpp-b1"}, []string{"OpenAbstractions", "engines"}},
	} {
		path := filepath.Join(append([]string{home.Data}, c.path...)...)
		if err := markNoBackupClass(home, path); err != nil {
			t.Fatal(err)
		}
		tag := filepath.Join(append(append([]string{home.Data}, c.class...), "CACHEDIR.TAG")...)
		body, err := os.ReadFile(tag)
		if runtime.GOOS == "windows" {
			if err == nil {
				t.Fatalf("windows marked %s", tag)
			}
			continue
		}
		if err != nil || string(body) != layout.CacheDirTag {
			t.Fatalf("%s: %q %v", tag, body, err)
		}
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("marking created the install folder %s itself", path)
		}
		assertTimeMachineExcluded(t, filepath.Dir(tag))
	}
	apps := filepath.Join(home.Data, "OpenAbstractions", "apps", "x", "1")
	if err := markNoBackupClass(home, apps); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home.Data, "OpenAbstractions", "apps")); err == nil {
		t.Fatal("an app folder outside the no-backup classes was created")
	}
}
