//go:build windows

package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"golang.org/x/sys/windows"
)

type observationLivenessPin struct {
	*identity.Binding
	aliveErr, peerErr error
	dead              bool
}

func TestMessagesNativeInboxSweepSharesHealthyPublicationCustody(t *testing.T) {
	f := messagesObserveFixture(t)
	instance, _ := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	d := f.run.d
	d.mu.Lock()
	book := d.nativeInboxes
	book.cancel()
	d.unlock()
	<-book.done

	lookup, stop := context.WithTimeout(f.run.ctx, time.Second)
	d.mu.RLock()
	completed := book.sweep(lookup)
	d.mu.RUnlock()
	stop()
	if !completed {
		t.Fatal("healthy sweep waited for directory write custody")
	}

	d.mu.Lock()
	row := book.rows["native-agent\x00source"]
	lease := d.instances[instance]
	lease.deadline = d.now()
	d.instances[instance] = lease
	d.unlock()
	if row == nil || !book.sweep(f.run.ctx) {
		t.Fatal("missing native inbox or failed expiry sweep")
	}
	d.mu.RLock()
	_, hasLease := d.instances[instance]
	_, hasRow := book.rows["native-agent\x00source"]
	d.mu.RUnlock()
	if _, err := row.pin.Peer(); hasLease || hasRow || err == nil {
		t.Fatal("expiry sweep retained the lease, inbox, or pin", hasLease, hasRow, err)
	}
}

func TestMessagesSendRejectsLapsedLeaseBeforeSweep(t *testing.T) {
	f := messagesObserveFixture(t)
	instance, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	d := f.run.d
	d.mu.Lock()
	book := d.nativeInboxes
	book.cancel()
	d.unlock()
	<-book.done
	d.mu.Lock()
	lease := d.instances[instance]
	lease.deadline = d.now()
	d.instances[instance] = lease
	d.unlock()

	request := messagesStateRequest(f, "lapsed-before-sweep", msg.KindNote)
	sent, err := sender.Send(instance, "source", request)
	if err != nil || sent.Outcome != msg.OutcomeForbidden {
		t.Fatal("lapsed native owner sent before cleanup", sent, err)
	}

	d.mu.Lock()
	_, hasLease := d.instances[instance]
	_, hasRow := book.rows["native-agent\x00source"]
	d.expireLocked()
	_, sweptLease := d.instances[instance]
	_, sweptRow := book.rows["native-agent\x00source"]
	d.unlock()
	if !hasLease || !hasRow {
		t.Fatal("send mutated the lapsed lease or inbox")
	}
	if sweptLease || sweptRow {
		t.Fatal("presence sweep retained the lapsed lease or inbox")
	}
}

func (p *observationLivenessPin) Alive() (bool, error) {
	if p.aliveErr != nil {
		return false, p.aliveErr
	}
	if p.dead {
		return false, nil
	}
	return p.Binding.Alive()
}

func (p *observationLivenessPin) Peer() (*identity.Peer, error) {
	if p.peerErr != nil {
		return nil, p.peerErr
	}
	return p.Binding.Peer()
}

func TestMessagesObservationLivenessInspectionRecoversAndDeathRefuses(t *testing.T) {
	f := messagesObserveFixture(t)
	observer, outcome := f.reader.messageObserver("same-context")
	if observer == nil || outcome != msg.OutcomePage {
		t.Fatal(outcome)
	}
	defer observer.pin.Close()
	bound := observer.pin.(*identity.Binding)
	pin := &observationLivenessPin{Binding: bound}
	observer.pin = pin
	// Exercise the actual Windows API error class returned by Binding.Alive.
	// No process is looked up or modified by this invalid-handle query.
	_, inspectionErr := windows.WaitForSingleObject(windows.Handle(0), 0)
	if inspectionErr == nil {
		t.Fatal("invalid process wait unexpectedly succeeded")
	}
	for _, phase := range []string{"alive", "peer"} {
		t.Run(phase, func(t *testing.T) {
			if phase == "alive" {
				pin.aliveErr = inspectionErr
			} else {
				pin.peerErr = inspectionErr
			}
			err := observer.check()
			if messageObservationFailure(err) != msg.OutcomeUnavailable || !errors.Is(err, inspectionErr) {
				t.Fatal("inspection failure became final refusal", err)
			}
			page, err := observer.headerSnapshot()
			if messageObservationFailure(err) != msg.OutcomeUnavailable || len(page.Headers) != 0 || len(page.Messages) != 0 || page.Revision != "" {
				t.Fatal("inspection uncertainty disclosed a snapshot", page, err)
			}
			pin.aliveErr, pin.peerErr = nil, nil
			if err := observer.check(); err != nil {
				t.Fatal("fresh same-binding inspection did not recover", err)
			}
		})
	}
	for _, terminal := range []string{"death", "moved", "missing"} {
		t.Run(terminal, func(t *testing.T) {
			switch terminal {
			case "death":
				pin.dead = true
			case "moved":
				pin.peerErr = identity.ErrPeerMoved
			case "missing":
				pin.aliveErr = identity.ErrNoBinding
			}
			page, err := observer.headerSnapshot()
			if messageObservationFailure(err) != msg.OutcomeForbidden || len(page.Headers) != 0 || len(page.Messages) != 0 {
				t.Fatal("confirmed native loss became recoverable", page, err)
			}
			pin.dead, pin.peerErr, pin.aliveErr = false, nil, nil
		})
	}
}

func TestMessagesObservationInitialClosedCallIsUnavailableAndMissingBindingRefuses(t *testing.T) {
	f := messagesObserveFixture(t)
	if err := f.reader.call.Close(); err != nil {
		t.Fatal(err)
	}
	page := f.reader.headersWait("same-context", "", 0)
	if page.Outcome != msg.OutcomeUnavailable || len(page.Headers) != 0 {
		t.Fatal("retired request claimed process death", page)
	}
	f.reader.call = &listen.FramedCall{}
	page = f.reader.headersWait("same-context", "", 0)
	if page.Outcome != msg.OutcomeForbidden || len(page.Headers) != 0 {
		t.Fatal("missing native binding became recoverable", page)
	}
}

func TestMessagesInboxInspectionRetirementPreservesQueuedQuestionAndReply(t *testing.T) {
	f := messagesObserveFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	ri, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	retireUninspectable := func(r *messagesReceiver, application, contextName string) {
		t.Helper()
		d := f.run.d
		d.mu.Lock()
		row := d.nativeInboxes.rows[application+"\x00"+contextName]
		if row == nil {
			d.mu.Unlock()
			t.Fatal("missing live inbox")
		}
		occurrence := row.process
		// Losing this independently owned inspection pin leaves the actual
		// authenticated call and native process alive. The real Binding.Alive
		// owner returns an error, rather than a confirmed false,nil death.
		if err := row.pin.Close(); err != nil {
			d.mu.Unlock()
			t.Fatal(err)
		}
		live, err := row.pin.Alive()
		if live || !errors.Is(err, net.ErrClosed) {
			d.mu.Unlock()
			t.Fatal(live, err)
		}
		_, _, _, _, err = nativeInboxPeer(row.pin, d.now())
		if !messageNativeInspectionUncertain(err) || errors.Is(err, identity.ErrPeerGone) {
			d.mu.Unlock()
			t.Fatal("inspection error reported as death", err)
		}
		d.expireNativeInboxesLocked()
		_, retained := d.nativeInboxes.rows[application+"\x00"+contextName]
		d.mu.Unlock()
		if retained {
			t.Fatal("existing lifecycle retained unusable inspection pin")
		}
		fresh, err := r.call.RetainProcess()
		if err != nil {
			t.Fatal(err)
		}
		defer fresh.Close()
		_, _, _, process, err := nativeInboxPeer(fresh, time.Now())
		if err != nil || !sameInboxProcess(occurrence, process) {
			t.Fatal("native occurrence changed", process, err)
		}
	}
	sent, err := sender.Send(si, "source", messagesStateRequest(f, "inspection-question", msg.KindQuestion))
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	retireUninspectable(f.other, "other-agent", "target")
	if page, err := receiver.Wait(ri, "target", msg.ViewBody, "", "", 0); err != nil || page.Outcome == msg.OutcomePage || len(page.Messages) != 0 {
		t.Fatal("unverified inbox disclosed queued question", page, err)
	}
	if opened, err := receiver.InboxOpen(ri, "target", ""); err != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, err)
	}
	page, err := receiver.Wait(ri, "target", msg.ViewBody, "", "", 0)
	if err != nil || page.Outcome != msg.OutcomePage || !messagesStateHas(page, sent.MessageID, msg.TaskStateSubmitted) {
		t.Fatal("fresh same-context admission lost queued question", page, err)
	}
	if answer, err := receiver.Reply(ri, "target", sent.MessageID, "answer survives inspection retirement"); err != nil || answer.Outcome != msg.OutcomeSent {
		t.Fatal(answer, err)
	}
	retireUninspectable(f.reader, "native-agent", "source")
	if page, err := sender.Wait(si, "source", msg.ViewBody, "", "", 0); err != nil || page.Outcome == msg.OutcomePage || len(page.Messages) != 0 {
		t.Fatal("unverified inbox disclosed queued reply", page, err)
	}
	if opened, err := sender.InboxOpen(si, "source", ""); err != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, err)
	}
	if page, err := sender.Wait(si, "source", msg.ViewBody, "", "", 0); err != nil || page.Outcome != msg.OutcomePage || !messagesReceiverHasReply(page, "answer survives inspection retirement") {
		t.Fatal("fresh same-context admission lost queued reply", page, err)
	}
}
