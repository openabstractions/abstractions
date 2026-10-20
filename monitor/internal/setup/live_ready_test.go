package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The owner's 2026-11-09 report: Setup said pictures were ready while every
// picture failed. A picture model the engine does not serve stops the part
// before its permissions and Ready; a rerun says nothing is set up.
func TestStepPicturesNotServedStopBeforeGrantAndReady(t *testing.T) {
	w := newStepWorld(t)
	w.apps = bothApps()[1:]
	f := newPictureFake(w)
	notServed := errors.New("the picture engine does not serve FLUX.2 Klein 4B; its own check did not pass")
	bind := func() SetupStep {
		s := f.with(w, w.step())
		s.Owners.Pictures.Configure = func(context.Context) error { f.configures++; return notServed }
		// The offer reads the runtime: an engine that does not serve the
		// model is unprepared, whatever the journal recorded.
		offer := s.Owners.Pictures.Offer
		s.Owners.Pictures.Offer = func(ctx context.Context, app StepApp) (StepPictureOffer, error) {
			o, err := offer(ctx, app)
			o.Configured = false
			return o, err
		}
		return s
	}
	if outcome, err := bind().RunAndReport(context.Background()); err == nil {
		t.Fatalf("an engine that does not serve the model set up: %q", outcome)
	}
	if len(f.grants) != 0 || f.configures != 1 {
		t.Fatalf("grants %d configures %d", len(f.grants), f.configures)
	}
	state, _ := w.step().Load(context.Background())
	if state.Granted || state.Progress == stepReady {
		t.Fatalf("journal = %+v", state)
	}
	for _, n := range w.notices {
		if strings.HasPrefix(n.Title, "Ready") || n.Title == "Already set up" {
			t.Fatalf("notice = %+v", n)
		}
	}
	if outcome, _ := bind().RunAndReport(context.Background()); outcome == stepAlreadySetUp || w.last().Title == "Already set up" {
		t.Fatalf("rerun = %q, %+v", outcome, w.last())
	}
}

// installedFixture is Setup's plan with one installed Lemonade package.
func installedFixture(t *testing.T) (Runner, Artifact, string) {
	t.Helper()
	ctx := context.Background()
	standInHome(t)
	catalog := fixtureCatalog()
	index := -1
	for i := range catalog.Components {
		if catalog.Components[i].ID == "lemonade-engine" {
			index = i
		}
	}
	archive, built := archiveFixture(t, packageEntries("lemonade-engine", "1.0.0")...)
	a := *catalog.Components[index].Artifact
	a.Version, a.SHA256, a.Bytes, a.PeakBytes = "1.0.0", built.SHA256, built.Bytes, built.PeakBytes
	catalog.Components[index].Artifact = &a
	journal := Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json"), catalogOverride: &catalog}
	plan := mustPlanOf(t, catalog, Lemonade)
	state, err := journal.SavePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	root, err := installPath(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallArchive(ctx, a, archive, root); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Update(ctx, plan.ID, "lemonade-engine", state.Effects["lemonade-engine"], Effect{Phase: Installed}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	return NewRunner(journal, &fakeTransfer{state: "complete", bytes: data}), a, root
}

// A deleted package reads Uncertain; Retry with its download gone requests
// the pinned package again and Advance installs it.
func TestRetryReinstallsADeletedPackage(t *testing.T) {
	ctx := context.Background()
	r, _, root := installedFixture(t)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(filepath.Join(filepath.Dir(r.Journal.Path), "downloads"))
	r.healthCheck = func(context.Context, string, Artifact, string) error { return nil }
	transfer := r.Transfer.(*fakeTransfer)
	status, err := r.Retry(ctx, "lemonade-engine")
	if err != nil || transfer.submit != 1 || status.Components[0].Effect.Phase != Requested {
		t.Fatalf("retry = %+v, %v (submits %d)", status.Components, err, transfer.submit)
	}
	for i := 0; i < 6; i++ {
		state, _ := r.Journal.Load(ctx)
		if state.Effects["lemonade-engine"].Phase == Installed {
			if reason := r.installedTreeMissing(state, "lemonade-engine"); reason != "" {
				t.Fatalf("reinstalled tree: %s", reason)
			}
			return
		}
		if _, err := r.Advance(ctx, "lemonade-engine"); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("the deleted package was not installed again")
}

// A partial folder is the person's data: Retry preserves it.
func TestRetryPreservesAPartialPackageFolder(t *testing.T) {
	ctx := context.Background()
	r, _, root := installedFixture(t)
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "mine.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Retry(ctx, "lemonade-engine"); err == nil || !strings.Contains(err.Error(), "existing app data is preserved") {
		t.Fatalf("retry over a partial folder = %v", err)
	}
}

// An installed package whose folder was deleted reads Uncertain with the
// reason, and the picture engine offer is no longer installed; the journal
// keeps its record.
func TestDeletedInstalledTreeReadsUncertain(t *testing.T) {
	ctx := context.Background()
	r, _, root := installedFixture(t)
	row := func() ComponentStatus {
		t.Helper()
		status, err := r.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range status.Components {
			if row.ID == "lemonade-engine" {
				return row
			}
		}
		t.Fatal("no row")
		return ComponentStatus{}
	}
	installed := FeatureStatus{Installed: true, Available: true}
	if got := row(); got.Effect.Phase != Installed {
		t.Fatalf("installed row = %+v", got.Effect)
	}
	if offer, err := r.StepEngineOffer(ctx, installed, Lemonade); err != nil || !offer.EngineReady {
		t.Fatalf("installed offer = %+v, %v", offer, err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	got := row()
	if got.Effect.Phase != Uncertain || !strings.Contains(got.Effect.Reason, "install receipt is missing") {
		t.Fatalf("deleted tree row = %+v", got.Effect)
	}
	if status, _ := r.Status(ctx); status.Ready {
		t.Fatal("a plan with a deleted package reads ready")
	}
	if offer, err := r.StepEngineOffer(ctx, installed, Lemonade); err != nil || offer.EngineReady {
		t.Fatalf("deleted tree offer = %+v, %v", offer, err)
	}
	if state, _ := r.Journal.Load(ctx); state.Effects["lemonade-engine"].Phase != Installed {
		t.Fatalf("the status read changed the journal: %+v", state.Effects["lemonade-engine"])
	}
}

// A sampled file the receipt lists (its first file) that is missing or of
// another size reads Uncertain without hashing; an intact folder reads
// installed.
func TestInstalledTreeCheckReadsSizes(t *testing.T) {
	_, a, root := installedFixture(t)
	ok, reason, err := archiveReceiptAndSizes(a, root)
	if err != nil || !ok {
		t.Fatalf("intact = %v %q %v", ok, reason, err)
	}
	receipt, _, err := archiveReceiptFor(a, root)
	if err != nil || len(receipt.Files) == 0 {
		t.Fatalf("receipt = %+v, %v", receipt, err)
	}
	path := filepath.Join(root, filepath.FromSlash(receipt.Files[0].Path))
	if err := os.WriteFile(path, append([]byte("x"), make([]byte, receipt.Files[0].Size)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, reason, err := archiveReceiptAndSizes(a, root); err != nil || ok || !strings.Contains(reason, "changed size") {
		t.Fatalf("resized = %v %q %v", ok, reason, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if ok, reason, err := archiveReceiptAndSizes(a, root); err != nil || ok || !strings.Contains(reason, "missing") {
		t.Fatalf("removed = %v %q %v", ok, reason, err)
	}
}

// configuredStarter writes the starter picture model journal beside
// journal with the provider recorded as prepared and the downloads gone.
func configuredStarter(t *testing.T, journal Journal) ModelController {
	t.Helper()
	plan := ApprovedBF16Plan()
	state := ModelState{PlanID: plan.ID, Approved: true, ProviderConfigured: true, Effects: map[string]ModelEffect{}}
	for _, a := range plan.Assets {
		state.Effects[a.ID] = ModelEffect{Phase: Verified, StagedPath: filepath.Join(t.TempDir(), "moved-"+a.ID)}
	}
	raw, _ := json.Marshal(state)
	path := filepath.Join(filepath.Dir(journal.Path), "model.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return ModelController{JournalPath: path}
}

// A Lemonade version activation takes back the starter picture model's
// prepared record; it was prepared under the earlier version.
func TestLemonadeActivationClearsThePreparedPictureModel(t *testing.T) {
	f, _ := newLemonadeUpdate(t)
	starter := configuredStarter(t, f.journal)
	// An offered choice's record (model-<id>.json) is taken back too.
	plan := PictureModelChoices()[0]
	choiceState := ModelState{PlanID: plan.ID, Approved: true, ProviderConfigured: true, Effects: map[string]ModelEffect{}}
	for _, a := range plan.Assets {
		choiceState.Effects[a.ID] = ModelEffect{Phase: Verified}
	}
	raw, _ := json.Marshal(choiceState)
	choice := ModelController{Plan: &plan, JournalPath: filepath.Join(filepath.Dir(starter.JournalPath), "model-"+strings.TrimPrefix(plan.Model, "user.")+".json")}
	if err := os.WriteFile(choice.JournalPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	r := f.runner()
	for _, phase := range []Phase{Requested, Verified, Staged, Checked} {
		f.advance(r, phase)
	}
	if state, err := starter.load(f.ctx); err != nil || !state.ProviderConfigured {
		t.Fatalf("before activation = %+v, %v", state, err)
	}
	f.advance(r, Installed)
	if state, err := starter.load(f.ctx); err != nil || state.ProviderConfigured {
		t.Fatalf("after activation = %+v, %v", state, err)
	}
	if state, err := choice.load(f.ctx); err != nil || state.ProviderConfigured {
		t.Fatalf("choice after activation = %+v, %v", state, err)
	}
}

// After the record was taken back, Configure asks the provider: a provider
// that still verifies is prepared again with no staging; one that does not
// stays unprepared.
func TestConfigureAdoptsAVerifiedProviderWhenTheDownloadsMoved(t *testing.T) {
	ctx := context.Background()
	journal := Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	starter := configuredStarter(t, journal)
	if err := starter.clearConfigured(ctx); err != nil {
		t.Fatal(err)
	}
	staged, verified := 0, false
	starter.ProviderRoot = t.TempDir()
	// The provider's configure refuses inputs that are gone, as
	// ConfigureLemonade does.
	starter.Stage = func(_ context.Context, _ string, files BF16StageFiles, _ func(int64, int64) error, _ bool) error {
		staged++
		if _, err := os.Lstat(files.Main); err != nil {
			return errors.New("Lemonade setup input is missing or is not a regular file")
		}
		return nil
	}
	starter.Probe = func(context.Context, string) (bool, error) { return verified, nil }
	if status, err := starter.Configure(ctx, false, nil); err == nil || status.ProviderConfigured {
		t.Fatalf("unverified provider = %+v, %v", status, err)
	}
	staged, verified = 0, true
	if status, err := starter.Configure(ctx, false, nil); err != nil || !status.ProviderConfigured || staged != 0 {
		t.Fatalf("verified provider = %+v, %v staged %d", status, err, staged)
	}
}
