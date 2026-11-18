package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// pictureModelFake is the picture-model controller's status and its
// prepare verbs.
type pictureModelFake struct {
	setup.ModelController
	status      setup.ModelStatus
	configures  int
	reconciles  int
	configureTo setup.ModelStatus
}

func (f *pictureModelFake) Status(context.Context) (setup.ModelStatus, error) { return f.status, nil }
func (f *pictureModelFake) Configure(context.Context, bool, func(int64, int64) error) (setup.ModelStatus, error) {
	f.configures++
	f.status = f.configureTo
	return f.status, nil
}
func (f *pictureModelFake) ReconcileProvider(context.Context) (setup.ModelStatus, error) {
	f.reconciles++
	f.status = f.configureTo
	return f.status, nil
}

const comfyProgramPath = `C:\Users\person\AppData\Local\Programs\OpenAbstractions\apps\comfyui\OpenAbstractions ComfyUI.exe`

func pictureOwnersFake(model *pictureModelFake, apps *setupApplicationsFake) (stepPictureOwners, *[]string, *int) {
	granted, confirmed := []string{}, 0
	return stepPictureOwners{
		engineOffer: func(_ context.Context, _ setup.FeatureStatus, engine setup.AppID) (setup.StepPictureOffer, error) {
			return setup.StepPictureOffer{PlanID: "engine-plan", Packages: []setup.AppID{engine}}, nil
		},
		engine: func(context.Context, setup.StepPictureOffer) (bool, error) { return true, nil },
		confirmModel: func(_ context.Context, engine setup.AppID) error {
			if engine != setup.Lemonade {
				return fmt.Errorf("confirmed the model of %s", engine)
			}
			confirmed++
			return nil
		},
		feature: func(setup.AppID) (setup.FeatureStatus, error) { return setup.FeatureStatus{Available: true}, nil },
		model:   model,
		apps:    func(context.Context) (setupApplications, error) { return apps, nil },
		grant: func(_ context.Context, program string, rules []setup.ApprovedRule) error {
			for _, rule := range rules {
				granted = append(granted, program+" "+rule.Action+" "+rule.Resource)
			}
			return nil
		},
	}, &granted, &confirmed
}

// The offer names the grants package's picture rules on the local engine's
// host, each as the sentence the question shows, for ComfyUI's registered
// program. A shared interpreter or a registration with no program gets none.
func TestSetupStepPictureOffer(t *testing.T) {
	ctx := context.Background()
	model := &pictureModelFake{}
	o, _, _ := pictureOwnersFake(model, &setupApplicationsFake{})
	offer, err := o.offer(ctx, setup.StepApp{Name: "comfyui", Title: "ComfyUI", Program: comfyProgramPath, Use: setup.UseImage, Engine: setup.Lemonade})
	if err != nil || offer.Reason != "" {
		t.Fatalf("offer = %+v, %v", offer, err)
	}
	want, _ := picturePermissionRules()
	if !slices.Equal(offer.Rules, want) || offer.Program != comfyProgramPath || offer.Title != "FLUX.2 Klein 4B" || offer.Configured || offer.PlanID != "engine-plan" ||
		offer.App != "comfyui" || offer.Engine != setup.Lemonade || !slices.Equal(offer.Packages, []setup.AppID{setup.Lemonade}) {
		t.Fatalf("offer = %+v", offer)
	}
	if strings.Join(offer.Permissions, " ") != "Make pictures on this computer. Save the pictures it makes. Read back the pictures it made." {
		t.Fatalf("permissions = %q", offer.Permissions)
	}
	// The journal's record of a preparation is not the engine serving it:
	// only the engine's own check and the runtime's route read prepared.
	model.status = setup.ModelStatus{Approved: true, ProviderConfigured: true}
	if offer, _ := o.offer(ctx, setup.StepApp{Name: "comfyui", Program: comfyProgramPath, Engine: setup.Lemonade}); offer.Configured {
		t.Fatalf("a recorded preparation the engine does not serve reads as prepared: %+v", offer)
	}
	model.status.Ready = true
	if offer, _ := o.offer(ctx, setup.StepApp{Name: "comfyui", Program: comfyProgramPath, Engine: setup.Lemonade}); !offer.Configured {
		t.Fatalf("a served model reads as unprepared: %+v", offer)
	}
	o.live = func(context.Context, setup.AppID, string) error {
		return errors.New("the runtime cannot route the picture model yet")
	}
	if offer, _ := o.offer(ctx, setup.StepApp{Name: "comfyui", Program: comfyProgramPath, Engine: setup.Lemonade}); offer.Configured {
		t.Fatalf("a model the runtime cannot route reads as prepared: %+v", offer)
	}
	for _, program := range []string{"", `C:\Python312\python.exe`} {
		offer, err := o.offer(ctx, setup.StepApp{Name: "comfyui", Program: program})
		if err != nil || offer.Reason == "" || len(offer.Rules) != 0 {
			t.Fatalf("program %q: offer = %+v, %v", program, offer, err)
		}
	}
}

// On a computer where the optimized PyTorch engine fits, the offer's rules
// name that engine's host and read as the same sentences; a name that is no
// picture engine's host gets no rules.
func TestSetupStepPictureRulesFollowTheEngine(t *testing.T) {
	ctx := context.Background()
	o, _, _ := pictureOwnersFake(&pictureModelFake{}, &setupApplicationsFake{})
	o.fallback = "A fallback sentence."
	offer, err := o.offer(ctx, setup.StepApp{Name: "comfyui", Title: "ComfyUI", Program: comfyProgramPath, Use: setup.UseImage, Engine: setup.PyTorch})
	if err != nil || offer.Reason != "" || offer.Engine != setup.PyTorch || offer.Fallback != "A fallback sentence." {
		t.Fatalf("offer = %+v, %v", offer, err)
	}
	want, _ := picturePermissionRulesFor("pytorch-image")
	if !slices.Equal(offer.Rules, want) || !slices.ContainsFunc(offer.Rules, func(rule setup.ApprovedRule) bool { return rule.Resource == "host:pytorch-image" }) {
		t.Fatalf("rules = %+v", offer.Rules)
	}
	if strings.Join(offer.Permissions, " ") != "Make pictures on this computer. Save the pictures it makes. Read back the pictures it made." {
		t.Fatalf("permissions = %q", offer.Permissions)
	}
	if _, err := picturePermissionRulesFor("comfyui"); err == nil {
		t.Fatal("rules were made for a host that is no picture engine")
	}
}

// ComfyUI's own program passes the offer in both forms VISION 2026-10-07
// allows, on real files laid out as the native package lays them out: the
// app-named host, and the venv's copied interpreter with its DLL whose
// pyvenv.cfg names the package's private interpreter as home. A hard link of
// that interpreter, the interpreter itself and a portable build's
// python_embeded with no venv are shared interpreters and get no offer.
func TestSetupStepPictureOfferOwnProgramForms(t *testing.T) {
	ctx := context.Background()
	app := filepath.Join(t.TempDir(), "comfyui")
	home := filepath.Join(app, "oa-backend", "standalone-env")
	venv := filepath.Join(app, "oa-backend", "ComfyUI", "custom_nodes", "abstraction_downloads", "helper")
	embedded := filepath.Join(app, "ComfyUI_windows_portable", "python_embeded")
	files := map[string]string{
		filepath.Join(home, "python.exe"):                  "interpreter",
		filepath.Join(home, "python313.dll"):               "runtime",
		filepath.Join(venv, "python.exe"):                  "interpreter copy",
		filepath.Join(venv, "python313.dll"):               "runtime copy",
		filepath.Join(venv, "pyvenv.cfg"):                  "home = " + home + "\ninclude-system-site-packages = false\n",
		filepath.Join(embedded, "python.exe"):              "portable interpreter",
		filepath.Join(app, "OpenAbstractions ComfyUI.exe"): "host",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if identity.ProgramKey(filepath.Join(venv, "python.exe")) == "" {
		t.Skip("no file identity on this platform")
	}
	o, _, _ := pictureOwnersFake(&pictureModelFake{}, &setupApplicationsFake{})
	for _, program := range []string{filepath.Join(app, "OpenAbstractions ComfyUI.exe"), filepath.Join(venv, "python.exe")} {
		offer, err := o.offer(ctx, setup.StepApp{Name: "comfyui", Program: program, Engine: setup.Lemonade})
		if err != nil || offer.Reason != "" || offer.Program != program || len(offer.Rules) == 0 {
			t.Fatalf("own program %s: offer = %+v, %v", program, offer, err)
		}
	}
	linked := filepath.Join(app, "linked")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, "pyvenv.cfg"), []byte("home = "+home+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := []string{filepath.Join(home, "python.exe"), filepath.Join(embedded, "python.exe")}
	if err := os.Link(filepath.Join(home, "python.exe"), filepath.Join(linked, "python.exe")); err == nil {
		shared = append(shared, filepath.Join(linked, "python.exe"))
	} else {
		t.Logf("no hard links here: %v", err)
	}
	for _, program := range shared {
		offer, err := o.offer(ctx, setup.StepApp{Name: "comfyui", Program: program})
		if err != nil || offer.Reason == "" || len(offer.Rules) != 0 {
			t.Fatalf("shared interpreter %s: offer = %+v, %v", program, offer, err)
		}
	}
}

// The rules are written only for the confirmed registration of the program
// the answer named, and only while the rule set is the answered one.
func TestSetupStepPictureGrantNamesTheConfirmedProgram(t *testing.T) {
	ctx := context.Background()
	rules, _ := picturePermissionRules()
	offer := setup.StepPictureOffer{Rules: rules, Program: comfyProgramPath, App: "comfyui", Engine: setup.Lemonade}
	entry := func(program string, self bool) fwire.ApplicationPage {
		return fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{
			{Descriptor: fwire.ApplicationDescriptor{Name: "opencode", Program: `C:\OpenCode\opencode.exe`}},
			{Descriptor: fwire.ApplicationDescriptor{Name: "comfyui", Program: program}, SelfRegistered: self}}}
	}
	apps := &setupApplicationsFake{page: entry(comfyProgramPath, false)}
	o, granted, _ := pictureOwnersFake(&pictureModelFake{}, apps)
	if err := o.grantRules(ctx, offer); err != nil || len(*granted) != len(rules) || !strings.HasPrefix((*granted)[0], comfyProgramPath+" ") {
		t.Fatalf("grant = %v %v", err, *granted)
	}
	for name, page := range map[string]fwire.ApplicationPage{
		"unconfirmed":     entry(comfyProgramPath, true),
		"another program": entry(`C:\Elsewhere\comfy.exe`, false),
		"removed":         {Outcome: fwire.ApplicationOutcomePage},
	} {
		apps.page = page
		*granted = nil
		if err := o.grantRules(ctx, offer); err == nil || len(*granted) != 0 {
			t.Fatalf("%s: grant = %v %v", name, err, *granted)
		}
	}
	apps.page = entry(comfyProgramPath, false)
	changed := offer
	changed.Rules = rules[:1]
	if err := o.grantRules(ctx, changed); err == nil || len(*granted) != 0 {
		t.Fatalf("a changed rule set was written: %v %v", err, *granted)
	}
}

// Configure follows the Setup page's Prepare: a prepared engine needs
// nothing, otherwise it prepares (Configure resolves a preparing marker);
// a ready model is recorded in Setup's plan journal.
func TestSetupStepPictureConfigure(t *testing.T) {
	ctx := context.Background()
	ready := setup.ModelStatus{Approved: true, ProviderConfigured: true, Ready: true}
	model := &pictureModelFake{configureTo: ready}
	o, _, confirmed := pictureOwnersFake(model, &setupApplicationsFake{})
	if err := o.configure(ctx); err != nil || model.configures != 1 || model.reconciles != 0 || *confirmed != 1 {
		t.Fatalf("configure = %v configures %d reconciles %d confirmed %d", err, model.configures, model.reconciles, *confirmed)
	}
	model.status = setup.ModelStatus{ProviderConfiguring: true}
	if err := o.configure(ctx); err != nil || model.reconciles != 0 || model.configures != 2 {
		t.Fatalf("preparing = %v configures %d reconciles %d", err, model.configures, model.reconciles)
	}
	if err := o.configure(ctx); err != nil || model.configures != 2 || model.reconciles != 0 || *confirmed != 3 {
		t.Fatalf("prepared = %v configures %d reconciles %d confirmed %d", err, model.configures, model.reconciles, *confirmed)
	}
	model.status, model.configureTo = setup.ModelStatus{}, setup.ModelStatus{ProviderConfiguring: true}
	if err := o.configure(ctx); err == nil {
		t.Fatal("an unprepared engine read as prepared")
	}
	// The owner's 2026-11-09 report: a recorded preparation whose engine
	// check fails is a failure, never a step on to the permissions and
	// Ready, and the model is not recorded in Setup's plan.
	before := *confirmed
	model.status = setup.ModelStatus{Approved: true, ProviderConfigured: true}
	if err := o.configure(ctx); err == nil || *confirmed != before {
		t.Fatalf("a model the engine does not serve configured: %v confirmed %d", err, *confirmed)
	}
	model.status.Ready = true
	var routed []string
	o.live = func(_ context.Context, engine setup.AppID, name string) error {
		routed = append(routed, string(engine)+" "+name)
		return errors.New("the runtime cannot route the picture model yet")
	}
	if err := o.configure(ctx); err == nil || !strings.Contains(err.Error(), "cannot route") || *confirmed != before {
		t.Fatalf("a model the runtime cannot route configured: %v confirmed %d", err, *confirmed)
	}
	if len(routed) != 1 || routed[0] != "lemonade "+setup.BF16ModelID {
		t.Fatalf("live check = %q", routed)
	}
	o.live = func(context.Context, setup.AppID, string) error { return nil }
	if err := o.configure(ctx); err != nil || *confirmed != before+1 {
		t.Fatalf("a served model = %v confirmed %d", err, *confirmed)
	}
}

// A fresh Lemonade preparation routes only once the runtime inventory has
// hashed the new files: configure keeps asking the router until it routes,
// then the rules are granted.
func TestSetupStepPictureConfigureWaitsForTheRoute(t *testing.T) {
	ctx := context.Background()
	ready := setup.ModelStatus{Approved: true, ProviderConfigured: true, Ready: true}
	model := &pictureModelFake{configureTo: ready}
	rules, _ := picturePermissionRules()
	apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{
		{Descriptor: fwire.ApplicationDescriptor{Name: "comfyui", Program: comfyProgramPath}}}}}
	o, granted, confirmed := pictureOwnersFake(model, apps)
	answers := []string{"not-here", "not-here", "would-load"}
	o.live = func(context.Context, setup.AppID, string) error {
		verdict := answers[0]
		answers = answers[1:]
		if verdict != "would-load" {
			return errors.New("the runtime cannot route the picture model yet: " + verdict)
		}
		return nil
	}
	waits := 0
	o.routeWait = func(context.Context) func(context.Context) error {
		return func(context.Context) error { waits++; return nil }
	}
	if err := o.configure(ctx); err != nil || *confirmed != 1 || waits != 2 || len(answers) != 0 {
		t.Fatalf("configure = %v confirmed %d waits %d", err, *confirmed, waits)
	}
	offer := setup.StepPictureOffer{Rules: rules, Program: comfyProgramPath, App: "comfyui", Engine: setup.Lemonade}
	if err := o.grantRules(ctx, offer); err != nil || len(*granted) != len(rules) {
		t.Fatalf("grant = %v %v", err, *granted)
	}
	// At the deadline the last answer is the failure.
	o.live = func(context.Context, setup.AppID, string) error { return errors.New("not-here") }
	o.routeWait = func(context.Context) func(context.Context) error {
		n := 0
		return func(context.Context) error {
			if n++; n > 3 {
				return errors.New("deadline")
			}
			return nil
		}
	}
	if err := o.configure(ctx); err == nil || err.Error() != "not-here" || *confirmed != 1 {
		t.Fatalf("an unrouted model configured: %v", err)
	}
}

// A step run's offer reads only cheap state: the engine's deep check
// (verify-setup, engine check) runs once, in configure after preparation.
func TestSetupStepPictureOfferRunsNoDeepCheck(t *testing.T) {
	ctx := context.Background()
	ready := setup.ModelStatus{Approved: true, ProviderConfigured: true, Ready: true}
	model := &pictureModelFake{status: ready, configureTo: ready}
	o, _, _ := pictureOwnersFake(model, &setupApplicationsFake{})
	checks := 0
	o.check = func(context.Context, setup.AppID) error { checks++; return nil }
	if offer, err := o.offer(ctx, setup.StepApp{Name: "comfyui", Program: comfyProgramPath, Engine: setup.Lemonade}); err != nil || !offer.Configured || checks != 0 {
		t.Fatalf("offer = %+v, %v, checks %d", offer, err, checks)
	}
	if err := o.configure(ctx); err != nil || checks != 1 {
		t.Fatalf("configure = %v, checks %d", err, checks)
	}
	o.check = func(context.Context, setup.AppID) error { return errors.New("the picture engine's check did not pass") }
	if err := o.configure(ctx); err == nil {
		t.Fatal("a failed engine check configured")
	}
}

// The step lists ComfyUI beside OpenCode with the use and engine the
// release catalog gives it, and the program the picture rules would name.
func TestSetupStepFindsRegisteredComfyUI(t *testing.T) {
	page := fwire.ApplicationPage{Applications: []fwire.ApplicationEntry{
		{Descriptor: fwire.ApplicationDescriptor{Name: "comfyui", Title: "ComfyUI", Program: comfyProgramPath}, SelfRegistered: true},
		{Descriptor: fwire.ApplicationDescriptor{Name: "lmstudio", Title: "LM Studio"}},
	}}
	apps := stepApps(page, setup.NewCatalog().StepChoices(), setup.Lemonade)
	if len(apps) != 1 || apps[0] != (setup.StepApp{Name: "comfyui", Title: "ComfyUI", Unconfirmed: true, Program: comfyProgramPath, Use: setup.UseImage, Engine: setup.Lemonade}) {
		t.Fatalf("apps = %+v", apps)
	}
}

// CQ-21: the step reads each app's use from the catalog choices it is
// given, so apps with other names get chat and pictures, titled by the
// catalog, and the Settings row names them the same way.
func TestSetupStepAppsFollowTheCatalogUse(t *testing.T) {
	choices := []setup.Choice{{ID: "quill", Title: "Quill Editor", Application: true, Use: setup.UseCode}, {ID: "easel", Title: "Easel Studio", Application: true, Use: setup.UseImage, Engines: []setup.AppID{"fixture-engine"}}}
	page := fwire.ApplicationPage{Applications: []fwire.ApplicationEntry{
		{Descriptor: fwire.ApplicationDescriptor{Name: "easel", Title: "Easel from its manifest", Program: `C:\Easel\easel.exe`}},
		{Descriptor: fwire.ApplicationDescriptor{Name: "quill", Title: "Quill Editor"}, SelfRegistered: true},
		{Descriptor: fwire.ApplicationDescriptor{Name: "comfyui", Title: "ComfyUI"}},
	}}
	apps := stepApps(page, choices, "")
	want := []setup.StepApp{{Name: "easel", Title: "Easel Studio", Program: `C:\Easel\easel.exe`, Use: setup.UseImage, Engine: "fixture-engine"},
		{Name: "quill", Title: "Quill Editor", Unconfirmed: true, Use: setup.UseCode}}
	if !slices.Equal(apps, want) {
		t.Fatalf("apps = %+v", apps)
	}
	subject := setup.TitlesOf(apps)
	on := &applications.SignInStart{Wanted: true, Runtime: true, Presenter: true}
	v := stepViewOf(setup.StepState{Progress: "ready", Title: "Qwen3.6 35B", Family: "f", Pictures: true, PicturesStopped: true}, subject, on, nil, true, nil)
	if v.Label != "Coding model for Quill Editor and pictures for Easel Studio" || v.Model != "Qwen3.6 35B on this computer; pictures for Easel Studio stopped" {
		t.Fatalf("view = %+v", v)
	}
	if n, ok := stepFailureNotice(errStepQuestionClosed, subject.All()); !ok || n.Title != "Set up Quill Editor and Easel Studio from Models" {
		t.Fatalf("lock notice = %+v", n)
	}
}

// The Settings row names the picture app's pictures beside the chat model,
// and a picture-only setup offers no chat-default action.
func TestSetupStepSettingsRowNamesPictures(t *testing.T) {
	on := &applications.SignInStart{Wanted: true, Runtime: true, Presenter: true}
	subject := setup.StepTitles{Chat: "OpenCode", Pictures: "ComfyUI"}
	both := stepViewOf(setup.StepState{Progress: "ready", Title: "Qwen3.6 35B", Family: "qwen3.6-35b-a3b", Pictures: true}, subject, on, nil, true, nil)
	if both.Model != "Qwen3.6 35B and pictures for ComfyUI on this computer" || both.Action != "Check" {
		t.Fatalf("both = %+v", both)
	}
	alone := stepViewOf(setup.StepState{Progress: "ready", Pictures: true}, subject, on, nil, false, nil)
	if alone.Model != "Pictures for ComfyUI on this computer" || alone.Action != "Check" || alone.ChatDefault == "" {
		t.Fatalf("pictures alone = %+v", alone)
	}
	// A3: chat works and pictures stopped: the row says so and offers the
	// retry, and the click writes its own sentence.
	stopped := stepViewOf(setup.StepState{Progress: "ready", Title: "Qwen3.6 35B", Family: "qwen3.6-35b-a3b", Pictures: true, PicturesStopped: true}, subject, on, nil, true, nil)
	if stopped.Model != "Qwen3.6 35B on this computer; pictures for ComfyUI stopped" || stopped.Action != "Try pictures again" || !stopped.Ready ||
		stepClickSentence(stopped.Action, false) != "Trying pictures again…" {
		t.Fatalf("pictures stopped = %+v", stopped)
	}
	progress := stepViewOf(setup.StepState{Progress: "approved", Title: "Qwen3.6 35B", Pictures: true}, subject, on, nil, false, nil)
	if progress.Model != "Qwen3.6 35B and pictures for ComfyUI: setting up" {
		t.Fatalf("in progress = %+v", progress)
	}
}
