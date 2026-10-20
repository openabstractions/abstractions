package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeMarkerBudgetChargesActualEscapedSourceLocator(t *testing.T) {
	_, a, _, _ := nativeArchiveFixture(t)
	files := []archiveFileRecord{{Path: "工具/<owner>.exe", Size: 1, SHA256: strings.Repeat("a", 64)}}
	dirs := []string{"工具"}
	base, err := nativeMarkerBase(a, files, dirs)
	if err != nil {
		t.Fatal(err)
	}
	a.NativeResources.MarkerBytes = base
	for _, locator := range []string{`C:\cache\host.zip`, `C:\` + strings.Repeat(`deep\`, 100) + `host.zip`, `C:\工具\<&>\host.zip`} {
		r := archiveReceipt{Format: 2, ArtifactSHA256: a.SHA256, Version: a.Version, InstallIdentity: a.InstallIdentity, Files: files, Dirs: dirs, ArchivePath: locator}
		actual, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := base+nativeArchivePathBytes(locator), int64(len(actual)+1); got != want {
			t.Fatalf("locator charge %d want %d", got, want)
		}
	}
	if err := validateNativeMarkerBudget(a, files, dirs); err != nil {
		t.Fatal(err)
	}
	a.NativeResources.MarkerBytes--
	if err := validateNativeMarkerBudget(a, files, dirs); err == nil {
		t.Fatal("undersized source marker budget accepted")
	}
	a.NativeResources.MarkerBytes = 16 << 20
	stage := t.TempDir()
	if err := writeArchiveReceipt(stage, a, `C:\工具\host.zip`, files, dirs); err == nil {
		t.Fatal("escaped locator overflow passed writer admission")
	}
	if _, err := os.Stat(filepath.Join(stage, archiveMarker)); !os.IsNotExist(err) {
		t.Fatal("overflow opened receipt before refusal")
	}
}
