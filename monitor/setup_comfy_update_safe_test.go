package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func comfyTestSum(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func TestKeptUpdateBindsThePendingPackageRegistration(t *testing.T) {
	for _, registration := range []string{"foreign-registration", ""} {
		name := registration
		if name == "" {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			f := newUpgradeFixture(t)
			stubComfyProcesses(t, 4242)
			f.step(t)
			pending := officialPicture.readRecord().PendingUpdate
			if pending == nil || pending.Application != officialPicture.spec.Registration || pending.Application == "" {
				t.Fatalf("queued upgrade has no declared registration: %+v", pending)
			}
			if err := officialPicture.changeRecord(context.Background(), func(r *setup.PackageMaintenanceRecord) {
				r.PendingUpdate.Application = registration
			}); err != nil {
				t.Fatal(err)
			}
			before := officialPicture.readRecord()
			stubComfyProcesses(t)
			f.step(t)
			if registration == "" {
				after := officialPicture.readRecord()
				if f.installs != 1 || f.service.replaces != 1 || after.PendingUpdate != nil || after.Previous == nil {
					t.Fatalf("legacy upgrade did not finish: installs %d, replacements %d, receipt %+v", f.installs, f.service.replaces, after)
				}
				return
			}
			got := installHandlerFor(f.panel.handler("setup-test-key"))("POST", "/setup/comfy/connect", `{"confirm":true}`)
			if got.Code != http.StatusOK {
				t.Fatalf("Update request: HTTP %d %s", got.Code, got.Body.String())
			}
			if o := waitOutcome(t, f.panel, officialPicture.legacy.ConnectStep); o.Failure != "a different integration owns the pending replacement" {
				t.Fatalf("explicit Update outcome: %+v", o)
			}
			after := officialPicture.readRecord()
			if !reflect.DeepEqual(before, after) || f.installs != 0 || f.restores != 0 || f.publishes != 0 || f.service.replaces != 0 {
				t.Fatalf("foreign registration changed the receipt or effects: before %+v after %+v", before, after)
			}
		})
	}
}

func TestKeptReplacementRefusesAPredecessorFromAnotherPackage(t *testing.T) {
	for _, route := range []string{"automatic", "explicit"} {
		t.Run(route, func(t *testing.T) {
			f := newUpgradeFixture(t)
			if err := officialPicture.changeRecord(context.Background(), func(r *setup.PackageMaintenanceRecord) {
				r.KeptPackageSHA256 = comfyTestSum("a different saved MSI")
			}); err != nil {
				t.Fatal(err)
			}
			before := officialPicture.readRecord()
			if route == "explicit" {
				got := installHandlerFor(f.panel.handler("setup-test-key"))("POST", "/setup/comfy/connect", `{"confirm":true}`)
				if got.Code != http.StatusOK {
					t.Fatalf("Update request: HTTP %d %s", got.Code, got.Body.String())
				}
				if o := waitOutcome(t, f.panel, officialPicture.legacy.ConnectStep); o.Failure != "the saved predecessor differs from the kept package digest" {
					t.Fatalf("explicit Update outcome: %+v", o)
				}
			} else {
				f.step(t)
			}
			after := officialPicture.readRecord()
			if !reflect.DeepEqual(before, after) || f.installs != 0 || f.restores != 0 || f.publishes != 0 ||
				f.service.replaces != 0 || len(f.panel.appActivity.lines()) != 0 {
				t.Fatalf("predecessor mismatch changed state: before %+v after %+v", before, after)
			}
		})
	}
}

func TestCatalogRepairsTheMissingCompanionOnlyForTheExactKeptPredecessor(t *testing.T) {
	for _, state := range []string{"matching predecessor", "first connection", "missing connector", "foreign registration", "pending registration", "unknown predecessor", "different saved package", "connector changed before dispatch"} {
		t.Run(state, func(t *testing.T) {
			f := newUpgradeFixture(t)
			f.found.Connected = false
			stubOfficialComfy(t, f.found)
			connector := officialPicture.extension.Connector(f.found.Base)
			companion := filepath.Join(filepath.Dir(connector), "__init__.py")
			if err := os.Remove(companion); err != nil {
				t.Fatal(err)
			}
			earlier := f.service.entry.pin
			if err := officialPicture.changeRecord(context.Background(), func(r *setup.PackageMaintenanceRecord) {
				r.Kept, r.KeptPackageSHA256 = earlier, comfyTestSum("earlier MSI")
				switch state {
				case "first connection":
					r.Kept, r.KeptPackageSHA256 = "", ""
				case "unknown predecessor":
					r.KeptPackageSHA256 = ""
				case "different saved package":
					r.KeptPackageSHA256 = comfyTestSum("another MSI")
				}
			}); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "first connection":
				f.service.entry = nil
			case "missing connector":
				if err := os.Remove(connector); err != nil {
					t.Fatal(err)
				}
			case "foreign registration":
				f.service.entry.program = filepath.Join(t.TempDir(), filepath.Base(connector))
			case "pending registration":
				f.service.entry.state = "pending"
			}
			oldConnect, oldInstall := officialPicture.connectPackage, officialPicture.installPinned
			t.Cleanup(func() { officialPicture.connectPackage = oldConnect })
			connectCalls := 0
			officialPicture.connectPackage = func(context.Context, setup.Catalog, setup.ExtensionHost, setup.PackageConnection) (bool, error) {
				connectCalls++
				return true, errors.New("ordinary-first-connection fixture reached")
			}
			officialPicture.installPinned = func(ctx context.Context, catalog setup.Catalog, base string, before func() error) (func(context.Context) error, error) {
				if state == "connector changed before dispatch" {
					if err := f.writeConnector(base, "foreign connector"); err != nil {
						return nil, err
					}
				}
				publish, err := oldInstall(ctx, catalog, base, before)
				if err != nil {
					return nil, err
				}
				return publish, os.WriteFile(companion, []byte("installed companion"), 0600)
			}
			got := installHandlerFor(f.panel.handler("setup-test-key"))("POST", "/setup/comfy/connect", `{"confirm":true}`)
			if got.Code != http.StatusOK {
				t.Fatalf("Catalog request: HTTP %d %s", got.Code, got.Body.String())
			}
			outcome := waitOutcome(t, f.panel, officialPicture.legacy.ConnectStep)
			r := officialPicture.readRecord()
			if state == "matching predecessor" {
				lines := f.panel.appActivity.lines()
				if outcome.Failure != "" || f.installs != 1 || f.service.replaces != 1 || f.publishes != 1 ||
					f.service.questions != 0 || f.service.removes != 0 || connectCalls != 0 || r.PendingUpdate != nil ||
					r.Previous == nil || r.Previous.Program != connector || r.Previous.SHA256 != earlier ||
					r.Previous.PackageSHA256 != comfyTestSum("earlier MSI") || len(lines) != 1 ||
					!lines[0].UndoRestores || lines[0].At != r.Previous.ActivityAt {
					t.Fatalf("repair outcome %+v, record %+v, Activity %+v, installs %d, replacements %d, publications %d, ordinary Connect %d",
						outcome, r, lines, f.installs, f.service.replaces, f.publishes, connectCalls)
				}
				return
			}
			wantConnect := 0
			if state == "first connection" {
				wantConnect = 1
			}
			if outcome.Failure == "" || f.installs != 0 || f.service.replaces != 0 || f.restores != 0 ||
				f.publishes != 0 || connectCalls != wantConnect || r.Previous != nil || len(f.panel.appActivity.lines()) != 0 {
				t.Fatalf("unsafe repair ran effects: outcome %+v, record %+v, installs %d, replacements %d, restores %d, publications %d, ordinary Connect %d",
					outcome, r, f.installs, f.service.replaces, f.restores, f.publishes, connectCalls)
			}
		})
	}
}

func TestCatalogRefusesDamagedSamePinWithoutStoredIdentityProof(t *testing.T) {
	for _, state := range []string{"same stored pin", "different stored pin at the same path"} {
		t.Run(state, func(t *testing.T) {
			f := newUpgradeFixture(t)
			program := officialPicture.extension.Connector(f.found.Base)
			if err := f.writeConnector(f.found.Base, connectorBytes); err != nil {
				t.Fatal(err)
			}
			digest := comfyTestSum(connectorBytes)
			f.service.entry.pin = digest
			if state == "different stored pin at the same path" {
				f.service.entry.pin = comfyTestSum("a different stored image")
			}
			pin, revision := f.service.entry.pin, f.service.revision()
			if err := os.Remove(filepath.Join(filepath.Dir(program), "__init__.py")); err != nil {
				t.Fatal(err)
			}
			f.found.Connected = false
			stubOfficialComfy(t, f.found)
			if err := officialPicture.changeRecord(context.Background(), func(r *setup.PackageMaintenanceRecord) {
				r.Kept, r.KeptPackageSHA256 = digest, f.journal.Catalog().LegacyConnectorPin.SHA256
			}); err != nil {
				t.Fatal(err)
			}
			before := officialPicture.readRecord()
			oldConnect := officialPicture.reconnect
			t.Cleanup(func() { officialPicture.reconnect = oldConnect })
			legacyCalls := 0
			officialPicture.reconnect = func(context.Context, setup.Catalog, setup.ExtensionHost) error {
				legacyCalls++
				return errors.New("ordinary Connect was reached")
			}
			got := installHandlerFor(f.panel.handler("setup-test-key"))("POST", "/setup/comfy/connect", `{"confirm":true}`)
			if got.Code != http.StatusOK {
				t.Fatalf("Catalog request: HTTP %d %s", got.Code, got.Body.String())
			}
			o := waitOutcome(t, f.panel, officialPicture.legacy.ConnectStep)
			if o.Failure != "the kept registration's exact connector image cannot be verified for repair" ||
				f.installs != 0 || f.service.replaces != 0 || f.service.expects != 0 || f.publishes != 0 || legacyCalls != 0 ||
				f.service.entry.pin != pin || f.service.revision() != revision || !reflect.DeepEqual(before, officialPicture.readRecord()) {
				t.Fatalf("unverified same-pin repair changed state: outcome %+v, entry %+v, installs %d, replacements %d, publications %d, ordinary Connect %d",
					o, f.service.entry, f.installs, f.service.replaces, f.publishes, legacyCalls)
			}
		})
	}
}

func TestKeptReplacementRequiresTheDispatchAcknowledgment(t *testing.T) {
	f := newUpgradeFixture(t)
	calls := 0
	officialPicture.installPinned = func(context.Context, setup.Catalog, string, func() error) (func(context.Context) error, error) {
		calls++
		return func(context.Context) error { f.publishes++; return nil }, nil
	}
	f.step(t)
	r := officialPicture.readRecord()
	if calls != 1 || r.PendingUpdate == nil || r.PendingUpdate.Replacement == nil || !r.PendingUpdate.Replacement.BeforeDispatch ||
		r.PendingUpdate.Replacement.InstallComplete || f.publishes != 0 || f.service.replaces != 0 || f.restores != 0 {
		t.Fatalf("installer success without dispatch: receipt %+v, calls %d, publications %d, replacements %d, restores %d",
			r, calls, f.publishes, f.service.replaces, f.restores)
	}
}

func TestKeptReplacementSavesExplicitRegistryAcknowledgmentAfterDeadline(t *testing.T) {
	f := newUpgradeFixture(t)
	ctx := undoDeadlineContext{Context: context.Background(), done: make(chan struct{})}
	f.panel.comfyApps = undoPackageRegistry{nativeComfyService: f.service, afterReplace: func() error {
		close(ctx.done)
		return ctx.Err()
	}}
	publish := officialPicture.publishPinned
	t.Cleanup(func() { officialPicture.publishPinned = publish })
	officialPicture.publishPinned = func(context.Context, setup.Catalog) error { f.publishes++; return nil }
	if handled, err := officialPicture.connectKept(f.panel, ctx, *f.journal, f.found); !handled || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("explicit update deadline: handled %t, error %v", handled, err)
	}
	r := officialPicture.readRecord()
	if r.PendingUpdate == nil || r.PendingUpdate.Replacement == nil || r.PendingUpdate.Replacement.After != f.service.revision() ||
		f.installs != 1 || f.service.replaces != 1 || f.publishes != 0 || f.restores != 0 {
		t.Fatalf("expired readback: record %+v installs %d replacements %d publications %d restores %d",
			r, f.installs, f.service.replaces, f.publishes, f.restores)
	}
	f.panel.comfyApps = f.service
	if handled, err := officialPicture.connectKept(f.panel, context.Background(), *f.journal, f.found); !handled || err != nil {
		t.Fatalf("explicit retry: handled %t, error %v", handled, err)
	}
	r = officialPicture.readRecord()
	if r.PendingUpdate != nil || r.Previous == nil || f.installs != 1 || f.service.replaces != 1 || f.publishes != 1 || f.restores != 0 {
		t.Fatalf("retry: record %+v installs %d replacements %d publications %d restores %d",
			r, f.installs, f.service.replaces, f.publishes, f.restores)
	}
}

// Owner's PC, R36: the update removed the R35 integration, and installing
// this build's MSI failed. ComfyUI was left with no integration and every
// agent call to it was refused. A failed install now restores the earlier
// integration from the kept package, the entry stays pinned to it, and the
// expectation stays for a later pass.
func TestUpgradeRestoresTheEarlierIntegrationWhenTheInstallFails(t *testing.T) {
	f := newUpgradeFixture(t)
	connector := officialPicture.extension.Connector(f.found.Base)
	earlier := f.service.entry.pin
	var saved string
	keep := officialPicture.keepPrevious
	officialPicture.keepPrevious = func(ctx context.Context) (string, string, error) {
		path, digest, err := keep(ctx)
		saved = path
		return path, digest, err
	}
	restore := officialPicture.installPackage
	officialPicture.installPackage = func(ctx context.Context, path, base string) error {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != "earlier MSI" {
			t.Errorf("rollback received different package bytes: %q %v", raw, err)
		}
		if runtime.GOOS == "windows" {
			if file, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
				_ = file.Close()
				t.Error("the installer received a package still openable for writing")
			}
		}
		return restore(ctx, path, base)
	}
	officialPicture.installPinned = func(_ context.Context, _ setup.Catalog, base string, markEffect func() error) (func(context.Context) error, error) {
		if err := markEffect(); err != nil {
			return nil, err
		}
		if r := officialPicture.readRecord(); r.PendingUpdate == nil || r.PendingUpdate.Replacement == nil || r.PendingUpdate.Replacement.BeforeDispatch {
			t.Fatalf("MSI effect began without a durable dispatch marker: %+v", r)
		}
		f.installs++
		if runtime.GOOS == "windows" {
			if err := os.WriteFile(saved, []byte("changed MSI"), 0600); err == nil {
				t.Error("replacement allowed writing the saved package")
			}
			if err := os.Rename(saved, saved+".moved"); err == nil {
				t.Error("replacement allowed renaming the saved package")
			}
			parent := filepath.Dir(saved)
			if err := os.Rename(parent, parent+".moved"); err == nil {
				t.Error("replacement allowed renaming the saved package directory")
			}
		}
		// The removal of the earlier product succeeded; the install did not.
		if err := os.Remove(officialPicture.extension.Connector(base)); err != nil {
			return nil, err
		}
		return nil, errors.New("Windows Installer could not install the ComfyUI integration (Windows Installer code 1603): custom_nodes already holds a comfyui-openabstractions folder this installer did not place")
	}
	f.step(t)
	raw, err := os.ReadFile(connector)
	if err != nil || string(raw) != comfyR33Connector {
		t.Fatalf("ComfyUI lost its integration: %q %v", raw, err)
	}
	s := f.service
	if f.installs != 1 || f.restores != 1 || s.entry.pin != earlier || s.replaces != 0 || len(f.panel.appActivity.lines()) != 0 {
		t.Fatalf("installs %d restores %d entry %+v replaces %d lines %d", f.installs, f.restores, s.entry, s.replaces, len(f.panel.appActivity.lines()))
	}
	if r := officialPicture.readRecord(); r.PendingUpdate == nil {
		t.Fatalf("the expectation was dropped: %+v", r)
	}
	// A later pass tries again and restores again.
	f.step(t)
	raw, err = os.ReadFile(connector)
	if f.installs != 2 || f.restores != 2 || err != nil || string(raw) != comfyR33Connector || s.entry.pin != earlier {
		t.Fatalf("second pass: installs %d restores %d connector %q %v entry %+v", f.installs, f.restores, raw, err, s.entry)
	}
}

func TestUpgradeRechecksTheOriginalRevisionBeforeDispatchAndRestore(t *testing.T) {
	for _, phase := range []string{"before dispatch", "before restore"} {
		t.Run(phase, func(t *testing.T) {
			f := newUpgradeFixture(t)
			start := f.service.revision()
			checks := 0
			officialPicture.installPinned = func(_ context.Context, _ setup.Catalog, _ string, markEffect func() error) (func(context.Context) error, error) {
				checks++
				if phase == "before dispatch" {
					f.service.entry.revision++
				}
				if err := markEffect(); err != nil {
					return nil, err
				}
				f.installs++
				f.service.entry.revision++
				return nil, errors.New("the replacement failed after dispatch")
			}
			f.step(t)
			r := officialPicture.readRecord()
			if r.PendingUpdate == nil || r.PendingUpdate.Replacement == nil || r.PendingUpdate.Replacement.Before != start ||
				r.PendingUpdate.Replacement.BeforeDispatch != (phase == "before dispatch") ||
				r.PendingUpdate.Replacement.InstallComplete || r.PendingUpdate.Replacement.After != "" ||
				r.PendingUpdate.Replacement.Candidate != f.journal.Catalog().LegacyConnectorPin.SHA256 ||
				r.Previous != nil || f.service.revision() == start {
				t.Fatalf("recovery proof changed: %+v, revision %q", r, f.service.revision())
			}
			prior := r.PendingUpdate.Replacement.Previous
			raw, err := os.ReadFile(prior.Package)
			if err != nil || comfyTestSum(string(raw)) != prior.PackageSHA256 {
				t.Fatalf("earlier package custody changed: %q, %v", raw, err)
			}
			wantInstalls := 0
			if phase == "before restore" {
				wantInstalls = 1
			}
			f.step(t)
			if checks != 1 || f.installs != wantInstalls || f.restores != 0 || f.service.replaces != 0 || f.publishes != 0 {
				t.Fatalf("concurrent revision triggered effects: checks %d installs %d restores %d replaces %d publications %d",
					checks, f.installs, f.restores, f.service.replaces, f.publishes)
			}
		})
	}
}

func TestUpgradePreflightRefusalNeverRestoresTheEarlierPackage(t *testing.T) {
	for _, failure := range []error{os.ErrNotExist, errors.New("bad candidate digest"), context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			f := newUpgradeFixture(t)
			earlier := f.service.entry.pin
			officialPicture.installPinned = func(context.Context, setup.Catalog, string, func() error) (func(context.Context) error, error) {
				return nil, failure
			}
			f.step(t)
			r := officialPicture.readRecord()
			if f.installs != 0 || f.restores != 0 || f.service.replaces != 0 || f.service.entry.pin != earlier ||
				r.PendingUpdate == nil || r.Previous != nil {
				t.Fatalf("preflight refusal ran effects: installs %d restores %d replacements %d record %+v", f.installs, f.restores, f.service.replaces, r)
			}
		})
	}
}

func TestUpgradeResumesFailedPackagePublicationWithoutInstallingOrRepinning(t *testing.T) {
	f := newUpgradeFixture(t)
	oldInstall, oldKeep, oldPublish := officialPicture.installPinned, officialPicture.keepPrevious, officialPicture.publishPinned
	t.Cleanup(func() { officialPicture.publishPinned = oldPublish })
	keeps, publications := 0, 0
	officialPicture.keepPrevious = func(ctx context.Context) (string, string, error) {
		keeps++
		return oldKeep(ctx)
	}
	officialPicture.installPinned = func(ctx context.Context, c setup.Catalog, base string, markEffect func() error) (func(context.Context) error, error) {
		_, err := oldInstall(ctx, c, base, markEffect)
		return func(context.Context) error {
			publications++
			return errors.New("the saved-current package could not be replaced")
		}, err
	}
	officialPicture.publishPinned = func(context.Context, setup.Catalog) error {
		publications++
		return nil
	}
	f.step(t)
	r := officialPicture.readRecord()
	if r.PendingUpdate == nil || r.PendingUpdate.Replacement == nil || r.PendingUpdate.Replacement.After != f.service.revision() ||
		r.Previous != nil || keeps != 1 || f.installs != 1 || f.service.replaces != 1 || publications != 1 {
		t.Fatalf("unpublished replacement: record %+v keeps %d installs %d replaces %d publications %d", r, keeps, f.installs, f.service.replaces, publications)
	}
	prior := r.PendingUpdate.Replacement.Previous
	candidate := r.PendingUpdate.Replacement.Candidate
	f.step(t)
	r = officialPicture.readRecord()
	lines := f.panel.appActivity.lines()
	if len(lines) != 1 {
		t.Fatalf("publication retry produced %d Activity lines", len(lines))
	}
	prior.ActivityAt = lines[0].At
	raw, err := os.ReadFile(prior.Package)
	if r.PendingUpdate != nil || r.Previous == nil || *r.Previous != prior || r.KeptPackageSHA256 != candidate ||
		prior.PackageSHA256 != comfyTestSum("earlier MSI") || err != nil || string(raw) != "earlier MSI" ||
		keeps != 1 || f.installs != 1 || f.restores != 0 || f.service.replaces != 1 || publications != 2 {
		t.Fatalf("publication retry: record %+v prior %+v bytes %q (%v) keeps %d installs %d restores %d replaces %d publications %d", r, prior, raw, err, keeps, f.installs, f.restores, f.service.replaces, publications)
	}
}

func TestUpgradeRequiresInstallerAcknowledgementBeforeResumingNewConnector(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		name := "missing acknowledgement"
		if acknowledged {
			name = "installer acknowledged"
		}
		t.Run(name, func(t *testing.T) {
			f := newUpgradeFixture(t)
			oldPublish := officialPicture.publishPinned
			t.Cleanup(func() { officialPicture.publishPinned = oldPublish })
			officialPicture.publishPinned = func(context.Context, setup.Catalog) error {
				f.publishes++
				return nil
			}
			if acknowledged {
				f.service.deferReplacement = true
			} else {
				officialPicture.installPinned = func(context.Context, setup.Catalog, string, func() error) (func(context.Context) error, error) {
					return nil, context.Canceled
				}
			}
			f.step(t)
			r := officialPicture.readRecord()
			if r.PendingUpdate == nil || r.PendingUpdate.Replacement == nil || r.PendingUpdate.Replacement.Before != f.service.revision() ||
				r.PendingUpdate.Replacement.InstallComplete != acknowledged || f.service.replaces != 0 || f.publishes != 0 {
				t.Fatalf("saved transaction: %+v, replaces %d publishes %d", r, f.service.replaces, f.publishes)
			}
			prior := r.PendingUpdate.Replacement.Previous
			if acknowledged {
				f.service.deferReplacement = false
			} else if err := f.writeConnector(f.found.Base, connectorBytes); err != nil {
				t.Fatal(err)
			}
			f.step(t)
			r = officialPicture.readRecord()
			raw, err := os.ReadFile(prior.Package)
			if err != nil || comfyTestSum(string(raw)) != prior.PackageSHA256 || f.restores != 0 {
				t.Fatalf("prior package changed: digest %q, error %v, restores %d", comfyTestSum(string(raw)), err, f.restores)
			}
			if acknowledged {
				lines := f.panel.appActivity.lines()
				if len(lines) != 1 || lines[0].At == 0 {
					t.Fatalf("acknowledged replacement Activity: %+v", lines)
				}
				prior.ActivityAt = lines[0].At
				if r.PendingUpdate != nil || r.Previous == nil || *r.Previous != prior || f.installs != 1 ||
					f.service.replaces != 1 || f.publishes != 1 {
					t.Fatalf("acknowledged replacement: record %+v installs %d replaces %d publications %d", r, f.installs, f.service.replaces, f.publishes)
				}
			} else if r.PendingUpdate == nil || r.PendingUpdate.Replacement == nil || r.PendingUpdate.Replacement.InstallComplete ||
				r.PendingUpdate.Replacement.Previous != prior || r.Previous != nil || f.installs != 0 || f.service.replaces != 0 || f.publishes != 0 {
				t.Fatalf("unacknowledged replacement: record %+v installs %d replaces %d publications %d", r, f.installs, f.service.replaces, f.publishes)
			}
		})
	}
}

func TestUpgradeRetiresOnlyAProvenUntouchedSupersededReplacement(t *testing.T) {
	for _, state := range []string{"before dispatch", "legacy without dispatch proof", "ambiguous changed connector", "foreign registration revision"} {
		t.Run(state, func(t *testing.T) {
			f := newUpgradeFixture(t)
			probes := 0
			officialPicture.installPinned = func(context.Context, setup.Catalog, string, func() error) (func(context.Context) error, error) {
				probes++
				return nil, context.Canceled
			}
			f.step(t)
			r := officialPicture.readRecord()
			if r.PendingUpdate == nil || r.PendingUpdate.Replacement == nil || !r.PendingUpdate.Replacement.BeforeDispatch || probes != 1 {
				t.Fatalf("pending before dispatch: %+v probes %d", r, probes)
			}
			currentBuild := r.PendingUpdate.SHA256
			earlierBuild := comfyTestSum("earlier release pin")
			if err := officialPicture.changeRecord(context.Background(), func(r *setup.PackageMaintenanceRecord) {
				r.Seen = earlierBuild
				r.PendingUpdate.SHA256 = earlierBuild
				if state == "legacy without dispatch proof" {
					r.PendingUpdate.Replacement.BeforeDispatch = false
				}
			}); err != nil {
				t.Fatal(err)
			}
			if state == "ambiguous changed connector" {
				if err := f.writeConnector(f.found.Base, connectorBytes); err != nil {
					t.Fatal(err)
				}
			}
			if state == "foreign registration revision" {
				f.service.entry.revision++
			}
			before := officialPicture.readRecord()
			prior := before.PendingUpdate.Replacement.Previous
			f.step(t)
			r = officialPicture.readRecord()
			if r.PendingUpdate == nil || r.PendingUpdate.Replacement == nil {
				t.Fatalf("pending evidence missing: %+v", r)
			}
			if state == "before dispatch" {
				if r.PendingUpdate.SHA256 != currentBuild || r.Seen != currentBuild || probes != 2 {
					t.Fatalf("new release did not gain a fresh pending record: %+v probes %d", r, probes)
				}
			} else if r.PendingUpdate.SHA256 != earlierBuild || r.Seen != earlierBuild ||
				*r.PendingUpdate.Replacement != *before.PendingUpdate.Replacement || probes != 1 {
				t.Fatalf("ambiguous evidence was modified: %+v probes %d", r, probes)
			}
			raw, err := os.ReadFile(prior.Package)
			if err != nil || comfyTestSum(string(raw)) != prior.PackageSHA256 || f.service.replaces != 0 || f.restores != 0 {
				t.Fatalf("previous custody changed: digest %q, error %v, replaces %d, restores %d", comfyTestSum(string(raw)), err, f.service.replaces, f.restores)
			}
		})
	}
}

func TestUpgradeDistinguishesAnAcknowledgedRevisionFromALostReply(t *testing.T) {
	for _, lostReply := range []bool{false, true} {
		name := "Observe lost after acknowledged Register"
		if lostReply {
			name = "Register reply lost"
		}
		t.Run(name, func(t *testing.T) {
			f := newUpgradeFixture(t)
			f.service.loseReplaceReply = lostReply
			f.service.loseReplaceReadback = !lostReply
			oldPublish := officialPicture.publishPinned
			t.Cleanup(func() { officialPicture.publishPinned = oldPublish })
			officialPicture.publishPinned = func(context.Context, setup.Catalog) error {
				f.publishes++
				return nil
			}
			f.step(t)
			r := officialPicture.readRecord()
			if r.PendingUpdate == nil || r.PendingUpdate.Replacement == nil || f.publishes != 0 || f.service.replaces != 1 || (r.PendingUpdate.Replacement.After == "") != lostReply {
				t.Fatalf("uncertain write: record %+v replacements %d publications %d", r, f.service.replaces, f.publishes)
			}
			f.step(t)
			r = officialPicture.readRecord()
			if f.installs != 1 || f.restores != 0 || f.service.replaces != 1 {
				t.Fatalf("recovery ran effects again: installs %d restores %d replacements %d", f.installs, f.restores, f.service.replaces)
			}
			if lostReply {
				if f.publishes != 0 || r.Previous != nil || r.PendingUpdate == nil || r.PendingUpdate.Replacement == nil {
					t.Fatalf("unacknowledged write was treated as ours: record %+v publications %d", r, f.publishes)
				}
			} else if f.publishes != 1 || r.Previous == nil || r.PendingUpdate != nil {
				t.Fatalf("acknowledged write was not completed: record %+v publications %d", r, f.publishes)
			}
		})
	}
}

func TestUpgradeRefusesUnverifiedSavedPackageBeforeReplacement(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(string) error
	}{
		{"changed", func(path string) error { return os.WriteFile(path, []byte("changed MSI"), 0600) }},
		{"missing", os.Remove},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpgradeFixture(t)
			earlier := f.service.entry.pin
			keep := officialPicture.keepPrevious
			officialPicture.keepPrevious = func(ctx context.Context) (string, string, error) {
				path, digest, err := keep(ctx)
				if err != nil {
					t.Errorf("keep fixture package: %v", err)
					return "", "", err
				}
				if err := tc.change(path); err != nil {
					t.Errorf("change saved fixture package: %v", err)
					return "", "", err
				}
				return path, digest, nil
			}
			f.step(t)
			record := officialPicture.readRecord()
			raw, err := os.ReadFile(officialPicture.extension.Connector(f.found.Base))
			if f.installs != 0 || f.restores != 0 || f.service.entry.pin != earlier || f.service.replaces != 0 ||
				record.PendingUpdate == nil || record.Previous != nil || len(f.panel.appActivity.lines()) != 0 ||
				err != nil || string(raw) != comfyR33Connector {
				t.Fatalf("unverified package: installs %d restores %d entry %+v record %+v connector %q %v", f.installs, f.restores, f.service.entry, record, raw, err)
			}
		})
	}
}

// An update whose earlier package cannot be kept would have nothing to
// restore: it waits, and the earlier integration stays.
func TestUpgradeWaitsWithoutTheEarlierPackage(t *testing.T) {
	f := newUpgradeFixture(t)
	officialPicture.keepPrevious = func(context.Context) (string, string, error) { return "", "", errors.New("no package copy") }
	f.step(t)
	if f.installs != 0 || officialPicture.readRecord().PendingUpdate == nil {
		t.Fatalf("installs %d record %+v", f.installs, officialPicture.readRecord())
	}
	raw, _ := os.ReadFile(officialPicture.extension.Connector(f.found.Base))
	if string(raw) != comfyR33Connector {
		t.Fatalf("the folder changed: %q", raw)
	}
}

func TestUpgradeWaitsUntilTheUpdateRecordCanBeWritten(t *testing.T) {
	f := newUpgradeFixture(t)
	connector := officialPicture.extension.Connector(f.found.Base)
	earlier := f.service.entry.pin
	recordFile := officialPicture.recordFile
	blockedRecord := t.TempDir()
	officialPicture.recordFile = func() (string, error) { return blockedRecord, nil }

	f.step(t)
	raw, err := os.ReadFile(connector)
	if f.installs != 0 || f.service.entry.pin != earlier || err != nil || string(raw) != comfyR33Connector {
		t.Fatalf("unwritable record: installs %d connector %q read error %v pin %q", f.installs, raw, err, f.service.entry.pin)
	}

	officialPicture.recordFile = recordFile
	f.step(t)
	if r := officialPicture.readRecord(); f.installs != 1 || r.PendingUpdate != nil || r.Previous == nil || r.Previous.Program != connector {
		t.Fatalf("writable record: installs %d record %+v", f.installs, r)
	}
}

// Owner's PC after the hand repair of R36: the connector on disk is this
// build's pin, the record says seen and recorded at this build and kept at
// the earlier image, and the kept entry still pins the earlier image, while
// ComfyUI runs. The presenter's pass re-pins the entry as the installing
// operator to the connector on disk, with no question, and the record
// settles.
func TestPresenterRepinsAKeptEntryLeftOnTheEarlierImage(t *testing.T) {
	f := newUpgradeFixture(t)
	s := f.service
	earlier := s.entry.pin
	if err := f.writeConnector(f.found.Base, connectorBytes); err != nil {
		t.Fatal(err)
	}
	build := comfyTestSum(connectorBytes)
	officialPicture.changeRecord(context.Background(), func(r *setup.PackageMaintenanceRecord) {
		r.Seen, r.Recorded, r.Kept, r.History = build, build, earlier, []string{earlier}
	})
	stubComfyProcesses(t, 4242)
	if officialPicture.settled(*f.journal) {
		t.Fatal("the presenter treats an entry pinned to the earlier image as settled")
	}
	claims := 0
	claim := func() (func(), bool, error) { claims++; return func() {}, true, nil }
	tick := make(chan time.Time)
	close(tick)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	watchComfyIntegration(ctx, *f.journal, claim, tick, func(ctx context.Context, journal setup.Journal) {
		officialPicture.maintain(f.panel, ctx, journal)
	})
	connector := officialPicture.extension.Connector(f.found.Base)
	if claims != 1 || s.replaces != 1 || s.entry.pin != s.images[connector] || s.entry.state != "enabled" ||
		s.questions != 0 || s.keepAsks != 0 || s.removes != 0 || f.installs != 0 {
		t.Fatalf("claims %d replaces %d entry %+v questions %d asks %d removes %d installs %d", claims, s.replaces, s.entry, s.questions, s.keepAsks, s.removes, f.installs)
	}
	if r := officialPicture.readRecord(); r.Kept != build || !officialPicture.settled(*f.journal) {
		t.Fatalf("record %+v", r)
	}
	f.step(t)
	if s.replaces != 1 {
		t.Fatal("a settled entry was re-pinned again")
	}
}
