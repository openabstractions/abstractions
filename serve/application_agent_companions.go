package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"reflect"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/cacheproducer"
)

// The producer owns the opaque declaration codec, recipe and native readback.
// Applications retains exactly those bytes in its existing Keep/intent CAS.
type agentCompanionProducer interface {
	Read(context.Context, string, string) ([]byte, error)
	Successor(context.Context, []byte, string, string) ([]byte, error)
	Rebind(context.Context, []byte, string, string) ([]byte, error)
	Validate([]byte) error
	Observe(context.Context, []byte) (cacheproducer.CopyReading, error)
}

func committedAgentUpdateCurrent(d *applicationDirectory, legacy map[string]wire.ApplicationDescriptor, marks map[string]selfMark, u *applicationAgentUpdate) bool {
	for _, c := range u.Members {
		v, binding, ok := d.agentEntryLocked(legacy, c.Application)
		m := marks[c.Application]
		if !ok || binding != c.Binding || !applicationRegistrationPrograms(v.Program, c.Old.Program, m.ProgramKey, c.Staged.Key) || m.Agent == nil || m.Removing || m.Agent.Activation != nil || m.Agent.State != "enrolled" || m.Agent.Account != d.owner || m.Agent.Binding != binding || m.ProgramKey != c.Staged.Key || c.CommittedRevision == "" || m.RegistrationRevision != c.CommittedRevision || !bytes.Equal(m.Agent.Companions, c.CompanionsAfter) {
			return false
		}
	}
	return true
}

// Only the producer's actual matching registry/cache readback advances the
// terminal helper Commit to Ready. Calls and acknowledgements supply no proof.
func (d *applicationDirectory) readyAgentUpdate(ctx context.Context, s rights.Subject, u *applicationAgentUpdate, result applications.AgentUpdateResult) applications.AgentUpdateResult {
	goals := make([]json.RawMessage, len(u.Members))
	for i, c := range u.Members {
		goal := c.CompanionsAfter
		if len(goal) == 0 {
			var err error
			goal, err = d.readAgentCompanions(ctx, c.Old.Program, c.Staged.Key)
			if err != nil {
				return result
			}
		}
		if !companionDeclarationBounded(goal) || d.companionProducer().Validate(goal) != nil {
			return result
		}
		reading, err := d.companionProducer().Observe(ctx, goal)
		if err != nil || reading.State != cacheproducer.CopyCurrent {
			if u.ReadyAt > 0 {
				return agentUpdateResult(applications.OutcomeUnavailable, "state")
			}
			return result
		}
		goals[i] = bytes.Clone(goal)
	}
	if !d.lock(ctx) {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	var done *applicationAgentUpdate
	err := d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, base, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		index := agentUpdateIndex(marks)
		stored := index[u.ID]
		anchor := marks[u.Members[0].Application]
		if !reflect.DeepEqual(stored, u) || anchor.Agent == nil || anchor.Agent.Update == nil || anchor.Agent.Update.ID != u.ID || !committedAgentUpdateCurrent(d, legacy, marks, u) {
			return errors.New("agent ready: committed binding changed")
		}
		if d.agentOperator(ctx, s) != wire.ApplicationOutcomeApplied {
			return errors.New("agent ready: operator changed")
		}
		if u.ReadyAt > 0 {
			done = u
			return nil
		}
		copy := *u
		copy.Members = append([]agentUpdateCapture(nil), u.Members...)
		copy.ReadyAt = d.now().UnixMilli()
		if copy.ReadyAt <= 0 {
			return errors.New("agent ready: ordinary service time unavailable")
		}
		if copy.ReadyAt < copy.CommittedMs {
			copy.ReadyAt = copy.CommittedMs
		}
		next := maps.Clone(marks)
		for i, c := range copy.Members {
			copy.Members[i].CompanionsAfter = bytes.Clone(goals[i])
			m := next[c.Application]
			a := *m.Agent
			a.Companions = bytes.Clone(goals[i])
			m.Agent = &a
			next[c.Application] = m
		}
		m := next[copy.Members[0].Application]
		a := *m.Agent
		a.Update = &copy
		m.Agent = &a
		next[copy.Members[0].Application] = m
		d.selfBase = base
		if err := d.persistAgentUpdate(ctx, next, legacy, map[string]bool{u.ID: true}); err != nil {
			return err
		}
		done = &copy
		return nil
	})
	d.unlock()
	if err != nil {
		return agentUpdateResult(applications.OutcomeConflict, "registration")
	}
	phase := applications.AgentUpdatePhaseReady
	result.Phase = &phase
	at := done.ReadyAt
	result.ReadyUnixMs = &at
	d.postAgentUpdateReady(ctx, done)
	return result
}

type nativeAgentCompanionProducer struct{}

var _ agentCompanionBackups = nativeAgentCompanionProducer{}

func (nativeAgentCompanionProducer) CaptureBackup(ctx context.Context, raw []byte, parent string, authorize func() error) ([]byte, error) {
	return cacheproducer.CaptureCompanionBackup(ctx, raw, parent, authorize)
}
func (nativeAgentCompanionProducer) ValidateBackup(raw []byte) error {
	return cacheproducer.ValidateCompanionBackup(raw)
}
func (nativeAgentCompanionProducer) RestoreBackup(ctx context.Context, prior, current, goal []byte, authorize func() error) error {
	return cacheproducer.RestoreCompanionBackup(ctx, prior, current, goal, authorize)
}

func (nativeAgentCompanionProducer) Read(ctx context.Context, program, key string) ([]byte, error) {
	return cacheproducer.ReadCompanionDeclaration(ctx, program, key)
}
func (nativeAgentCompanionProducer) Successor(ctx context.Context, raw []byte, program, key string) ([]byte, error) {
	return cacheproducer.SuccessorCompanionDeclaration(ctx, raw, program, key)
}
func (nativeAgentCompanionProducer) Rebind(ctx context.Context, raw []byte, program, key string) ([]byte, error) {
	return cacheproducer.RebindCompanionDeclaration(ctx, raw, program, key)
}
func (nativeAgentCompanionProducer) Validate(raw []byte) error {
	return cacheproducer.ValidateCompanionDeclaration(raw)
}
func (nativeAgentCompanionProducer) Observe(ctx context.Context, raw []byte) (cacheproducer.CopyReading, error) {
	return cacheproducer.ObserveCompanionDeclaration(ctx, raw)
}

func (d *applicationDirectory) companionProducer() agentCompanionProducer {
	if d.agentCompanionProducer != nil {
		return d.agentCompanionProducer
	}
	return nativeAgentCompanionProducer{}
}

func companionDeclarationBounded(raw []byte) bool {
	return len(raw) > 0 && len(raw) <= cacheproducer.CompanionDeclarationMaxBytes && json.Valid(raw)
}
func companionDeclarationDigest(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (d *applicationDirectory) readAgentCompanions(ctx context.Context, program, key string) (json.RawMessage, error) {
	raw, err := d.companionProducer().Read(ctx, program, key)
	if err != nil {
		return nil, err
	}
	if !companionDeclarationBounded(raw) {
		return nil, errors.New("agent companion declaration unavailable")
	}
	if err := d.companionProducer().Validate(raw); err != nil {
		return nil, err
	}
	return bytes.Clone(raw), nil
}

func (d *applicationDirectory) successorAgentCompanions(ctx context.Context, raw []byte, program, key string) (json.RawMessage, error) {
	if !companionDeclarationBounded(raw) {
		return nil, errors.New("agent prior companion declaration unavailable")
	}
	next, err := d.companionProducer().Successor(ctx, raw, program, key)
	if err != nil {
		return nil, err
	}
	if !companionDeclarationBounded(next) {
		return nil, errors.New("agent successor companion declaration unavailable")
	}
	if err := d.companionProducer().Validate(next); err != nil {
		return nil, err
	}
	return bytes.Clone(next), nil
}
