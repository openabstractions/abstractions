package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// upgradeFixture is the person's computer after an OA upgrade: ComfyUI holds
// the earlier OA's integration (comfyR33Connector), its entry is kept and
// pinned to that connector, and this build pins connectorBytes. installed
// is what the extension MSI writes as the connector.
type upgradeFixture struct {
	found     setup.ExtensionHost
	journal   *setup.Journal
	service   *nativeComfyService
	panel     *servicePanel
	installed string
	publishes int
	installs  int
	restores  int
}

func newUpgradeFixture(t *testing.T) *upgradeFixture {
	t.Helper()
	found, journal, service := keptOutdatedComfy(t, comfyR33Connector, true)
	f := &upgradeFixture{found: found, journal: journal, service: service, installed: connectorBytes}
	f.panel = connectClickPanel(t, found, journal, service)
	stubComfyProcesses(t)
	record := filepath.Join(t.TempDir(), "comfyui-integration.json")
	previous := filepath.Join(t.TempDir(), "comfyui-openabstractions-previous.msi")
	oldFile, oldState, oldPinned, oldPackage, oldKeep := officialPicture.recordFile, readPackageRegistrationState, officialPicture.installPinned, officialPicture.installPackage, officialPicture.keepPrevious
	t.Cleanup(func() {
		officialPicture.recordFile, readPackageRegistrationState, officialPicture.installPinned, officialPicture.installPackage, officialPicture.keepPrevious = oldFile, oldState, oldPinned, oldPackage, oldKeep
	})
	officialPicture.recordFile = func() (string, error) { return record, nil }
	readPackageRegistrationState = func(context.Context, packageConnectTarget) (registrationwire.State, bool, bool) {
		switch {
		case service.entry == nil:
			return 0, false, true
		case service.entry.state == "pending":
			return registrationwire.StatePendingApproval, true, true
		case service.entry.state == "disabled":
			return registrationwire.StateDisabled, true, true
		}
		return registrationwire.StateEnabled, true, true
	}
	officialPicture.keepPrevious = func(context.Context) (string, string, error) {
		return previous, comfyTestSum("earlier MSI"), os.WriteFile(previous, []byte("earlier MSI"), 0600)
	}
	officialPicture.installPinned = func(_ context.Context, _ setup.Catalog, base string, markEffect func() error) (func(context.Context) error, error) {
		if err := markEffect(); err != nil {
			return nil, err
		}
		f.installs++
		if err := f.writeConnector(base, f.installed); err != nil {
			return nil, err
		}
		return func(context.Context) error {
			f.publishes++
			return nil
		}, nil
	}
	officialPicture.installPackage = func(_ context.Context, msi, base string) error {
		if msi != previous {
			t.Fatalf("Undo installed %s", msi)
		}
		f.restores++
		return f.writeConnector(base, comfyR33Connector)
	}
	return f
}

func (f *upgradeFixture) writeConnector(base, body string) error {
	connector := officialPicture.extension.Connector(base)
	if err := os.WriteFile(connector, []byte(body), 0600); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(body))
	f.service.images[connector] = hex.EncodeToString(sum[:])
	return nil
}

func (f *upgradeFixture) step(t *testing.T) {
	t.Helper()
	officialPicture.maintain(f.panel, context.Background(), *f.journal)
}

func TestMaintainedPackageRevisionRequiresTheSelectedKeptProgram(t *testing.T) {
	f := newUpgradeFixture(t)
	ctx := context.Background()
	connector := officialPicture.extension.Connector(f.found.Base)
	target := officialPicture.target(connector)
	if observer, revision, err := target.observeMaintainedRevision(ctx, f.panel); err != nil || observer == nil || revision == "" {
		t.Fatalf("selected kept program: revision %q, error %v", revision, err)
	}
	foreign := officialPicture.target(filepath.Join(t.TempDir(), filepath.Base(connector)))
	if observer, revision, err := foreign.observeMaintainedRevision(ctx, f.panel); err == nil || observer != nil || revision != "" {
		t.Fatalf("foreign program observed a replacement revision: %q, error %v", revision, err)
	}
	f.service.entry.state = "disabled"
	if observer, revision, err := target.observeMaintainedRevision(ctx, f.panel); err == nil || observer != nil || revision != "" {
		t.Fatalf("disabled registration observed a replacement revision: %q, error %v", revision, err)
	}
}

// The owner-hit case: OA upgrades while ComfyUI is closed. The Setup step
// updates the kept integration with no question, Activity says so with Undo,
// and ComfyUI starting days later registers its connector silently and
// opens connected. Undo restores the earlier connector and keeps the entry.
func TestUpgradeUpdatesTheKeptComfyIntegrationWithoutAQuestion(t *testing.T) {
	f, current := newUndoUpgradeFixture(t)
	canonical := useUndoPackageState(t)
	if err := os.WriteFile(canonical, current, 0600); err != nil {
		t.Fatal(err)
	}
	connector := officialPicture.extension.Connector(f.found.Base)
	f.step(t)
	s := f.service
	if f.publishes != 1 || f.installs != 1 || s.entry == nil || s.entry.state != "enabled" || s.entry.pin != s.images[connector] ||
		s.questions != 0 || s.keepAsks != 0 || s.removes != 0 || s.expects != 1 || s.recorded != s.images[connector] {
		t.Fatalf("after the upgrade: installs %d entry %+v questions %d asks %d removes %d expects %d recorded %q", f.installs, s.entry, s.questions, s.keepAsks, s.removes, s.expects, s.recorded)
	}
	lines := f.panel.appActivity.lines()
	if len(lines) != 1 || !strings.HasPrefix(lines[0].What, "ComfyUI's OpenAbstractions integration was updated with OpenAbstractions") ||
		!lines[0].UndoRestores || len(lines[0].Undo) != 1 {
		t.Fatalf("Activity %+v", lines)
	}
	if r := officialPicture.readRecord(); r.PendingUpdate != nil || r.Kept != s.images[connector] || r.Previous == nil {
		t.Fatalf("record %+v", r)
	}

	// Days later ComfyUI starts: its connector registers itself.
	if err := connectorRegisterSelf(s)(context.Background(), f.found.Base); err != nil || s.entry.state != "enabled" || s.questions != 0 {
		t.Fatalf("ComfyUI's start: %v entry %+v questions %d", err, s.entry, s.questions)
	}
	if got, err := s.Activate(context.Background(), setup.ComfyApplicationName); err != nil || got.Outcome != fwire.ApplicationActivationOutcomeReady {
		t.Fatalf("open: %+v %v", got, err)
	}
	if got := readOverview(t, f.panel).PictureApplication.setupPackageCardFacts; got.State != "connected" || got.Update || got.Changed {
		t.Fatalf("row %+v", got)
	}
	// A second step changes nothing.
	f.step(t)
	if f.installs != 1 || len(f.panel.appActivity.lines()) != 1 {
		t.Fatalf("second step: installs %d lines %d", f.installs, len(f.panel.appActivity.lines()))
	}

	body, _ := json.Marshal(setupKeepUndo{At: lines[0].At, Apps: lines[0].Undo})
	got := installHandlerFor(f.panel.handler("setup-test-key"))("POST", "/activity/app-requests", string(body))
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "earlier OpenAbstractions integration") {
		t.Fatalf("Undo: %d %s", got.Code, got.Body.String())
	}
	if f.restores != 1 || s.entry == nil || s.entry.state != "enabled" || s.entry.pin != s.images[connector] || s.removes != 0 {
		t.Fatalf("after Undo: restores %d entry %+v", f.restores, s.entry)
	}
	sum := sha256.Sum256([]byte(comfyR33Connector))
	if s.entry.pin != hex.EncodeToString(sum[:]) {
		t.Fatalf("Undo pinned %s", s.entry.pin)
	}
	// The undone build's integration is not applied again on its own; the
	// Catalog offers Update.
	f.step(t)
	if f.installs != 1 {
		t.Fatalf("the undone update ran again")
	}
	if got := readOverview(t, f.panel).PictureApplication.setupPackageCardFacts; !got.Update {
		t.Fatalf("row after Undo: %+v", got)
	}
}

// ComfyUI open during the upgrade: its folder is left alone, the
// expectation waits, and the step with ComfyUI closed applies it.
func TestUpgradeWaitsForComfyToClose(t *testing.T) {
	f := newUpgradeFixture(t)
	stubComfyProcesses(t, 4242)
	f.step(t)
	if f.installs != 0 || officialPicture.readRecord().PendingUpdate == nil {
		t.Fatalf("ComfyUI open: installs %d record %+v", f.installs, officialPicture.readRecord())
	}
	stubComfyProcesses(t)
	f.step(t)
	if f.installs != 1 || officialPicture.readRecord().PendingUpdate != nil || f.service.keepAsks != 0 {
		t.Fatalf("ComfyUI closed: installs %d record %+v", f.installs, officialPicture.readRecord())
	}
}

func TestUpgradeWaitsWhenPrimaryConnectorChangesAfterAdmission(t *testing.T) {
	t.Run("distinct connectors", func(t *testing.T) { testUpgradeWaitsWhenPrimaryChanges(t, false) })
	t.Run("hard-linked connector", func(t *testing.T) { testUpgradeWaitsWhenPrimaryChanges(t, true) })
}

func testUpgradeWaitsWhenPrimaryChanges(t *testing.T, hardLink bool) {
	t.Helper()
	f := newUpgradeFixture(t)
	other := f.found
	other.Base = t.TempDir()
	connector := officialPicture.extension.Connector(other.Base)
	if err := os.MkdirAll(filepath.Dir(connector), 0700); err != nil {
		t.Fatal(err)
	}
	if hardLink {
		if err := os.Link(officialPicture.extension.Connector(f.found.Base), connector); err != nil {
			t.Skipf("connector hard links unavailable: %v", err)
		}
	} else if err := f.writeConnector(other.Base, comfyR33Connector); err != nil {
		t.Fatal(err)
	}
	calls := 0
	officialPicture.discover = func() []setup.ExtensionHost {
		calls++
		if calls == 1 {
			return []setup.ExtensionHost{f.found}
		}
		return []setup.ExtensionHost{other}
	}
	f.step(t)
	pending := officialPicture.readRecord().PendingUpdate
	if calls != 2 || f.installs != 0 || pending == nil || pending.Program != officialPicture.extension.Connector(f.found.Base) {
		t.Fatalf("primary changed: lookups %d, installs %d, pending %+v", calls, f.installs, pending)
	}
	f.step(t)
	if got := officialPicture.readRecord().PendingUpdate; f.installs != 0 || got == nil || *got != *pending {
		t.Fatalf("different primary consumed the queued update: installs %d, pending %+v", f.installs, got)
	}
	same := f.found
	same.Base += string(filepath.Separator) + "."
	stubOfficialComfy(t, same)
	f.step(t)
	got := officialPicture.readRecord()
	if f.installs != 1 || got.PendingUpdate != nil || got.Previous == nil || got.Previous.Program != pending.Program {
		t.Fatalf("original connector retry: installs %d, record %+v", f.installs, got)
	}
}

// A connector whose bytes differ from the pin after the install is never
// pinned: the entry stays on its earlier image, the saved original remains
// available, and the Catalog row says it changed since the person connected it.
func TestUpgradeLeavesATamperedConnectorChanged(t *testing.T) {
	f := newUpgradeFixture(t)
	f.installed = "comfyui-connector tampered"
	earlier := f.service.entry.pin
	f.step(t)
	if f.service.entry.pin != earlier || f.service.replaces != 0 || f.service.removes != 0 || len(f.panel.appActivity.lines()) != 0 {
		t.Fatalf("tampered connector: entry %+v replaces %d lines %d", f.service.entry, f.service.replaces, len(f.panel.appActivity.lines()))
	}
	r := officialPicture.readRecord()
	if !r.Changed || r.PendingUpdate == nil || r.PendingUpdate.Replacement == nil || !r.PendingUpdate.Replacement.InstallComplete {
		t.Fatalf("record %+v", r)
	}
	prior := r.PendingUpdate.Replacement.Previous
	if raw, err := os.ReadFile(prior.Package); err != nil || comfyTestSum(string(raw)) != prior.PackageSHA256 {
		t.Fatalf("retained recovery digest: %q (%v)", raw, err)
	}
	got := readOverview(t, f.panel).PictureApplication.setupPackageCardFacts
	if got.State != "connected" || !got.Changed || got.Update {
		t.Fatalf("row %+v", got)
	}
	f.step(t)
	if f.installs != 1 || f.publishes != 0 {
		t.Fatalf("the used expectation ran again")
	}
}

// A person's disconnect wins: ComfyUI removed from Apps, or turned off,
// keeps its folder as it is after an upgrade, and a ComfyUI never connected
// is never connected.
func TestUpgradeLeavesADisconnectedComfyAlone(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(*upgradeFixture)
	}{
		{"removed", func(f *upgradeFixture) { f.service.entry = nil }},
		{"turned off", func(f *upgradeFixture) { f.service.entry.state = "disabled" }},
		{"waiting", func(f *upgradeFixture) { f.service.entry.state = "pending" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newUpgradeFixture(t)
			c.setup(f)
			f.step(t)
			if f.installs != 0 || f.service.replaces != 0 || len(f.panel.appActivity.lines()) != 0 || officialPicture.readRecord().PendingUpdate != nil {
				t.Fatalf("installs %d replaces %d record %+v", f.installs, f.service.replaces, officialPicture.readRecord())
			}
			raw, _ := os.ReadFile(officialPicture.extension.Connector(f.found.Base))
			if string(raw) != comfyR33Connector {
				t.Fatalf("the folder changed: %q", raw)
			}
		})
	}
	t.Run("never connected", func(t *testing.T) {
		f := newUpgradeFixture(t)
		f.found.Connected = false
		stubOfficialComfy(t, f.found)
		f.service.entry = nil
		f.step(t)
		if f.installs != 0 || officialPicture.readRecord().PendingUpdate != nil {
			t.Fatalf("installs %d", f.installs)
		}
	})
}

// An OA older than the one that last updated the integration does not put
// its earlier integration back.
func TestUpgradeRefusesADowngrade(t *testing.T) {
	f := newUpgradeFixture(t)
	sum := sha256.Sum256([]byte(connectorBytes))
	officialPicture.changeRecord(context.Background(), func(r *setup.PackageMaintenanceRecord) {
		r.History = []string{hex.EncodeToString(sum[:]), "later-pin"}
	})
	f.step(t)
	if f.installs != 0 || officialPicture.readRecord().PendingUpdate != nil {
		t.Fatalf("downgrade: installs %d record %+v", f.installs, officialPicture.readRecord())
	}
}

// Architect R35 migration: the owner's ComfyUI before R35, kept (through the
// Keep question or a Connect before records existed) with its connector
// already at this build's pin. The R35 step names the pin and re-pins the
// entry to the same image once: the runtime writes the installer_update
// record and the integration shares the card. Nothing is installed, nobody
// is asked, Activity stays quiet, and a later step does nothing more.
func TestR35StepRecordsAKeptIntegrationAtThePinOnce(t *testing.T) {
	found, journal, service := keptOutdatedComfy(t, connectorBytes, true)
	f := &upgradeFixture{found: found, journal: journal, service: service, installed: connectorBytes}
	f.panel = connectClickPanel(t, found, journal, service)
	stubComfyProcesses(t)
	oldState := readPackageRegistrationState
	t.Cleanup(func() { readPackageRegistrationState = oldState })
	readPackageRegistrationState = func(context.Context, packageConnectTarget) (registrationwire.State, bool, bool) {
		return registrationwire.StateEnabled, service.entry != nil, true
	}
	connector := officialPicture.extension.Connector(found.Base)
	f.step(t)
	if service.records != 1 || service.recorded != service.images[connector] || service.replaces != 1 || service.expects != 1 ||
		service.questions != 0 || service.keepAsks != 0 || len(f.panel.appActivity.lines()) != 0 {
		t.Fatalf("migration: records %d recorded %q replaces %d expects %d questions %d lines %d", service.records, service.recorded, service.replaces, service.expects, service.questions, len(f.panel.appActivity.lines()))
	}
	if r := officialPicture.readRecord(); r.Recorded != service.images[connector] || r.PendingUpdate != nil {
		t.Fatalf("record %+v", r)
	}
	f.step(t)
	if service.replaces != 1 || service.expects != 1 {
		t.Fatalf("a second step re-pinned again: replaces %d expects %d", service.replaces, service.expects)
	}
}

// The migration leaves a running ComfyUI alone and records at a later step
// with ComfyUI closed.
func TestR35StepRecordsOnceComfyIsClosed(t *testing.T) {
	found, journal, service := keptOutdatedComfy(t, connectorBytes, true)
	f := &upgradeFixture{found: found, journal: journal, service: service, installed: connectorBytes}
	f.panel = connectClickPanel(t, found, journal, service)
	oldState := readPackageRegistrationState
	t.Cleanup(func() { readPackageRegistrationState = oldState })
	readPackageRegistrationState = func(context.Context, packageConnectTarget) (registrationwire.State, bool, bool) {
		return registrationwire.StateEnabled, true, true
	}
	stubComfyProcesses(t, 4242)
	f.step(t)
	if service.replaces != 0 || service.records != 0 {
		t.Fatalf("ComfyUI open: replaces %d records %d", service.replaces, service.records)
	}
	stubComfyProcesses(t)
	f.step(t)
	if service.records != 1 {
		t.Fatalf("ComfyUI closed: records %d", service.records)
	}
}

// A never-connected integration gets nothing: no expectation, no re-pin, no
// record, and the step does not try again.
func TestR35StepGivesANeverConnectedComfyNothing(t *testing.T) {
	f := newUpgradeFixture(t)
	f.found.Connected = false
	stubOfficialComfy(t, f.found)
	f.service.entry = nil
	f.step(t)
	f.step(t)
	if f.service.expects != 0 || f.service.replaces != 0 || f.service.records != 0 || f.installs != 0 {
		t.Fatalf("expects %d replaces %d records %d installs %d", f.service.expects, f.service.replaces, f.service.records, f.installs)
	}
	if r := officialPicture.readRecord(); r.Recorded == "" {
		t.Fatalf("the step did not settle the never-connected integration: %+v", r)
	}
}

// R35 review: Undo installs the kept package only while its bytes are the
// ones the update kept. A package replaced in Setup's folder is refused and
// the entry stays on the update's image.
func TestUndoRefusesAReplacedKeptPackage(t *testing.T) {
	f := newUpgradeFixture(t)
	useUndoPackageState(t)
	connector := officialPicture.extension.Connector(f.found.Base)
	f.step(t)
	s := f.service
	lines := f.panel.appActivity.lines()
	record := officialPicture.readRecord()
	if len(lines) != 1 || record.Previous == nil || record.Previous.PackageSHA256 == "" {
		t.Fatalf("Activity %+v record %+v", lines, record)
	}
	if err := os.WriteFile(record.Previous.Package, []byte("another MSI"), 0600); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(setupKeepUndo{At: lines[0].At, Apps: lines[0].Undo})
	got := installHandlerFor(f.panel.handler("setup-test-key"))("POST", "/activity/app-requests", string(body))
	if got.Code == http.StatusOK || f.restores != 0 || s.entry.pin != s.images[connector] {
		t.Fatalf("Undo of a replaced package: %d %s restores %d pin %s", got.Code, got.Body.String(), f.restores, s.entry.pin)
	}
}
