//go:build windows

package setup

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

func TestFreshExtensionPackageRefusesRelatedProductBeforeNativeEffects(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	home, err := CurrentHome()
	if err != nil {
		t.Fatal(err)
	}
	const related = "{8DC059C0-A7EF-4E7B-8DD9-D65255F72DB1}"
	spec := product.PackageSpec{
		ID: "example", Title: "Example App", HostSubfolder: "extensions",
		InstallFolder: "example-integration", Connector: "example-connector.exe", HostKey: "hostFolder",
		UpgradeCode: "{DBED50A1-2435-4D68-8031-2BD5BEA7C506}",
	}
	base := t.TempDir()
	var enumerations, configurations, installs, marks int
	enumerate := func(upgrade string) ([]string, error) {
		enumerations++
		if upgrade != spec.UpgradeCode {
			t.Fatalf("enumerated unrelated upgrade family %q", upgrade)
		}
		return []string{related}, nil
	}
	dispatch := func(_ context.Context, products []string, _, config, folder string, gotSpec product.PackageSpec, _ extensionPackageMode, markEffect func() error) error {
		configurations += len(products)
		installs++
		if !slices.Equal(products, []string{related}) {
			t.Fatalf("replacement lost related products %v", products)
		}
		contents, err := os.ReadFile(config)
		var owner map[string]string
		if err != nil || json.Unmarshal(contents, &owner) != nil || owner[spec.HostKey] != base ||
			folder != filepath.Join(base, spec.HostSubfolder, spec.InstallFolder) || gotSpec != spec {
			t.Fatalf("installer did not receive its declared layout: config %q, folder %q, spec %+v, read error %v", contents, folder, gotSpec, err)
		}
		return markEffect()
	}
	ctx := context.Background()
	mark := func() error { marks++; return nil }
	err = connectExtensionPackageWithOperations(ctx, "candidate.msi", base, spec, extensionFreshPackage, mark, enumerate, dispatch)
	if !errors.Is(err, errFreshExtensionProductPresent) || enumerations != 1 || configurations != 0 || installs != 0 || marks != 0 {
		t.Fatalf("fresh refusal: enumeration %d configure %d install %d marks %d error %v", enumerations, configurations, installs, marks, err)
	}
	if _, err := os.Stat(filepath.Join(home.Config, spec.ID+"-host.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh refusal wrote the host configuration: %v", err)
	}
	if err := connectExtensionPackageWithOperations(ctx, "candidate.msi", base, spec, extensionReplacePackage, mark, enumerate, dispatch); err != nil ||
		enumerations != 2 || configurations != 1 || installs != 1 || marks != 1 {
		t.Fatalf("replacement: enumeration %d configure %d install %d marks %d error %v", enumerations, configurations, installs, marks, err)
	}
}

func TestFreshExtensionMSICommandIsOnlyForFreshMode(t *testing.T) {
	const config = `C:\fixture\host.json`
	const ordinary = `OAHOSTCONFIG="C:\fixture\host.json"`
	if got := extensionInstallProperties(config, extensionReplacePackage); got != ordinary {
		t.Fatalf("replacement properties = %q", got)
	}
	if got := extensionInstallProperties(config, extensionFreshPackage); got != ordinary+" OAFRESHONLY=1" {
		t.Fatalf("fresh properties = %q", got)
	}
}

func TestFreshExtensionMSITableProof(t *testing.T) {
	guarded := os.Getenv("OA_TEST_FRESH_EXTENSION_MSI")
	legacy := os.Getenv("OA_TEST_LEGACY_EXTENSION_MSI")
	if guarded == "" || legacy == "" {
		t.Skip("set both independently built fixture MSI paths to inspect their tables")
	}
	raw, err := os.ReadFile(guarded)
	if err != nil {
		t.Fatal(err)
	}
	release, err := HoldSavedPackage(context.Background(), guarded, fmt.Sprintf("%x", sha256.Sum256(raw)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	})
	if err := verifyFreshExtensionMSI(guarded, fixtureOfficialExtension().spec.UpgradeCode); err != nil {
		t.Fatalf("guarded MSI: %v", err)
	}
	if err := verifyFreshExtensionMSI(legacy, fixtureOfficialExtension().spec.UpgradeCode); !errors.Is(err, errNoFreshMSI) {
		t.Fatalf("legacy MSI guard proof: %v", err)
	}
	if err := verifyFreshExtensionMSI(guarded, "{11111111-1111-1111-1111-111111111111}"); !errors.Is(err, errNoFreshMSI) {
		t.Fatalf("wrong upgrade family guard proof: %v", err)
	}
}
