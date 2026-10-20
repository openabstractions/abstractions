package setup

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// A fresh per-machine install (preview-1031f): "OpenCode setup stopped.
// Setup could not finish." with no step journal and no reason anywhere.
// Every way a run stops before the person's answer now names its stage in
// the notice, keeps the reason in the journal without changing the phase,
// and hands it to OA logging; a later run that asks clears it.
func TestStepStoppedBeforeTheAnswerSaysWhyAndKeepsIt(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(w *stepWorld, s *SetupStep)
		title  string
		body   string
		stage  string
	}{
		{"apps unreadable", func(_ *stepWorld, s *SetupStep) {
			s.Owners.Apps = func(context.Context) ([]StepApp, error) {
				return nil, errors.New("resolve applications@2: unavailable")
			}
		}, "Catalog stopped", "OpenAbstractions could not read which apps are installed. Open Settings to try again.", stageApps},
		{"question not shown", func(_ *stepWorld, s *SetupStep) {
			s.Owners.Ask = func(context.Context, StepQuestion) (bool, error) {
				return false, errors.New("TaskDialogIndirect: 0x80070057")
			}
		}, "OpenCode setup stopped", "The setup question could not be shown. Open Settings to try again.", stageQuestion},
		{"pick without a reviewed plan", func(w *stepWorld, s *SetupStep) {
			s.Owners.Fit = func(context.Context, string) (StepFit, error) {
				pick := w.pick()
				pick.Digest = "sha256:another"
				return StepFit{Pick: &pick, PoolBytes: 1 << 40}, nil
			}
		}, "OpenCode setup stopped", "This Setup has no reviewed download for the model that fits this computer. Open Settings to try again.", stagePlan},
		{"fit unreadable", func(_ *stepWorld, s *SetupStep) {
			s.Owners.Fit = func(context.Context, string) (StepFit, error) { return StepFit{}, errors.New("catalog@1: forbidden") }
		}, "OpenCode setup stopped", "OpenAbstractions cannot tell now which model fits this computer. Open Settings to try again.", stageFit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newStepWorld(t)
			s := w.step()
			var logged []StepStopped
			s.Owners.Record = func(_ context.Context, stopped StepStopped, who string) error {
				logged = append(logged, stopped)
				return errors.New("abstraction.logging: not_ready")
			}
			c.break_(w, &s)
			if _, err := s.RunAndReport(context.Background()); err == nil {
				t.Fatal("a stopped run reported success")
			}
			if n := w.last(); n.Title != c.title || n.Body != c.body {
				t.Fatalf("notice = %+v", n)
			}
			state, err := w.step().Load(context.Background())
			if err != nil || state.Progress != stepNew || state.Stopped == nil || state.Stopped.Stage != c.stage ||
				state.Stopped.Reason+" Open Settings to try again." != c.body || state.Stopped.Detail == "" {
				t.Fatalf("journal = %+v %+v, %v", state, state.Stopped, err)
			}
			if pending, _ := w.step().Pending(context.Background()); pending {
				t.Fatal("a stopped record reads as approved work")
			}
			if len(logged) != 1 || *state.Stopped != logged[0] {
				t.Fatalf("logged %+v", logged)
			}
			// The next run asks and finishes; the record goes.
			if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp {
				t.Fatalf("next run = %q, %v", outcome, err)
			}
			if state, _ := w.step().Load(context.Background()); state.Stopped != nil {
				t.Fatalf("a finished run kept %+v", state.Stopped)
			}
		})
	}
	// A journal that cannot be read still gives its own sentence and logs.
	w := newStepWorld(t)
	s := w.step()
	if err := os.WriteFile(s.JournalPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	logged := 0
	s.Owners.Record = func(context.Context, StepStopped, string) error { logged++; return nil }
	if _, err := s.RunAndReport(context.Background()); err == nil {
		t.Fatal("an unreadable journal reported success")
	}
	if n := w.last(); n.Body != "Setup could not read its record of this step. Open Settings to try again." || logged != 1 {
		t.Fatalf("notice = %+v logged %d", n, logged)
	}
}

// OpenCode with OA installed but never registered (its registration page
// was not confirmed): the step still asks about it by its manifest's title,
// and the answer starts that registration page again through Confirm. A
// picture app in the same state gets no picture part, whose rules name a
// confirmed registration.
func TestStepAsksAboutAnInstalledAppNotRegisteredYet(t *testing.T) {
	w := newStepWorld(t)
	w.apps = []StepApp{
		{Name: "opencode", Title: "OpenCode with OA", Use: UseCode, Manifest: `C:\Users\person\AppData\Local\OpenAbstractions\apps\opencode\application-manifest.json`},
		{Name: "comfyui", Title: "ComfyUI", Use: UseImage, Engine: Lemonade, Manifest: `C:\apps\comfyui\application-manifest.json`, Program: `C:\apps\comfyui\host.exe`},
	}
	var confirmed []StepApp
	f := newPictureFake(w)
	s := f.with(w, w.step())
	s.Owners.Confirm = func(_ context.Context, apps []StepApp) error { confirmed = append(confirmed, apps...); return nil }
	outcome, err := s.Run(context.Background())
	if err != nil || outcome != stepSetUp {
		t.Fatalf("run = %q, %v", outcome, err)
	}
	if q := w.questions[0]; q.Title != "Set up OpenCode with OA?" || !strings.HasPrefix(q.Details, "OpenCode with OA gets Qwen3.6 35B") {
		t.Fatalf("question = %+v", q)
	}
	if len(confirmed) != 1 || confirmed[0].Name != "opencode" || confirmed[0].Manifest == "" {
		t.Fatalf("confirmed %+v", confirmed)
	}
	if f.engineCalls != 0 || len(f.grants) != 0 {
		t.Fatalf("an unregistered picture app got pictures: engine %d grants %d", f.engineCalls, len(f.grants))
	}
}
