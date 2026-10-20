package main

import (
	"context"
	"sync/atomic"
	"testing"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// chatStartFixture is a question to a retained Codex chat whose start queues
// while the Codex app runs. It replaces the retired attach-* fixture for the
// start properties every conversation shares.
func chatStartFixture(t *testing.T) (*applicationsHost, string, *messageRecord, *atomic.Int32, *bool) {
	t.Helper()
	h, key, r, calls, valid := messagesStartProgressFixtureWith(t, "", "codex-v1", &msg.ChatBinding{ChatID: codexChatThread})
	h.directory.messageCodexRunning = func(context.Context) (bool, error) { return true, nil }
	return h, key, r, calls, valid
}

// chatFixtureContext is the chat-* context the fixture's question addresses.
func chatFixtureContext(t *testing.T, r *messageRecord) string {
	t.Helper()
	request, err := decodeRetainedMessageRequest(r.Spec)
	if err != nil {
		t.Fatal(err)
	}
	return request.Context
}
