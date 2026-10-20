package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// connectClickPanel is a Panel whose Catalog finds found and whose runtime
// is service, with the production read of the Apps entry.
func connectClickPanel(t *testing.T, found setup.ExtensionHost, journal *setup.Journal, service *nativeComfyService) *servicePanel {
	t.Helper()
	production := officialPicture.entryKept
	stubOverviewMachine(t)
	stubOfficialComfy(t, found)
	officialPicture.entryKept = production
	return &servicePanel{setupJournal: journal, comfyApps: service,
		appActivity:       applicationActivityLog{path: filepath.Join(t.TempDir(), "activity.json")},
		maintenanceReview: func(context.Context) setup.MaintenanceReview { return setup.MaintenanceReview{} }}
}

// Architect ruling 2026-11-11: the person's Connect click is the Keep. The
// click records the connector's path and the pinned SHA-256 (ExpectConnect),
// the connector's own registration of that image is kept without a
// question, and one Activity line says so with Undo, which removes the
// registration. Reading the Catalog or pressing Open records nothing.
func TestConnectClickKeepsWithoutAQuestionAndUndoRemoves(t *testing.T) {
	found, journal := upgradedComfy(t, connectorBytes)
	connector := officialPicture.extension.Connector(found.Base)
	service := connectorService(t, found, "")
	service.entry = nil
	panel := connectClickPanel(t, found, journal, service)

	readOverview(t, panel)
	installHandlerFor(panel.handler("setup-test-key"))("POST", "/setup/comfy/open", `{"confirm":true}`)
	if service.expects != 0 {
		t.Fatalf("reading the Catalog or Open recorded %d Connect clicks", service.expects)
	}
	if o := connectThroughPanel(t, panel, found, service); o.Failure != "" {
		t.Fatalf("connect: %+v", o)
	}
	if service.expects != 1 || service.expected != nil {
		t.Fatalf("Connect clicks recorded %d, left %+v", service.expects, service.expected)
	}
	if e := service.entry; e == nil || e.state != "enabled" || e.program != connector || service.keepAsks != 0 {
		t.Fatalf("after Connect: entry %+v, keep asks %d", e, service.keepAsks)
	}
	if got := readOverview(t, panel).PictureApplication.setupPackageCardFacts; got.State != "connected" {
		t.Fatalf("kept connector row: %+v", got)
	}
	lines := panel.appActivity.lines()
	if len(lines) != 1 || lines[0].What != "ComfyUI connected to OpenAbstractions" || lines[0].App != "ComfyUI" ||
		len(lines[0].Undo) != 1 || lines[0].Undo[0] != "comfyui" || !lines[0].UndoRemoves {
		t.Fatalf("Activity lines %+v", lines)
	}

	// A second Connect on the kept connector adds no line and asks nothing.
	if o := connectThroughPanel(t, panel, found, service); o.Failure != "" || len(panel.appActivity.lines()) != 1 || service.keepAsks != 0 {
		t.Fatalf("second connect: %+v lines %d asks %d", o, len(panel.appActivity.lines()), service.keepAsks)
	}

	body, _ := json.Marshal(setupKeepUndo{At: lines[0].At, Apps: lines[0].Undo})
	got := installHandlerFor(panel.handler("setup-test-key"))("POST", "/activity/app-requests", string(body))
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "ComfyUI was removed from OpenAbstractions") {
		t.Fatalf("Undo: %d %s", got.Code, got.Body.String())
	}
	if service.entry != nil || service.removes != 1 {
		t.Fatalf("Undo left entry %+v, removes %d", service.entry, service.removes)
	}
	if lines := panel.appActivity.lines(); len(lines[0].Undo) != 0 {
		t.Fatalf("the line keeps its Undo: %+v", lines)
	}
	if got := readOverview(t, panel).PictureApplication.setupPackageCardFacts; got.State != "connect" {
		t.Fatalf("row after Undo: %+v", got)
	}
}

// The owner's machine after R31: the comfyui entry has waited since a
// Connect under R29 and was never asked about. One Connect re-registers it
// through the same match, keeps it and asks nothing.
func TestConnectClickKeepsTheOwnersWaitingEntry(t *testing.T) {
	found, journal := upgradedComfy(t, connectorBytes)
	connector := officialPicture.extension.Connector(found.Base)
	service := connectorService(t, found, "pending")
	service.entry.pin = service.images[connector]
	panel := connectClickPanel(t, found, journal, service)
	if got := readOverview(t, panel).PictureApplication.setupPackageCardFacts; got.State != "connect" {
		t.Fatalf("waiting entry row: %+v", got)
	}
	if o := connectThroughPanel(t, panel, found, service); o.Failure != "" {
		t.Fatalf("connect: %+v", o)
	}
	if e := service.entry; e.state != "enabled" || service.keepAsks != 0 || service.removes != 0 || len(panel.appActivity.lines()) != 1 {
		t.Fatalf("entry %+v asks %d removes %d lines %d", e, service.keepAsks, service.removes, len(panel.appActivity.lines()))
	}
}

// A runtime without ExpectConnect still asks the person about the verified
// waiting registration and adds no Activity line claiming Keep.
func TestConnectClickUnavailableExpectationAsksInstead(t *testing.T) {
	found, journal := upgradedComfy(t, connectorBytes)
	service := connectorService(t, found, "")
	service.entry = nil
	service.noExpect = true
	panel := connectClickPanel(t, found, journal, service)
	if o := connectThroughPanel(t, panel, found, service); o.Failure != "" {
		t.Fatalf("connect: %+v", o)
	}
	if e := service.entry; e == nil || e.state != "pending" || service.keepAsks != 1 || len(panel.appActivity.lines()) != 0 {
		t.Fatalf("entry %+v asks %d lines %d", e, service.keepAsks, len(panel.appActivity.lines()))
	}
}
