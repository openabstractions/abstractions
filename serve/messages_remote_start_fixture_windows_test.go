//go:build windows

package main

import (
	"context"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
)

// pairedAttachReceiver gives side 1's "other-agent" an approved recipe and
// returns that side's host.
func pairedAttachReceiver(t *testing.T, f *remoteMessagesFixture) *applicationsHost {
	t.Helper()
	h := f.nodes[1].reader.host
	if err := h.messageRights.policy.RegisterAction(grants.ApplicationMessageStartAction); err != nil {
		t.Fatal(err)
	}
	h.directory.messageStartProfile = func(context.Context, fwire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
		return applicationMessageStartRequest{Session: "fixture", Context: "ctx", ProfileFingerprint: "approved-folder-and-budget", Validate: func(context.Context) error { return nil }}, nil
	}
	return h
}

// pairedStartQuestion returns side 1's pending start question for the paired
// subject, or a zero record.
func pairedStartQuestion(t *testing.T, f *remoteMessagesFixture) asks.Record {
	t.Helper()
	entries, _, err := f.nodes[1].run.f.a.book.ApplicationHistoryScoped()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		p := entry.Record.Permission
		if p != nil && entry.Record.Pending() && len(p.Targets) == 1 && p.Targets[0].Action == grants.ApplicationMessageStartAction {
			return entry.Record
		}
	}
	return asks.Record{}
}

// pairedAttachQuestion is a paired question the caller addresses.
func pairedAttachQuestion(f *remoteMessagesFixture, key string) msg.SendRequest {
	request := f.request(0, key)
	request.Kind, request.AnswerByUnixMs = msg.KindQuestion, f.nodes[0].now.Add(time.Minute).UnixMilli()
	return request
}
