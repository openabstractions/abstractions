package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDarwinVersionedCodexReceivingRequiresCurrentAdmission(t *testing.T) {
	name := "oa-codex-integration-" + strings.Repeat("a", 32)
	a := &agentToolAdmission{ready: true, deadline: time.Now().Add(time.Minute)}
	b := &messagesBackend{application: name, contextName: "exact-context", status: &messagesInboxStatus{admission: a}}
	if err := codexReceivingPresence(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if b.application != name || b.contextName != "exact-context" {
		t.Fatal("native route canonicalized")
	}
	for _, bad := range []string{name + "x", "oa-claude-integration-" + strings.Repeat("a", 32), "foreign"} {
		b.application = bad
		if err := codexReceivingPresence(context.Background(), b); err == nil {
			t.Fatal(bad)
		}
	}
	b.application = name
	a.ready = false
	if err := codexReceivingPresence(context.Background(), b); err == nil {
		t.Fatal("unadmitted version accepted")
	}
	a.ready = true
	a.closed = true
	if err := codexReceivingPresence(context.Background(), b); err == nil {
		t.Fatal("closed admission accepted")
	}
}
