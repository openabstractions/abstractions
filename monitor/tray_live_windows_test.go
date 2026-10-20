//go:build windows

package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-facade/go/probe"
	"github.com/openabstractions/abstractions/monitor/win"
)

var hp8Live = flag.Bool("hp8-live", false, "show the native permission prompt on this desktop (HP8 demonstration; a person must be watching)")

var hp8Program = flag.String("hp8-program", filepath.Join(os.Getenv("ProgramFiles"), "Git", "cmd", "git.exe"),
	"the requesting program the demonstration question names; a signed program shows a verified publisher")

var hp8Action = flag.String("hp8-action", "abstraction.inference/complete", "synthetic typed target action for the renderer fixture; no capability call or real grant is performed")
var hp8Resource = flag.String("hp8-resource", "host:lemonade", "synthetic typed target resource for the renderer fixture")

// TestLiveNativePermissionPrompt shows the real Windows prompt for one
// runtime first-use question, answered through an isolated runtime's asks
// operator that admits only this process by its bound identity. It is
// skipped unless run with -args -hp8-live. Locking the session while the
// prompt is open closes it; after unlock it is shown again. No rights rule is
// written and no browser is opened.
func TestLiveNativePermissionPrompt(t *testing.T) {
	if !*hp8Live {
		t.Skip("run with -args -hp8-live while a person watches the desktop")
	}
	book, operator := trayTestBook(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	id := ask(t, book, "hp8-live", *hp8Program, *hp8Action, *hp8Resource)
	records, err := listQuestions(ctx, operator)
	if err != nil {
		t.Fatal(err)
	}
	var record askclient.RecordMetadata
	for _, r := range records {
		if r.ID == id {
			record = r
		}
	}
	rules := &permissionFixtureRules{operator: operator}
	var outcome presentResult
	err = win.RunHidden(trayDisplayName+" HP8 demonstration", func(w *win.Window) {
		session := &trayWindowsSession{}
		if err := w.WatchSessionChanges(func(locked bool) {
			if locked {
				session.revision.Add(1)
				win.DismissActiveQuestion()
			}
		}); err != nil {
			t.Errorf("session notifications: %v", err)
			w.Close()
			return
		}
		session.prompting = true
		presenter := &questionPresenter{
			session:    session,
			surface:    &trayWindowsSurface{w: w, session: session},
			operator:   defaultPresenterOperator,
			rules:      func() grants.RuleOperator { return rules },
			account:    probe.Self().Account,
			watchEvery: trayPollInterval,
		}
		go func() {
			defer w.Do(w.Close)
			for ctx.Err() == nil {
				if !session.Unlocked() {
					time.Sleep(time.Second)
					continue
				}
				outcome = presenter.present(ctx, record)
				t.Logf("prompt outcome %d stale=%s err=%v", outcome.Outcome, outcome.Stale, outcome.Err)
				report := presenterResultReport(outcome)
				if report.Requeue {
					t.Log("the session locked while the prompt was open; nothing was sent, presenting again after unlock")
					continue
				}
				if report.Show {
					done := make(chan struct{})
					w.Do(func() {
						defer close(done)
						_ = win.ShowQuestionResult(trayDialogText(report.Title), trayDialogText(report.Instruction), trayDialogText(report.Content), "HP8 demonstration: no rights rule was written.", win.PromptLevel(reportPromptLevel()))
					})
					<-done
				}
				return
			}
		}()
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := listQuestions(context.Background(), operator)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range after {
		if r.ID == id {
			t.Logf("book after the prompt: option=%q answered=%q", r.Option, r.Answered)
		}
	}
	t.Logf("rights edits the answer would have written: %+v", rules.edits)
}
