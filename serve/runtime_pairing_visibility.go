package main

import (
	"errors"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	awire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	"github.com/openabstractions/abstraction-identity/listen"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

const pairingStateWarningTitle = "Pairing records changed"
const pairingStateWarningBody = "Pairing records changed outside this runtime. Pairing access is unavailable. Review Paired PCs in the Panel."

// The state callback runs under its mutex and only signals. The sole bounded
// worker holds no pairing lock while posting through Asks; Book stays its owner.
func (p *runtimePairing) startVisibility() {
	changed := make(chan struct{}, 1)
	p.state.onUnexpected = func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	p.workers.Add(1)
	go func() {
		defer p.workers.Done()
		select {
		case <-p.ctx.Done():
			return
		case <-changed:
			if err := p.postStateWarning(); err != nil && p.report != nil {
				p.report(err)
			}
		}
	}()
}

func (p *runtimePairing) postStateWarning() error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	until := time.Now().Add(24 * time.Hour).UnixMilli()
	request := awire.NotificationRequest{
		RequestRef: awire.RequestRef{Key: "pairing-state-" + p.epoch, ReplayUntilMs: until},
		Title:      pairingStateWarningTitle, Body: pairingStateWarningBody,
		Delivery: awire.NotificationDeliveryNow, ExpiresMs: until,
		Tag: "pairing-state", Actions: []awire.NotificationAction{},
	}
	program := p.rights.self().Program
	result, _, err := p.asks.book.PostNotificationFrom(p.asks.scope, program, request,
		listen.Seen{Why: "runtime detected changed pairing state"}, asks.Publisher{}, func() error {
			if err := p.ctx.Err(); err != nil {
				return err
			}
			decision := p.rights.decide(p.ctx, rwire.Subject{Account: p.rights.owner, Program: program}, notificationPostAction, notificationPostResource)
			if decision.Outcome == rwire.DecisionOutcomePermitted {
				return nil
			}
			return asks.ErrNotificationSuppressed
		})
	if err != nil {
		return err
	}
	if result.Outcome != awire.NotificationOutcomePosted {
		return errors.New("pairing: state warning notification unavailable")
	}
	return nil
}
