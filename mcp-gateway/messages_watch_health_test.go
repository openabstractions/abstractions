package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type watchHealthProbe struct {
	*messagesProbe
	statuses            []string
	instances, contexts []string
	openErr             error
}

func (p *watchHealthProbe) InboxOpen(instance, contextName, status string) (msg.InboxChange, error) {
	p.statuses = append(p.statuses, status)
	p.instances = append(p.instances, instance)
	p.contexts = append(p.contexts, contextName)
	return msg.InboxChange{Outcome: msg.OutcomeOpened}, p.openErr
}

func TestMessagesWatchHealthIsOwnTelemetryWithoutOwnershipDispatch(t *testing.T) {
	d, ctx, dir := diagnosticFixture(t)
	p := &watchHealthProbe{messagesProbe: &messagesProbe{page: msg.Page{Outcome: msg.OutcomePage, Revision: "r"}}}
	deadline := time.Now().Add(time.Minute)
	a := &agentToolAdmission{ready: true, admitted: true, deadline: deadline, generation: 4}
	status := &messagesInboxStatus{confirmed: "busy", admission: a}
	b := &messagesBackend{application: "oa-claude-integration", instance: "own-instance", contextName: "native-11111111111111111111111111111111", status: status, native: func(context.Context) (nativeMessages, error) { return p, nil }}
	if _, err := status.open(p, b.instance, b.contextName, nil); err != nil {
		t.Fatal(err)
	}
	out, err := b.MessagesWatch(ctx, gateway.MessagesWatchInput{Revision: "prior", Health: "stopped_protocol"})
	if err != nil || out.Outcome.String() != "page" || !out.HealthSupported || p.calls != 1 || p.view != msg.ViewHeaders || p.instance != "" || p.contextName != b.contextName {
		t.Fatal(out, err, p.messagesProbe)
	}
	if len(p.statuses) != 1 || status.confirmed != "busy" || status.watchHealth != "stopped_protocol" || a.deadline != deadline || a.generation != 4 || !a.ready || status.routingBound {
		t.Fatal("health changed native ownership/readiness/upkeep", p.statuses, status, a)
	}
	rows := readAgentDiagnostics(ctx, dir)
	if len(rows) != 1 || rows[0].WatchHealth == nil || rows[0].WatchHealth.Claim != "stopped_protocol" || rows[0].WatchHealth.Context != b.contextName || rows[0].PID != d.record.PID {
		t.Fatal("own untrusted C1 claim missing", rows)
	}
	// The existing scheduled renewal alone projects fixed text to the already
	// owned instance/context. Health did not schedule or dispatch this call.
	if _, err := status.open(p, b.instance, b.contextName, nil); err != nil {
		t.Fatal(err)
	}
	if len(p.statuses) != 2 || !strings.Contains(p.statuses[1], "busy | Background Messages stopped: protocol invalid (host claim)") || p.instances[1] != b.instance || p.contexts[1] != b.contextName {
		t.Fatal(p.statuses, p.instances, p.contexts)
	}
	if status.confirmed != "busy" {
		t.Fatal("health replaced explicit status", status.confirmed)
	}
	before := p.calls
	if out, _ := b.MessagesWatch(ctx, gateway.MessagesWatchInput{Health: "peer text"}); out.Outcome.String() != "invalid" || p.calls != before {
		t.Fatal("unbounded health dispatched", out)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if out, _ := b.MessagesWatch(canceled, gateway.MessagesWatchInput{Health: "watching"}); out.Outcome.String() != "unavailable" || p.calls != before || status.watchHealth != "stopped_protocol" {
		t.Fatal("canceled health changed status", out)
	}
	if d.record.WatchHealth.Claim != "stopped_protocol" {
		t.Fatal("canceled health changed C1")
	}
	a.mu.Lock()
	a.ready = false
	a.final = true
	a.mu.Unlock()
	if out, _ := b.MessagesWatch(ctx, gateway.MessagesWatchInput{Health: "stopped_forbidden"}); out.Outcome.String() != "unavailable" || p.calls != before || status.watchHealth != "stopped_protocol" || len(p.statuses) != 2 {
		t.Fatal("unadmitted health projected or dispatched", out)
	}
	if d.record.WatchHealth.Claim != "stopped_forbidden" {
		t.Fatal("local untrusted claim missing after admission loss")
	}
}

func TestMessagesWatchHealthPreservesUnknownStatusReplayAndBase(t *testing.T) {
	p := &watchHealthProbe{messagesProbe: new(messagesProbe)}
	s := &messagesInboxStatus{confirmed: "busy"}
	if !s.recordWatchHealth(context.Background(), "recovering") {
		t.Fatal("claim refused")
	}
	p.openErr = context.DeadlineExceeded
	if _, err := s.open(p, "own", "ctx", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if !s.recordWatchHealth(context.Background(), "stopped_protocol") {
		t.Fatal("claim refused")
	}
	p.openErr = nil
	if _, err := s.open(p, "own", "ctx", nil); err != nil {
		t.Fatal(err)
	}
	if len(p.statuses) != 2 || p.statuses[0] != p.statuses[1] || !strings.Contains(p.statuses[1], "recovering") {
		t.Fatal("unknown renewal changed status", p.statuses)
	}
	if _, err := s.open(p, "own", "ctx", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.statuses[2], "protocol invalid") || s.confirmed != "busy" {
		t.Fatal(p.statuses, s.confirmed)
	}
	if got := messagesWatchHealthStatus(strings.Repeat("界", 200), "stopped_protocol"); len([]rune(got)) != 200 || !strings.Contains(got, "host claim") {
		t.Fatal(got)
	}
}
