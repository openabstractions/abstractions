package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// A ComfyUI tick plans its picture engine with no choice asked for
// (architect ruling 2026-11-08); the review names that companion, the
// engine line's choices and the sizes the summary states from the pins.
func TestSetupPlanReviewNamesTheIncludedEngineAndPinSizes(t *testing.T) {
	withOtherCopies(t, nil)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	call := installHandler(t, &journal)
	got := call("POST", "/setup/plan", `{"selected":["comfyui"]}`)
	var review setupPlanReview
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &review) != nil {
		t.Fatalf("ComfyUI plan: %d %s", got.Code, got.Body.String())
	}
	engine := setup.NewCatalog().PictureEngine(setup.ComfyUI)
	if engine == "" || !slices.Equal(review.Included, []setup.AppID{engine}) || !slices.Contains(review.Selected, engine) {
		t.Fatalf("the plan does not include ComfyUI's engine %q: included %v, selected %v", engine, review.Included, review.Selected)
	}
	line := review.PictureEngine
	if line == nil || line.App != setup.ComfyUI || line.Engine != engine || line.Best != engine || !slices.Equal(line.Options, []setup.AppID{setup.PyTorch, setup.Lemonade}) {
		t.Fatalf("engine line: %+v", line)
	}
	for _, blocker := range review.Blockers {
		if strings.Contains(blocker, "picture engine") {
			t.Fatalf("ComfyUI still asks for an engine: %v", review.Blockers)
		}
	}
	// On a fresh computer the summary's sizes are the plan's pins.
	got = call("POST", "/setup/plan", `{"selected":["thenoise"]}`)
	review = setupPlanReview{}
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &review) != nil {
		t.Fatalf("TheNoise plan: %d %s", got.Code, got.Body.String())
	}
	if review.AddDownloadBytes != review.DownloadBytes || review.AddDiskBytes != review.PeakBytes || len(review.Included) != 0 || review.PictureEngine != nil {
		t.Fatalf("summary sizes %d/%d differ from the plan's pins %d/%d", review.AddDownloadBytes, review.AddDiskBytes, review.DownloadBytes, review.PeakBytes)
	}
}

// Each reason or failure sentence is logged once, when it first appears.
func TestSetupNoticeLogsEachSentenceOnce(t *testing.T) {
	call := installHandler(t, &setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")})
	logged := func(body string) bool {
		t.Helper()
		got := call("POST", "/setup/notice", body)
		var reply struct {
			Logged bool `json:"logged"`
		}
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &reply) != nil {
			t.Fatalf("notice: %d %s", got.Code, got.Body.String())
		}
		return reply.Logged
	}
	blocked := `{"kind":"blocked","sentence":"Nothing selected. Tick what you want to install."}`
	if !logged(blocked) || logged(blocked) {
		t.Fatal("a reason sentence is not logged exactly once")
	}
	failed := `{"kind":"failed","sentence":"ComfyUI could not be installed: a package did not finish installing.","detail":"PyTorch installation was interrupted"}`
	if !logged(failed) || logged(failed) {
		t.Fatal("a failure sentence is not logged exactly once")
	}
	for _, bad := range []string{`{"kind":"debug","sentence":"x"}`, `{"kind":"blocked","sentence":""}`, `{"kind":"blocked","sentence":"x","extra":1}`} {
		if got := call("POST", "/setup/notice", bad); got.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, got.Code)
		}
	}
}

// A package step finishes and records its phase after the page goes away:
// the step's context outlives the request's.
func TestSetupActionOutlivesTheClosedPage(t *testing.T) {
	ctx, closePage := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/setup/advance-next", nil).WithContext(ctx)
	step, cancel := setupActionCall(req, setup.Staged, false)
	defer cancel()
	closePage()
	if step.Err() != nil {
		t.Fatal("closing the page cancelled the package step")
	}
	if _, ok := step.Deadline(); !ok {
		t.Fatal("the package step has no deadline")
	}
}

// Cancel records the stop on the approved install; the package driver then
// starts no further step until the next Install click approves again.
func TestInstallCancelStopsTheDriverUntilTheNextInstall(t *testing.T) {
	withOtherCopies(t, nil)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	call := installHandler(t, &journal)
	plan, err := setup.NewCatalog().Plan([]setup.AppID{setup.TheNoise})
	if err != nil {
		t.Fatal(err)
	}
	// TheNoise alone gets its starter picture model in the same click.
	raw, _ := json.Marshal(setupChoiceRequest{Selected: []setup.AppID{setup.TheNoise}, PlanID: plan.ID, ModelPlanID: setup.TheNoiseModelPlan().ID, Confirm: true})
	if got := call("POST", "/setup/install", string(raw)); got.Code != 200 {
		t.Fatalf("install: %d %s", got.Code, got.Body.String())
	}
	if got := call("POST", "/setup/install/cancel", `{"setup_plan_id":"other"}`); got.Code != http.StatusConflict {
		t.Fatalf("cancel of another plan: %d", got.Code)
	}
	if got := call("POST", "/setup/install/cancel", `{"setup_plan_id":"`+plan.ID+`"}`); got.Code != 200 {
		t.Fatalf("cancel: %d %s", got.Code, got.Body.String())
	}
	got := call("GET", "/setup/install", "")
	var state setupInstallReply
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &state) != nil || !state.Install.Cancelled {
		t.Fatalf("the cancelled install reads: %d %s", got.Code, got.Body.String())
	}
	next := call("POST", "/setup/advance-next", `{"plan_id":"`+plan.ID+`"}`)
	var advanced setupAdvanceNextReply
	if next.Code != 200 || json.Unmarshal(next.Body.Bytes(), &advanced) != nil || advanced.Advanced != "" {
		t.Fatalf("a cancelled install advanced: %d %s", next.Code, next.Body.String())
	}
	for _, row := range advanced.Components {
		if row.Effect.Phase != setup.Pending {
			t.Fatalf("a cancelled install changed %s: %+v", row.ID, row.Effect)
		}
	}
	if got := call("POST", "/setup/install", string(raw)); got.Code != 200 {
		t.Fatalf("install again: %d %s", got.Code, got.Body.String())
	}
	got = call("GET", "/setup/install", "")
	state = setupInstallReply{}
	if json.Unmarshal(got.Body.Bytes(), &state) != nil || state.Install.Cancelled {
		t.Fatalf("the next Install click did not approve again: %s", got.Body.String())
	}
}

// Owner, 2026-11-09: an OpenAbstractions update interrupts the install.
// The first read by the new Panel build records the interruption and
// starts nothing; the person's Install continues the same approval.
func TestUpdateInterruptsTheInstallUntilInstall(t *testing.T) {
	withOtherCopies(t, nil)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	call := installHandler(t, &journal)
	plan, err := setup.NewCatalog().Plan([]setup.AppID{setup.TheNoise})
	if err != nil {
		t.Fatal(err)
	}
	build := "r17"
	previous := setup.PanelBuild
	setup.PanelBuild = func() string { return build }
	t.Cleanup(func() { setup.PanelBuild = previous })
	raw, _ := json.Marshal(setupChoiceRequest{Selected: []setup.AppID{setup.TheNoise}, PlanID: plan.ID, ModelPlanID: setup.TheNoiseModelPlan().ID, Confirm: true})
	if got := call("POST", "/setup/install", string(raw)); got.Code != 200 {
		t.Fatalf("install: %d %s", got.Code, got.Body.String())
	}
	read := func() setupInstallReply {
		t.Helper()
		got := call("GET", "/setup/install", "")
		var state setupInstallReply
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &state) != nil {
			t.Fatalf("read: %d %s", got.Code, got.Body.String())
		}
		return state
	}
	if state := read(); state.Install.Interrupted != "" || state.Install.Build != "r17" {
		t.Fatalf("same build: %+v", state.Install)
	}

	build = "r18"
	state := read()
	if state.Install.Interrupted != setup.InterruptedByUpdate || state.Install.PlanID != plan.ID {
		t.Fatalf("after the update: %+v", state.Install)
	}
	for _, row := range state.Components {
		if row.Effect.Phase != setup.Pending {
			t.Fatalf("reading the stopped install changed %s: %+v", row.ID, row.Effect)
		}
	}
	if got := call("POST", "/setup/install/continue", `{"setup_plan_id":"other"}`); got.Code != http.StatusConflict {
		t.Fatalf("continue of another plan: %d", got.Code)
	}
	got := call("POST", "/setup/install/continue", `{"setup_plan_id":"`+plan.ID+`"}`)
	var continued setupInstallReply
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &continued) != nil || continued.Install.Interrupted != "" || continued.Install.Build != "r18" || continued.Install.ModelPlanID != setup.TheNoiseModelPlan().ID {
		t.Fatalf("continue: %d %s", got.Code, got.Body.String())
	}
	if state := read(); state.Install.Interrupted != "" {
		t.Fatalf("read after Install: %+v", state.Install)
	}
}

// An interrupted PyTorch component setup stays staged on its root; the
// Panel's Retry reaches Runner.Retry's same-root retry instead of refusing it.
func TestStagedPyTorchRootIsRetryable(t *testing.T) {
	effect := setup.Effect{Phase: setup.Staged, ComponentRootKey: "root-key"}
	if setupEffectAllowsAction(effect, true, false) {
		t.Fatal("the shared phase rule admits staged retries for every package")
	}
	if !setupSameRootRetry("pytorch-engine", effect) {
		t.Fatal("the staged PyTorch root has no retry")
	}
	for _, other := range []struct {
		id     string
		effect setup.Effect
	}{
		{"comfyui-app", effect},
		{"pytorch-engine", setup.Effect{Phase: setup.Staged}},
		{"pytorch-engine", setup.Effect{Phase: setup.Staged, ComponentRootKey: "root-key", Removing: true}},
		{"pytorch-engine", setup.Effect{Phase: setup.Checked, ComponentRootKey: "root-key"}},
	} {
		if setupSameRootRetry(other.id, other.effect) {
			t.Fatalf("%s %+v is retried as an install", other.id, other.effect)
		}
	}
}

// The Panel state names each Setup step running now with its start and its
// timeout, and Try again (retry, recover) is refused while one runs instead
// of queueing behind it.
func TestRunningSetupStepIsReportedAndHoldsTryAgain(t *testing.T) {
	withOtherCopies(t, nil)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	panel := &servicePanel{setupJournal: &journal}
	handler := panel.handler("setup-test-key")
	call := installHandlerFor(handler)
	ctx, cancel := context.WithTimeout(context.Background(), setupActionTimeout(setup.Verified, false))
	defer cancel()
	done := panel.beginSetupAction("comfyui-app", ctx)
	var state setupPanelStatus
	if got := call("GET", "/setup/state", ""); got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &state) != nil {
		t.Fatalf("state: %d %s", got.Code, got.Body.String())
	}
	action, ok := state.Actions["comfyui-app"]
	if !ok || action.StartedAt == "" || action.Deadline == "" {
		t.Fatalf("the running step is not reported: %+v", state.Actions)
	}
	for _, path := range []string{"/setup/retry", "/setup/recover"} {
		got := call("POST", path, `{"component_id":"comfyui-app","plan_id":"any"}`)
		if got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "still running") {
			t.Fatalf("%s while a step runs: %d %s", path, got.Code, got.Body.String())
		}
	}
	done()
	state = setupPanelStatus{}
	if got := call("GET", "/setup/state", ""); json.Unmarshal(got.Body.Bytes(), &state) != nil || len(state.Actions) != 0 {
		t.Fatalf("a finished step is still reported: %s", got.Body.String())
	}
	if got := call("POST", "/setup/retry", `{"component_id":"comfyui-app","plan_id":"any"}`); strings.Contains(got.Body.String(), "still running") {
		t.Fatalf("retry after the step ended is still held: %s", got.Body.String())
	}
}

// A retry that arrives while a package step holds the step lock, just
// before the step is recorded as running, gets 409 at once instead of
// waiting behind it.
func TestRetryDoesNotQueueBehindTheStepLock(t *testing.T) {
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	panel := &servicePanel{setupJournal: &journal}
	call := installHandlerFor(panel.handler("setup-test-key"))
	panel.setupActionMu.Lock()
	defer panel.setupActionMu.Unlock()
	for _, path := range []string{"/setup/retry", "/setup/recover"} {
		answered := make(chan *httptest.ResponseRecorder, 1)
		go func() { answered <- call("POST", path, `{"component_id":"comfyui-app","plan_id":"any"}`) }()
		select {
		case got := <-answered:
			if got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "running") {
				t.Fatalf("%s behind the step lock: %d %s", path, got.Code, got.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s waited behind the step lock", path)
		}
	}
}

// A package step whose download the service refused, at advance-next or at
// /setup/retry, answers 503 with the marked error; other step errors 409.
func TestDownloadsRefusalAnswersServiceUnavailable(t *testing.T) {
	refused := httptest.NewRecorder()
	setupActionFailed(refused, fmt.Errorf("%w: download acceptance rejected: quota", setup.ErrDownloadsUnavailable))
	if refused.Code != http.StatusServiceUnavailable || !strings.HasPrefix(refused.Body.String(), "downloads are unavailable") {
		t.Fatalf("refusal: %d %s", refused.Code, refused.Body.String())
	}
	other := httptest.NewRecorder()
	setupActionFailed(other, errors.New("download source host is not approved"))
	if other.Code != http.StatusConflict {
		t.Fatalf("a refused pin: %d", other.Code)
	}
}
