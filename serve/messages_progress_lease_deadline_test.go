package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// The receiver's lease deadline passes with no directory call to expire it.
func TestMessageProgressLeaseDeadlineReachesSettledStart(t *testing.T) {
	h, _, _, _, _ := messagesStartProgressFixture(t)
	messagesProgressSends(t, h, msg.KindQuestion, 3)
	var consents atomic.Int32
	h.messageRights.firstUse = func(_ rights.Subject, _ func() asks.Publisher, action, _ string) string {
		if action == grants.ApplicationMessageStartAction {
			consents.Add(1)
		}
		return "pending-native-person-question"
	}
	h.messageStartSettings = func(context.Context, string) (messageStartSettings, error) {
		return defaultMessageStartSettings(), nil
	}
	receiver := rights.Subject{Account: h.messageRights.owner, Program: h.directory.descriptors["receiver"].Program}
	h.directory.instances["running"] = applicationLease{application: "receiver", owner: receiver, session: "fixture", deadline: time.Now().Add(1500 * time.Millisecond)}
	var reports []error
	h.ctx, h.report = t.Context(), func(err error) { reports = append(reports, err) }
	stop := h.followMessageChanges(t.Context())
	defer stop()
	for range 3 {
		if failing := h.messageProgressTick(""); failing != "" {
			t.Fatal("settling pass failed", failing, reports)
		}
	}
	if consents.Load() != 0 {
		t.Fatal("started early")
	}
	time.Sleep(2 * time.Second)
	for range 3 {
		if failing := h.messageProgressTick(""); failing != "" {
			t.Fatal("pass failed", failing, reports)
		}
	}
	if consents.Load() == 0 {
		t.Fatal("lease deadline passed; settled start not reached within 3 ticks")
	}
}
