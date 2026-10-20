package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"

	asks "github.com/openabstractions/abstraction-asks/go"
	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-identity/remote"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

type messagePeerFacts struct {
	path   string
	raw    []byte
	ids    []fileid.StoredFileID
	active map[string]bool
}

func (f *messagePeerFacts) same(other *messagePeerFacts) bool {
	return f != nil && other != nil && f.path == other.path && bytes.Equal(f.raw, other.raw) && slices.Equal(f.ids, other.ids)
}

func (r *runtimeRights) refreshMessagePeerFacts(ctx context.Context) {
	if r.messagePairing == nil || r.applications == nil {
		return
	}
	s := r.messagePairing
	var facts *messagePeerFacts
	if !s.mu.TryLock() {
		return // Busy discovery supplies no authoritative change to peer metadata.
	}
	{
		d, raw, err := s.read(ctx)
		if err == nil {
			facts = &messagePeerFacts{path: s.path, raw: raw, ids: []fileid.StoredFileID{s.rootID, s.parentID, s.fileID}, active: map[string]bool{}}
			for key, peer := range d.Peers {
				if peer.State == "active" {
					facts.active[key] = true
				}
			}
		}
		s.mu.Unlock()
	}
	if facts == nil {
		return // The prepared permit still revalidates canonical bytes and identity.
	}
	d := r.applications
	for {
		roles := d.roles.Load()
		if roles == nil {
			return
		}
		if facts == nil && roles.messagePeers == nil {
			return
		}
		if roles.messagePeers.same(facts) {
			return
		}
		updated := *roles
		updated.messagePeers = facts
		if d.roles.CompareAndSwap(roles, &updated) {
			return
		}
	}
}

func (r *runtimeRights) prepareMessagePeerPermit(ctx context.Context, subject wire.Subject, key string) (func() error, error) {
	if r.applications == nil || remote.ValidSubjectProgram(subject.Program) {
		return nil, errors.New("messages: outbound peer target unavailable")
	}
	roles := r.applications.roles.Load()
	if roles == nil || roles.messagePeers == nil || !roles.messagePeers.active[key] {
		return nil, errPairingState
	}
	facts := roles.messagePeers
	// The policy commit guard uses the exact prepared canonical input without
	// taking the pairing mutex under Rights. Pairing's own edits acquire the
	// pairing owner before Rights; a reverse lock order would deadlock.
	return func() error {
		// Registration and enrollment publish this shared projection too. Fence
		// the exact pairing input; their independent facts own separate guards.
		currentRoles := r.applications.roles.Load()
		if currentRoles == nil || currentRoles.messagePeers == nil || !currentRoles.messagePeers.active[key] || !facts.same(currentRoles.messagePeers) {
			return errPairingRevision
		}
		for _, id := range facts.ids {
			_, state, e := fileid.Revalidate(ctx, id)
			if e != nil || state != fileid.StateSame {
				return errPairingState
			}
		}
		current, e := cas.ReadLimitContext(ctx, facts.path, pairingStateLimit)
		if e != nil || !bytes.Equal(current, facts.raw) {
			return errPairingRevision
		}
		for _, id := range facts.ids {
			_, state, e := fileid.Revalidate(ctx, id)
			if e != nil || state != fileid.StateSame {
				return errPairingState
			}
		}
		return ctx.Err()
	}, nil
}

func (r *messagesReceiver) messageQuestion(subject wire.Subject, resource, peerKey, label, claim string, questionID *string) (bool, error) {
	return r.host.pairedPermissionQuestion(subject, ActionApplicationMessage, resource, peerKey, label, claim, questionID)
}

// pairedPermissionQuestion asks the person on this computer once for a
// paired subject's exact permit, in a question naming the paired computer.
// A send permit keeps its established question key; another action's key
// also names its resource, so two receivers never share one question.
func (h *applicationsHost) pairedPermissionQuestion(subject wire.Subject, action, resource, peerKey, label, claim string, questionID *string) (bool, error) {
	if h.directory.agentQuestions == nil || h.directory.agentQuestions.a == nil {
		return false, errors.New("messages: person-only question owner unavailable")
	}
	a := h.directory.agentQuestions.a
	if a.rights == nil || a.rights.policy == nil {
		return false, errors.New("messages: permission owner unavailable")
	}
	permission, err := asks.NewPermissionRequest(asks.PermissionTarget{Account: subject.Account, Program: subject.Program, Action: action, Resource: resource})
	if err != nil {
		return false, err
	}
	base := "message-peer:" + messageDigest(subject.Program+"\x00"+peerKey+"\x00"+action)
	if action != ActionApplicationMessage {
		base = "message-peer:" + messageDigest(subject.Program+"\x00"+peerKey+"\x00"+action+"\x00"+resource)
	}
	for generation := 0; generation < maxFirstUseGenerations; generation++ {
		key := fmt.Sprintf("%s:%d", base, generation)
		record, outcome, e := a.book.ObserveApplication(a.scope, key)
		if e != nil {
			return false, e
		}
		if outcome == "answered" {
			again, e := a.asksAgain(record)
			if e != nil {
				return false, e
			}
			if !again {
				return false, nil
			}
			continue
		}
		if outcome == "pending" {
			retained, ok := asks.PermissionForRecord(subject.Account, record)
			if !ok || len(retained.Targets) != 1 || retained.Targets[0] != permission.Targets[0] {
				return false, errors.New("messages: another target owns pending peer/action question")
			}
			if questionID != nil {
				*questionID = record.ID
			}
			return true, nil
		}
		if outcome != "unknown" {
			continue
		}
		display := a.display
		if remote.ValidSubjectProgram(subject.Program) {
			display = fmt.Sprintf("Paired computer %q.", nativeMessageHeader(label))
			if claim != "" {
				display = fmt.Sprintf("Paired computer %q. Unverified program claim %q.", nativeMessageHeader(label), nativeMessageHeader(claim))
			}
		}
		record, outcome, e = a.book.AskPermissionFrom(a.scope, display, key, permission, listen.Seen{Why: "paired Messages service exact first use"}, asks.Publisher{})
		if e != nil {
			return false, e
		}
		if questionID != nil {
			*questionID = record.ID
		}
		return outcome == "pending" && record.ID != "", nil
	}
	return false, errors.New("messages: peer question generation bound")
}
