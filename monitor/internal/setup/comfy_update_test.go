package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func TestComfyExtensionMaintenanceRevision(t *testing.T) {
	for _, pin := range []*ConnectorPackagePin{nil, {File: "extension.msi"}} {
		if revision := pin.MaintenanceRevision(); revision != "" {
			t.Fatalf("unpinned extension revision %q", revision)
		}
	}
	const digest = "ABC012"
	pin := ConnectorPackagePin{ConnectorSHA256: digest, ConnectorBytes: 3}
	if revision := pin.MaintenanceRevision(); revision != "abc012" || pin.ConnectorSHA256 != digest || pin.ConnectorBytes != 3 {
		t.Fatalf("maintenance revision %q changed package pin: %+v", revision, pin)
	}
}

// An integration whose connector differs from this build's pin is outdated,
// and rewriting the connector is seen on the next read.
func TestComfyConnectorOutdatedFollowsThePin(t *testing.T) {
	base := t.TempDir()
	folder := filepath.Join(base, "custom_nodes", "comfyui-openabstractions")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	pinned := []byte("comfyui-connector pinned")
	sum := sha256.Sum256(pinned)
	c := Catalog{LegacyConnectorPin: &ConnectorPackagePin{ConnectorSHA256: hex.EncodeToString(sum[:]), ConnectorBytes: int64(len(pinned))}}
	connector := fixtureOfficialExtension().Connector(base)
	if _, err := fixtureOfficialExtension().Outdated(c, base); err == nil {
		t.Fatal("a missing connector compared")
	}
	if err := os.WriteFile(connector, []byte("comfyui-connector earlier"), 0o600); err != nil {
		t.Fatal(err)
	}
	if outdated, err := fixtureOfficialExtension().Outdated(c, base); err != nil || !outdated {
		t.Fatalf("earlier connector: %v %v", outdated, err)
	}
	if err := os.WriteFile(connector, pinned, 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(connector, later, later); err != nil {
		t.Fatal(err)
	}
	if outdated, err := fixtureOfficialExtension().Outdated(c, base); err != nil || outdated {
		t.Fatalf("pinned connector: %v %v", outdated, err)
	}
	if outdated, err := fixtureOfficialExtension().Outdated(Catalog{}, base); err != nil || outdated {
		t.Fatalf("a build without a pin: %v %v", outdated, err)
	}
}

// The pinned MSI in the package folder makes Update possible; without it
// the row says why.
func TestComfyExtensionMissingNamesTheReason(t *testing.T) {
	old := LocalPackageFolder
	t.Cleanup(func() { LocalPackageFolder = old })
	LocalPackageFolder = t.TempDir()
	c := Catalog{LegacyConnectorPin: &ConnectorPackagePin{File: "comfyui-0.8.0.msi", Bytes: 10,
		URL: "https://github.com/openabstractions/redist/releases/download/v0.8.0/comfyui-0.8.0.msi"}}
	if got := fixtureOfficialExtension().PackageMissing(c); got == "" {
		t.Fatal("a missing MSI is ready")
	}
	if err := os.WriteFile(filepath.Join(LocalPackageFolder, "comfyui-0.8.0.msi"), make([]byte, 10), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := fixtureOfficialExtension().PackageMissing(c); got != "" {
		t.Fatalf("the MSI is in the package folder: %q", got)
	}
	if got := fixtureOfficialExtension().PackageMissing(Catalog{}); got == "" {
		t.Fatal("a build without the integration is ready")
	}
}

// ComfyUI's programs are the ones in its folder and beside it.
func TestComfyProcessesFindsProgramsBesideComfyUI(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("process images are read on Windows")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pids, err := fixtureOfficialExtension().Processes(ExtensionHost{Base: filepath.Dir(self)})
	if err != nil || !slices.Contains(pids, uint32(os.Getpid())) {
		t.Fatalf("programs in the folder: %v %v", pids, err)
	}
	if pids, err := fixtureOfficialExtension().Processes(ExtensionHost{Base: filepath.Join(t.TempDir(), "ComfyUI")}); err != nil || len(pids) != 0 {
		t.Fatalf("an idle ComfyUI: %v %v", pids, err)
	}
}
