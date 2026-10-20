package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// The Setup page's facts beside its ticks (architect ruling 2026-11-09 in
// backlog/hp4-hp5-adopter-updates.md): which catalog rows this computer is
// offered, the engine OA chose for pictures, each installed package's size
// and folder, the picture and coding models with whether each is ready, and
// the coding app and its integration. The plan, its
// sizes and the install progress stay with /setup/plan and /setup/state.

type setupOverview struct {
	// Version is this release's version, for the OpenAbstractions row.
	Version string `json:"version,omitempty"`
	// Offered are the catalog choices this computer is offered
	// (setup.Catalog.Available); the page shows no row for another.
	Offered []setup.AppID `json:"offered"`
	// PictureEngine is the engine pictures run in on this computer, the one
	// a picture app's plan includes; "" when no picture app is offered.
	PictureEngine setup.AppID `json:"picture_engine,omitempty"`
	// Installed is each installed package by component: its size on disk
	// and its folder, as its install receipt records them.
	Installed map[string]setupInstalledPackage `json:"installed"`
	// PictureModels are PictureEngine's models, the recommended one first.
	PictureModels []setupPictureOffer `json:"picture_models"`
	// CodingModel is the coding model for chat apps.
	CodingModel setupChatModel `json:"coding_model"`
	// DesktopIntegration reports the desktop app and this release's action.
	DesktopIntegration setupDesktopIntegrationFacts `json:"desktop_integration"`
	// Fixes are the installed apps' adopter fix-ups (setup.Fixup): an app
	// whose fix is gone shows its sentence and Repair, never Installed.
	Fixes []setup.FixupStatus `json:"fixes"`
	// Engines are the engines OA ships or adopts that this computer has or
	// is offered, each under its upstream name with the capabilities it
	// serves for OA (architect ruling 2026-11-09, option c).
	Engines []setupEngine `json:"engines"`
	// PictureApplication reports the official picture app and the presence
	// of an earlier native build.
	PictureApplication setupPictureApplication `json:"picture_application"`
}

// setupEngine is one Engines row. Kind is "included" for the model host's
// llama.cpp, "provider" for an engine the core installs whose state the
// page shows without a tick, and "package"
// for a catalogue choice the page ticks. Ready is a provider's picture
// model in place.
type setupEngine struct {
	ID        setup.AppID `json:"id"`
	Title     string      `json:"title"`
	Caption   string      `json:"caption"`
	Kind      string      `json:"kind"`
	Ready     bool        `json:"ready,omitempty"`
	CanRemove bool        `json:"can_remove,omitempty"`
}

// modelHostEngineWords are the model host's upstream engine and what it
// serves for OA. A catalogue engine reads by its provider's title and words
// (through the choice's Host), so Setup and the Panel name
// each engine alike. A runtime it needs (the ROCm torch runtime) has no row;
// it is in the download line of what needs it. An engine without provider
// words shows under its catalogue title.
var modelHostEngineWords = [2]string{shippedEngineTitle(product.ModelHost), "Chat and embeddings"}

var setupInstalledEngineOverview = setup.InstalledEngineOverview

// setupEngines lists the Engines rows: the model host's llama.cpp, then
// each offered catalogue engine or its installed core provider fallback.
func setupEngines(ctx context.Context, catalog setup.Catalog, journal setup.Journal, offered []setup.AppID) []setupEngine {
	engines := []setupEngine{{ID: "modelhost", Title: modelHostEngineWords[0], Caption: modelHostEngineWords[1], Kind: "included"}}
	for _, choice := range catalog.Choices {
		if choice.Application || choice.Host == "" {
			continue
		}
		title, caption := choice.Name(), ""
		if name, description, ok := product.EnginePresentation(choice.Host); ok {
			title, caption = name, description
		}
		if slices.Contains(offered, choice.ID) {
			engines = append(engines, setupEngine{
				ID: choice.ID, Title: title, Caption: caption, Kind: "package",
				CanRemove: setup.PictureEngineRemoval(choice.ID) != nil,
			})
			continue
		}
		if installed, ready := setupInstalledEngineOverview(ctx, journal, choice.ID); installed {
			engines = append(engines, setupEngine{ID: choice.ID, Title: title, Caption: caption, Kind: "provider", Ready: ready})
		}
	}
	// One order for listing and routing (architect ruling 2026-11-09):
	// llama.cpp for chat first, then the picture engines in the order image@1
	// asks them (product.EngineRank).
	rank := func(id setup.AppID) int {
		if id == "modelhost" {
			return product.EngineRank(product.ModelHost)
		}
		for _, choice := range catalog.Choices {
			if choice.ID == id {
				return product.EngineRank(choice.Host)
			}
		}
		return product.EngineRank("")
	}
	slices.SortStableFunc(engines, func(a, b setupEngine) int { return rank(a.ID) - rank(b.ID) })
	return engines
}

type setupInstalledPackage struct {
	Bytes  int64  `json:"bytes"`
	Folder string `json:"folder"`
}

type setupChatModel struct {
	setup.ModelPlan
	// Installed is whether the model is on this computer: Setup's own
	// download is verified, or the setup step set it up.
	Installed bool `json:"installed"`
	// UsedBy are the titles of the apps the setup step gave it.
	UsedBy   []string `json:"used_by"`
	Archived bool     `json:"archived,omitempty"`
}

type setupDesktopIntegrationFacts struct {
	App    setup.AppID `json:"app,omitempty"`
	Coding bool        `json:"coding,omitempty"`
	// Installed is whether the official desktop program is present.
	Installed bool   `json:"installed"`
	Action    string `json:"action,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// setupOverviewStep reads the setup step's journal; tests replace it.
var setupOverviewStep = func(p *servicePanel, ctx context.Context) setup.StepState { return p.readStepState(ctx) }

// desktopProgramPresent reports the app's official program in the
// account's local application data; tests replace it.
var desktopProgramPresent = func(app setup.AppID) bool {
	official, ok := setup.OfficialProgramOf(app)
	local, err := layout.LocalAppData()
	if !ok || err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(local, filepath.FromSlash(official.LocalAppData)))
	return err == nil && info.Mode().IsRegular()
}

func (p *servicePanel) setupOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	journal, err := p.setupJournalForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	if err := setupFillImageDefault(ctx, journal); err != nil {
		log.Printf("setup: picture default: %v", err)
	}
	if err := setupFillVideoDefault(ctx); err != nil {
		log.Printf("setup: video default: %v", err)
	}
	panelJSON(w, p.readSetupOverview(ctx, journal))
}

// readSetupOverview gathers the overview. A fact Setup cannot read is left
// at its zero value and logged; the page then shows less, never an error.
func (p *servicePanel) readSetupOverview(ctx context.Context, journal setup.Journal) setupOverview {
	catalog := journal.Catalog()
	overview := setupOverview{Offered: []setup.AppID{}, Installed: map[string]setupInstalledPackage{}, PictureModels: []setupPictureOffer{}, Fixes: []setup.FixupStatus{}}
	if version, err := setup.ReleaseVersion(); err == nil {
		overview.Version = version
	}
	for _, choice := range catalog.Choices {
		if catalog.Available(choice.ID) {
			overview.Offered = append(overview.Offered, choice.ID)
		}
	}
	// The picture app's catalogue choice selects the engine for this computer.
	// The engine can be offered independently of the app.
	for _, choice := range catalog.Choices {
		if len(choice.Engines) > 0 {
			if engine := catalog.PictureEngine(choice.ID); slices.Contains(overview.Offered, engine) {
				overview.PictureEngine = engine
			}
			break
		}
	}
	if installed, err := journal.InstalledVersions(ctx); err != nil {
		log.Printf("setup: installed versions: %v", err)
	} else {
		for id, artifact := range installed {
			if folder, bytes, err := setup.InstalledSize(artifact); err == nil {
				overview.Installed[id] = setupInstalledPackage{Bytes: bytes, Folder: folder}
			}
		}
	}
	if overview.PictureEngine != "" {
		if _, _, offers, err := setupPictureOffers(ctx, journal, overview.PictureEngine, nil); err != nil {
			log.Printf("setup: picture models: %v", err)
		} else {
			for i := range offers {
				offers[i].Archived = !offers[i].Prepared && setup.ArchiveHolds(offers[i].Downloads())
			}
			overview.PictureModels = append(overview.PictureModels, offers...)
		}
	}
	overview.Engines = setupEngines(ctx, catalog, journal, overview.Offered)
	overview.PictureApplication = p.readPictureApplication(ctx, catalog, journal)
	overview.CodingModel = p.setupCodingFacts(ctx, catalog)
	// A model the archive's listing does not hold lists the archive again in
	// the background, at most every ten minutes; this read waits for none.
	var wanted []setup.PinnedDownload
	for _, offer := range overview.PictureModels {
		if !offer.Prepared && !offer.Archived {
			wanted = append(wanted, offer.Downloads()...)
		}
	}
	if !overview.CodingModel.Installed && !overview.CodingModel.Archived {
		wanted = append(wanted, overview.CodingModel.Downloads()...)
	}
	setup.ArchiveLookFor(wanted)
	if catalog.DesktopIntegration != nil {
		overview.DesktopIntegration = readDesktopIntegration(catalog, catalog.DesktopIntegration.App)
	} else {
		for _, choice := range catalog.Choices {
			if choice.Application && choice.Official != nil && choice.Use == setup.UseCode {
				overview.DesktopIntegration = readDesktopIntegration(catalog, choice.ID)
				break
			}
		}
	}
	if fixes, err := setupFixups(ctx, catalog); err != nil {
		log.Printf("setup: fix-ups: %v", err)
	} else {
		overview.Fixes = fixes
	}
	return overview
}

// setupCodingFacts is the coding model's row: ready when Setup's own
// download is verified or the setup step set it up and it was not removed
// from OA since, used by the apps the step gave it.
func (p *servicePanel) setupCodingFacts(ctx context.Context, catalog setup.Catalog) setupChatModel {
	model := setupChatModel{ModelPlan: setup.CodingModelPlan(), UsedBy: []string{}}
	if c, err := p.codingModelController(); err == nil {
		if status, err := c.Status(ctx); err == nil {
			model.Installed = len(status.Assets) > 0 && status.Assets[0].Phase == setup.Verified
		}
	}
	step := setupOverviewStep(p, ctx)
	// The step's pick is this model when it approved this plan's download,
	// or served or adopted the model's own family; its title is display.
	sameModel := step.ModelPlanID == model.ID || (step.ModelPlanID == "" && step.Family != "" && strings.EqualFold(step.Family, model.Model))
	if step.Ready() && sameModel && !step.RemovedFromOA {
		model.Installed = true
		for _, app := range step.Apps {
			for _, choice := range catalog.Choices {
				if string(choice.ID) == app && !slices.Contains(model.UsedBy, choice.Name()) {
					model.UsedBy = append(model.UsedBy, choice.Name())
				}
			}
		}
	}
	model.Archived = !model.Installed && setup.ArchiveHolds(model.Downloads())
	return model
}
