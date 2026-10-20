package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	leasewire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/presenter"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

type popupLease struct {
	operator *askclient.Operator
	claim    leasewire.Lease
	shown    atomic.Bool
}

var errPopupLeaseBusy = errors.New("asks presentation slot is held")

type popupLeaseKey struct{}

type popupPresentation struct {
	lease  *popupLease
	cancel context.CancelFunc
}

// claimPopup uses the already resolved operator's exact endpoint and trust.
func claimPopup(ctx context.Context, operator *askclient.Operator, id string) (*popupLease, bool, error) {
	call, cancel := context.WithTimeout(ctx, trayPollBudget)
	defer cancel()
	claim, err := operator.PresenterContext(call).Claim(id)
	if err != nil {
		var service *leasewire.ServiceError
		return nil, errors.As(err, &service) && service.Code == leasewire.ServiceErrorCodeUnknownService, err
	}
	if claim.Outcome == leasewire.LeaseOutcomeBusy {
		return nil, false, errPopupLeaseBusy
	}
	if claim.Outcome == leasewire.LeaseOutcomeIdle {
		return nil, false, nil
	}
	if claim.Outcome != leasewire.LeaseOutcomeHeld || claim.ID == "" || claim.Token == "" ||
		!time.Now().Before(time.UnixMilli(claim.ExpiresMs)) || id != "" && claim.ID != id {
		return nil, false, fmt.Errorf("could not show the question: %v", claim.Outcome)
	}
	return &popupLease{operator: operator, claim: claim}, false, nil
}

func (l *popupLease) run(ctx context.Context, present func(context.Context) presentResult) presentResult {
	display, stop := context.WithCancel(ctx)
	defer stop()
	result := present(context.WithValue(display, popupLeaseKey{}, popupPresentation{l, stop}))
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), trayPollBudget)
	defer cancel()
	l.operator.PresenterContext(release).Release(l.claim.Token, l.shown.Load() && result.Outcome != presentDeferred)
	return result
}

func (p *questionPresenter) askPopup(ctx context.Context, occurrence bookOccurrence) (string, error) {
	presentation, leased := ctx.Value(popupLeaseKey{}).(popupPresentation)
	if !leased {
		return p.surface.Ask(occurrence)
	}
	l := presentation.lease
	expires := time.UnixMilli(l.claim.ExpiresMs)
	if occurrence.ID() != l.claim.ID || !time.Now().Before(expires) || ctx.Err() != nil {
		return "", errPromptNotShown
	}
	screen, ok := p.surface.(interface {
		AskLeased(context.Context, bookOccurrence, func() bool, func()) (string, error)
	})
	if !ok {
		return "", errPromptNotShown
	}
	show, cancel := context.WithCancel(ctx)
	defer cancel()
	started := make(chan struct{})
	done := make(chan struct{})
	var lost atomic.Bool
	var validThrough atomic.Int64
	validThrough.Store(l.claim.ExpiresMs)
	go func() {
		defer close(done)
		timer := time.NewTimer(time.Until(expires))
		defer timer.Stop()
		select {
		case <-show.Done():
			if ctx.Err() != nil {
				p.surface.Dismiss()
			}
			return
		case <-timer.C:
			lost.Store(true)
			presentation.cancel()
			p.surface.Dismiss()
			return
		case <-started:
		}
		for {
			renewAt := time.Until(expires) / 2
			timer.Reset(renewAt)
			select {
			case <-show.Done():
				if ctx.Err() != nil {
					p.surface.Dismiss()
				}
				return
			case <-timer.C:
			}
			call, finish := context.WithDeadline(show, expires)
			next, err := l.operator.PresenterContext(call).Renew(l.claim.Token)
			finish()
			if err != nil || next.Outcome != leasewire.LeaseOutcomeHeld || next.ID != l.claim.ID ||
				next.Token != l.claim.Token || !time.Now().Before(expires) || next.ExpiresMs <= expires.UnixMilli() {
				if err == nil || show.Err() == nil || ctx.Err() != nil {
					lost.Store(true)
					presentation.cancel()
					p.surface.Dismiss()
				}
				return
			}
			expires = time.UnixMilli(next.ExpiresMs)
			validThrough.Store(next.ExpiresMs)
		}
	}()
	var start sync.Once
	option, err := screen.AskLeased(show, occurrence, func() bool {
		call, finish := context.WithDeadline(show, time.UnixMilli(l.claim.ExpiresMs))
		defer finish()
		result, err := l.operator.PresenterContext(call).Start(l.claim.Token)
		return err == nil && result.Outcome == leasewire.LeaseOutcomeHeld && call.Err() == nil
	}, func() { start.Do(func() { close(started) }) })
	cancel()
	<-done
	if lost.Load() || ctx.Err() != nil || time.Now().UnixMilli() >= validThrough.Load() {
		return "", errPromptNotShown
	}
	if err == nil {
		select {
		case <-started:
			l.shown.Store(true)
		default:
			return "", errPromptNotShown
		}
	}
	return option, err
}

func pollLeasedQuestion(ctx context.Context, p *questionPresenter) (presentResult, bool) {
	read, cancel := context.WithTimeout(ctx, trayPollBudget)
	operator, err := p.operator(read)
	cancel()
	if err != nil {
		return presentResult{Outcome: presentDeferred, Err: err}, false
	}
	native, ok := operator.(*askclient.Operator)
	if !ok {
		return presentResult{Outcome: presentDeferred, Err: errors.New("asks presenter requires the local operator transport")}, false
	}
	lease, legacy, err := claimPopup(ctx, native, "")
	if !legacy && !errors.Is(err, errPopupLeaseBusy) && err == nil {
		noticeCtx, stop := context.WithTimeout(ctx, trayPollBudget)
		deliverPopupHold(noticeCtx, native)
		stop()
	}
	if lease == nil {
		if errors.Is(err, errPopupLeaseBusy) {
			err = nil
		}
		return presentResult{Outcome: presentDeferred, Err: err}, legacy
	}
	return lease.run(ctx, func(ctx context.Context) presentResult {
		read, cancel := context.WithTimeout(ctx, trayPollBudget)
		defer cancel()
		bound := *p
		bound.operator = func(context.Context) (presenterOperator, error) { return native, nil }
		permission := native.PermissionOperator()
		bound.permissions = func(context.Context) (presenterPermissionOperator, error) { return permission, nil }
		id := lease.claim.ID
		ordinary, err := native.ReadAttributedQuestionContext(read, id)
		if err != nil {
			return presentResult{Outcome: presentDeferred, Err: err}
		}
		record := ordinary.Question.Record
		if ordinary.Question.Outcome == askclient.OperatorQuestionOutcomeInvalid {
			details, err := permission.ReadPermissionDetailsContext(read, id)
			if err != nil {
				return presentResult{Outcome: presentDeferred, Err: err}
			}
			record = details.Question.Question.Record
			if record != nil && record.Key != asksTypedPermissionKey {
				return presentResult{Outcome: presentDeferred}
			}
		}
		if record == nil || record.ID != id || record.Option != "" || !asks.AutomaticNativeQuestion(record.Key) {
			return presentResult{Outcome: presentDeferred}
		}
		return bound.present(ctx, *record)
	}), false
}

func beginPopupProviderCompletion(ctx context.Context, id string) (bool, error) {
	presentation, ok := ctx.Value(popupLeaseKey{}).(popupPresentation)
	if !ok {
		return false, nil
	}
	lease := presentation.lease
	if lease.claim.ID != id {
		return true, errProviderConnectionChanged
	}
	result, err := lease.operator.PresenterContext(ctx).BeginProviderCompletion(lease.claim.Token)
	if err != nil {
		return true, err
	}
	if result.Outcome != leasewire.LeaseOutcomeCompletion {
		return true, errProviderConnectionChanged
	}
	return true, nil
}

// The caller retains the native popup lock while waiting for the earlier
// service lease to expire. Recheck the exact occurrence before showing it.
func awaitHandedPopup(ctx context.Context, operator *askclient.Operator, original askclient.RecordMetadata, show func(context.Context) presentResult) presentResult {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		lease, legacy, err := claimPopup(ctx, operator, original.ID)
		if legacy && (original.Option == "" || original.Key != asks.SetupModelsKey) {
			return show(ctx)
		}
		if errors.Is(err, errPopupLeaseBusy) {
			select {
			case <-ctx.Done():
				return presentResult{Outcome: presentUnavailable, Err: ctx.Err()}
			case <-ticker.C:
				continue
			}
		}
		if err != nil && !legacy {
			return presentResult{Outcome: presentUnavailable, Err: err}
		}
		recheck := func(ctx context.Context) presentResult {
			read, cancel := context.WithTimeout(ctx, trayPollBudget)
			defer cancel()
			current, err := operator.ReadAttributedQuestionContext(read, original.ID)
			q := current.Question
			if err != nil || q.Outcome != askclient.OperatorQuestionOutcomeFound || q.Record == nil ||
				!sameQuestionContent(original, *q.Record) {
				return presentResult{Outcome: presentUnavailable, Err: err}
			}
			if original.Key == asks.SetupModelsKey && q.Record.Option != "" && q.AnsweredBy != nil &&
				*q.AnsweredBy == askclient.AnswererOperator {
				// The parent re-reads and validates the recorded option and
				// retained plan facts via the existing no-option recovery path.
				return presentResult{Outcome: presentClosed}
			}
			if lease == nil {
				return presentResult{Outcome: presentUnavailable}
			}
			return show(ctx)
		}
		if lease != nil {
			return lease.run(ctx, recheck)
		}
		return recheck(ctx)
	}
}

func deliverPopupHold(ctx context.Context, native *askclient.Operator) {
	client := native.PresenterContext(ctx)
	notice, err := client.ReadHoldNotice()
	if err != nil || notice.Outcome != leasewire.LeaseOutcomeHeld || notice.Program == "" || notice.Token == "" {
		return
	}
	app := []rune(asks.AskerDisplay(notice.Program))
	if len(app) > 180 {
		app = app[:180]
	}
	n := presenterNotice{Title: "Questions paused", Body: string(app) + " is asking too often; its questions are paused.", Action: noticeReviewQuestions,
		Key: presenterNoticePrefix + "hold-" + notice.Token, ExpiresMs: notice.ExpiresMs}
	if err := postPresenterNotice(ctx, n); err != nil {
		log.Printf("native question hold notice: %v", err)
		return
	}
	client.AckHoldNotice(notice.Token)
}
