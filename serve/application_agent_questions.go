package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"sync"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/listen"
)

type agentEnrollmentQuestions struct {
	a         *runtimeAsks
	d         *applicationDirectory
	ctx       context.Context
	mu        sync.Mutex
	following map[string]bool
}

// Both components retain their full identity through the digest. The normal
// runtime scope is already a digest namespace; appending its generation
// exceeded the Book's existing 128-byte admission-scope bound.
func agentEnrollmentScope(runtimeScope, revision string) string {
	sum := sha256.Sum256([]byte(runtimeScope + "\x00agent-enrollment\x00" + revision))
	return "agent-enrollment:" + hex.EncodeToString(sum[:])
}

// Only an explicit operator Set may use this result to rotate an expired or
// retired pending intent. Book reads happen after all directory locks release.
func (d *applicationDirectory) expiredAgentQuestion(ctx context.Context, request applications.AgentEnrollmentRequest, image applicationAgentImage) (string, string, error) {
	if !request.Enabled || d.agentQuestions == nil {
		return "", "", nil
	}
	if !d.lock(ctx) {
		return "", "", ctx.Err()
	}
	var candidate selfMark
	err := d.withRegistrationReadLocked(ctx, func(_ map[string]wire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		m := marks[request.Application]
		if !m.Removing && m.Kind == "agent" && m.Agent != nil && m.Agent.State == "pending" && m.Question != "" && m.ProgramKey == image.Key && m.Agent.Binding == image.Binding && (request.ExpectedRevision == m.RegistrationRevision || request.ExpectedRevision == m.Agent.BaseRevision) {
			candidate = m
		}
		return nil
	})
	d.unlock()
	if err != nil || candidate.Question == "" {
		return "", "", err
	}
	_, found, err := d.agentQuestions.a.book.ApplicationRecord(candidate.Question)
	if err != nil || found {
		return "", "", err
	}
	return candidate.RegistrationRevision, candidate.Question, nil
}

func (a *runtimeAsks) keepAgentEnrollments(ctx context.Context, d *applicationDirectory) {
	k := &agentEnrollmentQuestions{a: a, d: d, ctx: ctx, following: map[string]bool{}}
	d.mu.Lock()
	d.agentQuestions = k
	names := []string{}
	for name, m := range d.unconfirmed {
		if m.Kind == "agent" && m.Agent != nil && m.Agent.State == "pending" {
			names = append(names, name)
		}
	}
	d.unlock()
	for _, name := range names {
		if err := k.ask(name); err != nil {
			a.report(err)
		}
	}
}

func (k *agentEnrollmentQuestions) ask(name string) error {
	k.d.mu.Lock()
	m, ok := k.d.unconfirmed[name]
	k.d.unlock()
	if !ok || m.Kind != "agent" || m.Agent == nil || m.Agent.State != "pending" {
		return nil
	}
	if m.Question != "" {
		r, found, err := k.a.book.ApplicationRecord(m.Question)
		if err != nil {
			return err
		}
		if !found {
			// Reads/startup/resume never renew an expired intent. A fresh
			// authorized Set rotates it under the registration CAS owner.
			return nil
		}
		if r.Key != asks.AgentEnrollmentKey {
			return errors.New("agent enrollment: recorded question unavailable")
		}
		if r.Pending() {
			k.follow(name, m.RegistrationRevision, r.ID)
			return nil
		}
		return k.d.applyAgentAnswer(k.ctx, name, m.RegistrationRevision, r)
	}
	// The durable generation makes a removed/recreated registration a distinct
	// trusted Asks scope, even when its app-written title/program are identical.
	v, binding, err := k.d.agentSnapshot(k.ctx, name)
	if err != nil {
		return err
	}
	if binding != m.Agent.Binding || v.Program != m.Program {
		return errors.New("agent enrollment: registration changed")
	}
	scope := agentEnrollmentScope(k.a.scope, m.RegistrationRevision)
	r, out, err := k.a.book.AskApplicationFrom(scope, k.a.display, "agent-enrollment:"+m.RegistrationRevision, asks.AgentEnrollmentKey, map[string]string{"program": selfRegisteredSlot(m.Program), "title": selfRegisteredTitle(v)}, listen.Seen{Why: "explicit operator request for person-kept native agent enrollment"}, m.publisher())
	if err != nil {
		return err
	}
	if (out != "pending" && out != "answered") || r.ID == "" || r.Key != asks.AgentEnrollmentKey {
		return errors.New("agent enrollment: question unavailable")
	}
	if !k.d.lock(k.ctx) {
		return k.ctx.Err()
	}
	err = k.d.withRegistrationStorageLocked(k.ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, base, err := k.d.readSelfMarksAtDescriptorLocked(k.ctx)
		if err != nil {
			return err
		}
		current := marks[name]
		_, nowBinding, found := k.d.agentEntryLocked(legacy, name)
		if !found || current.Kind != "agent" || current.Agent.State != "pending" || current.RegistrationRevision != m.RegistrationRevision || current.Agent.Binding != nowBinding || current.Question != "" && current.Question != r.ID {
			return errors.New("agent enrollment: stale question")
		}
		current.Question = r.ID
		next := maps.Clone(marks)
		next[name] = current
		k.d.selfBase = base
		return k.d.persistUnconfirmedAtDescriptorLocked(k.ctx, next, legacy)
	})
	k.d.unlock()
	if err != nil {
		return err
	}
	// Admission may have committed and received a person answer before the
	// metadata write (or a prior process may have stopped between those writes).
	// Bind the exact occurrence first, then consume its retained decision.
	if r.Pending() {
		k.follow(name, m.RegistrationRevision, r.ID)
		return nil
	}
	return k.d.applyAgentAnswer(k.ctx, name, m.RegistrationRevision, r)
}

func (k *agentEnrollmentQuestions) follow(name, revision, id string) {
	k.mu.Lock()
	if k.following[id] {
		k.mu.Unlock()
		return
	}
	k.following[id] = true
	k.mu.Unlock()
	decided := k.a.book.Decided(id)
	go func() {
		defer func() { k.mu.Lock(); delete(k.following, id); k.mu.Unlock() }()
		select {
		case <-k.ctx.Done():
			return
		case <-decided:
		}
		r, found, err := k.a.book.ApplicationRecord(id)
		if err == nil && found {
			err = k.d.applyAgentAnswer(k.ctx, name, revision, r)
		}
		if err != nil {
			k.a.report(err)
		}
	}()
}
