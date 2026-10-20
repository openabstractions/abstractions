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

func TestPackageConnectionRecoversOnlyAnAcknowledgedInstall(t *testing.T) {
	for _, acknowledged := range []bool{true, false} {
		t.Run(fmt.Sprintf("acknowledged=%t", acknowledged), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base := t.TempDir()
			saved := LocalPackageFolder
			t.Cleanup(func() { LocalPackageFolder = saved })
			LocalPackageFolder = t.TempDir()
			const previous, candidate, connector = "previous MSI", "approved MSI", "approved connector"
			digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
			target := packageTarget{catalogID: "example-app", registration: "example", connector: filepath.Join(base, "connector.exe")}
			spec := product.PackageSpec{ID: target.catalogID, Registration: target.registration, Connector: filepath.Base(target.connector)}
			current, source := filepath.Join(base, "example.msi"), filepath.Join(LocalPackageFolder, "example.msi")
			writeComfyFixture(t, current, previous)
			writeComfyFixture(t, source, candidate)
			pin := PinnedDownload{URL: releaseAssetURL("v0.8.0", "example.msi"), SHA256: digest(candidate), Bytes: int64(len(candidate))}
			image := sevenZipPin{Path: target.connector, SHA256: digest(connector), Bytes: int64(len(connector))}
			record := filepath.Join(base, "connection.json")
			load := func() *PackageConnectionReceipt {
				raw, err := os.ReadFile(record)
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				if err != nil {
					t.Fatal(err)
				}
				var receipt PackageConnectionReceipt
				if err := json.Unmarshal(raw, &receipt); err != nil {
					t.Fatal(err)
				}
				return &receipt
			}
			calls, confirms, commits := 0, 0, 0
			install := func(_ context.Context, _ string, mark func() error) error {
				if err := mark(); err != nil {
					return err
				}
				calls++
				if receipt := load(); receipt == nil || receipt.InstallerAcknowledged || receipt.PackageSHA256 != pin.SHA256 {
					t.Fatalf("MSI dispatched without exact receipt: %+v", receipt)
				}
				writeComfyFixture(t, target.connector, connector)
				cancel()
				if !acknowledged {
					return errors.New("result lost")
				}
				return nil
			}
			ops := PackageConnection{
				Admit:  func(context.Context, string, string, bool) error { return nil },
				Expect: func(context.Context, string, string, string) {},
				Confirm: func(ctx context.Context, _, _ string) error {
					confirms++
					return ctx.Err()
				},
				Save: func(ctx context.Context, before, after *PackageConnectionReceipt) error {
					got := load()
					if (got == nil) != (before == nil) || before != nil && *got != *before {
						t.Fatalf("receipt changed: got %+v, expected %+v", got, before)
					}
					raw, err := json.Marshal(after)
					if err != nil {
						return err
					}
					return writePinnedPackage(ctx, record, raw, 4096)
				},
				Commit: func(ctx context.Context, receipt PackageConnectionReceipt, publish func(context.Context) error) error {
					commits++
					if got := load(); got == nil || *got != receipt || !got.InstallerAcknowledged {
						t.Fatalf("committed without acknowledged receipt: %+v", got)
					}
					if err := publish(ctx); err != nil {
						return err
					}
					return os.Remove(record)
				},
			}
			if err := runPackageConnection(ctx, spec, target, pin, image, current, false, install, ops, ""); err == nil || commits != 0 {
				t.Fatalf("canceled install: commits %d, error %v", commits, err)
			}
			ops.ExistingReceipt = load()
			if ops.ExistingReceipt == nil || ops.ExistingReceipt.InstallerAcknowledged != acknowledged || calls != 1 {
				t.Fatalf("acknowledgment: calls %d record %+v", calls, ops.ExistingReceipt)
			}
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			beforeConfirm := confirms
			otherRelease := pin
			otherRelease.URL = releaseAssetURL("v0.8.1", "example.msi")
			if err := runPackageConnection(context.Background(), spec, target, otherRelease, image, current, true, install, ops, ""); err == nil || calls != 1 || confirms != beforeConfirm || commits != 0 {
				t.Fatalf("different release reached pending installation: calls %d, confirms %d, commits %d, error %v", calls, confirms, commits, err)
			}
			// A separate session can have read an empty record before this
			// one wrote its receipt. It loses the receipt claim after staging.
			const otherPackage = "different release MSI"
			otherRelease.SHA256, otherRelease.Bytes = digest(otherPackage), int64(len(otherPackage))
			writeComfyFixture(t, source, otherPackage)
			stale := ops
			stale.ExistingReceipt = nil
			stale.Expect = func(context.Context, string, string, string) {
				t.Fatal("the losing receipt writer changed the native expectation")
			}
			lostClaim := errors.New("another session owns the receipt")
			stale.Save = func(context.Context, *PackageConnectionReceipt, *PackageConnectionReceipt) error {
				return lostClaim
			}
			if err := runPackageConnection(context.Background(), spec, target, otherRelease, image, current, false, install, stale, ""); !errors.Is(err, lostClaim) {
				t.Fatalf("stale session returned %v, want lost claim", err)
			}
			err := runPackageConnection(context.Background(), spec, target, pin, image, current, true, install, ops, "")
			raw, readErr := os.ReadFile(current)
			if readErr != nil || calls != 1 {
				t.Fatalf("retry touched native installer or saved path: calls %d read %v", calls, readErr)
			}
			if acknowledged {
				if err != nil || commits != 1 || confirms != beforeConfirm+1 || load() != nil || string(raw) != candidate {
					t.Fatalf("acknowledged retry: commits %d, confirms %d, saved %q, error %v", commits, confirms, raw, err)
				}
			} else if err == nil || commits != 0 || confirms != beforeConfirm || load() == nil || string(raw) != previous {
				t.Fatalf("unknown retry: commits %d, confirms %d, saved %q, error %v", commits, confirms, raw, err)
			}
		})
	}
}

func TestExampleAppPackageConnectionPublishesAfterNativeProof(t *testing.T) {
	for _, tc := range []struct {
		name         string
		knownForeign bool
		lateConflict bool
		reconnect    bool
		relatedMSI   bool
		steps        []string
	}{
		{name: "first connection", steps: []string{"admit", "admit", "expect", "install", "confirm", "commit"}},
		{name: "same registration owned by another identity", knownForeign: true, steps: []string{"admit"}},
		{name: "identity conflict during native registration", lateConflict: true, steps: []string{"admit", "admit", "expect", "install", "confirm"}},
		{name: "related native product with no Registry entry", relatedMSI: true, steps: []string{"admit", "native preflight"}},
		{name: "the present connector has no installer proof", reconnect: true, steps: []string{"admit", "expect", "confirm"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			base := t.TempDir()
			saved := LocalPackageFolder
			t.Cleanup(func() { LocalPackageFolder = saved })
			LocalPackageFolder = t.TempDir()
			const previous, candidate, image = "saved MSI", "reviewed MSI", "pinned connector"
			digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
			spec := product.PackageSpec{ID: "example-app", Title: "Example App", Registration: "example-runtime", Connector: "connector.exe"}
			target := packageTarget{catalogID: spec.ID, registration: spec.Registration, connector: filepath.Join(base, spec.Connector)}
			current := filepath.Join(base, "example.msi")
			writeComfyFixture(t, current, previous)
			writeComfyFixture(t, filepath.Join(LocalPackageFolder, "example.msi"), candidate)
			if tc.reconnect {
				writeComfyFixture(t, target.connector, image)
			}
			pin := PinnedDownload{URL: releaseAssetURL("v0.8.0", "example.msi"), SHA256: digest(candidate), Bytes: int64(len(candidate))}
			checkSaved := func(want string) {
				t.Helper()
				got, err := os.ReadFile(current)
				if err != nil || string(got) != want {
					t.Fatalf("saved package = %q, %v, want %q", got, err, want)
				}
			}
			var steps []string
			const otherIdentity = "another signer"
			registry := ""
			if tc.knownForeign {
				registry = otherIdentity
			}
			conflict := errors.New("native identity conflict")
			related := errors.New("related MSI is present")
			err := runPackageConnection(ctx, spec, target, pin, sevenZipPin{Path: target.connector, SHA256: digest(image), Bytes: int64(len(image))},
				current, tc.reconnect, func(_ context.Context, msi string, mark func() error) error {
					if err := mark(); err != nil {
						return err
					}
					steps = append(steps, "install")
					checkSaved(previous)
					data, err := os.ReadFile(msi)
					if err != nil || string(data) != candidate || msi == current {
						t.Fatalf("staged MSI = %q, %v, from %q", data, err, msi)
					}
					return os.WriteFile(target.connector, []byte(image), 0600)
				}, PackageConnection{
					checkFreshProduct: func(context.Context) error {
						if tc.relatedMSI {
							steps = append(steps, "native preflight")
							return related
						}
						return nil
					},
					Admit: func(_ context.Context, name, program string, _ bool) error {
						steps = append(steps, "admit")
						if name != spec.Registration || program != target.connector {
							t.Fatalf("admitted %q at %q", name, program)
						}
						if registry == otherIdentity {
							return conflict
						}
						return nil
					},
					Expect: func(_ context.Context, name, program, sha string) {
						steps = append(steps, "expect")
						checkSaved(previous)
						if name != spec.Registration || program != target.connector || sha != digest(image) {
							t.Fatalf("expected %q at %q with %q", name, program, sha)
						}
					},
					Confirm: func(_ context.Context, name, program string) error {
						steps = append(steps, "confirm")
						checkSaved(previous)
						if name != spec.Registration || program != target.connector {
							t.Fatalf("confirmed %q at %q", name, program)
						}
						if tc.lateConflict {
							registry = otherIdentity
							return conflict
						}
						registry = program
						return nil
					},
					Save: func(context.Context, *PackageConnectionReceipt, *PackageConnectionReceipt) error { return nil },
					Commit: func(ctx context.Context, receipt PackageConnectionReceipt, publish func(context.Context) error) error {
						steps = append(steps, "commit")
						checkSaved(previous)
						if !receipt.InstallerAcknowledged || receipt.ConnectorSHA256 != digest(image) || receipt.PackageSHA256 != pin.SHA256 || registry != target.connector {
							t.Fatalf("receipt %+v registry %q", receipt, registry)
						}
						if err := publish(ctx); err != nil {
							return err
						}
						checkSaved(candidate)
						return nil
					},
				}, "")
			if !slices.Equal(steps, tc.steps) {
				t.Fatalf("events = %v, error = %v", steps, err)
			}
			if tc.relatedMSI {
				checkSaved(previous)
				if _, statErr := os.Stat(packageStagePath(current, pin.SHA256)); !errors.Is(err, related) || !errors.Is(statErr, os.ErrNotExist) || registry != "" {
					t.Fatalf("related native product refusal: entry %q, error %v, stage %v", registry, err, statErr)
				}
			} else if tc.knownForeign || tc.lateConflict {
				checkSaved(previous)
				if !errors.Is(err, conflict) || registry != otherIdentity {
					t.Fatalf("identity conflict: entry %q, error %v", registry, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if tc.reconnect {
					checkSaved(previous)
				}
			}
		})
	}
}

func TestConnectorInstallUsesDeclaredHostAndAdapterReadback(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	spec := product.PackageSpec{ID: "example-app", Title: "Example App", HostSubfolder: "plugins",
		InstallFolder: "example-integration", Connector: "example.exe"}
	const marker = "extension.ready"
	connector := filepath.Join(base, "plugins", "example-integration", "example.exe")
	calls, effects := 0, 0
	wantMode := extensionFreshPackage
	mark := func() error { effects++; return nil }
	dispatch := func(_ context.Context, msi, host string, got product.PackageSpec, mode extensionPackageMode, markEffect func() error) error {
		calls++
		if msi != "reviewed MSI" || host != base || got != spec || mode != wantMode {
			t.Fatalf("dispatch %q, %q, %+v, %v", msi, host, got, mode)
		}
		if err := markEffect(); err != nil {
			return err
		}
		writeComfyFixture(t, connector, "installed")
		return nil
	}
	install := func(marker string) error {
		return installExtensionAtHost(ctx, "reviewed MSI", base, spec, marker, wantMode, mark, dispatch)
	}
	if err := install(marker); err == nil || calls != 0 || effects != 0 {
		t.Fatalf("missing host reached the installer: %d, %d, %v", calls, effects, err)
	}
	if err := os.Mkdir(filepath.Dir(filepath.Dir(connector)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := install(marker); err == nil || calls != 1 || effects != 1 {
		t.Fatalf("missing marker passed readback: %d, %d, %v", calls, effects, err)
	}
	wantMode = extensionReplacePackage
	writeComfyFixture(t, filepath.Join(filepath.Dir(connector), marker), "ready")
	if err := install(marker); err != nil || calls != 2 || effects != 2 {
		t.Fatalf("verified replacement: %d, %d, %v", calls, effects, err)
	}
	if err := os.Remove(filepath.Join(filepath.Dir(connector), marker)); err != nil {
		t.Fatal(err)
	}
	wantMode = extensionFreshPackage
	if err := install(""); err != nil || calls != 3 || effects != 3 {
		t.Fatalf("connector-only package: %d, %d, %v", calls, effects, err)
	}
}

func TestDeclaredConnectorProofSeparatesReadbackAndCustody(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	spec := product.PackageSpec{ID: "example-app", Registration: "example-runtime", HostSubfolder: "plugins",
		InstallFolder: "reviewed-extension", Connector: "example.exe"}
	path := packageConnector(spec, base)
	target := packageTarget{catalogID: spec.ID, registration: spec.Registration, connector: path}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("exact")))
	binding := admittedPackage{spec: spec, releaseTag: "v0.8.0",
		pin: adopterExtensionPin{File: "example.msi", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("msi"))),
			Bytes: 3, ConnectorSHA256: digest, ConnectorBytes: 5}}
	catalog := Catalog{connectorPackages: map[string]admittedPackage{spec.ID: binding}}
	pin, err := catalog.connectorPin(spec.ID)
	if err != nil || pin == nil {
		t.Fatalf("admitted connector pin: %v", err)
	}
	if outdated, err := pin.connectorOutdated(path); outdated || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing connector = outdated %t, error %v", outdated, err)
	}
	writeComfyFixture(t, path, "wrong")
	if outdated, err := pin.connectorOutdated(path); !outdated || err != nil {
		t.Fatalf("changed connector = outdated %t, error %v", outdated, err)
	}
	if held, err := catalog.holdCurrentConnector(ctx, base, target, spec, pin); !errors.Is(err, errPinnedDigest) {
		_ = held.Close()
		t.Fatalf("changed bytes accepted or misclassified: %v", err)
	}
	writeComfyFixture(t, path, "exact")
	if _, err := pin.packageDigestForConnector(digest, 4); err == nil {
		t.Fatal("a different connector length supplied a package proof")
	}
	if got, err := pin.packageDigestForConnector(digest, 5); err != nil || got != binding.pin.SHA256 {
		t.Fatalf("connector package proof = %q, %v", got, err)
	}
	held, err := catalog.holdCurrentConnector(ctx, base, target, spec, pin)
	if err != nil || held.Path != path || held.SHA256 != digest {
		t.Fatalf("declared connector hold = %+v, %v", held, err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	target.registration = "another-entry"
	if held, err := catalog.holdCurrentConnector(ctx, base, target, spec, pin); err == nil {
		_ = held.Close()
		t.Fatal("mismatched registration received a connector hold")
	}
}

func TestDeclaredLegacyConnectionOrdersRetainTheSameDurableReceipt(t *testing.T) {
	for _, order := range []string{publishFirst, installFirst} {
		t.Run(order, func(t *testing.T) {
			ctx := context.Background()
			previousPackageFolder := LocalPackageFolder
			t.Cleanup(func() { LocalPackageFolder = previousPackageFolder })
			LocalPackageFolder = t.TempDir()
			base := t.TempDir()
			current := filepath.Join(base, "example.msi")
			spec := product.PackageSpec{ID: "example-app", Registration: "example-runtime", Connector: "connector.exe"}
			target := packageTarget{catalogID: spec.ID, registration: spec.Registration, connector: filepath.Join(base, spec.Connector)}
			const earlier, candidate = "earlier MSI", "exact legacy MSI"
			pin := PinnedDownload{URL: releaseAssetURL("v0.8.0", "example.msi"), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(candidate))), Bytes: int64(len(candidate))}
			writeComfyFixture(t, current, earlier)
			writeComfyFixture(t, filepath.Join(LocalPackageFolder, "example.msi"), candidate)
			readCurrent := func(want string) {
				t.Helper()
				raw, err := os.ReadFile(current)
				if err != nil || string(raw) != want {
					t.Fatalf("saved-current = %q, %v, want %q", raw, err, want)
				}
			}
			const connector = "verified legacy connector"
			writeComfyFixture(t, target.connector, connector)
			image := sevenZipPin{Path: target.connector, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(connector))), Bytes: int64(len(connector))}
			store := PackageMaintenanceStore{Path: filepath.Join(base, "maintenance.json")}
			earlierPin := fmt.Sprintf("%x", sha256.Sum256([]byte("earlier connector")))
			if err := store.Edit(ctx, func(record *PackageMaintenanceRecord) error {
				record.RecordKeptConnector(earlierPin)
				record.Declined = image.SHA256
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			conflict := errors.New("legacy registry authority refused")
			confirmations, installs, commits := 0, 0, 0
			ops := PackageConnection{
				Expect: func(_ context.Context, registration, program, digest string) {
					if registration != spec.Registration || program != target.connector || digest != image.SHA256 {
						t.Fatalf("unexpected legacy expectation %q %q %q", registration, program, digest)
					}
				},
				ConfirmLegacy: func(_ context.Context, registration, program string) error {
					confirmations++
					if registration != spec.Registration || program != target.connector {
						t.Fatalf("confirmed a foreign registration: %q %q", registration, program)
					}
					if confirmations == 1 {
						return conflict
					}
					return nil
				},
				Save: func(ctx context.Context, before, after *PackageConnectionReceipt) error {
					return store.Edit(ctx, func(record *PackageMaintenanceRecord) error { return record.SaveConnection(before, after) })
				},
				Publish: func(ctx context.Context, prepared PackageConnectionReceipt, publish func(context.Context) error) error {
					return store.Edit(ctx, func(record *PackageMaintenanceRecord) error { return record.PublishConnection(ctx, prepared, publish) })
				},
				Commit: func(ctx context.Context, installed PackageConnectionReceipt, publish func(context.Context) error) error {
					commits++
					return store.Edit(ctx, func(record *PackageMaintenanceRecord) error { return record.CommitConnection(ctx, installed, publish) })
				},
			}
			duringInstall := earlier
			if order == publishFirst {
				duringInstall = candidate
			}
			install := func(_ context.Context, staged string, before func() error) error {
				if err := before(); err != nil {
					return err
				}
				installs++
				readCurrent(duringInstall)
				raw, err := os.ReadFile(staged)
				if err != nil || string(raw) != candidate || staged == current {
					t.Fatalf("installer did not receive exact staged bytes: %q %v", raw, err)
				}
				record, err := store.Load()
				if err != nil || record.Connection == nil || record.Connection.LegacyOrder != order || record.Connection.InstallerAcknowledged {
					t.Fatalf("dispatch did not bind legacy order: %+v, %v", record.Connection, err)
				}
				return nil
			}
			err := runPackageConnection(ctx, spec, target, pin, image, current, false, install, ops, order)
			if !errors.Is(err, conflict) || installs != 1 || commits != 0 {
				t.Fatalf("register refusal: installs %d, commits %d, error %v", installs, commits, err)
			}
			readCurrent(duringInstall)
			record, err := store.Load()
			if err != nil || record.Connection == nil || !record.Connection.InstallerAcknowledged || record.Connection.LegacyOrder != order {
				t.Fatalf("positive install proof was lost: %+v, %v", record.Connection, err)
			}
			ops.ExistingReceipt = record.Connection
			otherOrder := publishFirst
			if order == publishFirst {
				otherOrder = installFirst
			}
			err = runPackageConnection(ctx, spec, target, pin, image, current, false, install, ops, otherOrder)
			if err == nil || confirmations != 1 || commits != 0 {
				t.Fatalf("different order accepted the receipt: confirmations %d, commits %d, error %v", confirmations, commits, err)
			}
			if order == publishFirst {
				writeComfyFixture(t, current, "changed saved package")
				err = runPackageConnection(ctx, spec, target, pin, image, current, false, install, ops, order)
				if err == nil || installs != 1 || confirmations != 1 || commits != 0 {
					t.Fatalf("unverified saved-current reached settlement: installs %d, confirmations %d, commits %d, error %v", installs, confirmations, commits, err)
				}
				writeComfyFixture(t, current, candidate)
			}
			err = runPackageConnection(ctx, spec, target, pin, image, current, false, install, ops, order)
			if err != nil || installs != 1 || confirmations != 2 || commits != 1 {
				t.Fatalf("exact resume: installs %d, confirmations %d, commits %d, error %v", installs, confirmations, commits, err)
			}
			readCurrent(candidate)
			record, err = store.Load()
			if err != nil || record.Connection != nil || record.Kept != image.SHA256 || record.Declined != "" ||
				record.KeptPackageSHA256 != "" || !slices.Equal(record.History, []string{earlierPin, image.SHA256}) {
				t.Fatalf("legacy completion did not settle the exact connector history: %+v, %v", record, err)
			}
		})
	}
}

func TestPublishFirstResumesAReceiptBeforeNativeDispatch(t *testing.T) {
	ctx := t.Context()
	base := t.TempDir()
	spec := product.PackageSpec{ID: "example-app", Registration: "example-runtime", Connector: "connector.exe"}
	target := packageTarget{catalogID: spec.ID, registration: spec.Registration, connector: filepath.Join(base, spec.Connector)}
	current := filepath.Join(base, "example.msi")
	const candidate = "verified staged MSI"
	pin := PinnedDownload{URL: "http://127.0.0.1:0/example.msi", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(candidate))), Bytes: int64(len(candidate))}
	stage := packageStagePath(current, pin.SHA256)
	writeComfyFixture(t, stage, candidate)
	writeComfyFixture(t, current, "earlier MSI")
	store := PackageMaintenanceStore{Path: filepath.Join(base, "maintenance.json")}
	claimed := PackageConnectionReceipt{
		CatalogID: spec.ID, Registration: spec.Registration, Program: target.connector,
		ReleaseURL: pin.URL, PackageSHA256: pin.SHA256, PackageBytes: pin.Bytes,
		LegacyOrder: publishFirst, BeforeDispatch: true,
	}
	save := func(ctx context.Context, before, after *PackageConnectionReceipt) error {
		return store.Edit(ctx, func(record *PackageMaintenanceRecord) error { return record.SaveConnection(before, after) })
	}
	if err := save(ctx, nil, &claimed); err != nil {
		t.Fatal(err)
	}
	start, err := store.Load()
	if err != nil || start.Connection == nil || *start.Connection != claimed {
		t.Fatalf("read persisted pre-dispatch claim: %+v, %v", start, err)
	}
	var installed, confirmed bool
	ops := PackageConnection{
		ExistingReceipt: start.Connection,
		Expect:          func(context.Context, string, string, string) {},
		ConfirmLegacy: func(context.Context, string, string) error {
			confirmed = installed
			return nil
		},
		Save: save,
		Publish: func(ctx context.Context, prepared PackageConnectionReceipt, publish func(context.Context) error) error {
			return store.Edit(ctx, func(record *PackageMaintenanceRecord) error { return record.PublishConnection(ctx, prepared, publish) })
		},
		Commit: func(ctx context.Context, receipt PackageConnectionReceipt, publish func(context.Context) error) error {
			return store.Edit(ctx, func(record *PackageMaintenanceRecord) error { return record.CommitConnection(ctx, receipt, publish) })
		},
	}
	install := func(_ context.Context, file string, before func() error) error {
		if file != stage {
			t.Fatalf("expected the original staging file, got %s", file)
		}
		raw, err := os.ReadFile(current)
		if err != nil || string(raw) != candidate {
			t.Fatalf("saved-current before MSI = %q, %v", raw, err)
		}
		if err := before(); err != nil {
			return err
		}
		record, err := store.Load()
		if err != nil || record.Connection == nil || record.Connection.BeforeDispatch || record.Connection.InstallerAcknowledged {
			t.Fatalf("native launch did not get its own durable dispatch state: %+v, %v", record, err)
		}
		installed = true
		return nil
	}
	image := sevenZipPin{Path: target.connector}
	if err := runPackageConnection(ctx, spec, target, pin, image, current, false, install, ops, publishFirst); err != nil {
		t.Fatal(err)
	}
	settled, err := store.Load()
	if err != nil || !installed || !confirmed || settled.Connection != nil || settled.Kept != "" {
		t.Fatalf("the exact pre-dispatch claim did not settle: %+v, installed=%v confirmed=%v: %v", settled, installed, confirmed, err)
	}
	if err := ops.Publish(ctx, claimed, func(context.Context) error {
		t.Fatal("stale publication can overwrite the settled package")
		return nil
	}); err == nil {
		t.Fatal("a settled operation accepted a stale publication claim")
	}
}
