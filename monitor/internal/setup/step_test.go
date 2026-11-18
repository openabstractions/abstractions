package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/client"
	jobapi "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

// stepTransfer stands in for the public download job service. It counts
// reservations (a new public job) and checks every submission names the
// job the step's journal saved first.
type stepTransfer struct {
	payload   []byte
	state     string
	reserves  int
	operation string
	submits   []string
	reserved  string
}

func (f *stepTransfer) Reserve(_ context.Context, _ PinnedDownload, key string) (WorkIdentity, error) {
	f.reserves++
	f.reserved = key
	b := client.JobsBinding{Endpoint: "pipe", LogicalOwner: "owner"}
	return WorkIdentity{Key: key, HistoryEpoch: "epoch", Binding: b, OperationsBinding: b}, nil
}
func (f *stepTransfer) Submit(_ context.Context, _ PinnedDownload, work WorkIdentity) error {
	if work.Key != f.reserved {
		return errors.New("submitted a job the journal did not save")
	}
	f.submits = append(f.submits, work.Key)
	return nil
}
func (f *stepTransfer) Observe(context.Context, WorkIdentity) (TransferObservation, error) {
	return TransferObservation{State: f.state, Done: int64(len(f.payload)) / 2, Total: int64(len(f.payload)), Operation: f.operation}, nil
}
func (f *stepTransfer) Copy(_ context.Context, _ WorkIdentity, dst io.Writer) (int64, error) {
	n, err := dst.Write(f.payload)
	return int64(n), err
}
func (f *stepTransfer) Link(_ context.Context, _ WorkIdentity, destination string) (jobapi.LinkOutcome, error) {
	store := destination + ".job-result"
	if err := os.WriteFile(store, f.payload, 0o600); err != nil {
		return 0, err
	}
	return jobapi.LinkOutcomeLinked, os.Link(store, destination)
}

// stepWorld is one account: Setup's journals in a temporary folder, the
// real coding-model controller over a fake transfer, and recorded effects.
type stepWorld struct {
	root       string
	transfer   *stepTransfer
	plan       ModelPlan
	answer     bool
	asked      int
	questions  []StepQuestion
	notices    []StepNotice
	confirmed  []string
	hosted     int
	hostedNow  bool
	hosting    StepHosting // the last hosting record Host or Adopt was given
	selects    int
	selected   bool
	lastFamily string
	apps       []StepApp
	waits      int
	waitErr    func(n int) error
}

func newStepWorld(t *testing.T) *stepWorld {
	t.Helper()
	payload := []byte("coding model weights fixture")
	sum := sha256.Sum256(payload)
	plan := ModelPlan{Model: "fixture-coder", Name: "Recommended coding model", Repository: "fixture/coder", Revision: "r1",
		Source: "https://huggingface.co/fixture/coder", Licenses: []string{"Apache-2.0"}, DownloadBytes: int64(len(payload)), PeakBytes: int64(len(payload)) + 1,
		Assets: []ModelAsset{{ID: "coding", Role: "coding", Path: "coder.gguf", License: "Apache-2.0", LicenseURL: "https://www.apache.org/licenses/LICENSE-2.0",
			Download: PinnedDownload{URL: "https://huggingface.co/fixture/coder/resolve/r1/coder.gguf", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(payload)), Label: "Recommended coding model"}}}}
	encoded, _ := json.Marshal(plan)
	id := sha256.Sum256(encoded)
	plan.ID = hex.EncodeToString(id[:])
	return &stepWorld{root: t.TempDir(), transfer: &stepTransfer{payload: payload, state: "running", operation: "op-first"}, plan: plan, answer: true,
		apps: []StepApp{{Name: "opencode", Title: "OpenCode", Use: UseCode}}}
}

func (w *stepWorld) controller() ModelController {
	plan := w.plan
	dir := filepath.Join(w.root, "models", "coding")
	return ModelController{Plan: &plan, JournalPath: filepath.Join(w.root, "coding-model.json"), StageDir: dir, ProviderRoot: dir,
		LocalResults: true, Transfer: w.transfer, FreeBytes: func(string) (uint64, error) { return 1 << 40, nil }}
}

func (w *stepWorld) pick() StepPick {
	return StepPick{Title: "Qwen3.6 35B", Family: "qwen3.6-35b-a3b", DownloadBytes: w.plan.DownloadBytes, NeedBytes: w.plan.DownloadBytes,
		License: "Apache-2.0", Digest: "sha256:" + w.plan.Assets[0].Download.SHA256, CatalogVersion: "2026-10-31.1", Context: 32768}
}

// step is a fresh process's view: nothing survives between runs except the
// files under root, as after signing out and back in.
func (w *stepWorld) step() SetupStep {
	return SetupStep{JournalPath: filepath.Join(w.root, "step.json"), Owners: StepOwners{
		Fit: func(context.Context, string) (StepFit, error) {
			pick := w.pick()
			return StepFit{Pick: &pick, PoolBytes: 1 << 40}, nil
		},
		Apps: func(context.Context) ([]StepApp, error) { return w.apps, nil },
		Ask: func(_ context.Context, q StepQuestion) (bool, error) {
			w.asked++
			w.questions = append(w.questions, q)
			return w.answer, nil
		},
		Notify: func(_ context.Context, n StepNotice) error { w.notices = append(w.notices, n); return nil },
		Confirm: func(_ context.Context, apps []StepApp) error {
			for _, a := range apps {
				w.confirmed = append(w.confirmed, a.Name)
			}
			return nil
		},
		Model: w.controller(),
		Host:  func(_ context.Context, h StepHosting) error { w.hosted++; w.hostedNow, w.hosting = true, h; return nil },
		Named: func(_ context.Context, state StepState) (bool, error) {
			return !w.hostedNow || w.hosting.Name == state.Family, nil
		},
		Hosting:  func(context.Context, StepState) (bool, error) { return w.hostedNow, nil },
		Selected: func(_ context.Context, family string) (bool, error) { return w.selected && family == w.lastFamily, nil },
		Select: func(_ context.Context, family string) error {
			w.selects++
			w.selected, w.lastFamily = true, family
			return nil
		},
		Wait: func(ctx context.Context) error {
			w.waits++
			if w.waitErr != nil {
				return w.waitErr(w.waits)
			}
			w.transfer.state = "complete"
			return nil
		},
	}}
}

func (w *stepWorld) last() StepNotice {
	if len(w.notices) == 0 {
		return StepNotice{}
	}
	return w.notices[len(w.notices)-1]
}

// Acceptance: exactly one question appears before first use, and the step
// then downloads, hosts and selects without another one.
func TestStepAsksOneQuestionThenSetsUp(t *testing.T) {
	w := newStepWorld(t)
	w.apps = []StepApp{{Name: "opencode", Title: "OpenCode", Use: UseCode, Unconfirmed: true, Revision: "r7"}}
	outcome, err := w.step().Run(context.Background())
	if err != nil || outcome != stepSetUp {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	if w.asked != 1 {
		t.Fatalf("asked %d questions", w.asked)
	}
	q := w.questions[0]
	if q.Title != "Set up OpenCode?" || q.Instruction != "About 1 GB to download." || !strings.HasPrefix(q.Details, "OpenCode gets Qwen3.6 35B on this computer. 1 GB free needed. Licence: Apache-2.0.") {
		t.Fatalf("question = %+v", q)
	}
	if w.transfer.reserves != 1 || w.hosted != 1 || w.selects != 1 || len(w.confirmed) != 1 || w.confirmed[0] != "opencode" {
		t.Fatalf("reserves %d hosted %d selects %d confirmed %v", w.transfer.reserves, w.hosted, w.selects, w.confirmed)
	}
	if n := w.last(); n.Title != "OpenCode is ready" || n.Body != "OpenCode uses Qwen3.6 35B on this computer." {
		t.Fatalf("last notice = %+v", n)
	}
	state, _ := w.step().Load(context.Background())
	if state.Progress != stepReady || !state.Selected || !state.Hosted || state.ModelPlanID != w.plan.ID {
		t.Fatalf("journal = %+v", state)
	}
}

// Acceptance: Not now writes no rules, selection or downloads; only the
// setup state.
func TestStepNotNowWritesOnlyTheSetupState(t *testing.T) {
	w := newStepWorld(t)
	w.answer = false
	w.apps = []StepApp{{Name: "opencode", Title: "OpenCode", Use: UseCode, Unconfirmed: true}}
	outcome, err := w.step().Run(context.Background())
	if err != nil || outcome != stepAnsweredNotNow {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	if w.transfer.reserves != 0 || len(w.transfer.submits) != 0 || w.hosted != 0 || w.selects != 0 || len(w.confirmed) != 0 {
		t.Fatalf("Not now caused effects: reserves %d submits %d hosted %d selects %d confirmed %v", w.transfer.reserves, len(w.transfer.submits), w.hosted, w.selects, w.confirmed)
	}
	entries, err := os.ReadDir(w.root)
	if err != nil {
		t.Fatal(err)
	}
	// The step journal and its CAS lock are all there is.
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "step.json") {
			t.Fatalf("Not now wrote %s", e.Name())
		}
	}
	state, _ := w.step().Load(context.Background())
	if state.Progress != stepDeclined || state.Title != "" || state.ModelPlanID != "" {
		t.Fatalf("journal = %+v", state)
	}
	if pending, _ := w.step().Pending(context.Background()); pending {
		t.Fatal("a declined step is pending at sign-in")
	}
}

// Acceptance: signing out mid-download and back in resumes the same public
// job without asking again. Sign-out is the first run's context ending
// while the job runs; sign-in is a new process reading the same journals.
func TestStepResumesTheSameJobAfterSignOut(t *testing.T) {
	w := newStepWorld(t)
	signedOut := errors.New("signed out")
	w.waitErr = func(int) error { return signedOut }
	if _, err := w.step().Run(context.Background()); !errors.Is(err, signedOut) {
		t.Fatalf("first run = %v", err)
	}
	if pending, err := w.step().Pending(context.Background()); err != nil || !pending {
		t.Fatalf("pending after sign-out = %v, %v", pending, err)
	}
	if w.transfer.reserves != 1 || len(w.transfer.submits) != 1 {
		t.Fatalf("first run reserves %d submits %d", w.transfer.reserves, len(w.transfer.submits))
	}
	job := w.transfer.submits[0]
	if !strings.Contains(w.last().Body, "Downloading Qwen3.6 35B") {
		t.Fatalf("progress notice = %+v", w.last())
	}

	w.waitErr = nil
	outcome, err := w.step().Run(context.Background())
	if err != nil || outcome != stepSetUp {
		t.Fatalf("resumed run = %q, %v", outcome, err)
	}
	if w.asked != 1 {
		t.Fatalf("resume asked again: %d questions", w.asked)
	}
	if w.transfer.reserves != 1 {
		t.Fatalf("resume reserved a new public job: %d reservations", w.transfer.reserves)
	}
	for _, key := range w.transfer.submits {
		if key != job {
			t.Fatalf("resume submitted job %q, first run %q", key, job)
		}
	}
}

// Acceptance: a rerun with everything in place says "Already set up" and
// downloads nothing.
func TestStepRerunSaysAlreadySetUp(t *testing.T) {
	w := newStepWorld(t)
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("first run = %q, %v", outcome, err)
	}
	reserves, submits, hosted, selects := w.transfer.reserves, len(w.transfer.submits), w.hosted, w.selects
	outcome, err := w.step().Run(context.Background())
	if err != nil || outcome != stepAlreadySetUp {
		t.Fatalf("rerun = %q, %v", outcome, err)
	}
	if w.asked != 1 || w.transfer.reserves != reserves || len(w.transfer.submits) != submits || w.hosted != hosted || w.selects != selects {
		t.Fatalf("rerun caused effects: asked %d reserves %d submits %d hosted %d selects %d", w.asked, w.transfer.reserves, len(w.transfer.submits), w.hosted, w.selects)
	}
	if n := w.last(); n.Title != "Already set up" {
		t.Fatalf("rerun notice = %+v", n)
	}
	// A changed chat default is put back by the same answer, with no question.
	w.selected = false
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp || w.asked != 1 || w.selects != selects+1 || w.transfer.reserves != reserves {
		t.Fatalf("repair run = %q, %v asked %d selects %d reserves %d", outcome, err, w.asked, w.selects, w.transfer.reserves)
	}
}

// The Models page's Remove from OA unhosts Setup's own model. ModelHost's
// list decides: the next run does not report it in place, hosts it again
// without a question, and is then in place again.
func TestStepHostsAgainAModelRemovedFromOA(t *testing.T) {
	w := newStepWorld(t)
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp || w.hosted != 1 {
		t.Fatalf("first run = %q, %v hosted %d", outcome, err, w.hosted)
	}
	w.hostedNow = false
	outcome, err := w.step().Run(context.Background())
	if err != nil || outcome == stepAlreadySetUp || w.hosted != 2 || !w.hostedNow || w.asked != 1 {
		t.Fatalf("run after Remove from OA = %q, %v hosted %d asked %d", outcome, err, w.hosted, w.asked)
	}
	if n := w.last(); n.Title == "Already set up" {
		t.Fatalf("an unhosted model was reported in place: %+v", n)
	}
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepAlreadySetUp || w.hosted != 2 {
		t.Fatalf("run after hosting again = %q, %v hosted %d", outcome, err, w.hosted)
	}
}

// Owner, 2026-11-01: OpenCode listed the coding model as its SHA-256. Setup
// stores its download as <sha256>.gguf and hosted it under that file name,
// so the router named the model by the digest. The step hosts the model
// under the catalogue's family, with the catalogue's context; an install an
// earlier Setup hosted under the file name is hosted again under the family
// on the next run, the same copy and no question, and is then in place.
func TestStepHostsTheModelUnderTheCatalogueFamily(t *testing.T) {
	w := newStepWorld(t)
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp || w.hosted != 1 {
		t.Fatalf("first run = %q, %v hosted %d", outcome, err, w.hosted)
	}
	if w.hosting != (StepHosting{Name: "qwen3.6-35b-a3b", Context: 32768}) {
		t.Fatalf("hosted as %+v", w.hosting)
	}
	// An earlier Setup's declaration: the same copy under its file's digest.
	w.hosting.Name = w.plan.Assets[0].Download.SHA256
	reserves := w.transfer.reserves
	outcome, err := w.step().Run(context.Background())
	if err != nil || outcome != stepSetUp || w.asked != 1 || w.hosted != 2 || w.transfer.reserves != reserves {
		t.Fatalf("run over a digest-named host = %q, %v asked %d hosted %d reserves %d (was %d)", outcome, err, w.asked, w.hosted, w.transfer.reserves, reserves)
	}
	if w.hosting.Name != "qwen3.6-35b-a3b" {
		t.Fatalf("hosted again as %+v", w.hosting)
	}
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepAlreadySetUp || w.hosted != 2 {
		t.Fatalf("run after naming = %q, %v hosted %d", outcome, err, w.hosted)
	}
	// An adopted copy keeps its record and is hosted again where it lies.
	w2 := newStepWorld(t)
	copyFound := StepLocal{Store: "hub", Object: "qwen"}
	adopted := []StepLocal{}
	step := func() SetupStep {
		s := w2.step()
		s.Owners.Local = func(context.Context, ModelPlan) (StepLookup, error) { return StepLookup{Local: &copyFound}, nil }
		s.Owners.Adopt = func(_ context.Context, local StepLocal, h StepHosting) error {
			adopted = append(adopted, local)
			w2.hostedNow, w2.hosting = true, h
			return nil
		}
		return s
	}
	if outcome, err := step().Run(context.Background()); err != nil || outcome != stepSetUp || len(adopted) != 1 {
		t.Fatalf("adopting run = %q, %v adopted %v", outcome, err, adopted)
	}
	w2.hosting.Name = "unsloth/Qwen3.6-35B-A3B-MTP-GGUF:Qwen3.6-35B-A3B-UD-Q4_K_XL"
	copyFound = StepLocal{Store: "other", Object: "elsewhere"} // the run must not move to another copy
	if outcome, err := step().Run(context.Background()); err != nil || outcome != stepSetUp || w2.asked != 1 ||
		len(adopted) != 2 || adopted[1] != (StepLocal{Store: "hub", Object: "qwen"}) || w2.hosting.Name != "qwen3.6-35b-a3b" {
		t.Fatalf("adopted rename = %q, %v asked %d adopted %v hosting %+v", outcome, err, w2.asked, adopted, w2.hosting)
	}
}

// Owner, 2026-11-01: a model the person removed from OA stays removed until
// an app's setup asks for it; that request hosts it again with no question,
// using the copy already on this computer. The inventory lists Setup's own
// downloaded file, which on main made the request ask again.
func TestStepAppRequestHostsAgainARemovedModelWithoutAQuestion(t *testing.T) {
	w := newStepWorld(t)
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp || w.hosted != 1 {
		t.Fatalf("first run = %q, %v hosted %d", outcome, err, w.hosted)
	}
	setupCopy := StepLocal{Store: "setup-models", Object: "coder"}
	adopted := 0
	request := func() SetupStep {
		s := w.step()
		s.Requester = &StepRequester{App: "OpenCode", Use: UseCode}
		s.Owners.Local = func(context.Context, ModelPlan) (StepLookup, error) { return StepLookup{Local: &setupCopy}, nil }
		s.Owners.Adopt = func(_ context.Context, _ StepLocal, h StepHosting) error {
			adopted++
			w.hostedNow, w.hosting = true, h
			return nil
		}
		return s
	}
	w.hostedNow = false
	// No request: nothing resumes the step at sign-in, so the removal holds.
	if pending, err := w.step().Pending(context.Background()); err != nil || pending || w.hosted != 1 || w.hostedNow {
		t.Fatalf("removed model resumes at sign-in: pending %v, %v hosted %d", pending, err, w.hosted)
	}
	reserves := w.transfer.reserves
	outcome, err := request().Run(context.Background())
	if err != nil || outcome != stepSetUp || w.asked != 1 || !w.hostedNow {
		t.Fatalf("request after Remove from OA = %q, %v asked %d hosted now %v", outcome, err, w.asked, w.hostedNow)
	}
	// Setup's own verified download is hosted again where Setup stored it.
	if w.hosted != 2 || adopted != 0 || w.transfer.reserves != reserves {
		t.Fatalf("hosted %d adopted %d reserves %d (was %d)", w.hosted, adopted, w.transfer.reserves, reserves)
	}
	if outcome, err := request().Run(context.Background()); err != nil || outcome != stepAlreadySetUp || w.hosted != 2 {
		t.Fatalf("request after hosting again = %q, %v hosted %d", outcome, err, w.hosted)
	}

	// When Setup's download record is gone, the copy the inventory found is
	// adopted where it lies and the journal names it; nothing is downloaded.
	w.hostedNow = false
	if err := os.Remove(filepath.Join(w.root, "coding-model.json")); err != nil {
		t.Fatal(err)
	}
	if outcome, err := request().Run(context.Background()); err != nil || outcome != stepSetUp || w.asked != 1 || adopted != 1 || w.transfer.reserves != reserves {
		t.Fatalf("request without the download record = %q, %v asked %d adopted %d reserves %d", outcome, err, w.asked, adopted, w.transfer.reserves)
	}
	if state, _ := w.step().Load(context.Background()); state.Adopted == nil || *state.Adopted != setupCopy || !state.Ready() {
		t.Fatalf("journal = %+v", state)
	}

	// An adopted copy that moved is adopted at its new place, again with no
	// question.
	w.hostedNow = false
	setupCopy = StepLocal{Store: "other-models", Object: "coder-moved"}
	if outcome, err := request().Run(context.Background()); err != nil || outcome != stepSetUp || w.asked != 1 || adopted != 2 {
		t.Fatalf("request for a moved copy = %q, %v asked %d adopted %d", outcome, err, w.asked, adopted)
	}
	if state, _ := w.step().Load(context.Background()); state.Adopted == nil || *state.Adopted != setupCopy {
		t.Fatalf("journal = %+v", state)
	}
}

// Coordinator, 2026-11-01: Setup's Finish after an update leaves a model
// the person removed from OA removed; the Models page records the removal.
// Settings, the person starting Setup, hosts it again and clears the record.
// Owner, 2026-11-09, supersedes "a Finish with no recorded removal still
// hosts a model OA lost": the Finish leaves it stopped, and the person's
// Set up hosts it again.
func TestStepUpdateFinishLeavesARecordedRemoval(t *testing.T) {
	w := newStepWorld(t)
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp || w.hosted != 1 {
		t.Fatalf("first run = %q, %v hosted %d", outcome, err, w.hosted)
	}
	w.hostedNow = false
	if err := w.step().RecordRemoval(context.Background()); err != nil {
		t.Fatal(err)
	}
	finish := w.step()
	finish.Finish = true
	notices := len(w.notices)
	outcome, err := finish.Run(context.Background())
	if err != nil || !outcome.NothingToSetUp() || !outcome.FinishOpensPanel(err) || w.hosted != 1 || w.hostedNow || w.asked != 1 || len(w.notices) != notices {
		t.Fatalf("update Finish = %q, %v hosted %d asked %d notices %v", outcome, err, w.hosted, w.asked, w.notices[notices:])
	}
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp || w.hosted != 2 || !w.hostedNow || w.asked != 1 {
		t.Fatalf("Settings run = %q, %v hosted %d asked %d", outcome, err, w.hosted, w.asked)
	}
	if state, _ := w.step().Load(context.Background()); state.RemovedFromOA {
		t.Fatalf("hosting again kept the removal record: %+v", state)
	}
	w.hostedNow = false
	if outcome, err := finish.Run(context.Background()); err != nil || outcome != stepStoppedByUpdate || w.hosted != 2 || w.hostedNow || w.asked != 1 {
		t.Fatalf("Finish without a recorded removal = %q, %v hosted %d", outcome, err, w.hosted)
	}
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp || w.hosted != 3 || w.asked != 1 {
		t.Fatalf("Set up after the stopped Finish = %q, %v hosted %d", outcome, err, w.hosted)
	}
	// A model hosted again outside the step clears the record.
	if err := w.step().RecordRemoval(context.Background()); err != nil {
		t.Fatal(err)
	}
	if outcome, err := finish.Run(context.Background()); err != nil || outcome != stepAlreadySetUp {
		t.Fatalf("Finish with the model hosted = %q, %v", outcome, err)
	}
	if state, _ := w.step().Load(context.Background()); state.RemovedFromOA {
		t.Fatalf("a hosted model kept the removal record: %+v", state)
	}
	// No finished step: the record is not written.
	fresh := newStepWorld(t)
	if err := fresh.step().RecordRemoval(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fresh.root, "step.json")); !os.IsNotExist(err) {
		t.Fatalf("a removal was recorded without a finished step: %v", err)
	}
}

func TestStepExplicitUpdateFinishLeavesAnUnrecordedRemoval(t *testing.T) {
	w := newStepWorld(t)
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("first run = %q, %v", outcome, err)
	}
	w.hostedNow = false // Old journal has no Models-page removal record.
	finish := w.step()
	finish.Finish, finish.Update = true, true
	if outcome, err := finish.Run(context.Background()); err != nil || !outcome.NothingToSetUp() || w.hosted != 1 || w.hostedNow || w.asked != 1 {
		t.Fatalf("explicit update = %q, %v hosted %d asked %d", outcome, err, w.hosted, w.asked)
	}
	// Owner, 2026-11-09: a Finish without the update fact (a repair, an old
	// bundle) also leaves the lost model stopped for the person's Set up.
	finish.Update = false
	if outcome, err := finish.Run(context.Background()); err != nil || outcome != stepStoppedByUpdate || w.hosted != 1 || w.hostedNow || w.asked != 1 {
		t.Fatalf("plain Finish = %q, %v hosted %d asked %d", outcome, err, w.hosted, w.asked)
	}
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp || w.hosted != 2 || !w.hostedNow || w.asked != 1 {
		t.Fatalf("Set up = %q, %v hosted %d asked %d", outcome, err, w.hosted, w.asked)
	}
}

// A removed model with no copy left on this computer would need a download:
// the app's request asks the person, as any download does.
func TestStepAppRequestAsksBeforeDownloadingARemovedModel(t *testing.T) {
	w := newStepWorld(t)
	found := &StepLocal{Store: "other-models", Object: "coder"}
	step := func() SetupStep {
		s := w.step()
		s.Owners.Local = func(context.Context, ModelPlan) (StepLookup, error) { return StepLookup{Local: found}, nil }
		s.Owners.Adopt = func(_ context.Context, _ StepLocal, h StepHosting) error {
			w.hostedNow, w.hosting = true, h
			return nil
		}
		return s
	}
	if outcome, err := step().Run(context.Background()); err != nil || outcome != stepSetUp || w.asked != 1 {
		t.Fatalf("first run = %q, %v asked %d", outcome, err, w.asked)
	}
	w.hostedNow, found = false, nil
	w.answer = false
	request := step()
	request.Requester = &StepRequester{App: "OpenCode", Use: UseCode}
	if outcome, err := request.Run(context.Background()); err != nil || outcome != stepAnsweredNotNow || w.asked != 2 || w.transfer.reserves != 0 {
		t.Fatalf("request with no copy left = %q, %v asked %d reserves %d", outcome, err, w.asked, w.transfer.reserves)
	}
	if q := w.questions[1]; !strings.Contains(q.Instruction, "to download") {
		t.Fatalf("question = %+v", q)
	}

	// A downloaded model whose download record is gone, with no copy found,
	// also asks before downloading again.
	d := newStepWorld(t)
	if outcome, err := d.step().Run(context.Background()); err != nil || outcome != stepSetUp || d.asked != 1 {
		t.Fatalf("first run = %q, %v asked %d", outcome, err, d.asked)
	}
	d.hostedNow, d.answer = false, false
	if err := os.Remove(filepath.Join(d.root, "coding-model.json")); err != nil {
		t.Fatal(err)
	}
	reserves := d.transfer.reserves
	request = d.step()
	request.Requester = &StepRequester{App: "OpenCode", Use: UseCode}
	if outcome, err := request.Run(context.Background()); err != nil || outcome != stepAnsweredNotNow || d.asked != 2 || d.transfer.reserves != reserves {
		t.Fatalf("request needing a download = %q, %v asked %d reserves %d", outcome, err, d.asked, d.transfer.reserves)
	}
}

// The remote case in slice 1: a model only a paired machine serves is no
// pick. The step writes a local chat candidate only, so it asks nothing,
// selects nothing and says what this computer lacks.
func TestStepServedOnlyByPairedMachineIsNoPick(t *testing.T) {
	w := newStepWorld(t)
	s := w.step()
	s.Owners.Fit = func(context.Context, string) (StepFit, error) {
		return StepFit{Pick: &StepPick{Title: "Qwen3.6 35B", Family: "qwen3.6-35b-a3b", Machine: "desktop", Served: true, License: "Apache-2.0"},
			PoolBytes: 4_000_000_000, SmallestTitle: "Qwen3.6 35B", SmallestNeed: 25_000_000_000}, nil
	}
	outcome, err := s.Run(context.Background())
	if err != nil || outcome != stepCannotRun {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	if w.asked != 0 || w.transfer.reserves != 0 || w.hosted != 0 || w.selects != 0 {
		t.Fatalf("paired-only pick caused effects: asked %d reserves %d hosted %d selects %d", w.asked, w.transfer.reserves, w.hosted, w.selects)
	}
	if n := w.last(); n.Body != "This computer has 4 GB free for models; Qwen3.6 35B needs 24 GB." || strings.Contains(n.Body, "desktop") {
		t.Fatalf("paired-only notice = %+v", n)
	}
	// With the smallest need computed after the local filter, Fit may name
	// no local entry at all.
	s.Owners.Fit = func(context.Context, string) (StepFit, error) { return StepFit{PoolBytes: 4_000_000_000}, nil }
	if outcome, err := s.Run(context.Background()); err != nil || outcome != stepCannotRun {
		t.Fatalf("no local entry = %q, %v", outcome, err)
	}
	if n := w.last(); n.Body != "This computer has 4 GB free for models, and no model in this release fits it." {
		t.Fatalf("no local entry notice = %+v", n)
	}
}

// A model this computer already serves needs no download; the question says so.
func TestStepServedHereDownloadsNothing(t *testing.T) {
	w := newStepWorld(t)
	s := w.step()
	s.Owners.Fit = func(context.Context, string) (StepFit, error) {
		return StepFit{Pick: &StepPick{Title: "Qwen3.6 35B", Family: "qwen3.6-35b-a3b", Served: true, License: "Apache-2.0"}}, nil
	}
	if outcome, err := s.Run(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	if q := w.questions[0]; q.Instruction != "Nothing to download." || !strings.HasPrefix(q.Details, "OpenCode gets Qwen3.6 35B, which this computer already runs. ") {
		t.Fatalf("question = %+v", q)
	}
	if w.transfer.reserves != 0 || w.hosted != 0 || w.selects != 1 {
		t.Fatalf("served here: reserves %d hosted %d selects %d", w.transfer.reserves, w.hosted, w.selects)
	}
}

// Blocking 2: step.json is a record of the answer, never its authority. A
// forged approved journal naming another family resumes nothing: the step
// asks for the pick Fit gives now, and Not now writes no selection.
func TestStepForgedJournalIsAskedAgain(t *testing.T) {
	w := newStepWorld(t)
	forged := StepState{Progress: stepApproved, Use: "code", Title: "Qwen3.6 35B", Family: "attacker-model", Served: true}
	writeStepState(t, w, forged)
	w.answer = false
	outcome, err := w.step().Run(context.Background())
	if err != nil || outcome != stepAnsweredNotNow || w.asked != 1 {
		t.Fatalf("forged journal = %q, %v asked %d", outcome, err, w.asked)
	}
	if w.selects != 0 || w.transfer.reserves != 0 || w.hosted != 0 {
		t.Fatalf("forged journal caused effects: selects %d reserves %d hosted %d", w.selects, w.transfer.reserves, w.hosted)
	}
	// A forged "ready" journal reads as set up only if the owners agree; it
	// writes nothing either way.
	writeStepState(t, w, StepState{Progress: stepReady, Title: "Qwen3.6 35B", Family: "attacker-model", Served: true})
	w.answer = true
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp || w.asked != 2 {
		t.Fatalf("forged ready = %q, %v asked %d", outcome, err, w.asked)
	}
	if w.lastFamily != "qwen3.6-35b-a3b" {
		t.Fatalf("selected %q, want Fit's family", w.lastFamily)
	}
}

// Blocking 3: an approved journal from another reviewed plan asks once for
// the current plan and then completes; it never loops on the old answer.
func TestStepPlanChangeAsksOnceThenCompletes(t *testing.T) {
	w := newStepWorld(t)
	writeStepState(t, w, StepState{Progress: stepApproved, Use: "code", Title: "Qwen3.6 35B", Family: "qwen3.6-35b-a3b", ModelPlanID: "an-older-plan"})
	outcome, err := w.step().Run(context.Background())
	if err != nil || outcome != stepSetUp || w.asked != 1 {
		t.Fatalf("plan change = %q, %v asked %d", outcome, err, w.asked)
	}
	state, _ := w.step().Load(context.Background())
	if state.ModelPlanID != w.plan.ID || !state.Ready() {
		t.Fatalf("journal = %+v", state)
	}
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepAlreadySetUp || w.asked != 1 {
		t.Fatalf("after plan change = %q, %v asked %d", outcome, err, w.asked)
	}
}

// Blocking 6: one notice when the download starts, bound to the job's
// operation, and one at the end; none per observation.
func TestStepNoticesPerDownloadAreBounded(t *testing.T) {
	w := newStepWorld(t)
	w.transfer.operation = "op-0123456789"
	w.waitErr = func(n int) error {
		if n >= 40 {
			w.transfer.state = "complete"
		}
		return nil
	}
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	if w.waits < 40 {
		t.Fatalf("only %d observations", w.waits)
	}
	if len(w.notices) != 2 {
		t.Fatalf("%d notices for one download: %+v", len(w.notices), w.notices)
	}
	if start := w.notices[0]; start.Operation != "op-0123456789" || !strings.HasPrefix(start.Body, "Downloading Qwen3.6 35B") {
		t.Fatalf("start notice = %+v", start)
	}
	if end := w.notices[1]; end.Title != "OpenCode is ready" || end.Operation != "" {
		t.Fatalf("end notice = %+v", end)
	}
}

// A resumed download does not announce itself again.
func TestStepResumeDoesNotAnnounceAgain(t *testing.T) {
	w := newStepWorld(t)
	signedOut := errors.New("signed out")
	w.waitErr = func(int) error { return signedOut }
	if _, err := w.step().Run(context.Background()); !errors.Is(err, signedOut) {
		t.Fatal(err)
	}
	w.waitErr = nil
	if _, err := w.step().Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	starts := 0
	for _, n := range w.notices {
		if strings.HasPrefix(n.Body, "Downloading") {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("download announced %d times", starts)
	}
}

// Blocking 3: a failure is one notice, not an error box; a run ended by
// sign-out reports nothing.
func TestStepReportsFailureThroughOneNotice(t *testing.T) {
	w := newStepWorld(t)
	s := w.step()
	s.Owners.Fit = func(context.Context, string) (StepFit, error) {
		return StepFit{}, errors.New("the model catalogue answered unavailable")
	}
	if _, err := s.RunAndReport(context.Background()); err == nil {
		t.Fatal("failure reported success")
	}
	if len(w.notices) != 1 || w.notices[0].Title != "Couldn't finish setting up OpenCode" ||
		w.notices[0].Body != "OpenAbstractions cannot tell now which model fits this computer. Open Models and choose Set up." {
		t.Fatalf("failure notices = %+v", w.notices)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.notices = nil
	if _, err := s.RunAndReport(ctx); err == nil || len(w.notices) != 0 {
		t.Fatalf("cancelled run = %v notices %+v", err, w.notices)
	}
	// A download that fails names no reason, hash or path.
	w2 := newStepWorld(t)
	w2.waitErr = func(int) error { w2.transfer.state = "failed"; return nil }
	if _, err := w2.step().RunAndReport(context.Background()); err == nil {
		t.Fatal("a failed download reported success")
	}
	if n := w2.last(); n.Body != "The model download stopped before it finished. Open Models and choose Set up." {
		t.Fatalf("download failure notice = %+v", n)
	}
	// An owner's mapping replaces Setup's wording.
	s.Owners.Failure = func(error, string) (StepNotice, bool) {
		return StepNotice{Title: "Set up OpenCode from Settings", Body: "mapped"}, true
	}
	w.notices = nil
	if _, err := s.RunAndReport(context.Background()); err == nil || len(w.notices) != 1 || w.notices[0].Body != "mapped" || w.notices[0].Tag != "setup" {
		t.Fatalf("mapped failure = %v %+v", err, w.notices)
	}
}

func TestStepCannotRunAndNoAppAskNothing(t *testing.T) {
	w := newStepWorld(t)
	s := w.step()
	s.Owners.Fit = func(context.Context, string) (StepFit, error) {
		return StepFit{PoolBytes: 4_000_000_000, SmallestTitle: "Qwen3.6 35B", SmallestNeed: 25_000_000_000}, nil
	}
	if outcome, err := s.Run(context.Background()); err != nil || outcome != stepCannotRun || w.asked != 0 {
		t.Fatalf("cannot run = %q, %v asked %d", outcome, err, w.asked)
	}
	if n := w.last(); n.Body != "This computer has 4 GB free for models; Qwen3.6 35B needs 24 GB." {
		t.Fatalf("cannot-run notice = %+v", n)
	}
	w.apps = nil
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepNoApp || w.asked != 0 {
		t.Fatalf("no app = %q, %v asked %d", outcome, err, w.asked)
	}
}

// Setup's Finish (owner, 2026-10-31): a first install asks the one
// question; when everything is in place, or no app has anything to set up,
// the run posts no notice and reports nothing to set up, so the Panel opens.
func TestStepFinishAsksWhenThereIsSetupAndOtherwiseOpensThePanel(t *testing.T) {
	w := newStepWorld(t)
	s := w.step()
	s.Finish = true
	if outcome, err := s.Run(context.Background()); err != nil || outcome != stepSetUp || w.asked != 1 || outcome.NothingToSetUp() {
		t.Fatalf("first Finish = %q, %v asked %d", outcome, err, w.asked)
	}
	notices := len(w.notices)
	if outcome, err := s.Run(context.Background()); err != nil || outcome != stepAlreadySetUp || !outcome.NothingToSetUp() || w.asked != 1 || len(w.notices) != notices {
		t.Fatalf("set-up Finish = %q, %v asked %d notices %v", outcome, err, w.asked, w.notices[notices:])
	}
	fresh := newStepWorld(t)
	fresh.apps = nil
	s = fresh.step()
	s.Finish = true
	if outcome, err := s.Run(context.Background()); err != nil || outcome != stepNoApp || !outcome.NothingToSetUp() || fresh.asked != 0 || len(fresh.notices) != 0 {
		t.Fatalf("app-less Finish = %q, %v asked %d notices %v", outcome, err, fresh.asked, fresh.notices)
	}
	// Without Finish the same run keeps its notice.
	if outcome, err := fresh.step().Run(context.Background()); err != nil || outcome != stepNoApp || fresh.last().Title != "Nothing to set up" {
		t.Fatalf("app-less run = %q, %v notice %+v", outcome, err, fresh.last())
	}
}

// Coordinator policy, 2026-11-01: a Finish the person pressed after an
// install, change or repair is the person starting Setup. It asks again after
// a Not now, as the Settings row does. Runtime owns whether an app request is
// admitted; this Panel fixture exercises each admitted question.
func TestStepFinishAsksAgainAfterNotNow(t *testing.T) {
	w := newStepWorld(t)
	w.answer = false
	request := w.step()
	request.Requester = &StepRequester{App: "OpenCode", Use: UseCode}
	if outcome, err := request.Run(context.Background()); err != nil || outcome != stepAnsweredNotNow || w.asked != 1 {
		t.Fatalf("request Not now = %q, %v asked %d", outcome, err, w.asked)
	}
	if outcome, err := request.Run(context.Background()); err != nil || outcome != stepAnsweredNotNow || w.asked != 2 {
		t.Fatalf("admitted request = %q, %v asked %d", outcome, err, w.asked)
	}
	finish := w.step()
	finish.Finish = true
	if outcome, err := finish.Run(context.Background()); err != nil || outcome != stepAnsweredNotNow || w.asked != 3 {
		t.Fatalf("Finish after a request's Not now = %q, %v asked %d", outcome, err, w.asked)
	}
	w.answer = true
	if outcome, err := finish.Run(context.Background()); err != nil || outcome != stepSetUp || w.asked != 4 {
		t.Fatalf("Finish after a plain Not now = %q, %v asked %d", outcome, err, w.asked)
	}
}

// Finish opens the Panel unless the person answered the question or approved
// work ran; a run that stopped still leaves OpenAbstractions on screen
// (preview-1031l: a Finish run ended with no window).
func TestStepFinishOpensThePanelUnlessThePersonAnswered(t *testing.T) {
	stopped := errors.New("stopped")
	for _, c := range []struct {
		outcome stepOutcome
		err     error
		open    bool
	}{
		{stepSetUp, nil, false}, {stepAnsweredNotNow, nil, false},
		{stepAlreadySetUp, nil, true}, {stepNoApp, nil, true}, {stepCannotRun, nil, true},
		{"", stopped, true}, {stepSetUp, stopped, true},
	} {
		if got := c.outcome.FinishOpensPanel(c.err); got != c.open {
			t.Errorf("FinishOpensPanel(%q, %v) = %v, want %v", c.outcome, c.err, got, c.open)
		}
	}
}

// The step downloads only the reviewed plan whose file the catalogue picked.
func TestStepRefusesAPickWithoutItsReviewedPlan(t *testing.T) {
	w := newStepWorld(t)
	s := w.step()
	s.Owners.Fit = func(context.Context, string) (StepFit, error) {
		pick := w.pick()
		pick.Digest = "sha256:" + strings.Repeat("0", 64)
		return StepFit{Pick: &pick}, nil
	}
	if _, err := s.Run(context.Background()); err == nil || w.asked != 0 {
		t.Fatalf("mismatched pick = %v asked %d", err, w.asked)
	}
}

// The shipped coding plan is the catalogue's rung 1 file.
func TestCodingModelPlanIsTheCatalogueRungOne(t *testing.T) {
	plan := CodingModelPlan()
	pick := StepPick{Digest: "sha256:55983c5a75a1ab969824077b3bb3de4146e82a9234072b48ad4e8f92ad3fe9f1", DownloadBytes: 22853663008}
	if !planDownloads(plan, pick) {
		t.Fatalf("CodingModelPlan %+v is not the catalogue's rung 1", plan.Assets)
	}
}

func writeStepState(t *testing.T, w *stepWorld, state StepState) {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.root, "step.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Item 6: the start notice names the job's operation. A short job the
// service never names gets no start notice, and a new download after Ready
// (a new operation) gets its own.
func TestStepStartNoticeFollowsTheOperation(t *testing.T) {
	w := newStepWorld(t)
	w.transfer.operation = ""
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	for _, n := range w.notices {
		if strings.HasPrefix(n.Body, "Downloading") {
			t.Fatalf("start notice without an operation: %+v", n)
		}
	}

	w = newStepWorld(t)
	if _, err := w.step().Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The downloaded model is removed: the same answer downloads again under
	// a new operation, which announces itself once.
	for _, name := range []string{"coding-model.json", "coding-model.json.lock"} {
		os.Remove(filepath.Join(w.root, name))
	}
	w.transfer.state, w.transfer.operation, w.notices = "running", "op-second", nil
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("re-download = %q, %v", outcome, err)
	}
	starts := 0
	for _, n := range w.notices {
		if strings.HasPrefix(n.Body, "Downloading") {
			starts++
			if n.Operation != "op-second" {
				t.Fatalf("re-download notice = %+v", n)
			}
		}
	}
	if starts != 1 {
		t.Fatalf("re-download announced %d times: %+v", starts, w.notices)
	}
}

// Review 23: a slice 1 journal holds "announced": true, a boolean. It still
// loads, the step still saves, and the next save drops the old key.
func TestStepReadsASliceOneJournal(t *testing.T) {
	w := newStepWorld(t)
	raw := fmt.Sprintf(`{"phase":"approved","use":"code","title":"Qwen3.6 35B","family":"qwen3.6-35b-a3b","catalog_version":"2026-10-31.1","model_plan_id":%q,"apps":["opencode"],"announced":true,"at":"2026-10-31T09:00:00Z"}`, w.plan.ID)
	path := filepath.Join(w.root, "step.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := w.step().Load(context.Background())
	if err != nil || !state.Approved() || state.Announced != "" || state.ModelPlanID != w.plan.ID {
		t.Fatalf("load = %+v, %v", state, err)
	}
	// An earlier build wrote it: sign-in leaves it for the person's Set up.
	if pending, err := w.step().Pending(context.Background()); err != nil || pending {
		t.Fatalf("pending = %v, %v", pending, err)
	}
	outcome, err := w.step().Run(context.Background())
	if err != nil || outcome != stepSetUp || w.asked != 0 {
		t.Fatalf("run = %q, %v asked %d", outcome, err, w.asked)
	}
	saved, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(saved), `"announced":true`) || !strings.Contains(string(saved), `"announced_operation":"op-first"`) {
		t.Fatalf("saved journal = %s, %v", saved, err)
	}
}

// Review 23: a job that still has no operation after a few rounds is
// announced without one; the operation that appears later in the same run
// is recorded without a second notice.
func TestStepAnnouncesAnUnnamedJob(t *testing.T) {
	w := newStepWorld(t)
	w.transfer.operation = ""
	w.waitErr = func(n int) error {
		if n == 5 {
			w.transfer.operation = "op-late"
		}
		if n >= 7 {
			w.transfer.state = "complete"
		}
		return nil
	}
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	var starts []StepNotice
	for _, n := range w.notices {
		if strings.HasPrefix(n.Body, "Downloading Qwen3.6 35B") {
			starts = append(starts, n)
		}
	}
	if len(starts) != 1 || starts[0].Operation != "" {
		t.Fatalf("start notices = %+v", starts)
	}
	state, _ := w.step().Load(context.Background())
	if state.Announced != "op-late" {
		t.Fatalf("announced = %q", state.Announced)
	}
}
