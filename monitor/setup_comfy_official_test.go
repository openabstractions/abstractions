package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// stubOfficialComfy makes found the ComfyUIs Setup finds on this computer.
func stubOfficialComfy(t *testing.T, found ...setup.ExtensionHost) {
	t.Helper()
	old, finish, claim := officialPicture.discover, officialPicture.desktopToFinish, officialPicture.claim
	t.Cleanup(func() {
		officialPicture.discover, officialPicture.desktopToFinish, officialPicture.claim = old, finish, claim
	})
	officialPicture.claim = func() (func(), bool, error) { return func() {}, true, nil }
	officialPicture.discover = func() []setup.ExtensionHost { return found }
	officialPicture.desktopToFinish = func() (setup.ExtensionHost, bool) { return setup.ExtensionHost{}, false }
}

// stubComfyDesktopToFinish makes Comfy Desktop present at program with no
// ComfyUI installed yet.
func stubComfyDesktopToFinish(t *testing.T, program string) {
	t.Helper()
	old := officialPicture.desktopToFinish
	t.Cleanup(func() { officialPicture.desktopToFinish = old })
	officialPicture.desktopToFinish = func() (setup.ExtensionHost, bool) {
		return setup.ExtensionHost{Kind: officialPicture.extension.InstalledDesktopKind(), Program: program}, true
	}
}

// waitOutcome waits for component's step to end and returns its outcome.
func waitOutcome(t *testing.T, p *servicePanel, component string) setupOutcome {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if o, ok := p.setupOutcomesNow()[component]; ok {
			return o
		}
	}
	t.Fatalf("the %s step did not end", component)
	return setupOutcome{}
}

// The ComfyUI row reads Get with no ComfyUI found, Connect with one found,
// and connected once the integration is in it; Open needs a known program.
func TestSetupOverviewComfyRowFollowsDiscovery(t *testing.T) {
	stubOverviewMachine(t)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	p := &servicePanel{setupJournal: &journal, maintenanceReview: func(context.Context) setup.MaintenanceReview { return setup.MaintenanceReview{} }}
	stubOfficialComfy(t)
	picture := readOverview(t, p).PictureApplication
	if picture.State != "get" || picture.DownloadPage != "https://www.comfy.org/download" || picture.OABuild {
		t.Fatalf("no ComfyUI: %+v", picture)
	}
	target := officialPicture.target("fixture-connector")
	if picture.ID != "comfyui" || picture.Title != "ComfyUI" || target.registration != "comfyui" || target.title != picture.Title ||
		picture.Publisher != "Comfy Org" || picture.License != "GPL-3.0" || picture.Source != "https://github.com/comfyanonymous/ComfyUI" ||
		picture.Finish != "Open Comfy Desktop to finish its setup" {
		t.Fatalf("picture card and Connect target diverged: %+v, %+v", picture, target)
	}
	if picture.Retained.ID != "comfyui-oa" || picture.Retained.Component != "comfyui-app" || picture.ConnectStep != "comfyui-connect" ||
		picture.Retained.Title != "ComfyUI (OA build)" || picture.Retained.RemovalTitle != "the OA build of ComfyUI" {
		t.Fatalf("retained row or step identity changed: %+v", picture)
	}
	registered := map[string]bool{}
	for _, route := range officialPictureSetupExtension().routes {
		registered[route.path] = true
	}
	for _, path := range []string{"/setup/comfy/status", "/setup/comfy/register", "/setup/comfy/activate", "/setup/comfy/remove",
		"/setup/comfy/connect", "/setup/comfy/open", "/setup/comfy/folder", "/setup/comfy/remove-oa-build"} {
		if !registered[path] {
			t.Fatalf("the legacy route changed: %s", path)
		}
	}
	for _, action := range []string{"connect", "update", "open", "folder", "remove-oa"} {
		if !registered[picture.Actions[action]] {
			t.Errorf("the picture application's %s route is not registered: %+v", action, picture.Actions)
		}
	}
	if !registered[officialPicture.legacy.Routes.Status] || !registered[officialPicture.legacy.Routes.Register] || !strings.Contains(setupPage, pictureWorkspaceScript()) {
		t.Fatal("the compiled workspace actions must reach the same registered adapter")
	}
	stubComfyDesktopToFinish(t, `C:\Programs\Comfy Desktop\Comfy Desktop.exe`)
	if got := readOverview(t, p).PictureApplication.setupPackageCardFacts; got.State != "finish" || got.Kind != "comfy-desktop" || !got.Open || got.Folder != "" {
		t.Fatalf("Comfy Desktop without a ComfyUI: %+v", got)
	}
	found := setup.ExtensionHost{Kind: setup.ExtensionDesktop, Base: `C:\ComfyUI`}
	stubOfficialComfy(t, found)
	if got := readOverview(t, p).PictureApplication.setupPackageCardFacts; got.State != "connect" || got.Kind != setup.ExtensionDesktop || got.Folder != `C:\ComfyUI` || got.Open {
		t.Fatalf("found ComfyUI: %+v", got)
	}
	found.Connected, found.Program = true, `C:\ComfyUI.exe`
	stubOfficialComfy(t, found)
	if got := readOverview(t, p).PictureApplication.setupPackageCardFacts; got.State != "connected" || !got.Open {
		t.Fatalf("connected ComfyUI: %+v", got)
	}
}

// The overview names an installed OA build of ComfyUI, from the journal's
// kept record, so its row offers its removal.
func TestOfficialPictureRowAndConnectUseTheAdmittedReleaseRevision(t *testing.T) {
	found, journal := upgradedComfy(t, connectorBytes)
	catalog := journal.Catalog()
	want := catalog.LegacyConnectorPin.MaintenanceRevision()
	if revision, available, err := officialPicture.extension.Revision(catalog); err != nil || !available || revision == "" || revision != want {
		t.Fatalf("admitted integration revision %q, available %v, error %v", revision, available, err)
	}
	stubOfficialComfy(t)
	p := &servicePanel{}
	if facts := officialPicture.cardFacts(p, t.Context(), catalog, *journal); !facts.Connectable || facts.State != "get" {
		t.Fatalf("admitted integration row: %+v", facts)
	}

	changed := *catalog.LegacyConnectorPin
	changed.ConnectorSHA256 = ""
	catalog.LegacyConnectorPin = &changed
	if revision, available, err := officialPicture.extension.Revision(catalog); err == nil || available || revision != "" {
		t.Fatalf("mismatched compatibility pin: revision %q, available %v, error %v", revision, available, err)
	}
	if facts := officialPicture.cardFacts(p, t.Context(), catalog, *journal); facts.Connectable {
		t.Fatalf("row offered an integration that differs from its admitted pin: %+v", facts)
	}
	if handled, err := officialPicture.connect(p, t.Context(), catalog, found); !handled || err == nil || err.Error() != "the integration package differs from its admitted release pin" {
		t.Fatalf("Connect attempted a fallback for mismatched pin: handled %v, error %v", handled, err)
	}

	legacy := setup.Catalog{LegacyConnectorPin: &setup.ConnectorPackagePin{}}
	if revision, available, err := officialPicture.extension.Revision(legacy); err != nil || !available || revision != "" {
		t.Fatalf("legacy integration without a connector pin: revision %q, available %v, error %v", revision, available, err)
	}
}

func TestSetupOverviewNamesTheOABuild(t *testing.T) {
	stubOverviewMachine(t)
	stubOfficialComfy(t)
	path := filepath.Join(t.TempDir(), "plan.json")
	writeOABuildJournal(t, path, false)
	journal := setup.Journal{Path: path}
	p := &servicePanel{setupJournal: &journal, maintenanceReview: func(context.Context) setup.MaintenanceReview { return setup.MaintenanceReview{} }}
	if got := readOverview(t, p).PictureApplication.setupPackageCardFacts; !got.OABuild || got.OABuildRemoving {
		t.Fatalf("OA build: %+v", got)
	}
	writeOABuildJournal(t, path, true)
	if got := readOverview(t, p).PictureApplication.setupPackageCardFacts; !got.OABuild || !got.OABuildRemoving {
		t.Fatalf("OA build being removed: %+v", got)
	}
}

// writeOABuildJournal writes a journal whose later plan keeps an installed
// OA build of ComfyUI; removing marks a removal that stopped part way.
func writeOABuildJournal(t *testing.T, path string, removing bool) {
	t.Helper()
	state := map[string]any{"plan": map[string]any{"id": "plan"}, "approved_plan_id": "plan", "effects": map[string]any{}, "configuration": map[string]any{},
		"oa_build_comfy": map[string]any{"artifact": map[string]any{"version": "0.8.0-r27", "native_resources": map[string]any{}},
			"effect": map[string]any{"phase": "installed", "removing": removing}}}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// Connect runs as a Setup step on the ComfyUI the row shows and keeps its
// outcome; it needs the person's confirmation and a ComfyUI to connect.
func TestSetupComfyConnectRunsAsAStep(t *testing.T) {
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	p := &servicePanel{setupJournal: &journal, comfyApps: &nativeComfyService{entry: &nativeComfyEntry{state: "enabled"}},
		appActivity: applicationActivityLog{path: filepath.Join(t.TempDir(), "activity.json")}}
	request := installHandlerFor(p.handler("setup-test-key"))
	old := officialPicture.reconnect
	t.Cleanup(func() { officialPicture.reconnect = old })
	var connected []setup.ExtensionHost
	fail := error(nil)
	officialPicture.reconnect = func(_ context.Context, _ setup.Catalog, found setup.ExtensionHost) error {
		step := p.setupActionsNow()[officialPicture.legacy.ConnectStep]
		if step.Kind != "connect" || step.StepID == "" || step.Cancellable || p.cancelSetupStep(officialPicture.legacy.ConnectStep) {
			return errors.New("Connect lost its non-cancellable step")
		}
		connected = append(connected, found)
		return fail
	}
	stubOfficialComfy(t)
	if got := request("POST", "/setup/comfy/connect", `{"confirm":true}`); got.Code != http.StatusConflict {
		t.Fatalf("connect without ComfyUI: %d %s", got.Code, got.Body.String())
	}
	found := setup.ExtensionHost{Kind: setup.ExtensionPortable, Base: `D:\ComfyUI_windows_portable\ComfyUI`}
	stubOfficialComfy(t, found)
	if got := request("POST", "/setup/comfy/connect", `{}`); got.Code != http.StatusBadRequest {
		t.Fatalf("connect without confirmation: %d", got.Code)
	}
	got := request("POST", "/setup/comfy/connect", `{"confirm":true}`)
	var accepted struct {
		StepID string `json:"step_id"`
	}
	if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &accepted) != nil || accepted.StepID == "" {
		t.Fatalf("connect: %d %s", got.Code, got.Body.String())
	}
	if o := waitOutcome(t, p, officialPicture.legacy.ConnectStep); o.StepID != accepted.StepID || o.Kind != "connect" || o.Failure != "" || len(connected) != 1 || connected[0] != found {
		t.Fatalf("connect outcome %+v, connected %+v", o, connected)
	}
	fail = errors.New("Windows Installer could not install the ComfyUI integration (Windows Installer code 1603)")
	p.setupOutcomes = nil
	if got := request("POST", "/setup/comfy/connect", `{"confirm":true}`); got.Code != http.StatusOK {
		t.Fatalf("second connect: %d %s", got.Code, got.Body.String())
	}
	if o := waitOutcome(t, p, officialPicture.legacy.ConnectStep); o.Failure != "Windows Installer could not install the ComfyUI integration (Windows Installer code 1603)" {
		t.Fatalf("failed connect outcome %+v", o)
	}
}

// Open starts only a connected ComfyUI and says why it cannot.
func TestSetupComfyOpenStartsTheConnectedComfy(t *testing.T) {
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	p := &servicePanel{setupJournal: &journal}
	request := installHandlerFor(p.handler("setup-test-key"))
	old := officialPicture.open
	t.Cleanup(func() { officialPicture.open = old })
	opened := 0
	officialPicture.open = func(found setup.ExtensionHost) error {
		opened++
		return setup.OpenExtensionHost(found, officialPictureAppTitle)
	}
	stubOfficialComfy(t, setup.ExtensionHost{Kind: setup.ExtensionDesktop, Base: `C:\ComfyUI`})
	stubComfyDesktopToFinish(t, `C:\Programs\Comfy Desktop\Comfy Desktop.exe`)
	if got := request("POST", "/setup/comfy/open", `{"confirm":false}`); got.Code == http.StatusOK || opened != 0 {
		t.Fatalf("Open without confirmation: %d, opened %d", got.Code, opened)
	}
	if got := request("POST", "/setup/comfy/open", `{"confirm":true}`); got.Code != http.StatusConflict || opened != 0 {
		t.Fatalf("open of an unconnected ComfyUI: %d", got.Code)
	}
	stubOfficialComfy(t, setup.ExtensionHost{Kind: officialPicture.extension.InstalledDesktopKind(), Base: `C:\ComfyUI`, Connected: true})
	if got := request("POST", "/setup/comfy/open", `{"confirm":true}`); got.Code != http.StatusConflict || got.Body.String() != "Start ComfyUI from the Start menu.\n" {
		t.Fatalf("open without a known program: %d %q", got.Code, got.Body.String())
	}
	// Comfy Desktop with no ComfyUI installed yet opens so the person
	// finishes its setup.
	officialPicture.open = func(found setup.ExtensionHost) error {
		if found.Program != `C:\Programs\Comfy Desktop\Comfy Desktop.exe` || found.Base != "" {
			t.Errorf("opened %+v", found)
		}
		opened++
		return nil
	}
	stubOfficialComfy(t)
	if got := request("POST", "/setup/comfy/open", `{"confirm":true}`); got.Code != http.StatusConflict || opened != 1 {
		t.Fatalf("open with nothing found: %d %d", got.Code, opened)
	}
	stubComfyDesktopToFinish(t, `C:\Programs\Comfy Desktop\Comfy Desktop.exe`)
	if got := request("POST", "/setup/comfy/open", `{"confirm":true}`); got.Code != http.StatusOK || opened != 2 {
		t.Fatalf("open Comfy Desktop to finish its setup: %d %s", got.Code, got.Body.String())
	}
}

// Choose folder keeps a folder holding ComfyUI and refuses another with one
// sentence; a window without a picker says so.
func TestSetupComfyFolderKeepsAPortableComfy(t *testing.T) {
	if err := setup.UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setup.UseUserState)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	p := &servicePanel{setupJournal: &journal}
	request := installHandlerFor(p.handler("setup-test-key"))
	if got := request("POST", "/setup/comfy/folder", `{}`); got.Code != http.StatusOK || got.Body.String() != "{\"available\":false}\n" {
		t.Fatalf("no picker: %d %s", got.Code, got.Body.String())
	}
	p.directoryPicker = func(context.Context) (string, error) { return "", nil }
	if got := request("POST", "/setup/comfy/folder", `{"folder":"caller-supplied"}`); got.Code != http.StatusOK ||
		got.Body.String() != "{\"available\":true,\"chosen\":false}\n" || officialPicture.extension.ChosenFolder() != "" {
		t.Fatalf("cancelled picker: %d %s", got.Code, got.Body.String())
	}
	chosen := t.TempDir()
	p.directoryPicker = func(context.Context) (string, error) { return chosen, nil }
	if got := request("POST", "/setup/comfy/folder", `{}`); got.Code != http.StatusConflict || officialPicture.extension.ChosenFolder() != "" {
		t.Fatalf("a folder without ComfyUI: %d %s", got.Code, got.Body.String())
	}
	if err := os.MkdirAll(filepath.Join(chosen, "ComfyUI", "custom_nodes"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := request("POST", "/setup/comfy/folder", `{}`); got.Code != http.StatusOK || officialPicture.extension.ChosenFolder() != chosen {
		t.Fatalf("a portable ComfyUI: %d %s", got.Code, got.Body.String())
	}
}

// Remove the OA build runs as a Setup step under its component, with Cancel,
// and only while the journal records the build.
func TestSetupRemoveOABuildRunsAsAStep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	journal := setup.Journal{Path: path}
	p := &servicePanel{setupJournal: &journal}
	request := installHandlerFor(p.handler("setup-test-key"))
	old := oaBuildRemoval
	t.Cleanup(func() { oaBuildRemoval = old })
	removed := 0
	oaBuildRemoval = func(_ *officialPackagePanel, _ *servicePanel, ctx context.Context, _ setup.Runner) error {
		removed++
		if p.setupActionsNow()["comfyui-app"].Cancellable != true {
			return errors.New("the removal step has no Cancel")
		}
		return nil
	}
	if got := request("POST", "/setup/comfy/remove-oa-build", `{"confirm":true}`); got.Code != http.StatusConflict && got.Code != http.StatusInternalServerError {
		t.Fatalf("removal without a build: %d %s", got.Code, got.Body.String())
	}
	writeOABuildJournal(t, path, false)
	if got := request("POST", "/setup/comfy/remove-oa-build", `{"confirm":false}`); got.Code != http.StatusBadRequest {
		t.Fatalf("removal without confirmation: %d", got.Code)
	}
	if got := request("POST", "/setup/comfy/remove-oa-build", `{"confirm":true}`); got.Code != http.StatusOK {
		t.Fatalf("removal: %d %s", got.Code, got.Body.String())
	}
	if o := waitOutcome(t, p, "comfyui-app"); o.Kind != "remove" || o.Failure != "" || removed != 1 {
		t.Fatalf("removal outcome %+v after %d runs", o, removed)
	}
}

// A second selected package cannot consume the picture package's restart
// record even when both observers can see the same host process.
func TestOfficialPackageRestartStateIsScopedToTheSelectedPackage(t *testing.T) {
	first := *officialPicture
	second := first
	second.spec.ID = "another-extension"
	running := []uint32{42}
	first.processes = func(setup.ExtensionHost) ([]uint32, error) { return running, nil }
	second.processes = first.processes
	panel := &servicePanel{packageRestarts: map[string][]uint32{first.spec.ID: {42}, second.spec.ID: {99}}}
	if second.restartPending(panel, setup.ExtensionHost{}) {
		t.Fatal("the second package borrowed the first package's running process")
	}
	if !first.restartPending(panel, setup.ExtensionHost{}) {
		t.Fatal("the second package cleared the first package's restart record")
	}
}

func TestOfficialPackageConnectPublishesOnlyItsPreparedReceipt(t *testing.T) {
	pkg := *officialPicture
	path := filepath.Join(t.TempDir(), "integration.json")
	pkg.recordFile = func() (string, error) { return path, nil }
	pkg.entryKept = func(*servicePanel, context.Context, string) (bool, bool) { return false, true }
	found := setup.ExtensionHost{Base: t.TempDir()}
	catalog := setup.Catalog{LegacyConnectorPin: &setup.ConnectorPackagePin{}}
	pkg.connectPackage = func(ctx context.Context, _ setup.Catalog, _ setup.ExtensionHost, ops setup.PackageConnection) (bool, error) {
		prepared := setup.PackageConnectionReceipt{
			CatalogID: pkg.spec.ID, Registration: pkg.spec.Registration, Program: pkg.extension.Connector(found.Base),
			LegacyOrder: pkg.legacy.Order, BeforeDispatch: true,
		}
		if err := ops.Save(ctx, nil, &prepared); err != nil {
			return true, err
		}
		if ops.Publish == nil {
			t.Fatal("Connect did not bind publication to its receipt store")
		}
		publishes := 0
		publish := func(context.Context) error {
			publishes++
			return nil
		}
		stale := prepared
		stale.Program += ".other"
		if err := ops.Publish(ctx, stale, publish); err == nil || publishes != 0 {
			t.Fatalf("publication for stale plan: error=%v publishes=%d", err, publishes)
		}
		if err := ops.Publish(ctx, prepared, publish); err != nil || publishes != 1 {
			t.Fatalf("publication for exact plan: error=%v publishes=%d", err, publishes)
		}
		return true, nil
	}
	if handled, err := pkg.connect(&servicePanel{}, t.Context(), catalog, found); err != nil || !handled {
		t.Fatalf("Connect handled=%v error=%v", handled, err)
	}
}
