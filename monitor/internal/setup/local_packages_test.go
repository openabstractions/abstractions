package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func localPackagePin(t *testing.T, body []byte, name string) PinnedDownload {
	t.Helper()
	sum := sha256.Sum256(body)
	return PinnedDownload{URL: "https://github.com/openabstractions/redist/releases/download/v0.8.0/" + name,
		SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(body)), Label: "Install comfyui"}
}

func TestLocalPackageFolderServesExactReleaseAsset(t *testing.T) {
	folder := t.TempDir()
	body := []byte("comfyui host package")
	if err := os.WriteFile(filepath.Join(folder, "ComfyUI-OA-windows-x64.zip"), body, 0600); err != nil {
		t.Fatal(err)
	}
	saved := LocalPackageFolder
	LocalPackageFolder = folder
	defer func() { LocalPackageFolder = saved }()
	pin := localPackagePin(t, body, "ComfyUI-OA-windows-x64.zip")
	ctx := context.Background()
	transfer := PublicTransfer{}
	// TestMain points runtime discovery at an endpoint nobody serves; a local
	// package never reaches it.
	work, err := transfer.Reserve(ctx, pin, "setup-key")
	if err != nil {
		t.Fatal(err)
	}
	if !validWorkIdentity(&work) || !isLocalPackageWork(work) {
		t.Fatalf("local work is not journal-valid: %+v", work)
	}
	if err := transfer.Submit(ctx, pin, work); err != nil {
		t.Fatal(err)
	}
	observation, err := transfer.Observe(ctx, work)
	if err != nil || observation.State != "complete" || observation.Total != int64(len(body)) {
		t.Fatalf("observation %+v, %v", observation, err)
	}
	var copied bytes.Buffer
	if n, err := transfer.Copy(ctx, work, &copied); err != nil || n != int64(len(body)) || !bytes.Equal(copied.Bytes(), body) {
		t.Fatalf("copy %d %v %q", n, err, copied.Bytes())
	}
	if err := transfer.Cancel(ctx, work); err != nil {
		t.Fatal(err)
	}
}

func TestLocalPackageFolderRefusesOtherAssets(t *testing.T) {
	folder := t.TempDir()
	body := []byte("pytorch component")
	if err := os.WriteFile(filepath.Join(folder, "pt.zip"), body, 0600); err != nil {
		t.Fatal(err)
	}
	saved := LocalPackageFolder
	defer func() { LocalPackageFolder = saved }()
	LocalPackageFolder = folder
	pin := localPackagePin(t, body, "pt.zip")
	wrongSize := pin
	wrongSize.Bytes++
	otherHost := pin
	otherHost.URL = "https://huggingface.co/x/y/resolve/0123456789012345678901234567890123456789/pt.zip"
	otherOwner := pin
	otherOwner.URL = "https://github.com/someone/redist/releases/download/v0.8.0/pt.zip"
	missing := localPackagePin(t, body, "absent.zip")
	for name, p := range map[string]PinnedDownload{"size": wrongSize, "host": otherHost, "owner": otherOwner, "missing": missing} {
		if localPackage(p) != "" {
			t.Fatalf("%s: folder served an unpinned asset", name)
		}
	}
	LocalPackageFolder = ""
	if localPackage(pin) != "" {
		t.Fatal("ordinary builds have no package folder")
	}
	LocalPackageFolder = "relative"
	if localPackage(pin) != "" {
		t.Fatal("a relative folder is refused")
	}
	// Journalled local work names its file; a build with another folder
	// refuses to read it.
	LocalPackageFolder = folder
	work := localPackageWork("k", filepath.Join(folder, "pt.zip"))
	LocalPackageFolder = t.TempDir()
	if observation, _ := (PublicTransfer{}).Observe(context.Background(), work); observation.State != "failed" {
		t.Fatalf("observation %+v", observation)
	}
}

// A package folder's adopter_assets.json pins the PyTorch component and the
// ComfyUI integration's extension MSI in Setup's catalogue; an earlier
// preview's ComfyUI row pins nothing (Setup installs no ComfyUI), and a
// refused document keeps the embedded pins.
func TestLocalPackageFolderPinsTheCatalogue(t *testing.T) {
	raw, err := os.ReadFile("testdata/native_producer_catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var exported struct {
		Row map[string]any `json:"row"`
	}
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatal(err)
	}
	row := exported.Row
	delete(row, "release_tag")
	pytorch := map[string]any{"id": "pytorch", "binary_zip": "pytorch-inputs.zip", "binary_sha256": strings.Repeat("a", 64), "binary_bytes": 100,
		"source_zip": "pytorch-source.zip", "source_sha256": strings.Repeat("d", 64), "source_bytes": 10,
		"source_commit": strings.Repeat("e", 40), "source_url": "https://github.com/openabstractions/abstractions/commit/" + strings.Repeat("e", 40),
		"signer": "unsigned", "version": "2.11.0", "platform": "windows/amd64", "peak_bytes": 4 << 20, "license": "BSD-3-Clause",
		"publisher": "openabstractions", "install_identity": "pt", "action": "archive_per_user"}
	extension := map[string]any{"id": "comfyui", "file": "comfyui-0.8.0.msi", "sha256": strings.Repeat("b", 64), "bytes": 4096, "version": "0.8.0"}
	document, err := json.Marshal(map[string]any{"schema": 2, "release_tag": "v0.8.0", "apps": []any{row, pytorch}, "extensions": []any{extension}})
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	saved, savedSHA := LocalPackageFolder, LocalPackageAssetsSHA256
	defer func() { LocalPackageFolder, LocalPackageAssetsSHA256 = saved, savedSHA }()
	LocalPackageFolder = folder
	if err := os.WriteFile(filepath.Join(folder, localPackageAssetsName), document, 0600); err != nil {
		t.Fatal(err)
	}
	pinned := func() bool {
		for _, component := range catalogForTarget("windows", "amd64").Components {
			if component.ID == "pytorch-engine" && component.Artifact != nil {
				return true
			}
		}
		return false
	}
	// Without the linked SHA-256, or with another, the document is ignored.
	for _, digest := range []string{"", strings.Repeat("0", 64)} {
		LocalPackageAssetsSHA256 = digest
		if pinned() {
			t.Fatalf("document used with linked SHA-256 %q", digest)
		}
	}
	sum := sha256.Sum256(document)
	LocalPackageAssetsSHA256 = hex.EncodeToString(sum[:])
	catalog := catalogForTarget("windows", "amd64")
	artifacts := map[string]*Artifact{}
	for _, component := range catalog.Components {
		artifacts[component.ID] = component.Artifact
	}
	if a := artifacts["comfyui-app"]; a != nil {
		t.Fatalf("an earlier preview's ComfyUI row pinned the OA build: %+v", a)
	}
	if e := catalog.LegacyConnectorPin; e == nil || e.File != "comfyui-0.8.0.msi" || e.Bytes != 4096 ||
		e.URL != "https://github.com/openabstractions/redist/releases/download/v0.8.0/comfyui-0.8.0.msi" {
		t.Fatalf("the ComfyUI integration is not pinned from the folder: %+v", e)
	}
	if a := artifacts["pytorch-engine"]; a == nil || a.InstallIdentity != "pt" || a.Bytes != 100 {
		t.Fatalf("PyTorch is not pinned from the folder: %+v", a)
	}
	refused := []byte(`{"schema":2}`)
	if err := os.WriteFile(filepath.Join(folder, localPackageAssetsName), refused, 0600); err != nil {
		t.Fatal(err)
	}
	sum = sha256.Sum256(refused)
	LocalPackageAssetsSHA256 = hex.EncodeToString(sum[:])
	if pinned() || catalogForTarget("windows", "amd64").LegacyConnectorPin != nil {
		t.Fatal("a refused folder document pinned a package")
	}
}
