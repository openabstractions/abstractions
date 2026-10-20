package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	model "github.com/openabstractions/abstraction-model/go/client"
	routerclient "github.com/openabstractions/abstraction-router/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// chatSubject is OpenCode registered alone, as the Settings row reads it.
var chatSubject = setup.StepTitles{Chat: "OpenCode"}

func TestStepTitleFamilyUsesCuratedNeedBesideMainLease(t *testing.T) {
	models := []libraryModel{{Key: "main", Size: 100, Ready: true, Running: true}, {Key: "a", Size: 30, Ready: true}, {Key: "b", Size: 20, Ready: true}, {Key: "outside", Size: 1, Ready: true}}
	offers := []routerclient.Offer{{Family: "b", Context: 4096, ContextSource: routerclient.ContextSourceMaximum, Servers: []string{"local"}}, {Family: "a", Context: 4096, ContextSource: routerclient.ContextSourceEffective, Servers: []string{"local"}}, {Family: "outside", Context: 4096, ContextSource: routerclient.ContextSourceEffective, Servers: []string{"local"}}}
	fit := func(free int64) model.FitResult {
		return model.FitResult{Outcome: model.FitOutcomeFitted, PoolBytes: free, Entries: []model.FitEntry{{Family: "main", NeedBytes: 120}, {Family: "a", NeedBytes: 20}, {Family: "b", NeedBytes: 25}}}
	}
	for _, tc := range []struct {
		name    string
		free    int64
		running bool
		want    string
	}{
		{"lease120_pool130", 10, true, ""}, {"lease120_pool160", 40, true, "a"},
		{"planned_main", 140, false, "a"}, {"planned_main_short", 139, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			models[0].Running = tc.running
			if got := stepTitleFamily("main", offers, models, fit(tc.free), fit(tc.free)); got != tc.want {
				t.Fatalf("title=%q want=%q", got, tc.want)
			}
		})
	}
	models[0].Running = true
	models[1].Running = true
	if got := stepTitleFamily("main", offers, models, fit(0), fit(0)); got != "a" {
		t.Fatalf("resident title charged twice: %q", got)
	}
	missing := fit(40)
	missing.Entries = nil
	if got := stepTitleFamily("main", offers, models, missing, fit(40)); got != "" {
		t.Fatal("missing title fact", got)
	}
	if got := stepTitleFamily("main", offers, models, fit(40), missing); got != "" {
		t.Fatal("missing main fact", got)
	}
	offers[1].Context = 4095
	models[1].Running = false
	if got := stepTitleFamily("main", offers, models, fit(40), fit(40)); got != "b" {
		t.Fatal("context bound", got)
	}
	offers[1].Context = 4096
	tied := fit(40)
	tied.Entries[2].NeedBytes = 20
	if got := stepTitleFamily("main", offers, models, tied, fit(40)); got != "a" {
		t.Fatal("need tie is not deterministic", got)
	}
	unknown := fit(40)
	unknown.Outcome = model.FitOutcomeUnavailable
	if got := stepTitleFamily("main", offers, models, unknown, fit(40)); got != "" {
		t.Fatal("unknown free facts", got)
	}
}

func TestStepTitlePolicyCreatesUnknownOnceAndPreservesFound(t *testing.T) {
	old := stepReadTitleFamily
	t.Cleanup(func() { stepReadTitleFamily = old })
	reads, writes := 0, 0
	stepReadTitleFamily = func(context.Context, string) (string, error) { reads++; return "small", nil }
	read := iclient.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeUnknown}
	put := func(_ context.Context, revision string, policy iclient.SelectionPolicyV2) (iclient.SelectionChange, error) {
		writes++
		if revision != "" || policy.Name != "title" || len(policy.Candidates) != 1 || policy.Candidates[0].Model != "small" || policy.Candidates[0].Placement != iwire.CandidatePlacementLocal {
			t.Fatalf("put %q %+v", revision, policy)
		}
		read = iclient.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeFound, Revision: "person-revision", Policy: &policy}
		return iclient.SelectionChange{Outcome: iwire.EditOutcomeApplied}, nil
	}
	if err := stepWriteTitle(context.Background(), "main", read, put); err != nil {
		t.Fatal(err)
	}
	read.Policy.Candidates[0].Model = "person-choice"
	if err := stepWriteTitle(context.Background(), "main", read, put); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || writes != 1 || read.Policy.Candidates[0].Model != "person-choice" {
		t.Fatalf("reads=%d writes=%d read=%+v", reads, writes, read)
	}
	stepReadTitleFamily = func(context.Context, string) (string, error) { return "", nil }
	if err := stepWriteTitle(context.Background(), "main", iclient.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeUnknown}, put); err != nil || writes != 1 {
		t.Fatal("absent title wrote a policy", err, writes)
	}
}

// Remove from OA records the removal only for the object that holds the
// step's downloaded model: the pinned digest, or the pinned file name
// before the inventory hashed it.
func TestSetupStepDownloadedObjectsMatchThePinnedFile(t *testing.T) {
	digest := strings.Repeat("a", 64)
	plan := setup.ModelPlan{Assets: []setup.ModelAsset{{Path: "coder.gguf", Download: setup.PinnedDownload{SHA256: digest}}}}
	models := []modelHostChatModel{{ID: "by-digest", Digest: "sha256:" + digest, Name: "renamed.gguf"},
		{ID: "by-name", Name: digest + ".gguf"}, {ID: "other", Digest: "sha256:" + strings.Repeat("b", 64), Name: "other.gguf"}}
	got := stepDownloadedObjects(plan, "oa-chat-1", models)
	want := []setup.StepLocal{{Store: "oa-chat-1", Object: "by-digest"}, {Store: "oa-chat-1", Object: "by-name"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("objects = %+v", got)
	}
	if got := stepDownloadedObjects(setup.ModelPlan{}, "oa-chat-1", models); got != nil {
		t.Fatalf("a plan with no single file matched %+v", got)
	}
}

// Acceptance: the Settings row shows the model, sign-in start and the chat
// default, with no empty field, in every state the step can leave.
func TestSetupStepSettingsRowHasNoEmptyField(t *testing.T) {
	ready := setup.StepState{Progress: "ready", Title: "Qwen3.6 35B", Family: "qwen3.6-35b-a3b"}
	on := &applications.SignInStart{Wanted: true, Runtime: true, Presenter: true}
	cases := []struct {
		name      string
		state     setup.StepState
		signIn    *applications.SignInStart
		signInErr error
		selected  bool
		chatErr   error
		want      stepView
	}{
		{"set up", ready, on, nil, true, nil, stepView{Model: "Qwen3.6 35B on this computer", SignIn: "Starts at sign-in", ChatDefault: "Chat default for apps", Action: "Check", Ready: true}},
		{"new", setup.StepState{}, &applications.SignInStart{}, nil, false, nil, stepView{Model: "Not set up yet", SignIn: "Starts when an app is set up", ChatDefault: "Apps choose their own model", Action: "Set up"}},
		{"declined", setup.StepState{Progress: "declined"}, nil, errors.New("down"), false, errors.New("down"), stepView{Model: "Not set up; you chose Not now", SignIn: "Sign-in start cannot be read now", ChatDefault: "The chat default cannot be read now", Action: "Set up"}},
		{"in progress", setup.StepState{Progress: "approved", Title: "Qwen3.6 35B"}, &applications.SignInStart{Wanted: true}, nil, false, nil, stepView{Model: "Qwen3.6 35B: setting up", SignIn: "Starting at sign-in is being turned on", ChatDefault: "Apps choose their own model", Action: "Show progress"}},
		{"default changed", ready, on, nil, false, nil, stepView{Model: "Qwen3.6 35B on this computer", SignIn: "Starts at sign-in", ChatDefault: "Not the chat default for apps", Action: "Make default", Ready: true}},
		{"served here", setup.StepState{Progress: "ready", Title: "Qwen3.6 35B", Served: true}, on, nil, false, nil, stepView{Model: "Qwen3.6 35B on this computer", SignIn: "Starts at sign-in", ChatDefault: "Not the chat default for apps", Action: "Make default", Ready: true}},
	}
	for _, c := range cases {
		got := stepViewOf(c.state, chatSubject, c.signIn, c.signInErr, c.selected, c.chatErr)
		c.want.Label = "Coding model for OpenCode"
		if got != c.want {
			t.Errorf("%s: view = %+v, want %+v", c.name, got, c.want)
		}
		if got.Label == "" || got.Model == "" || got.SignIn == "" || got.ChatDefault == "" || got.Action == "" {
			t.Errorf("%s: empty field in %+v", c.name, got)
		}
	}
	if !strings.Contains(settingsPage, `id="setup-step-caption">Reading the established setup.<`) || !strings.Contains(settingsPage, "setupStepLoad();") ||
		!strings.Contains(settingsPage, `id="setup-step-label">Coding model and pictures for apps<`) {
		t.Fatal("the Settings page has no setup row that shows state on arrival")
	}
	if v := stepViewOf(setup.StepState{}, setup.StepTitles{}, nil, nil, false, nil); v.Label != "Coding model and pictures for apps" {
		t.Fatalf("unread apps label = %q", v.Label)
	}
}

// The background presenter starts the step again only for approved,
// unfinished work.
func TestSetupStepResumesAtSignInOnlyWhenPending(t *testing.T) {
	ctx := context.Background()
	if err := setup.UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setup.UseUserState)
	journal, err := setup.DefaultStepJournal()
	if err != nil {
		t.Fatal(err)
	}
	launches := 0
	launch := func() error { launches++; return nil }
	if err := resumeSetupStep(ctx, launch); err != nil || launches != 0 {
		t.Fatalf("new account launched %d, %v", launches, err)
	}
	for _, phase := range []string{"declined", "ready", "approved"} {
		writeStepPhase(t, journal, phase)
		if err := resumeSetupStep(ctx, launch); err != nil {
			t.Fatal(err)
		}
	}
	if launches != 1 {
		t.Fatalf("launched %d times; only approved work resumes", launches)
	}
}

func writeStepPhase(t *testing.T, journal string, phase string) {
	t.Helper()
	s := setup.SetupStep{JournalPath: journal, Owners: setup.StepOwners{
		Apps: func(context.Context) ([]setup.StepApp, error) {
			return []setup.StepApp{{Name: "opencode", Title: "OpenCode", Use: setup.UseCode}}, nil
		},
		Fit: func(context.Context, string) (setup.StepFit, error) {
			return setup.StepFit{Pick: &setup.StepPick{Title: "Qwen3.6 35B", Family: "f", Served: true}}, nil
		},
		Ask:   func(context.Context, setup.StepQuestion) (bool, error) { return phase != "declined", nil },
		Model: setup.ModelController{Plan: &setup.ModelPlan{ID: "x"}},
		Selected: func(context.Context, string) (bool, error) {
			if phase == "approved" {
				return false, errors.New("stop before ready")
			}
			return true, nil
		},
		Select: func(context.Context, string) error { return nil },
	}}
	if err := removeStepJournal(journal); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Run(context.Background())
	state, err := s.Load(context.Background())
	if err != nil || state.Progress != phase {
		t.Fatalf("journal phase %q, want %q: %v", state.Progress, phase, err)
	}
}

func removeStepJournal(path string) error {
	matches, err := filepath.Glob(path + "*")
	if err != nil {
		return err
	}
	for _, m := range matches {
		if err := os.Remove(m); err != nil {
			return err
		}
	}
	return nil
}

func TestSetupStepPicksFromFit(t *testing.T) {
	result := model.FitResult{Outcome: model.FitOutcomeFitted, CatalogVersion: "v", PoolBytes: 10, Entries: []model.FitEntry{
		{Title: "Big", Family: "big", Fit: model.FitNo, Availability: model.AvailabilityDownload, NeedBytes: 40},
		{Title: "Served", Family: "served", Fit: model.FitNo, Availability: model.AvailabilityServed, Machine: "desktop", NeedBytes: 30},
		{Title: "Small", Family: "small", Fit: model.FitFits, Availability: model.AvailabilityDownload, NeedBytes: 5, DownloadBytes: 4, Digest: "sha256:x"},
	}}
	fit, err := stepFitOf(result)
	// Slice 1: an entry a paired machine serves is no pick; the first entry
	// this computer fits is.
	if err != nil || fit.Pick == nil || fit.Pick.Title != "Small" || fit.Pick.Served || fit.Pick.Machine != "" || fit.SmallestTitle != "Small" || fit.SmallestNeed != 5 {
		t.Fatalf("fit = %+v %+v, %v", fit, fit.Pick, err)
	}
	result.Entries = result.Entries[:1]
	fit, err = stepFitOf(result)
	if err != nil || fit.Pick != nil || fit.SmallestTitle != "Big" || fit.PoolBytes != 10 {
		t.Fatalf("nothing fits = %+v, %v", fit, err)
	}
	if _, err := stepFitOf(model.FitResult{Outcome: model.FitOutcomeForbidden}); err == nil {
		t.Fatal("a refused Fit was read as an answer")
	}
}

func TestSetupStepChatPolicyPutsTheLocalModelFirst(t *testing.T) {
	read := iclient.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeFound, Revision: "r1", Policy: &iclient.SelectionPolicyV2{Name: chatPolicy, Candidates: []iclient.SelectionCandidateV2{
		{Model: "gpt-x", Placement: iwire.CandidatePlacementHosted, Credential: "openrouter"},
		{Model: "qwen3.6-35b-a3b", Placement: iwire.CandidatePlacementLocal},
	}}}
	if stepSelects(read, "qwen3.6-35b-a3b") {
		t.Fatal("a second candidate counted as selected")
	}
	got := stepChatPolicy(read, "qwen3.6-35b-a3b")
	want := []iclient.SelectionCandidateV2{{Model: "qwen3.6-35b-a3b", Placement: iwire.CandidatePlacementLocal}, {Model: "gpt-x", Placement: iwire.CandidatePlacementHosted, Credential: "openrouter"}}
	if got.Name != chatPolicy || !reflect.DeepEqual(got.Candidates, want) {
		t.Fatalf("policy = %+v", got)
	}
	if !stepSelects(iclient.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeFound, Policy: &got}, "qwen3.6-35b-a3b") {
		t.Fatal("the written policy does not read as selected")
	}
	empty := stepChatPolicy(iclient.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeUnknown}, "f")
	if len(empty.Candidates) != 1 {
		t.Fatalf("new policy = %+v", empty)
	}
}

func TestSetupStepFindsRegisteredOpenCode(t *testing.T) {
	page := fwire.ApplicationPage{Applications: []fwire.ApplicationEntry{
		{Descriptor: fwire.ApplicationDescriptor{Name: "lmstudio", Title: "LM Studio"}},
		{Descriptor: fwire.ApplicationDescriptor{Name: "opencode", Title: "OpenCode"}, SelfRegistered: true},
	}}
	apps := stepApps(page, setup.NewCatalog().StepChoices(), "")
	if len(apps) != 1 || apps[0] != (setup.StepApp{Name: "opencode", Title: "OpenCode", Unconfirmed: true, Use: setup.UseCode}) {
		t.Fatalf("apps = %+v", apps)
	}
}

// The paired-case text: Fit answering only a model a paired machine serves
// gives no pick, so the step never writes it as a local chat candidate.
func TestSetupStepPairedOnlyFitIsNoPick(t *testing.T) {
	fit, err := stepFitOf(model.FitResult{Outcome: model.FitOutcomeFitted, PoolBytes: 4, Entries: []model.FitEntry{
		{Title: "Qwen3.6 35B", Family: "qwen3.6-35b-a3b", Fit: model.FitFits, Availability: model.AvailabilityServed, Machine: "desktop", NeedBytes: 25},
	}})
	if err != nil || fit.Pick != nil || fit.SmallestTitle != "" || fit.SmallestNeed != 0 {
		t.Fatalf("paired-only fit = %+v %+v, %v", fit, fit.Pick, err)
	}
}

// Blocking 5: the background presenter starts once, before the first
// notice, however many notices follow; it writes no sign-in entry.
func TestSetupStepStartsThePresenterOnceBeforeItsFirstNotice(t *testing.T) {
	var events []string
	notify := presenterFirst(func() error { events = append(events, "presenter"); return nil },
		func(_ context.Context, n setup.StepNotice) error { events = append(events, n.Title); return nil })
	for _, title := range []string{"Setting up OpenCode", "Ready. Open OpenCode."} {
		if err := notify(context.Background(), setup.StepNotice{Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"presenter", "Setting up OpenCode", "Ready. Open OpenCode."}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v", events)
	}
	failing := presenterFirst(func() error { return errors.New("no presenter") },
		func(context.Context, setup.StepNotice) error { return nil })
	if err := failing(context.Background(), setup.StepNotice{Title: "x"}); err != nil {
		t.Fatalf("a presenter that cannot start blocked the notice: %v", err)
	}
}

func TestSetupStepClickSentencePerState(t *testing.T) {
	ready := setup.StepState{Progress: "ready", Title: "Qwen3.6 35B", Family: "f"}
	on := &applications.SignInStart{Wanted: true, Runtime: true, Presenter: true}
	cases := []struct {
		name string
		view stepView
		open bool
		want string
	}{
		{"new", stepViewOf(setup.StepState{}, chatSubject, on, nil, false, nil), false, "Setup opened its question."},
		{"running", stepViewOf(setup.StepState{Progress: "approved", Title: "Qwen3.6 35B"}, chatSubject, on, nil, false, nil), false, "Setup continues."},
		{"selected", stepViewOf(ready, chatSubject, on, nil, true, nil), false, "Setup checks what is set up."},
		{"not the default", stepViewOf(ready, chatSubject, on, nil, false, nil), false, "Setup makes it the chat default for apps."},
		// The default cannot be read: the row shows Check, and the click
		// sentence says Check's sentence.
		{"default unreadable", stepViewOf(ready, chatSubject, on, nil, false, errors.New("down")), false, "Setup checks what is set up."},
		{"open", stepViewOf(ready, chatSubject, on, nil, true, nil), true, "Catalog is already open."},
	}
	for _, c := range cases {
		if got := stepClickSentence(c.view.Action, c.open); got != c.want {
			t.Errorf("%s (%s): %q, want %q", c.name, c.view.Action, got, c.want)
		}
	}
	if v := stepViewOf(ready, chatSubject, on, nil, false, errors.New("down")); v.Action != "Check" || v.ChatDefault != "The chat default cannot be read now" {
		t.Fatalf("unreadable default row = %+v", v)
	}
}

// The known outcomes reach the person as sentences, with no raw error text;
// a lock is a neutral pointer to Settings.
func TestSetupStepFailureNoticesAreSentences(t *testing.T) {
	cases := map[error]string{
		fmt.Errorf("%w: %s", errStepFit, model.FitOutcomeUnavailable): "OpenAbstractions cannot tell now which model fits this computer. Open Settings to try again.",
		errStepQuestionClosed: "The question closed when this computer locked. Open Settings and choose Set up.",
	}
	for err, want := range cases {
		n, ok := stepFailureNotice(err, "OpenCode")
		if !ok || n.Body != want || strings.Contains(n.Body, "conflict") || strings.Contains(n.Body, "unavailable") {
			t.Errorf("%v: %+v %v", err, n, ok)
		}
	}
	if n, _ := stepFailureNotice(errStepQuestionClosed, "OpenCode"); n.Title != "Set up OpenCode from Settings" {
		t.Fatalf("a lock reads as a failure: %+v", n)
	}
	if n, _ := stepFailureNotice(errStepFit, ""); n.Title != "Catalog stopped" {
		t.Fatalf("no app read: %+v", n)
	}
	if _, ok := stepFailureNotice(errors.New("something else"), "OpenCode"); ok {
		t.Fatal("an unknown error was mapped")
	}
}
