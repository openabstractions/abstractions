//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/openabstractions/abstraction-facade/go/probe"
	"github.com/openabstractions/abstractions/monitor/win"
)

// runBackground is the account's native permission prompt: a client of the
// runtime's public asks operator service in the signed-in user's session.
// Its hidden window owns the message loop and session-lock notifications.
// The Panel is opened through its ordinary entry point only when needed.
func runBackground() error {
	endpoint := ""
	if panelExplicit != nil {
		endpoint = panelExplicit.Endpoint
	}
	release, acquired, err := claimQuestionPresenter(endpoint)
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer release()
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()

	var startErr error
	stopNotifications := func() {}
	defer func() { stop(); stopNotifications() }()
	err = win.RunHidden(trayDisplayName, func(w *win.Window) {
		session := &trayWindowsSession{}
		if err := w.WatchSessionChanges(func(locked bool) {
			if locked {
				session.revision.Add(1)
				win.DismissActiveQuestion()
			}
		}); err != nil {
			startErr = fmt.Errorf("native question prompts unavailable; open Questions in the Panel: %w", err)
			w.Close()
			return
		}
		session.prompting = true
		// Application notifications (ASK-N7) share this process and its
		// session; questions keep working when toasts cannot start.
		if stopToasts, err := startNotifications(lifetime, session.Unlocked); err != nil {
			log.Printf("native notifications unavailable: %v", err)
		} else {
			stopNotifications = stopToasts
		}
		openQuestions := func() {
			if err := launchQuestionPanel(); err != nil {
				log.Printf("open Questions: %v", err)
				w.Do(func() { win.Say(panelWindowTitle, "Could not open Questions in the Panel: "+err.Error()) })
			}
		}
		presenter := &questionPresenter{
			session:    session,
			surface:    &trayWindowsSurface{w: w, session: session},
			operator:   defaultPresenterOperator,
			account:    probe.Self().Account,
			watchEvery: trayPollInterval,
		}
		go pollTrayLoop(lifetime, w, presenter, openQuestions)
	})
	if err != nil {
		return err
	}
	return startErr
}

// trayWindowsSession is prompting only while WTS lock notifications are
// registered and the input desktop is the user's unlocked Default desktop.
type trayWindowsSession struct {
	prompting bool
	revision  atomic.Uint64
}

func (s *trayWindowsSession) Revision() uint64 { return s.revision.Load() }

func (s *trayWindowsSession) Unlocked() bool { return s.prompting && win.InteractiveDesktop() }

// trayWindowsSurface shows the native dialog on the hidden window's thread. showing
// is touched only on that thread, so a Dismiss queued after one prompt closed
// cannot close the next one.
type trayWindowsSurface struct {
	w       *win.Window
	session *trayWindowsSession
	showing bool
}

func (s *trayWindowsSurface) Ask(prompt trayQuestionPresentation) (string, error) {
	type reply struct {
		option string
		err    error
	}
	done := make(chan reply, 1)
	s.w.Do(func() {
		// A lock, the secure desktop or a screen saver between queueing and
		// running leaves the prompt unshown; the question is deferred.
		if !s.session.Unlocked() {
			done <- reply{err: errPromptNotShown}
			return
		}
		choices := make([]win.QuestionChoice, 0, len(prompt.Choices))
		for _, choice := range prompt.Choices {
			choices = append(choices, win.QuestionChoice{Label: trayDialogText(choice.Label), Value: choice.Option})
		}
		s.showing = true
		if permission := prompt.Permission; permission != nil {
			prompt.Instruction = permission.Instruction
			prompt.Content = permission.Content
			prompt.Details = permission.Details
			prompt.Footer = permission.Footer
		}
		native := win.QuestionPrompt{
			Title:       trayDialogText(prompt.Title),
			Instruction: trayDialogText(prompt.Instruction),
			Content:     trayDialogText(prompt.Content),
			Details:     trayDialogText(prompt.Details),
			Footer:      trayDialogText(prompt.Footer),
			Choices:     choices,
			Default:     prompt.Default,
			AppChoice:   prompt.AppChoice,
			Permission:  prompt.Permission != nil,
			Level:       win.PromptLevel(prompt.Level),
		}
		if permission := prompt.Permission; permission != nil {
			var warning *string
			if permission.Warning != nil {
				text := trayDialogText(*permission.Warning)
				warning = &text
			}
			native.PermissionView = &win.PermissionView{
				Question: trayDialogText(permission.Question), Requester: trayDialogText(permission.Requester),
				Publisher: trayDialogText(permission.Publisher), Scope: trayDialogText(permission.Scope),
				Effects:          trayDialogText(permission.Effects),
				RequesterProgram: permission.RequesterProgram,
				Warning:          warning,
			}
		}
		option, err := s.w.ShowQuestion(native)
		s.showing = false
		done <- reply{option, err}
	})
	r := <-done
	return r.option, r.err
}

// OwnerWindow is the window that owned the prompt the person just answered;
// the administrator prompt that follows a system-access once is owned by it
// too (FAC-H3).
func (s *trayWindowsSurface) OwnerWindow() uintptr { return s.w.Handle() }

// The presenter finds the owner through this method; keep the two in step.
var _ interface{ OwnerWindow() uintptr } = (*trayWindowsSurface)(nil)

func (s *trayWindowsSurface) Dismiss() {
	s.w.Do(func() {
		if s.showing {
			win.DismissActiveQuestion()
		}
	})
}

// pollTrayLoop presents newly pending permission questions, one at a time.
// Applications own presentation of ordinary choices. Questions deferred by a
// lock are left unseen so they are presented after the person returns.
func pollTrayLoop(ctx context.Context, w *win.Window, presenter *questionPresenter, openQuestions func()) {
	announcer := newTrayAnnouncer()
	degraded := false
	ticker := time.NewTicker(trayPollInterval)
	defer ticker.Stop()
	for {
		if !presenter.session.Unlocked() {
			if waitTrayPoll(ctx, ticker.C) != nil {
				return
			}
			continue
		}
		fresh, _, unavailable := pollTray(ctx, panelMachine(), announcer)
		if unavailable != degraded {
			degraded = unavailable
			if degraded {
				log.Print("native question prompts: runtime unavailable; continuing to poll")
			} else {
				log.Print("native question prompts: runtime available")
			}
		}
		show := func(report presenterReport) bool {
			done := make(chan struct{})
			w.Do(func() {
				defer close(done)
				if presenter.session.Unlocked() {
					_ = win.ShowQuestionResult(trayDialogText(report.Title), trayDialogText(report.Instruction), trayDialogText(report.Content), "", win.PromptLevel(reportPromptLevel()))
				}
			})
			select {
			case <-done:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if !presentFresh(ctx, fresh, presenter.present, announcer, openQuestions, show) {
			return
		}
		if waitTrayPoll(ctx, ticker.C) != nil {
			return
		}
	}
}

func waitTrayPoll(ctx context.Context, ticks <-chan time.Time) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ticks:
		return nil
	}
}
