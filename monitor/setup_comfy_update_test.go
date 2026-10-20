package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// comfyR33Connector is the connector an earlier OpenAbstractions put into
// the person's ComfyUI: its bytes differ from the pinned connectorBytes.
const comfyR33Connector = "comfyui-connector 0.8.0 r33"

// keptOutdatedComfy is the owner's ComfyUI after an OA upgrade: the
// integration folder holds the earlier connector, and the comfyui entry
// keeps that connector's image. withPackage puts the pinned extension MSI
// into this build's package folder.
func keptOutdatedComfy(t *testing.T, onDisk string, withPackage bool) (setup.ExtensionHost, *setup.Journal, *nativeComfyService) {
	t.Helper()
	found, journal := upgradedComfy(t, onDisk)
	service := connectorService(t, found, "enabled")
	service.entry.pin = service.images[officialPicture.extension.Connector(found.Base)]
	old, oldRecord := setup.LocalPackageFolder, officialPicture.recordFile
	t.Cleanup(func() { setup.LocalPackageFolder, officialPicture.recordFile = old, oldRecord })
	record := filepath.Join(t.TempDir(), "comfyui-integration.json")
	officialPicture.recordFile = func() (string, error) { return record, nil }
	setup.LocalPackageFolder = t.TempDir()
	if withPackage {
		if err := os.WriteFile(filepath.Join(setup.LocalPackageFolder, "comfyui-0.8.1.msi"), make([]byte, 100000), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return found, journal, service
}

// stubComfyProcesses makes pids the running programs of every ComfyUI.
func stubComfyProcesses(t *testing.T, pids ...uint32) {
	t.Helper()
	old := officialPicture.processes
	t.Cleanup(func() { officialPicture.processes = old })
	officialPicture.processes = func(setup.ExtensionHost) ([]uint32, error) { return pids, nil }
}

// updateThroughPanel presses Update and checks the durable kept-replacement path.
func updateThroughPanel(t *testing.T, f *upgradeFixture) setupOutcome {
	t.Helper()
	f.panel.setupOutcomes = nil
	if got := installHandlerFor(f.panel.handler("setup-test-key"))("POST", "/setup/comfy/connect", `{"confirm":true}`); got.Code != http.StatusOK {
		t.Fatalf("update: %d %s", got.Code, got.Body.String())
	}
	o := waitOutcome(t, f.panel, officialPicture.legacy.ConnectStep)
	r := officialPicture.readRecord()
	if f.installs != 1 || f.publishes != 1 || f.restores != 0 || f.service.replaces != 1 ||
		r.PendingUpdate != nil || r.Previous == nil || r.KeptPackageSHA256 != f.journal.Catalog().LegacyConnectorPin.SHA256 {
		t.Fatalf("explicit replacement: installs %d publishes %d restores %d replaces %d receipt %+v", f.installs, f.publishes, f.restores, f.service.replaces, r)
	}
	return o
}

// Owner-hit on R34: the kept ComfyUI holds the integration an earlier
// OpenAbstractions installed. Its row offers Update, Update installs the
// pinned integration and keeps the entry on the new connector with no
// question, Activity says to restart the running ComfyUI, and the row says
// so until those processes end.
func TestCatalogUpdatesAnOutdatedIntegrationWithoutAQuestion(t *testing.T) {
	f := newUpgradeFixture(t)
	connector := officialPicture.extension.Connector(f.found.Base)
	panel, service := f.panel, f.service
	stubComfyProcesses(t, 4242)

	if got := readOverview(t, panel).PictureApplication.setupPackageCardFacts; got.State != "connected" || !got.Update || got.UpdateBlocked != "" || got.Restart {
		t.Fatalf("outdated integration row: %+v", got)
	}
	o := updateThroughPanel(t, f)
	if o.Failure != "" || o.Kind != "update" {
		t.Fatalf("update: %+v", o)
	}
	if e := service.entry; e == nil || e.state != "enabled" || e.program != connector || e.pin != service.images[connector] ||
		service.keepAsks != 0 || service.questions != 0 || service.removes != 0 {
		t.Fatalf("after Update: entry %+v asks %d questions %d removes %d", e, service.keepAsks, service.questions, service.removes)
	}
	lines := panel.appActivity.lines()
	if len(lines) != 1 || lines[0].What != "Updated the OpenAbstractions integration in ComfyUI; restart ComfyUI to use it" ||
		lines[0].App != "ComfyUI" || len(lines[0].Undo) != 1 || !lines[0].UndoRestores {
		t.Fatalf("Activity lines %+v", lines)
	}
	if got := readOverview(t, panel).PictureApplication.setupPackageCardFacts; got.State != "connected" || got.Update || !got.Restart {
		t.Fatalf("row while the earlier ComfyUI runs: %+v", got)
	}
	stubComfyProcesses(t, 5151)
	if got := readOverview(t, panel).PictureApplication.setupPackageCardFacts; got.Update || got.Restart {
		t.Fatalf("row after ComfyUI restarted: %+v", got)
	}
}

// Update with ComfyUI closed says nothing about a restart.
func TestCatalogUpdateWithComfyClosed(t *testing.T) {
	f, current := newUndoUpgradeFixture(t)
	canonical := useUndoPackageState(t)
	if err := officialPicture.changeRecord(context.Background(), func(r *setup.PackageMaintenanceRecord) {
		r.KeptPackageSHA256 = comfyTestSum("earlier MSI")
	}); err != nil {
		t.Fatal(err)
	}
	panel := f.panel
	stubComfyProcesses(t)
	if o := updateThroughPanel(t, f); o.Failure != "" || o.Kind != "update" {
		t.Fatalf("update: %+v", o)
	}
	lines := panel.appActivity.lines()
	previous := officialPicture.readRecord().Previous
	if len(lines) != 1 || lines[0].What != "Updated the OpenAbstractions integration in ComfyUI" ||
		!lines[0].UndoRestores || len(lines[0].Undo) != 1 || previous == nil || previous.ActivityAt != lines[0].At {
		t.Fatalf("Activity lines %+v", lines)
	}
	if got := readOverview(t, panel).PictureApplication.setupPackageCardFacts; got.Update || got.Restart {
		t.Fatalf("row after Update: %+v", got)
	}
	if err := os.WriteFile(canonical, current, 0600); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(setupKeepUndo{At: lines[0].At, Apps: lines[0].Undo})
	if err != nil {
		t.Fatal(err)
	}
	got := installHandlerFor(panel.handler("setup-test-key"))("POST", "/activity/app-requests", string(body))
	if got.Code != http.StatusOK || f.restores != 1 || f.service.entry.pin != comfyTestSum(comfyR33Connector) {
		t.Fatalf("explicit Update Undo: HTTP %d %s, restores %d, entry %+v", got.Code, got.Body.String(), f.restores, f.service.entry)
	}
}

// The integration this build pins offers no Update; a build without the
// extension MSI says why on an outdated row and offers no Update.
func TestCatalogUpdateOnlyForAnOutdatedIntegration(t *testing.T) {
	stubComfyProcesses(t, 4242)
	found, journal, service := keptOutdatedComfy(t, connectorBytes, true)
	panel := connectClickPanel(t, found, journal, service)
	if got := readOverview(t, panel).PictureApplication.setupPackageCardFacts; got.State != "connected" || got.Update || got.UpdateBlocked != "" || got.Restart {
		t.Fatalf("current integration row: %+v", got)
	}

	found, journal, service = keptOutdatedComfy(t, comfyR33Connector, false)
	panel = connectClickPanel(t, found, journal, service)
	if got := readOverview(t, panel).PictureApplication.setupPackageCardFacts; got.State != "connected" || !got.Update ||
		got.UpdateBlocked != "this OpenAbstractions build doesn't include the integration update" {
		t.Fatalf("outdated integration without the MSI: %+v", got)
	}
}
