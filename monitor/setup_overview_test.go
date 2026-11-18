package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/product"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// stubOverviewMachine keeps a test's overview off this computer: no setup
// step journal, no OpenCode program and no ComfyUI.
func stubOverviewMachine(t *testing.T) {
	t.Helper()
	step, program, comfy, finish, kept := setupOverviewStep, desktopProgramPresent, officialPicture.discover, officialPicture.desktopToFinish, officialPicture.entryKept
	t.Cleanup(func() {
		setupOverviewStep, desktopProgramPresent, officialPicture.discover, officialPicture.desktopToFinish, officialPicture.entryKept = step, program, comfy, finish, kept
	})
	officialPicture.entryKept = func(*servicePanel, context.Context, string) (bool, bool) { return true, true }
	setupOverviewStep = func(*servicePanel, context.Context) setup.StepState { return setup.StepState{} }
	desktopProgramPresent = func(setup.AppID) bool { return false }
	officialPicture.discover = func() []setup.ExtensionHost { return nil }
	officialPicture.desktopToFinish = func() (setup.ExtensionHost, bool) { return setup.ExtensionHost{}, false }
}

func readOverview(t *testing.T, p *servicePanel) setupOverview {
	t.Helper()
	got := installHandlerFor(p.handler("setup-test-key"))("GET", "/setup/overview", "")
	if got.Code != http.StatusOK {
		t.Fatalf("GET /setup/overview: %d %s", got.Code, got.Body.String())
	}
	var overview setupOverview
	if err := json.Unmarshal(got.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	return overview
}

// The overview offers only pinned rows and reads OpenCode's facts.
func TestSetupOverviewOffersPinnedRowsAndOpenCodeFacts(t *testing.T) {
	stubOverviewMachine(t)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	p := &servicePanel{setupJournal: &journal}
	overview := readOverview(t, p)
	picture := overview.PictureApplication
	spec, ok := product.PackageDeclaration(string(setup.ComfyUI))
	if !ok || string(picture.ID) != spec.ID || picture.Title != spec.Title || picture.State != "get" {
		t.Fatalf("official picture row: %+v, package: %+v", picture, spec)
	}
	if slices.Contains(overview.Offered, setup.OpenCode) {
		t.Fatalf("a row this release does not pin is offered: %v", overview.Offered)
	}
	if overview.Version == "" || overview.DesktopIntegration.App != setup.OpenCode || !overview.DesktopIntegration.Coding || overview.DesktopIntegration.Installed || overview.DesktopIntegration.Action != "" {
		t.Fatalf("version or OpenCode facts: %+v", overview)
	}
	if overview.CodingModel.Title != "Qwen3.6 35B" || overview.CodingModel.Installed || len(overview.CodingModel.UsedBy) != 0 {
		t.Fatalf("coding model facts: %+v", overview.CodingModel)
	}
	desktopProgramPresent = func(setup.AppID) bool { return true }
	if overview = readOverview(t, p); !overview.DesktopIntegration.Installed || overview.DesktopIntegration.Action != "" {
		t.Fatalf("an installed OpenCode reads no action: %+v", overview.DesktopIntegration)
	}
}

// The coding model is installed when the setup step set it up for apps,
// used by those apps; a model the person removed from OA is not.
func TestSetupOverviewCodingModelFollowsTheSetupStep(t *testing.T) {
	stubOverviewMachine(t)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	p := &servicePanel{setupJournal: &journal}
	var step setup.StepState
	if err := json.Unmarshal([]byte(`{"phase":"ready","title":"Another title","family":"qwen3.6-35b-a3b","apps":["opencode"]}`), &step); err != nil {
		t.Fatal(err)
	}
	if !step.Ready() {
		t.Fatal("the fixture step is not ready")
	}
	setupOverviewStep = func(*servicePanel, context.Context) setup.StepState { return step }
	if got := readOverview(t, p).CodingModel; !got.Installed || !slices.Equal(got.UsedBy, []string{"OpenCode"}) {
		t.Fatalf("a model the step set up for OpenCode: %+v", got)
	}
	// The step's pick is matched by family or plan, never by its title.
	other := step
	other.Family = "another-model"
	setupOverviewStep = func(*servicePanel, context.Context) setup.StepState { return other }
	if got := readOverview(t, p).CodingModel; got.Installed {
		t.Fatalf("another family reads as the coding model: %+v", got)
	}
	setupOverviewStep = func(*servicePanel, context.Context) setup.StepState { return step }
	step.RemovedFromOA = true
	if got := readOverview(t, p).CodingModel; got.Installed || len(got.UsedBy) != 0 {
		t.Fatalf("a model removed from OA still reads installed: %+v", got)
	}
}

// Engines are the upstream engines OA ships or adopts, each with the
// capabilities it serves for OA (architect ruling 2026-11-09, option c):
// four on a computer the Diffusers entry supports, three elsewhere, and no
// row or caption names a runtime. They list in the order image@1 asks them
// (product.EngineRank): llama.cpp, then Diffusers, Lemonade and TheNoise.
func TestSetupEnginesAreTheUpstreamEngines(t *testing.T) {
	facts := setupInstalledEngineOverview
	t.Cleanup(func() { setupInstalledEngineOverview = facts })
	setupInstalledEngineOverview = func(_ context.Context, _ setup.Journal, engine setup.AppID) (bool, bool) {
		return engine == setup.Lemonade, engine == setup.Lemonade
	}
	catalog := setup.NewCatalog()
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	rows := func(offered ...setup.AppID) []string {
		var out []string
		for _, e := range setupEngines(context.Background(), catalog, journal, offered) {
			if strings.Contains(e.Title+e.Caption, "PyTorch") || strings.Contains(e.Title+e.Caption, "ROCm") {
				t.Fatalf("an engine row names a runtime: %+v", e)
			}
			if e.Ready != (e.Kind == "provider" && e.ID == setup.Lemonade) {
				t.Fatalf("unexpected engine readiness: %+v", e)
			}
			if e.CanRemove != (e.Kind == "package" && (e.ID == setup.PyTorch || e.ID == setup.TheNoise)) {
				t.Fatalf("unexpected engine removal capability: %+v", e)
			}
			out = append(out, e.Title+" — "+e.Caption+" ("+e.Kind+")")
		}
		return out
	}
	want := []string{"llama.cpp — Chat and embeddings (included)", "Diffusers — Pictures and video on AMD graphics (package)",
		"Lemonade — Pictures, speech and transcription (provider)", "TheNoise — Pictures on the processor (package)"}
	if got := rows(setup.ComfyUI, setup.PyTorch, setup.TheNoise); !slices.Equal(got, want) {
		t.Fatalf("on gfx1151:\n%s", strings.Join(got, "\n"))
	}
	if got := rows(setup.TheNoise); !slices.Equal(got, []string{want[0], want[2], want[3]}) {
		t.Fatalf("elsewhere:\n%s", strings.Join(got, "\n"))
	}
	setupInstalledEngineOverview = func(context.Context, setup.Journal, setup.AppID) (bool, bool) { return false, false }
	if got := rows(setup.TheNoise); !slices.Equal(got, []string{want[0], want[3]}) {
		t.Fatalf("without the core's Lemonade provider:\n%s", strings.Join(got, "\n"))
	}
}

// On Windows x64 the release pins Lemonade's engine package: the overview
// offers it with a tick, and on a computer without it the package reads
// offered and not installed, even where the core's adapter is present.
func TestSetupOverviewOffersThePinnedLemonadeEngine(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("the release pins Lemonade's package for Windows x64")
	}
	stubOverviewMachine(t)
	facts := setupInstalledEngineOverview
	t.Cleanup(func() { setupInstalledEngineOverview = facts })
	setupInstalledEngineOverview = func(_ context.Context, _ setup.Journal, engine setup.AppID) (bool, bool) {
		if engine == setup.Lemonade {
			t.Fatal("an offered engine should not probe its installed-provider fallback")
		}
		return false, false
	}
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	p := &servicePanel{setupJournal: &journal}
	overview := readOverview(t, p)
	if !slices.Contains(overview.Offered, setup.Lemonade) {
		t.Fatalf("Lemonade's pinned package is not offered: %v", overview.Offered)
	}
	if _, ok := overview.Installed["lemonade-engine"]; ok {
		t.Fatalf("Lemonade reads installed with no package on this computer: %+v", overview.Installed)
	}
	row := slices.IndexFunc(overview.Engines, func(e setupEngine) bool { return e.ID == setup.Lemonade })
	if row < 0 || overview.Engines[row].Kind != "package" || overview.Engines[row].Title != "Lemonade" || overview.Engines[row].Ready || overview.Engines[row].CanRemove {
		t.Fatalf("Lemonade's Engines row: %+v", overview.Engines)
	}
	plan, err := journal.Catalog().Plan([]setup.AppID{setup.Lemonade})
	if err != nil || !plan.Preparable {
		t.Fatalf("Lemonade cannot be installed: %+v, %v", plan, err)
	}
}

// /setup/overview answers only a keyed GET: a POST and a request without
// the Panel key are refused.
func TestSetupOverviewRefusesPostAndUnkeyedRequests(t *testing.T) {
	stubOverviewMachine(t)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	handler := (&servicePanel{setupJournal: &journal}).handler("setup-test-key")
	if got := installHandlerFor(handler)("POST", "/setup/overview", "{}"); got.Code == http.StatusOK {
		t.Fatalf("POST /setup/overview: %d %s", got.Code, got.Body.String())
	}
	req := httptest.NewRequest("GET", "/setup/overview", nil)
	req.Host = "127.0.0.1"
	got := httptest.NewRecorder()
	handler.ServeHTTP(got, req)
	if got.Code == http.StatusOK || strings.Contains(got.Body.String(), "picture_models") {
		t.Fatalf("GET without the Panel key: %d %s", got.Code, got.Body.String())
	}
}
