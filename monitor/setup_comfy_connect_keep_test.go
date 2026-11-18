package main

import (
	"testing"
)

// Owner on R30, 2026-11-11: Connect installed the integration into Comfy
// Desktop's ComfyUI, the connector registered itself and waited for Keep,
// and nothing asked the person; the row read connected with Open. Without a
// Connect click the runtime can match (here a runtime that does not know
// ExpectConnect), Connect ends with the runtime's one Keep question, the row
// offers Connect while the entry waits, and Connect again asks again. A kept
// entry reads connected.
func TestConnectAsksKeepAndTheRowWaitsForIt(t *testing.T) {
	production := officialPicture.entryKept
	stubOverviewMachine(t)
	found, journal := upgradedComfy(t, connectorBytes)
	stubOfficialComfy(t, found)
	officialPicture.entryKept = production
	connector := officialPicture.extension.Connector(found.Base)
	service := connectorService(t, found, "")
	service.entry = nil
	service.noExpect = true
	panel := &servicePanel{setupJournal: journal, comfyApps: service}
	row := func() setupPackageCardFacts { return readOverview(t, panel).PictureApplication.setupPackageCardFacts }

	if got := row(); got.State != "connect" || !got.Connectable {
		t.Fatalf("integration present, nothing registered: %+v", got)
	}
	if o := connectThroughCatalog(t, found, journal, service, service); o.Failure != "" {
		t.Fatalf("connect: %+v", o)
	}
	if e := service.entry; e == nil || e.state != "pending" || e.program != connector || service.keepAsks != 1 {
		t.Fatalf("after Connect: entry %+v keep asks=%d", e, service.keepAsks)
	}
	if got := row(); got.State != "connect" {
		t.Fatalf("a connector waiting for Keep reads %+v", got)
	}
	// The owner's machine: the entry waits and its question is gone. Connect
	// again asks again and registers nothing new.
	if o := connectThroughCatalog(t, found, journal, service, service); o.Failure != "" || service.keepAsks != 2 || service.entry.revision != 2 {
		t.Fatalf("second connect: %+v keep asks=%d entry %+v", o, service.keepAsks, service.entry)
	}
	service.entry.state = "enabled"
	if got := row(); got.State != "connected" {
		t.Fatalf("kept connector: %+v", got)
	}
	if o := connectThroughCatalog(t, found, journal, service, service); o.Failure != "" || service.keepAsks != 2 {
		t.Fatalf("connect on a kept connector asked again: %+v keep asks=%d", o, service.keepAsks)
	}
}
