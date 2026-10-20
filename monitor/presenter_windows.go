//go:build windows && !panel

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/probe"
	"github.com/openabstractions/abstractions/monitor/win"
)

// This file is the Asks presenter's drawing side. Only the presenter program
// ("OpenAbstractions Presenter.exe", built with -tags presenter) runs it; the
// shipped Panel is built with -tags panel and links none of it (ARCHITECTURE:
// native questions and notifications are drawn by the Asks presenter
// process).

// runPresenter is the account's native question and notification presenter:
// a client of the runtime's public asks operator service in the signed-in
// user's session. Its hidden window owns the message loop and session-lock
// notifications. The Panel is opened through its ordinary entry point only
// when needed.
func runPresenter() error {
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
	var logSink panelLog
	defer routeBackgroundLog(&logSink)()
	permissionFailures := make(chan map[string]string, 8)
	permissionLogDone := make(chan struct{})
	go func() {
		defer close(permissionLogDone)
		var sink panelLog
		sink.writePermissionCompletions(lifetime, permissionFailures)
	}()
	defer func() {
		stop()
		<-permissionLogDone
	}()
	if panelExplicit == nil {
		startSessionDuties(lifetime)
	}

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
			// A failure here is no action the person took: it is logged and
			// shows no popup (architect popup ruling 2026-11-10, row 33).
			if err := launchQuestionPanel(); err != nil {
				log.Printf("open Questions: %v", err)
			}
		}
		presenter := windowsPresenter(w, session)
		presenter.permissionFailures = permissionFailures
		go pollTrayLoop(lifetime, w, presenter, openQuestions)
	})
	if err != nil {
		return err
	}
	return startErr
}

// windowsPresenter is the presenter over the hidden window w: the native
// question surface, the asks operator, the spend reader and the provider
// credential form.
func windowsPresenter(w *win.Window, session *trayWindowsSession) *questionPresenter {
	return &questionPresenter{
		session:          session,
		surface:          &trayWindowsSurface{w: w, session: session},
		operator:         defaultPresenterOperator,
		spend:            presenterSpend,
		providerForms:    nativeProviderForm,
		providerComplete: completeProviderConnection,
		account:          probe.Self().Account,
		watchEvery:       trayPollInterval,
	}
}

// runPresentOnce is the presenter's -present mode: a Panel operation that
// needs the person's own consent hands it the one Book occurrence it raised
// (Setup's question, or Finish setup's credential form) by its id, and waits.
// The presenter reads everything it draws from the Book; an id that names no
// Book occurrence of those two kinds is refused without drawing. The exit
// code is the occurrence's outcome (occurrenceExitCode).
func runPresentOnce(id string) int {
	var result presentResult
	err := win.RunHidden(trayDisplayName, func(w *win.Window) {
		session := &trayWindowsSession{prompting: true}
		if err := w.WatchSessionChanges(func(locked bool) {
			if locked {
				session.revision.Add(1)
				win.DismissActiveQuestion()
			}
		}); err != nil {
			result = presentResult{Outcome: presentFailed, Err: err}
			w.Close()
			return
		}
		presenter := windowsPresenter(w, session)
		go func() {
			result = withQuestionPopup(context.Background(), claimQuestionPopup, true, func(ctx context.Context) presentResult {
				return presenter.presentOccurrence(ctx, id)
			})
			w.Do(w.Close)
		}()
	})
	if err != nil {
		result = presentResult{Outcome: presentFailed, Err: err}
	}
	if result.Err != nil {
		log.Printf("present %s: %v", id, result.Err)
	}
	return occurrenceExitCode(occurrenceOutcome(result))
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

// trayWindowsQuestionPrompt maps the existing private display view only.
// It performs no question read, answer or permission change.
func trayWindowsQuestionPrompt(prompt trayQuestionPresentation) (win.QuestionPrompt, error) {
	selection, err := selectNativeQuestionPresentation(prompt)
	if err != nil {
		return win.QuestionPrompt{}, err
	}
	choices := make([]win.QuestionChoice, 0, len(prompt.Choices))
	for _, choice := range prompt.Choices {
		choices = append(choices, win.QuestionChoice{Label: trayDialogText(choice.Label), Value: choice.Option})
	}
	if permission := selection.card; permission != nil {
		prompt.Instruction = permission.Instruction
		prompt.Content = permission.Content
		prompt.Details = permission.Details
		prompt.Footer = permission.Footer
	}
	native := win.QuestionPrompt{
		Title:         trayDialogText(prompt.Title),
		Instruction:   trayDialogText(prompt.Instruction),
		Content:       trayDialogText(prompt.Content),
		Details:       trayDialogText(prompt.Details),
		Footer:        trayDialogText(prompt.Footer),
		Choices:       choices,
		Default:       prompt.Default,
		Dismiss:       prompt.Dismiss,
		AppChoice:     selection.choice,
		Permission:    selection.consent,
		PairingPrompt: selection.pairing,
		Level:         win.PromptLevel(prompt.Level),
	}
	if permission := selection.card; permission != nil {
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
	if form := prompt.CredentialForm; form != nil {
		native.CredentialForm = &win.CredentialForm{Label: form.Label, BudgetUnit: form.BudgetUnit, NeedsKey: form.NeedsKey, DefaultBudget: form.DefaultBudget, PendingReason: form.PendingReason,
			NeedsKeyNow: form.NeedsKeyNow, RetryOnly: form.RetryOnly,
			Warning: form.Warning, GetKey: func() { openExternal(form.KeyPage) }, Allow: func(ctx context.Context, candidate []byte, budget int64) error {
				return form.Allow(ctx, candidate, budget)
			}}
	}
	return native, nil
}

func (*trayWindowsSurface) CredentialFormSupported() bool { return win.CredentialFormSupported() }

func (s *trayWindowsSurface) Ask(occurrence bookOccurrence) (string, error) {
	return s.AskLeased(context.Background(), occurrence, func() bool { return true }, func() {})
}

func (s *trayWindowsSurface) AskLeased(ctx context.Context, occurrence bookOccurrence, start func() bool, started func()) (string, error) {
	native, err := trayWindowsQuestionPrompt(occurrence.Presentation())
	if err != nil {
		return "", err
	}
	if kind := occurrence.Authority(); kind != "" {
		native.RealInputOnly, native.Kind = true, kind
	}
	type reply struct {
		option string
		err    error
	}
	done := make(chan reply, 1)
	s.w.Do(func() {
		// A lock, the secure desktop or a screen saver between queueing and
		// running leaves the prompt unshown; the question is deferred.
		if ctx.Err() != nil || !s.session.Unlocked() {
			done <- reply{err: errPromptNotShown}
			return
		}
		if !start() || ctx.Err() != nil || !s.session.Unlocked() {
			done <- reply{err: errPromptNotShown}
			return
		}
		s.showing = true
		native.OnShown = started
		option, err := s.w.ShowQuestion(win.Occurrence{ID: occurrence.ID(), Prompt: native})
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
		var legacy bool
		result := withQuestionPopup(ctx, claimQuestionPopup, false, func(ctx context.Context) presentResult {
			var result presentResult
			result, legacy = pollLeasedQuestion(ctx, presenter)
			return result
		})
		if ctx.Err() != nil {
			return
		}
		if !legacy {
			report := presenterResultReport(result)
			if result.Err != nil {
				log.Printf("Could not show the question: %v", result.Err)
			}
			if report.OpenQuestions {
				openQuestions()
			}
			if report.Notice != nil {
				if err := postPresenterNotice(ctx, *report.Notice); err != nil {
					log.Printf("presenter notice: %v", err)
				}
			}
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
		present := func(ctx context.Context, record askclient.RecordMetadata) presentResult {
			return withQuestionPopup(ctx, claimQuestionPopup, false, func(ctx context.Context) presentResult {
				return presenter.present(ctx, record)
			})
		}
		if !presentFresh(ctx, fresh, present, announcer, openQuestions) {
			return
		}
		if waitTrayPoll(ctx, ticker.C) != nil {
			return
		}
	}
}

// The background tray can retry on its next poll. An explicit Panel handoff
// waits in its own child process, whose caller already owns cancellation.
func withQuestionPopup(ctx context.Context, claim func(context.Context) (func(), error), wait bool, present func(context.Context) presentResult) presentResult {
	lockCtx := ctx
	if !wait {
		var cancel context.CancelFunc
		lockCtx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
	}
	release, err := claim(lockCtx)
	if err != nil {
		if !wait && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return presentResult{Outcome: presentDeferred}
		}
		return presentResult{Outcome: presentUnavailable, Err: err}
	}
	defer release()
	return present(ctx)
}

func waitTrayPoll(ctx context.Context, ticks <-chan time.Time) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ticks:
		return nil
	}
}

// startSessionDuties runs the account's sign-in work that the presenter
// process carries with its presentation: it is the one OA program every
// sign-in starts in the person's session, and these duties raise questions
// and notices it presents. None of them draws.
func startSessionDuties(lifetime context.Context) {
	go func() {
		if err := desktopReconnectNoticeAtStart(lifetime); err != nil && lifetime.Err() == nil {
			log.Printf("desktop reconnect notice: %v", err)
		}
	}()
	// Approved setup work left unfinished by a sign-out continues now.
	go func() {
		if err := resumeSetupStep(lifetime, launchSetupStep); err != nil {
			log.Printf("resume setup: %v", err)
		}
	}()
	// An app's setup request (applications@3 RequestSetup) starts the
	// same step, naming the app.
	startSetupRequestWatch(lifetime)
	// Saved agent connections keep their start recipe on the agent's
	// current command, also after the agent updated itself.
	go func() {
		ctx, cancel := context.WithTimeout(lifetime, 2*time.Minute)
		defer cancel()
		if err := ensureSavedAgentStartRecipes(ctx, new(servicePanel)); err != nil && lifetime.Err() == nil {
			log.Printf("agent start recipes: %v", err)
		}
	}()
	// After an install or an upgrade, Setup's picture engine serves this
	// version's picture models and OA's picture folder is a store the
	// router reads, with no Setup step.
	go reconcileSetupPictureEngine(lifetime)
	// The served video set is the account's video model, also when video
	// became ready while Setup's page was closed.
	go fillVideoDefaultAtSignIn(lifetime)
	// Continue kept extension maintenance even when no Setup step runs.
	startSetupExtensionDuties(lifetime)
	startDesktopIntegrationDuty(lifetime)
	// After a runtime upgrade, an enrolled agent connection still runs
	// the previous gateway; ask the person to update it.
	go func() {
		if err := raiseAgentHelperUpdatesAtLogin(lifetime, defaultAgentHelperNotices(), 30*time.Second); err != nil && lifetime.Err() == nil {
			log.Printf("agent connection update notice: %v", err)
		}
	}()
}
