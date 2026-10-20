package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/mcp-gateway/cacheproducer"
)

// Native producer facts are injected here. The producer corpus independently
// exercises the actual receipt, vendor registry and cache readback owners.
type agentCompanionFixture struct {
	current           bool
	fail              bool
	changed           string
	backupUnavailable bool
}

func (p *agentCompanionFixture) Read(_ context.Context, program, key string) ([]byte, error) {
	return json.Marshal([]string{program, key + p.changed})
}

func TestAgentMaintenanceIdempotentKeepPreservesCompanionMembership(t *testing.T) {
	run, request := agentMaintenanceFixture(t)
	member := request.Members[0]
	before := companionDeclarationDigest(run.d.unconfirmed[member.Application].Agent.Companions)
	run.d.agentCompanionProducer.(*agentCompanionFixture).changed = "different membership"
	agent1478Set(t, run, member.Application, member.ExpectedRevision, true)
	if got := companionDeclarationDigest(run.d.unconfirmed[member.Application].Agent.Companions); got != before {
		t.Fatal("idempotent Keep replaced original membership")
	}
	refused := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if refused.Outcome != applications.OutcomeConflict {
		t.Fatal("changed declaration reused original Keep", refused)
	}
}

func TestAgentMaintenanceRevocationBeforeReadyWins(t *testing.T) {
	run, request := agentMaintenanceFixture(t)
	prepared := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	publishAgentUpdateFixture(t, request)
	committed := run.d.commitAgentUpdate(run.ctx, run.f.panel, prepared.Intent)
	if len(committed.Enrollments) == 0 {
		t.Fatal(committed)
	}
	entry := committed.Enrollments[0]
	agent1478Set(t, run, entry.Application, entry.Revision, false)
	run.d.agentCompanionProducer.(*agentCompanionFixture).current = true
	got := run.d.commitAgentUpdate(run.ctx, run.f.panel, prepared.Intent)
	if got.Phase != nil && *got.Phase == applications.AgentUpdatePhaseReady {
		t.Fatal("revocation admitted Ready", got)
	}
	notices, _, err := run.f.a.book.NotificationHistory()
	if err != nil || len(notices) != 0 {
		t.Fatal("revocation emitted notice", notices, err)
	}
}
func (p *agentCompanionFixture) Successor(ctx context.Context, _ []byte, program, key string) ([]byte, error) {
	return p.Read(ctx, program, key)
}
func (p *agentCompanionFixture) Rebind(ctx context.Context, _ []byte, program, key string) ([]byte, error) {
	return p.Read(ctx, program, key)
}
func (p *agentCompanionFixture) CaptureBackup(_ context.Context, raw []byte, id string, authorize func() error) ([]byte, error) {
	if p.backupUnavailable {
		return nil, errors.New("unsupported closed recipe or backup capacity")
	}
	if err := authorize(); err != nil {
		return nil, err
	}
	return json.Marshal([]string{id, string(raw)})
}

func TestAgentMaintenanceMissingBackupRetainsFreshNativeReview(t *testing.T) {
	run, request := agentMaintenanceFixture(t)
	run.d.agentCompanionProducer.(*agentCompanionFixture).backupUnavailable = true
	got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if got.Outcome != applications.OutcomeApplied || got.Reason != "permission_pending" || got.Question == "" {
		t.Fatal("missing retention silently authorized maintenance", got)
	}
	retry := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if retry.Intent != got.Intent || retry.Question != got.Question {
		t.Fatal("missing retention renewed native review", retry)
	}
	for _, member := range request.Members {
		current := run.d.currentAgentEnrollment(run.ctx, member.Application)
		if current.Enrollment == nil || current.Enrollment.Revision != member.ExpectedRevision {
			t.Fatal("review changed existing Keep", current)
		}
	}
}
func (*agentCompanionFixture) ValidateBackup(raw []byte) error {
	if !json.Valid(raw) {
		return errors.New("fixture backup invalid")
	}
	return nil
}
func (p *agentCompanionFixture) RestoreBackup(_ context.Context, _, _, _ []byte, authorize func() error) error {
	if err := authorize(); err != nil {
		return err
	}
	p.current = true
	return nil
}
func (*agentCompanionFixture) Validate(raw []byte) error {
	var v []string
	if json.Unmarshal(raw, &v) != nil || len(v) != 2 || v[0] == "" || v[1] == "" {
		return errors.New("fixture declaration missing")
	}
	return nil
}
func (p *agentCompanionFixture) Observe(context.Context, []byte) (cacheproducer.CopyReading, error) {
	if p.fail {
		return cacheproducer.CopyReading{}, errors.New("native readback unavailable")
	}
	if p.current {
		return cacheproducer.CopyReading{State: cacheproducer.CopyCurrent}, nil
	}
	return cacheproducer.CopyReading{}, nil
}

func TestAgentMaintenanceReadyRequiresActualCompanionReadback(t *testing.T) {
	run, request := agentMaintenanceFixture(t)
	prepared := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if prepared.Reason != "update_ready" {
		t.Fatal(prepared)
	}
	publishAgentUpdateFixture(t, request)
	before := run.d.commitAgentUpdate(run.ctx, run.f.panel, prepared.Intent)
	if before.Phase == nil || *before.Phase != applications.AgentUpdatePhaseCommitted || before.ReadyUnixMs != nil {
		t.Fatal(before)
	}
	producer := run.d.agentCompanionProducer.(*agentCompanionFixture)
	producer.current = true
	ready := run.d.commitAgentUpdate(run.ctx, run.f.panel, prepared.Intent)
	if ready.Phase == nil || *ready.Phase != applications.AgentUpdatePhaseReady || ready.ReadyUnixMs == nil {
		t.Fatal(ready)
	}
	encoded := applications.EncodeAgentUpdateResult(&ready)
	if !bytes.Contains(encoded, []byte(`"ready_unix_ms"`)) || bytes.Contains(encoded, []byte(`"ready_at"`)) {
		t.Fatal("ready result has the wrong time field", string(encoded))
	}
	if _, err := applications.DecodeAgentUpdateResult(bytes.ReplaceAll(encoded, []byte(`"ready_unix_ms"`), []byte(`"ready_at"`))); err == nil {
		t.Fatal("unpublished result accepted the superseded time field")
	}
	again := run.d.commitAgentUpdate(run.ctx, run.f.panel, prepared.Intent)
	if again.ReadyUnixMs == nil || *again.ReadyUnixMs != *ready.ReadyUnixMs {
		t.Fatal("Ready replay changed time", again)
	}
	notices, _, err := run.f.a.book.NotificationHistory()
	if err != nil || len(notices) != 1 {
		t.Fatal("Ready must own one notification", len(notices), err)
	}
	producer.fail = true
	lost := run.d.commitAgentUpdate(run.ctx, run.f.panel, prepared.Intent)
	if lost.Outcome != applications.OutcomeUnavailable {
		t.Fatal("lost readback retained actionable Ready", lost)
	}
}
