package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func TestInstallerDesktopUpgradeDefersTheVerifiedPackageUntilOpenCodeCloses(t *testing.T) {
	busy, kept, action := true, true, "update"
	read, verify, readKeep, readBusy := readCatalogDesktopPackage, verifyCatalogDesktopHost, desktopUpgradeKept, desktopUpgradeBusy
	apply, resume, local := applyKeptDesktopUpgrade, applyStagedDesktopUpgrade, setup.LocalPackageFolder
	t.Cleanup(func() {
		readCatalogDesktopPackage, verifyCatalogDesktopHost, desktopUpgradeKept, desktopUpgradeBusy = read, verify, readKeep, readBusy
		applyKeptDesktopUpgrade, applyStagedDesktopUpgrade, setup.LocalPackageFolder = apply, resume, local
	})
	setup.LocalPackageFolder = t.TempDir()
	readCatalogDesktopPackage = func(setup.DesktopIntegration) (string, error) { return action, nil }
	verifyCatalogDesktopHost = func(setup.AppID) error { return nil }
	desktopUpgradeKept = func(context.Context, *servicePanel, setup.AppID) (bool, error) { return kept, nil }
	desktopUpgradeBusy = func(setup.DesktopIntegration) (bool, error) { return busy, nil }
	applyKeptDesktopUpgrade = func(setup.DesktopIntegration, context.Context, setup.Transfer, func(context.Context) error) error {
		t.Fatal("a busy fixture must not run Windows Installer")
		return nil
	}
	body := []byte("inert exact OpenCode MSI bytes")
	pin := &setup.DesktopIntegration{App: setup.OpenCode, Title: "OpenCode", File: "opencode-0.8.0.msi",
		URL:    "https://github.com/openabstractions/redist/releases/download/v0.8.0/opencode-0.8.0.msi",
		SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Bytes: int64(len(body))}
	source := filepath.Join(setup.LocalPackageFolder, pin.File)
	config := filepath.Join(t.TempDir(), "setup")
	if err := os.WriteFile(source, body, 0600); err != nil {
		t.Fatal(err)
	}
	p := &servicePanel{appActivity: applicationActivityLog{path: filepath.Join(config, "activity.json")}}
	run := func(mode desktopUpgradeMode, want int) {
		t.Helper()
		if got := p.upgradeDesktopIntegration(t.Context(), pin, config, mode); got != want {
			t.Fatalf("mode %d returned %d, want %d", mode, got, want)
		}
	}
	resumed := 0
	applyStagedDesktopUpgrade = func(candidate setup.DesktopIntegration, ctx context.Context, dir string, keep func(context.Context) error) error {
		if candidate.SHA256 != pin.SHA256 {
			t.Fatalf("changed integration pin: %v", candidate)
		}
		raw, err := os.ReadFile(filepath.Join(dir, candidate.File))
		if err != nil || !bytes.Equal(raw, body) {
			t.Fatalf("retained source = %q (%v)", raw, err)
		}
		resumed++
		return keep(ctx)
	}
	run(desktopUpgradeApply, desktopUpgradeRunning)
	run(desktopUpgradeDefer, desktopUpgradeLater)
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	run(desktopUpgradeResume, desktopUpgradeRunning)
	busy = false
	run(desktopUpgradeResume, desktopUpgradeSettled)
	run(desktopUpgradeResume, desktopUpgradeSettled)
	if resumed != 1 {
		t.Fatalf("deferred package applied %d times", resumed)
	}
	lines := p.appActivity.lines()
	if len(lines) != 2 || lines[0].Outcome != "Deferred" || lines[1].Outcome != "Updated" {
		t.Fatalf("activity: %+v", lines)
	}
	if err := os.WriteFile(source, body, 0600); err != nil {
		t.Fatal(err)
	}
	run(desktopUpgradeDefer, desktopUpgradeLater)
	kept = false
	run(desktopUpgradeResume, desktopUpgradeSettled)
	if resumed != 1 {
		t.Fatalf("an owner's disconnect applied the staged package")
	}
	kept, action = true, "install"
	run(desktopUpgradeApply, desktopUpgradeSettled)
	staged := filepath.Join(config, "deferred-desktop-integration-"+pin.SHA256, pin.File)
	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the cancelled staging file remains: %v", err)
	}
}

func TestInstallerIntegrationUpgradeConfirmsComfyUIOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, connector string
		want, installs  int
		sameConnector   bool
	}{
		{"verified", connectorBytes, desktopUpgradeSettled, 1, false},
		{"refused", "changed unexpected connector", desktopUpgradeRefused, 1, false},
		{"new package with the current connector", connectorBytes, desktopUpgradeSettled, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpgradeFixture(t)
			f.installed = tc.connector
			claim := officialPicture.claim
			t.Cleanup(func() { officialPicture.claim = claim })
			officialPicture.claim = func() (func(), bool, error) { return func() {}, true, nil }
			if tc.sameConnector {
				if err := f.writeConnector(f.found.Base, connectorBytes); err != nil {
					t.Fatal(err)
				}
				revision, _, err := officialPicture.extension.Revision(f.journal.Catalog())
				if err != nil {
					t.Fatal(err)
				}
				if err := officialPicture.changeRecord(t.Context(), func(record *setup.PackageMaintenanceRecord) {
					record.Kept, record.Seen, record.Recorded = revision, revision, revision
					record.KeptPackageSHA256 = comfyTestSum("earlier MSI")
				}); err != nil {
					t.Fatal(err)
				}
			}
			got := f.panel.finishIntegrationUpgrades(t.Context(), *f.journal, desktopUpgradeSettled)
			if got != tc.want || f.installs != tc.installs {
				t.Fatalf("maintenance = %d after %d installs, want %d after %d", got, f.installs, tc.want, tc.installs)
			}
		})
	}
}

func TestInstallerIntegrationUpgradeDefersSignedComfyCatalogPackage(t *testing.T) {
	f := newUpgradeFixture(t)
	claim, source, transfer := officialPicture.claim, setup.LocalPackageFolder, maintainedPackageTransfer
	t.Cleanup(func() {
		officialPicture.claim, setup.LocalPackageFolder, maintainedPackageTransfer = claim, source, transfer
	})
	officialPicture.claim = func() (func(), bool, error) { return func() {}, true, nil }
	pin := f.journal.Catalog().LegacyConnectorPin
	raw, err := os.ReadFile(filepath.Join(source, pin.File))
	if err != nil {
		t.Fatal(err)
	}
	document := bytes.Replace(comfyExtensionFixturePins(), []byte(pin.SHA256), []byte(fmt.Sprintf("%x", sha256.Sum256(raw))), 1)
	journal, err := f.journal.WithAdopterAssets(document)
	if err != nil {
		t.Fatal(err)
	}
	f.journal = &journal
	if err := setup.UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setup.UseUserState)
	home, err := setup.CurrentHome()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home.Config, 0o700); err != nil {
		t.Fatal(err)
	}
	setup.LocalPackageFolder = ""
	download := &codingPanelTransfer{panelTransfer: &panelTransfer{data: raw}}
	maintainedPackageTransfer = func(*servicePanel) setup.Transfer { return download }
	stubComfyProcesses(t, 100)
	if got := f.panel.finishIntegrationUpgrades(t.Context(), *f.journal, desktopUpgradeSettled); got != desktopUpgradeLater || f.installs != 0 {
		t.Fatalf("active signed candidate = %d, installs=%d", got, f.installs)
	}
	stubComfyProcesses(t)
	if got := f.panel.finishIntegrationUpgrades(t.Context(), *f.journal, desktopUpgradeSettled); got != desktopUpgradeSettled || f.installs != 1 || download.reserves != 1 {
		t.Fatalf("closed signed candidate = %d, installs=%d, transfers=%d", got, f.installs, download.reserves)
	}
}
