package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The release catalog gives each integrated app its use and the picture app
// its ranked engines, the optimized PyTorch engine first and the Lemonade
// engine as the fallback; the step reads only these.
func TestStepChoicesComeFromTheCatalog(t *testing.T) {
	got := map[AppID]Choice{}
	for _, choice := range NewCatalog().StepChoices() {
		got[choice.ID] = choice
	}
	if len(got) != 2 || got[OpenCode].Use != UseCode || got[OpenCode].Engines != nil ||
		got[ComfyUI].Use != UseImage || !slices.Equal(got[ComfyUI].Engines, []AppID{PyTorch, Lemonade}) {
		t.Fatalf("step choices = %+v", got)
	}
	if ApprovedBF16Plan().PictureEngine() != got[ComfyUI].PictureEngine("") {
		t.Fatal("the reviewed starter picture plan runs in another engine than the picture app's fallback")
	}
	if PyTorchModelPlan().PictureEngine() != got[ComfyUI].PictureEngine(PyTorch) {
		t.Fatal("the reviewed PyTorch picture plan runs in another engine than the picture app's optimized one")
	}
	if got[OpenCode].PictureEngine(PyTorch) != "" || got[ComfyUI].PictureEngine(TheNoise) != Lemonade {
		t.Fatal("an engine the choice does not list falls back to its last engine")
	}
}

// The setup step's picture engine (Runner.StepPictureEngine): PyTorch on a
// Strix Halo card when its reviewed package is preparable and the core has
// its adapter, the Lemonade engine on every other computer and while
// PyTorch's package is still pending. It reads the registry only.
func TestStepPictureEngineFitsTheComputer(t *testing.T) {
	strix := []string{`PCI\VEN_1002&DEV_1586&SUBSYS_38291043&REV_C1`}
	other := []string{`PCI\VEN_10DE&DEV_2684&SUBSYS_16F31043&REV_A1`}
	if got := graphicsOf(append(strix, other...)); !slices.Equal(got, []string{"gfx1151"}) {
		t.Fatalf("graphics = %v", got)
	}
	ready := Artifact{Version: "2.11.0", SourceCommit: strings.Repeat("a", 40),
		SourceURL: "https://github.com/openabstractions/abstraction-provider-pytorch/commit/" + strings.Repeat("a", 40),
		Platform:  "windows/amd64", URL: "https://github.com/openabstractions/redist/releases/download/v0.8.0/inputs.zip",
		SHA256: strings.Repeat("b", 64), Bytes: 1673323779, PeakBytes: 3 << 30, License: "PSF-2.0, BSD-3-Clause and bundled notices",
		Publisher: "OpenAbstractions", Signature: "unsigned", InstallIdentity: "pt", Action: "archive_per_user",
		SourceArchiveURL:    "https://github.com/openabstractions/redist/releases/download/v0.8.0/source.zip",
		SourceArchiveSHA256: strings.Repeat("c", 64), SourceArchiveBytes: 1 << 20}
	// Every x64 core installs the PyTorch adapter; Setup only reads it.
	added := func(AppID) (FeatureStatus, error) { return FeatureStatus{Available: true, Installed: true}, nil }
	missing := func(AppID) (FeatureStatus, error) {
		return adapterPresence(filepath.Join(t.TempDir(), "tools", "Abstraction Panel.exe"), "pytorchd.exe", "PyTorch Image"), nil
	}
	engines := []AppID{PyTorch, Lemonade}
	for _, tc := range []struct {
		name     string
		hardware []string
		pinned   bool
		feature  func(AppID) (FeatureStatus, error)
		want     AppID
		fallback string
	}{
		{"strix with the reviewed package", strix, true, added, PyTorch, ""},
		{"another card", other, true, added, Lemonade, ""},
		{"no card read", nil, true, added, Lemonade, ""},
		{"package still pending", strix, false, added, Lemonade, fallbackNoPackage},
		{"core without the feature", strix, true, missing, Lemonade, fallbackNoFeature},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hardware := tc.hardware
			old := machineGraphics
			machineGraphics = func() ([]string, error) { return hardware, nil }
			t.Cleanup(func() { machineGraphics = old })
			catalog := NewCatalog()
			if tc.pinned {
				for i := range catalog.Components {
					if catalog.Components[i].ID == "pytorch-engine" {
						pin := ready
						catalog.Components[i].Pending, catalog.Components[i].Artifact = "", &pin
					}
				}
			}
			r := NewRunner(Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json"), catalogOverride: &catalog}, nil)
			got, fallback, err := r.StepPictureEngine(context.Background(), engines, tc.feature)
			if err != nil || got != tc.want || fallback != tc.fallback {
				t.Fatalf("engine = %q, %q, %v; want %q, %q", got, fallback, err, tc.want, tc.fallback)
			}
		})
	}
	// Show details states why the faster engine is not used, in one sentence.
	work := &stepPictureWork{app: StepApp{Name: "easel", Title: "Easel"}, plan: ApprovedBF16Plan(),
		offer: StepPictureOffer{Title: "FLUX.2 Klein 4B", Fallback: fallbackNoPackage}}
	if details := pictureDetails(work); !strings.HasSuffix(details, " "+fallbackNoPackage) {
		t.Fatalf("details = %q", details)
	}
	if adopterName.MatchString(fallbackNoPackage + fallbackNoFeature) {
		t.Fatal("a fallback sentence names an engine product")
	}
	if PictureHost(PyTorch) != "pytorch-image" || PictureHost(Lemonade) != "lemonade-image" || PictureHost(ComfyUI) != "" {
		t.Fatal("picture hosts differ from the providers' declaration names")
	}
}

// adopterName is any spelling of an adopter or engine product, as
// scripts/app_names.py matches it.
var adopterName = regexp.MustCompile(`(?i)comfy|open[ _-]?code|lemonade`)

// CQ-21 (architect review 30 B): apps with other names and titles still get
// chat and pictures; every question and notice names them by their
// registered titles, and none names an adopter.
func TestStepServesAppsByTheirCatalogUse(t *testing.T) {
	w := newStepWorld(t)
	w.apps = []StepApp{
		{Name: "quill", Title: "Quill Editor", Use: UseCode, Unconfirmed: true},
		{Name: "easel", Title: "Easel Studio", Use: UseImage, Engine: "fixture-engine", Unconfirmed: true, Program: `C:\Programs\Easel\easel.exe`},
		{Name: "notes", Title: "Notes"},
	}
	f := newPictureFake(w)
	f.app = "easel"
	f.plan.Engine = "fixture-engine"
	encoded, _ := json.Marshal(f.plan)
	id := sha256.Sum256(encoded)
	f.plan.ID = hex.EncodeToString(id[:])
	f.offer.Packages, f.offer.Program = []AppID{"fixture-engine"}, `C:\Programs\Easel\easel.exe`
	f.offer.Rules = []ApprovedRule{{Action: "abstraction.inference/complete", Resource: "host:fixture-image"}}
	outcome, err := f.with(w, w.step()).Run(context.Background())
	if err != nil || outcome != stepSetUp {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	q := w.questions[0]
	if q.Title != "Set up Quill Editor and Easel Studio?" ||
		q.Instruction != "About 16 GB to download." ||
		!strings.HasPrefix(q.Details, "Quill Editor gets Qwen3.6 35B on this computer. Easel Studio gets pictures on this computer. 22 GB free needed. ") ||
		!strings.Contains(q.Details, "Quill Editor asks before its first chat.") || !strings.Contains(q.Details, "Easel Studio gets these permissions:") {
		t.Fatalf("question = %+v", q)
	}
	if strings.Join(w.confirmed, ",") != "quill,easel" || w.selects != 1 || len(f.grants) != 1 || f.grants[0].Program != `C:\Programs\Easel\easel.exe` {
		t.Fatalf("confirmed %v selects %d grants %+v", w.confirmed, w.selects, f.grants)
	}
	if n := w.last(); n.Title != "Ready. Open Quill Editor or Easel Studio." || n.Body != "Qwen3.6 35B answers Quill Editor and Easel Studio makes pictures on this computer." {
		t.Fatalf("last notice = %+v", n)
	}
	if outcome, err := f.with(w, w.step()).Run(context.Background()); err != nil || outcome != stepAlreadySetUp {
		t.Fatalf("rerun = %q, %v", outcome, err)
	}

	// A stopped picture part names the picture app and keeps chat Ready.
	w2 := newStepWorld(t)
	w2.apps = w.apps
	f2 := newPictureFake(w2)
	f2.app, f2.plan, f2.offer = f.app, f.plan, f.offer
	f2.grantErr = context.DeadlineExceeded
	if outcome, err := f2.with(w2, w2.step()).RunAndReport(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("stopped run = %q, %v", outcome, err)
	}
	if n := w2.last(); n.Title != "Quill Editor is ready; Easel Studio setup stopped" {
		t.Fatalf("stopped notice = %+v", n)
	}
	for _, world := range []*stepWorld{w, w2} {
		for _, q := range world.questions {
			if text := q.Title + q.Instruction + q.Details; adopterName.MatchString(text) {
				t.Fatalf("question names an adopter: %s", text)
			}
		}
		for _, n := range world.notices {
			if text := n.Title + n.Body; adopterName.MatchString(text) {
				t.Fatalf("notice names an adopter: %s", text)
			}
		}
	}
}

// A picture app whose catalog engine is not the reviewed picture plan's
// gets no picture part; the chat app is still set up.
func TestStepPicturesNeedTheReviewedPlansEngine(t *testing.T) {
	w := newStepWorld(t)
	w.apps = bothApps()
	w.apps[1].Engine = TheNoise
	f := newPictureFake(w)
	if outcome, err := f.with(w, w.step()).Run(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	if q := w.questions[0]; q.Title != "Set up OpenCode?" || f.engineCalls != 0 || len(f.grants) != 0 {
		t.Fatalf("question %+v engine %d grants %d", q, f.engineCalls, len(f.grants))
	}
}

// Review 28: a stopped picture part is tried again only by a run the person
// starts. An app's request leaves it, posts no notice and resubmits nothing,
// however often the app asks.
func TestStepAppRequestHoldsStoppedPictures(t *testing.T) {
	w := newStepWorld(t)
	w.apps = bothApps()
	f := newPictureFake(w)
	s := f.with(w, w.step())
	s.Owners.Wait = func(context.Context) error {
		w.transfer.state, f.transfer.state = "complete", "failed"
		return nil
	}
	if outcome, err := s.RunAndReport(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("first run = %q, %v", outcome, err)
	}
	notices, submits := len(w.notices), len(f.transfer.submits)
	for i := 0; i < 3; i++ {
		request := f.with(w, w.step())
		request.Owners.Wait = s.Owners.Wait
		request.Requester = &StepRequester{App: "ComfyUI", Use: UseImage}
		if outcome, err := request.RunAndReport(context.Background()); err != nil || outcome != stepAlreadySetUp {
			t.Fatalf("request %d = %q, %v", i, outcome, err)
		}
	}
	if len(w.notices) != notices || len(f.transfer.submits) != submits || w.asked != 1 {
		t.Fatalf("app requests caused notices %d→%d submits %d→%d asked %d", notices, len(w.notices), submits, len(f.transfer.submits), w.asked)
	}
	if state, _ := w.step().Load(context.Background()); !state.PicturesStopped || !state.Ready() {
		t.Fatalf("journal after requests = %+v", state)
	}
	// Settings' Try pictures again resubmits the failed download.
	retry := f.with(w, w.step())
	retry.Owners.Wait = func(context.Context) error { f.transfer.state = "complete"; return nil }
	if outcome, err := retry.RunAndReport(context.Background()); err != nil || outcome != stepSetUp || len(f.transfer.submits) <= submits {
		t.Fatalf("retry = %q, %v submits %d", outcome, err, len(f.transfer.submits))
	}
	if state, _ := w.step().Load(context.Background()); state.PicturesStopped || !state.Granted {
		t.Fatalf("journal after retry = %+v", state)
	}
}
