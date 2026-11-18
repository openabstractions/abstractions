package setup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// pictureFake is the picture part's owners: the real model controller over
// its own fake transfer, and recorded engine, configure and grant effects.
type pictureFake struct {
	// app is the registered name the part is offered for.
	app         string
	transfer    *stepTransfer
	plan        ModelPlan
	offer       StepPictureOffer
	engineCalls int
	// engineSteps is how many Engine calls install the engine.
	engineSteps int
	configures  int
	grants      []StepPictureOffer
	grantErr    error
}

func newPictureFake(w *stepWorld) *pictureFake {
	payload := []byte("picture model weights fixture")
	sum := sha256.Sum256(payload)
	plan := ModelPlan{Model: "fixture-picture", Repository: "fixture/picture", Revision: "p1", Licenses: []string{"Apache-2.0", "MIT"},
		DownloadBytes: 16_000_000_000, PeakBytes: 21_000_000_000,
		Assets: []ModelAsset{{ID: "main", Path: "picture.safetensors", License: "Apache-2.0", LicenseURL: "https://www.apache.org/licenses/LICENSE-2.0",
			Download: PinnedDownload{URL: "https://huggingface.co/fixture/picture/resolve/p1/picture.safetensors", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(payload)), Label: "Picture weights"}}}}
	encoded, _ := json.Marshal(plan)
	id := sha256.Sum256(encoded)
	plan.ID = hex.EncodeToString(id[:])
	return &pictureFake{app: "comfyui", transfer: &stepTransfer{payload: payload, state: "running", operation: "op-picture"}, plan: plan, engineSteps: 2,
		offer: StepPictureOffer{Title: "FLUX.2 Klein 4B", PlanID: "engine-plan-1", Packages: []AppID{Lemonade}, PackageBytes: 1_000_000_000, PackagePeakBytes: 2_000_000_000,
			PackageLicenses: []string{"Apache-2.0"}, Rules: []ApprovedRule{{Action: "abstraction.inference/complete", Resource: "host:lemonade-image"}},
			Permissions: []string{"Make pictures on this computer."}, Program: `C:\Programs\ComfyUI\OpenAbstractions ComfyUI.exe`}}
}

func (f *pictureFake) controller(w *stepWorld) ModelController {
	plan := f.plan
	dir := filepath.Join(w.root, "models", "picture")
	return ModelController{Plan: &plan, JournalPath: filepath.Join(w.root, "picture-model.json"), StageDir: dir, ProviderRoot: dir,
		LocalResults: true, Transfer: f.transfer, FreeBytes: func(string) (uint64, error) { return 1 << 40, nil }}
}

// with binds the picture part to s; the step's wait completes both
// downloads unless w.waitErr says otherwise.
func (f *pictureFake) with(w *stepWorld, s SetupStep) SetupStep {
	s.Owners.Pictures = &StepPictures{Model: f.controller(w),
		Offer: func(_ context.Context, app StepApp) (StepPictureOffer, error) {
			if app.Name != f.app {
				return StepPictureOffer{}, errors.New("offered for another app")
			}
			offer := f.offer
			offer.EngineReady = f.engineCalls >= f.engineSteps
			offer.Configured = f.configures > 0
			return offer, nil
		},
		Engine: func(_ context.Context, offer StepPictureOffer) (bool, error) {
			if offer.PlanID != f.offer.PlanID {
				return false, errors.New("another engine plan")
			}
			f.engineCalls++
			return f.engineCalls >= f.engineSteps, nil
		},
		Configure: func(context.Context) error { f.configures++; return nil },
		Grant: func(_ context.Context, offer StepPictureOffer) error {
			if f.grantErr != nil {
				return f.grantErr
			}
			f.grants = append(f.grants, offer)
			return nil
		},
	}
	s.Owners.Wait = func(ctx context.Context) error {
		w.waits++
		if w.waitErr != nil {
			return w.waitErr(w.waits)
		}
		w.transfer.state, f.transfer.state = "complete", "complete"
		return nil
	}
	return s
}

func bothApps() []StepApp {
	return []StepApp{{Name: "opencode", Title: "OpenCode", Use: UseCode, Unconfirmed: true}, {Name: "comfyui", Title: "ComfyUI", Use: UseImage, Engine: Lemonade, Unconfirmed: true, Program: `C:\Programs\ComfyUI\OpenAbstractions ComfyUI.exe`}}
}

// Acceptance (slice 2): one question names what OpenCode and ComfyUI get,
// the download and the free space for both; licences and ComfyUI's
// permissions sit behind Show details. The answer then runs every owner.
func TestStepPicturesJoinTheOneQuestion(t *testing.T) {
	w := newStepWorld(t)
	w.apps = bothApps()
	f := newPictureFake(w)
	outcome, err := f.with(w, w.step()).Run(context.Background())
	if err != nil || outcome != stepSetUp {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	if w.asked != 1 {
		t.Fatalf("asked %d questions", w.asked)
	}
	q := w.questions[0]
	// 1 GB (chat) + 16 GB (picture model) + 1 GB (engine package) to download.
	if q.Title != "Set up OpenCode and ComfyUI?" || q.Instruction != "About 16 GB to download." ||
		!strings.HasPrefix(q.Details, "OpenCode gets Qwen3.6 35B on this computer. ComfyUI gets pictures on this computer. 22 GB free needed. ") {
		t.Fatalf("question = %+v", q)
	}
	for _, want := range []string{"Licence: Apache-2.0", "Pictures: FLUX.2 Klein 4B", "Licences: Apache-2.0, MIT.", "ComfyUI gets these permissions: Make pictures on this computer."} {
		if !strings.Contains(q.Details, want) {
			t.Fatalf("details lack %q: %s", want, q.Details)
		}
	}
	if strings.Contains(q.Instruction+q.Details, "ComfyUI.exe") || strings.Contains(q.Instruction+q.Details, f.plan.Assets[0].Download.SHA256) {
		t.Fatalf("question shows a path or digest: %+v", q)
	}
	if strings.Join(w.confirmed, ",") != "opencode,comfyui" {
		t.Fatalf("confirmed %v", w.confirmed)
	}
	if f.engineCalls != 2 || f.transfer.reserves != 1 || f.configures != 1 || len(f.grants) != 1 || f.grants[0].Program != f.offer.Program {
		t.Fatalf("engine %d reserves %d configures %d grants %+v", f.engineCalls, f.transfer.reserves, f.configures, f.grants)
	}
	if w.transfer.reserves != 1 || w.hosted != 1 || w.selects != 1 {
		t.Fatalf("chat part: reserves %d hosted %d selects %d", w.transfer.reserves, w.hosted, w.selects)
	}
	starts := []string{}
	for _, n := range w.notices {
		if strings.HasPrefix(n.Body, "Downloading") {
			starts = append(starts, n.Title+": "+n.Body+" @"+n.Operation)
		}
	}
	if len(starts) != 2 || starts[1] != "Setting up ComfyUI: Downloading FLUX.2 Klein 4B, about 15 GB. Activity shows its progress. @op-picture" {
		t.Fatalf("start notices = %v", starts)
	}
	if n := w.last(); n.Title != "OpenCode and ComfyUI are ready" || n.Body != "OpenCode uses Qwen3.6 35B and ComfyUI makes pictures on this computer." {
		t.Fatalf("last notice = %+v", n)
	}
	state, _ := w.step().Load(context.Background())
	if !state.Ready() || !state.Pictures || !state.Granted || state.PicturePlanID != f.plan.ID || state.PicturePackages != "engine-plan-1" ||
		state.PictureRules != RulesDigest(f.offer.Rules) || state.PictureProgram != f.offer.Program || strings.Join(state.Apps, ",") != "opencode,comfyui" {
		t.Fatalf("journal = %+v", state)
	}

	// A rerun with everything in place asks nothing and writes nothing.
	engine, configures := f.engineCalls, f.configures
	if outcome, err := f.with(w, w.step()).Run(context.Background()); err != nil || outcome != stepAlreadySetUp || w.asked != 1 {
		t.Fatalf("rerun = %q, %v asked %d", outcome, err, w.asked)
	}
	if f.engineCalls != engine || f.configures != configures || len(f.grants) != 1 || f.transfer.reserves != 1 {
		t.Fatalf("rerun caused effects: engine %d configures %d grants %d reserves %d", f.engineCalls, f.configures, len(f.grants), f.transfer.reserves)
	}
	if n := w.last(); n.Title != "Already set up" || n.Body != "OpenCode uses Qwen3.6 35B and ComfyUI makes pictures on this computer." {
		t.Fatalf("rerun notice = %+v", n)
	}
}

// Not now writes only the setup state: no engine, download, rule or
// confirmation.
func TestStepPicturesNotNowWritesNothing(t *testing.T) {
	w := newStepWorld(t)
	w.apps, w.answer = bothApps(), false
	f := newPictureFake(w)
	if outcome, err := f.with(w, w.step()).Run(context.Background()); err != nil || outcome != stepAnsweredNotNow {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	if f.engineCalls != 0 || f.transfer.reserves != 0 || f.configures != 0 || len(f.grants) != 0 || len(w.confirmed) != 0 {
		t.Fatalf("Not now caused effects: engine %d reserves %d configures %d grants %d confirmed %v", f.engineCalls, f.transfer.reserves, f.configures, len(f.grants), w.confirmed)
	}
}

// ComfyUI without OpenCode is the picture part alone; Fit is not read.
func TestStepPicturesAlone(t *testing.T) {
	w := newStepWorld(t)
	w.apps = bothApps()[1:]
	f := newPictureFake(w)
	s := f.with(w, w.step())
	s.Owners.Fit = func(context.Context, string) (StepFit, error) {
		return StepFit{}, errors.New("Fit was read for pictures")
	}
	outcome, err := s.Run(context.Background())
	if err != nil || outcome != stepSetUp {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	q := w.questions[0]
	if q.Title != "Set up ComfyUI?" || q.Instruction != "About 16 GB to download." || !strings.HasPrefix(q.Details, "ComfyUI gets pictures on this computer. 22 GB free needed. ") {
		t.Fatalf("question = %+v", q)
	}
	if w.transfer.reserves != 0 || w.selects != 0 || w.hosted != 0 {
		t.Fatalf("chat effects for pictures alone: reserves %d selects %d hosted %d", w.transfer.reserves, w.selects, w.hosted)
	}
	if n := w.last(); n.Title != "ComfyUI is ready" || n.Body != "ComfyUI makes pictures on this computer." {
		t.Fatalf("last notice = %+v", n)
	}
	if outcome, err := f.with(w, w.step()).Run(context.Background()); err != nil || outcome != stepAlreadySetUp {
		t.Fatalf("rerun = %q, %v", outcome, err)
	}
}

// A picture part this Setup cannot prepare here leaves the question to chat
// and ComfyUI unconfirmed and untouched.
func TestStepPicturesNotOfferedLeaveChatAlone(t *testing.T) {
	w := newStepWorld(t)
	w.apps = bothApps()
	f := newPictureFake(w)
	f.offer.Reason = "the picture engine has no reviewed package for this computer"
	if outcome, err := f.with(w, w.step()).Run(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	if q := w.questions[0]; q.Title != "Set up OpenCode?" || q.Instruction != "About 1 GB to download." || !strings.HasPrefix(q.Details, "OpenCode gets Qwen3.6 35B on this computer. 1 GB free needed. ") {
		t.Fatalf("question = %+v", q)
	}
	if strings.Join(w.confirmed, ",") != "opencode" || f.engineCalls != 0 || len(f.grants) != 0 {
		t.Fatalf("confirmed %v engine %d grants %d", w.confirmed, f.engineCalls, len(f.grants))
	}
	state, _ := w.step().Load(context.Background())
	if state.Pictures || strings.Join(state.Apps, ",") != "opencode" {
		t.Fatalf("journal = %+v", state)
	}
}

// A chat-only answer from slice 1 meets a newly offered picture part: the
// step asks once for the new work, then completes; the chat model is not
// downloaded again.
func TestStepPicturesAfterAChatOnlyAnswerAskOnce(t *testing.T) {
	w := newStepWorld(t)
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("chat run = %q, %v", outcome, err)
	}
	w.apps = bothApps()
	w.apps[0].Unconfirmed = false
	f := newPictureFake(w)
	if outcome, err := f.with(w, w.step()).Run(context.Background()); err != nil || outcome != stepSetUp || w.asked != 2 {
		t.Fatalf("pictures run = %q, %v asked %d", outcome, err, w.asked)
	}
	if q := w.questions[1]; q.Title != "Set up OpenCode and ComfyUI?" || q.Instruction != "About 16 GB to download." {
		t.Fatalf("second question = %+v", q)
	}
	if w.transfer.reserves != 1 || len(f.grants) != 1 || strings.Join(w.confirmed, ",") != "comfyui" {
		t.Fatalf("chat reserves %d grants %d confirmed %v", w.transfer.reserves, len(f.grants), w.confirmed)
	}
}

// The journal is no authority: a changed rule set or program asks again.
func TestStepPicturesChangedRulesAskAgain(t *testing.T) {
	w := newStepWorld(t)
	w.apps = bothApps()
	f := newPictureFake(w)
	if _, err := f.with(w, w.step()).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.offer.Rules = append(f.offer.Rules, ApprovedRule{Action: "abstraction.content/write", Resource: "output:image"})
	f.offer.Permissions = append(f.offer.Permissions, "Save the pictures it makes.")
	w.answer = false
	if outcome, err := f.with(w, w.step()).Run(context.Background()); err != nil || outcome != stepAnsweredNotNow || w.asked != 2 {
		t.Fatalf("changed rules = %q, %v asked %d", outcome, err, w.asked)
	}
	if len(f.grants) != 1 {
		t.Fatalf("a changed rule set was written without an answer: %d grants", len(f.grants))
	}
}

// Coordinator, 2026-11-01: a picture app's request hosts only what it asks
// for. The coding model the person removed from OA stays removed, also
// when pictures need a new answer; that answer keeps the coding model's, so
// a later request for a coding model hosts it again without a question.
func TestStepPictureRequestLeavesARemovedCodingModel(t *testing.T) {
	w := newStepWorld(t)
	w.apps = bothApps()
	f := newPictureFake(w)
	if outcome, err := f.with(w, w.step()).Run(context.Background()); err != nil || outcome != stepSetUp || w.hosted != 1 {
		t.Fatalf("first run = %q, %v hosted %d", outcome, err, w.hosted)
	}
	w.hostedNow = false
	pictures := func() SetupStep {
		s := f.with(w, w.step())
		s.Requester = &StepRequester{App: "ComfyUI", Use: UseImage}
		return s
	}
	if outcome, err := pictures().Run(context.Background()); err != nil || outcome != stepAlreadySetUp || w.hosted != 1 || w.hostedNow || w.asked != 1 {
		t.Fatalf("picture request = %q, %v hosted %d hosted now %v asked %d", outcome, err, w.hosted, w.hostedNow, w.asked)
	}
	if n := w.last(); n.Title != "Already set up" || n.Body != "ComfyUI makes pictures on this computer." {
		t.Fatalf("notice = %+v", n)
	}

	// Changed picture rules ask about pictures alone; the coding model stays
	// removed through the work and its answer stays in the journal.
	f.offer.Rules = append(f.offer.Rules, ApprovedRule{Action: "abstraction.content/write", Resource: "output:image"})
	if outcome, err := pictures().Run(context.Background()); err != nil || outcome != stepSetUp || w.asked != 2 || w.hosted != 1 || w.hostedNow {
		t.Fatalf("changed rules = %q, %v asked %d hosted %d", outcome, err, w.asked, w.hosted)
	}
	if q := w.questions[1]; q.Title != "Set up pictures for ComfyUI?" {
		t.Fatalf("question = %+v", q)
	}
	if n := w.last(); n.Title != "ComfyUI is ready" || n.Body != "ComfyUI makes pictures on this computer." {
		t.Fatalf("notice = %+v", n)
	}
	state, _ := w.step().Load(context.Background())
	if !state.Ready() || state.Family != w.pick().Family || state.ModelPlanID != w.plan.ID || !state.RemovedFromOA || len(f.grants) != 2 {
		t.Fatalf("journal = %+v grants %d", state, len(f.grants))
	}
	code := f.with(w, w.step())
	code.Requester = &StepRequester{App: "OpenCode", Use: UseCode}
	if outcome, err := code.Run(context.Background()); err != nil || outcome != stepSetUp || w.asked != 2 || w.hosted != 2 || !w.hostedNow {
		t.Fatalf("code request = %q, %v asked %d hosted %d", outcome, err, w.asked, w.hosted)
	}
	if state, _ := w.step().Load(context.Background()); state.RemovedFromOA {
		t.Fatalf("hosting again kept the removal record: %+v", state)
	}
}

// Review of 1a98de54a, B1: the chat model is hosted before the picture
// download, while the step is still approved. Remove from OA in that window
// is recorded, and neither the sign-in resume nor the person's Set up after
// an update's Finish hosts the model again; both finish pictures alone. The
// update's Finish itself only stops the work (owner, 2026-11-09).
func TestStepRemovalDuringPictureWorkHolds(t *testing.T) {
	for _, run := range []string{"sign-in resume", "update Finish"} {
		t.Run(run, func(t *testing.T) {
			w := newStepWorld(t)
			w.apps = bothApps()
			f := newPictureFake(w)
			session, signOut := context.WithCancel(context.Background())
			w.waitErr = func(int) error {
				if f.transfer.reserves > 0 {
					signOut()
					return session.Err()
				}
				w.transfer.state = "complete"
				return nil
			}
			if _, err := f.with(w, w.step()).Run(session); !errors.Is(err, context.Canceled) {
				t.Fatalf("first run = %v", err)
			}
			if state, _ := w.step().Load(context.Background()); !state.Approved() || !state.Hosted || w.hosted != 1 {
				t.Fatalf("interrupted journal = %+v hosted %d", state, w.hosted)
			}
			w.hostedNow, w.waitErr = false, nil
			if err := w.step().RecordRemoval(context.Background()); err != nil {
				t.Fatal(err)
			}
			if state, _ := w.step().Load(context.Background()); !state.RemovedFromOA {
				t.Fatalf("removal during picture work was not recorded: %+v", state)
			}
			if run == "update Finish" {
				stop := f.with(w, w.step())
				stop.Finish, stop.Update = true, true
				if outcome, err := stop.Run(context.Background()); err != nil || outcome != stepStoppedByUpdate || w.hosted != 1 || w.asked != 1 {
					t.Fatalf("update Finish = %q, %v hosted %d asked %d", outcome, err, w.hosted, w.asked)
				}
				if n := w.last(); n.Title != "Couldn't finish setup" || n.Body != "Setup paused while OpenAbstractions updated. Open Models and choose Set up." {
					t.Fatalf("stop notice = %+v", n)
				}
				if state, _ := w.step().Load(context.Background()); !state.Approved() || state.Stopped == nil || state.Stopped.Stage != stageUpdate {
					t.Fatalf("stopped journal = %+v", state)
				}
			} else if pending, err := w.step().Pending(context.Background()); err != nil || !pending {
				t.Fatalf("same-build sign-in pending = %v, %v", pending, err)
			}
			s := f.with(w, w.step())
			if outcome, err := s.Run(context.Background()); err != nil || outcome != stepSetUp || w.hosted != 1 || w.hostedNow || w.asked != 1 {
				t.Fatalf("%s = %q, %v hosted %d asked %d", run, outcome, err, w.hosted, w.asked)
			}
			if n := w.last(); n.Title != "ComfyUI is ready" || n.Body != "ComfyUI makes pictures on this computer." {
				t.Fatalf("notice = %+v", n)
			}
			finish := f.with(w, w.step())
			finish.Finish = true
			if outcome, err := finish.Run(context.Background()); err != nil || !outcome.NothingToSetUp() || w.hosted != 1 || w.hostedNow {
				t.Fatalf("update Finish afterwards = %q, %v hosted %d", outcome, err, w.hosted)
			}
		})
	}
}

// Signing out during the picture download resumes the same job without a
// second question or start notice.
func TestStepPicturesResumeTheSameJob(t *testing.T) {
	w := newStepWorld(t)
	w.apps = bothApps()
	f := newPictureFake(w)
	// Sign-out ends the run's context while the picture job runs.
	session, signOut := context.WithCancel(context.Background())
	w.waitErr = func(n int) error {
		if f.transfer.reserves > 0 {
			signOut()
			return session.Err()
		}
		w.transfer.state = "complete"
		return nil
	}
	if _, err := f.with(w, w.step()).Run(session); !errors.Is(err, context.Canceled) {
		t.Fatalf("first run = %v", err)
	}
	if pending, _ := w.step().Pending(context.Background()); !pending || f.transfer.reserves != 1 {
		t.Fatalf("pending %v reserves %d", pending, f.transfer.reserves)
	}
	job := f.transfer.submits[0]
	w.waitErr = nil
	if outcome, err := f.with(w, w.step()).Run(context.Background()); err != nil || outcome != stepSetUp || w.asked != 1 {
		t.Fatalf("resumed = %q, %v asked %d", outcome, err, w.asked)
	}
	for _, key := range f.transfer.submits {
		if key != job {
			t.Fatalf("resume submitted %q, first run %q", key, job)
		}
	}
	starts := 0
	for _, n := range w.notices {
		if strings.HasPrefix(n.Body, "Downloading FLUX") {
			starts++
		}
	}
	if f.transfer.reserves != 1 || starts != 1 {
		t.Fatalf("reserves %d picture start notices %d", f.transfer.reserves, starts)
	}
}

// A picture failure is one notice that names ComfyUI, with no raw reason.
func TestStepPicturesFailureNamesComfyUI(t *testing.T) {
	w := newStepWorld(t)
	w.apps = bothApps()
	f := newPictureFake(w)
	f.grantErr = errors.New("rights: conflict at revision 7")
	// Review 25 A3: a working OpenCode stays Ready; pictures keep their own
	// state and sentence, and the next run tries pictures again.
	if outcome, err := f.with(w, w.step()).RunAndReport(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("a picture failure hid a working OpenCode: %q, %v", outcome, err)
	}
	if n := w.last(); n.Title != "OpenCode is ready; couldn't finish setting up ComfyUI" || n.Body != "OpenCode uses Qwen3.6 35B on this computer. Pictures could not be set up. Open Models and choose Set up." {
		t.Fatalf("failure notice = %+v", n)
	}
	state, _ := w.step().Load(context.Background())
	if !state.Ready() || !state.Selected || !state.PicturesStopped || state.Granted {
		t.Fatalf("journal = %+v", state)
	}
	reserves := w.transfer.reserves
	f.grantErr = nil
	if outcome, err := f.with(w, w.step()).RunAndReport(context.Background()); err != nil || outcome != stepSetUp || w.asked != 1 {
		t.Fatalf("retry = %q, %v asked %d", outcome, err, w.asked)
	}
	if state, _ := w.step().Load(context.Background()); state.PicturesStopped || !state.Granted || w.transfer.reserves != reserves {
		t.Fatalf("after retry = %+v reserves %d", state, w.transfer.reserves)
	}
	if n := w.last(); n.Title != "OpenCode and ComfyUI are ready" {
		t.Fatalf("retry notice = %+v", n)
	}
	// Pictures alone keep the old shape: one notice that names ComfyUI.
	w1 := newStepWorld(t)
	w1.apps = bothApps()[1:]
	f1 := newPictureFake(w1)
	f1.grantErr = errors.New("rights: conflict at revision 7")
	if _, err := f1.with(w1, w1.step()).RunAndReport(context.Background()); err == nil {
		t.Fatal("a refused grant reported success")
	}
	if n := w1.last(); n.Title != "Couldn't finish setting up ComfyUI" || n.Body != "Pictures could not be set up. Open Models and choose Set up." {
		t.Fatalf("pictures-alone failure notice = %+v", n)
	}
	w2 := newStepWorld(t)
	w2.apps = bothApps()
	f2 := newPictureFake(w2)
	s := f2.with(w2, w2.step())
	s.Owners.Wait = func(context.Context) error {
		w2.transfer.state, f2.transfer.state = "complete", "failed"
		return nil
	}
	if outcome, err := s.RunAndReport(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("a failed picture download hid a working OpenCode: %q, %v", outcome, err)
	}
	if n := w2.last(); n.Title != "OpenCode is ready; couldn't finish setting up ComfyUI" || n.Body != "OpenCode uses Qwen3.6 35B on this computer. The picture model download stopped before it finished. Open Models and choose Set up." {
		t.Fatalf("download failure notice = %+v", n)
	}
	// Trying again resubmits the failed download, with no new question.
	retry := f2.with(w2, w2.step())
	retry.Owners.Wait = func(context.Context) error { f2.transfer.state = "complete"; return nil }
	if outcome, err := retry.RunAndReport(context.Background()); err != nil || outcome != stepSetUp || w2.asked != 1 {
		t.Fatalf("download retry = %q, %v asked %d", outcome, err, w2.asked)
	}
	if state, _ := w2.step().Load(context.Background()); state.PicturesStopped || !state.Granted || f2.transfer.reserves != 1 || len(f2.transfer.submits) < 2 {
		t.Fatalf("after retry = %+v reserves %d submits %d", state, f2.transfer.reserves, len(f2.transfer.submits))
	}
}

// engineWorld is Setup's plan journal over the fixture catalog with a real
// Lemonade engine archive and a fake public transfer.
func engineWorld(t *testing.T) (Runner, *fakeTransfer) {
	t.Helper()
	standInHome(t)
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.Create("lemonade/lemond.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("fixed executable bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := archive.Bytes()
	sum := sha256.Sum256(data)
	c := fixtureCatalog()
	for i := range c.Components {
		if c.Components[i].ID == "lemonade-engine" {
			c.Components[i].Artifact.SHA256 = hex.EncodeToString(sum[:])
			c.Components[i].Artifact.Bytes = int64(len(data))
			c.Components[i].Artifact.PeakBytes = 1024
		}
	}
	f := &fakeTransfer{state: "running", bytes: data}
	r := NewRunner(Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}, f)
	r.healthCheck = func(context.Context, string, Artifact, string) error { return nil }
	return r, f
}

// The engine owner: the offer names the package plan and what it still
// downloads; StepEngine records that plan, adds the feature once and
// advances the package to installed one transition per call.
func TestStepEngineInstallsThroughTheRunner(t *testing.T) {
	ctx := context.Background()
	r, f := engineWorld(t)
	feature := FeatureStatus{Available: true}
	offer, err := r.StepEngineOffer(ctx, feature, Lemonade)
	if err != nil || offer.Reason != "" || offer.EngineReady || offer.PackageBytes == 0 || len(offer.Packages) != 1 || offer.Packages[0] != Lemonade {
		t.Fatalf("offer = %+v, %v", offer, err)
	}
	adds := 0
	add := func(context.Context) (FeatureStatus, error) {
		adds++
		feature.Installed = true
		return feature, nil
	}
	read := func() (FeatureStatus, error) { return feature, nil }
	done := false
	for step := 0; step < 12 && !done; step++ {
		if done, err = r.StepEngine(ctx, offer, read, add); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		f.state = "complete"
	}
	if !done || adds != 1 || f.reserve != 1 {
		t.Fatalf("done %v feature adds %d reserves %d", done, adds, f.reserve)
	}
	state, _ := r.Journal.Load(ctx)
	if state.Plan.ID != offer.PlanID || state.Effects["lemonade-engine"].Phase != Installed {
		t.Fatalf("journal = %+v", state)
	}
	again, err := r.StepEngineOffer(ctx, feature, Lemonade)
	if err != nil || !again.EngineReady || again.PackageBytes != 0 || again.PlanID != offer.PlanID {
		t.Fatalf("offer after install = %+v, %v", again, err)
	}
	// A package plan other than the answered one is refused.
	changed := offer
	changed.PlanID = "another-plan"
	changed.Packages = []AppID{Lemonade, TheNoise}
	if _, err := r.StepEngine(ctx, changed, read, add); err == nil {
		t.Fatal("a changed engine plan was accepted")
	}
	if after, _ := r.Journal.Load(ctx); after.Plan.ID != offer.PlanID {
		t.Fatalf("a refused plan replaced the answered one: %s", after.Plan.ID)
	}
}

// An earlier One Install plan's choices stay in the engine plan, so its
// installed packages keep their records.
func TestStepEngineKeepsTheEarlierChoices(t *testing.T) {
	ctx := context.Background()
	r, _ := engineWorld(t)
	if _, err := r.Start(ctx, []AppID{fixtureApp}); err != nil {
		t.Fatal(err)
	}
	offer, err := r.StepEngineOffer(ctx, FeatureStatus{Available: true}, Lemonade)
	if err != nil || offer.Reason != "" {
		t.Fatalf("offer = %+v, %v", offer, err)
	}
	if len(offer.Packages) != 2 || !slices.Contains(offer.Packages, fixtureApp) || !slices.Contains(offer.Packages, Lemonade) {
		t.Fatalf("packages = %v", offer.Packages)
	}
}

// The release catalog has no reviewed engine package for 0.8.0 yet, and a
// feature that cannot be added: either way the part is not offered.
func TestStepEngineOfferNamesWhyNot(t *testing.T) {
	ctx := context.Background()
	r := NewRunner(Journal{Path: filepath.Join(t.TempDir(), "plan.json")}, &fakeTransfer{})
	c := NewCatalog()
	r.Journal.catalogOverride = &c
	if plan, err := c.Plan([]AppID{Lemonade}); err == nil && !plan.Preparable {
		offer, err := r.StepEngineOffer(ctx, FeatureStatus{Available: true}, Lemonade)
		if err != nil || offer.Reason == "" {
			t.Fatalf("unpinned engine offer = %+v, %v", offer, err)
		}
	}
	r, _ = engineWorld(t)
	offer, err := r.StepEngineOffer(ctx, FeatureStatus{Reason: "Windows Installer cannot add the feature"}, Lemonade)
	if err != nil || offer.Reason != "Windows Installer cannot add the feature" {
		t.Fatalf("feature offer = %+v, %v", offer, err)
	}
}

// An app's RequestSetup reaches the person as the same one question, headed
// by the app that asked; the answer stays the person's.
func TestStepNamesTheRequestingApp(t *testing.T) {
	w := newStepWorld(t)
	s := w.step()
	s.Requester = &StepRequester{App: "OpenCode", Use: "code"}
	w.answer = false
	if outcome, err := s.Run(context.Background()); err != nil || outcome != stepAnsweredNotNow {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	if q := w.questions[0]; q.Title != "Set up a coding model for OpenCode?" || q.Instruction != "About 1 GB to download." {
		t.Fatalf("question = %+v", q)
	}
	if w.transfer.reserves != 0 || w.selects != 0 {
		t.Fatal("a request caused effects before the person's answer")
	}
	if got := (StepRequester{App: "ComfyUI", Use: "image"}).sentence(); got != "Set up pictures for ComfyUI?" {
		t.Fatalf("image sentence = %q", got)
	}
}

// Review B1 (2026-11-09): a Ready step whose picture model no longer reads
// configured (a stale preparing flag) is still answered work. The
// installer's Finish after an update leaves it stopped: no configure, no
// engine step, no question, one stop notice, and no build that sign-in
// would resume it for.
func TestStepFinishAfterUpdateLeavesReadyWorkStopped(t *testing.T) {
	w := newStepWorld(t)
	w.apps = bothApps()
	f := newPictureFake(w)
	if outcome, err := f.with(w, w.step()).Run(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("first run = %q, %v", outcome, err)
	}
	f.configures = 0 // the picture model now reads not configured
	engine, hosted := f.engineCalls, w.hosted
	finish := f.with(w, w.step())
	finish.Finish, finish.Update = true, true
	outcome, err := finish.Run(context.Background())
	if err != nil || outcome != stepStoppedByUpdate {
		t.Fatalf("update Finish = %q, %v", outcome, err)
	}
	if f.configures != 0 || f.engineCalls != engine || w.hosted != hosted || w.asked != 1 {
		t.Fatalf("update Finish ran work: configures %d engine %d→%d hosted %d→%d asked %d", f.configures, engine, f.engineCalls, hosted, w.hosted, w.asked)
	}
	if n := w.last(); n.Title != "Couldn't finish setup" || n.Body != "Setup paused while OpenAbstractions updated. Open Models and choose Set up." {
		t.Fatalf("stop notice = %+v", n)
	}
	state, _ := w.step().Load(context.Background())
	if !state.Ready() || state.Stopped == nil || state.Stopped.Stage != stageUpdate || state.Build != "" {
		t.Fatalf("stopped journal = %+v", state)
	}
	// The person's Set up afterwards prepares the picture model again.
	if outcome, err := f.with(w, w.step()).Run(context.Background()); err != nil || outcome == stepStoppedByUpdate || f.configures != 1 {
		t.Fatalf("Set up = %q, %v configures %d", outcome, err, f.configures)
	}
}
