package main

import (
	"context"
	"errors"
	"fmt"

	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
)

// Only the held process identity can associate an announcement with a channel.
// A matching executable name, PID reused by a later process, or request field
// supplies no such association.
func (g *applicationV3Gateway) announcingSession(ctx context.Context, c applicationV3Caller, application string) (*ownedApplicationSession, *identity.Peer) {
	if c.peer == nil {
		return nil, nil
	}
	g.mu.Lock()
	sessions := make([]*ownedApplicationSession, 0, len(g.owned))
	for _, s := range g.owned {
		sessions = append(sessions, s)
	}
	g.mu.Unlock()
	for _, s := range sessions {
		peer, err := s.announcementPeer(ctx, c.peer, c.matcher)
		if err != nil || peer == nil {
			continue
		}
		p, err := peer.Process.AtLeast(identity.ProofBound)
		if err != nil {
			continue
		}
		desktop, ok := applicationSession(peer)
		if !ok {
			continue
		}
		b := s.bindingValue()
		if b.application == application && b.account == c.subject.Account && b.desktop == desktop && b.runtimeEpoch == g.epoch && s.matchesAnnouncementProcess(b, p) {
			select {
			case <-s.done:
				continue
			default:
				return s, peer
			}
		}
	}
	return nil, nil
}

func (g *applicationV3Gateway) announce(ctx context.Context, c applicationV3Caller, key string, presence legacy.ApplicationPresence) legacy.ApplicationChange {
	presence = fitApplicationPresence(presence)
	if !c.valid(g.directory.owner) || !validApplicationPresence(presence) {
		return appChange(legacy.ApplicationOutcomeForbidden, "caller")
	}
	k, err := g.load(ctx, c.peer, presence.Application)
	if err != nil {
		return appChange(legacy.ApplicationOutcomeUnavailable, "manifest")
	}
	defer k.close()
	if presence.Instance == "" && g.directory.unsignedLaunch != nil && samePrograms(k.program, c.subject.Program) {
		// A first announcement is the program starting; a lease renewal names
		// its instance.
		g.directory.unsignedLaunch(presence.Application, false)
	}
	quiet := func() error { return g.quiet(ctx, c, k, ActionApplicationAnnounce) }
	if !samePrograms(k.program, c.subject.Program) || quiet() != nil {
		return appChange(legacy.ApplicationOutcomeForbidden, "rights")
	}
	// A child can connect immediately after Start, before its native owner has
	// published the retained process handle. Wait on that finite start attempt;
	// do not classify it from its program spelling while the handle is pending.
	g.mu.Lock()
	starting := g.starting[presence.Application]
	g.mu.Unlock()
	if starting != nil {
		select {
		case <-starting:
		case <-ctx.Done():
			return appChange(legacy.ApplicationOutcomeUnavailable, "starting")
		}
	}
	s, announcementPeer := g.announcingSession(ctx, c, presence.Application)
	if announcementPeer == nil {
		announcementPeer = c.peer
	}
	desktop, ok := applicationSession(announcementPeer)
	if !ok {
		return appChange(legacy.ApplicationOutcomeForbidden, "session")
	}
	if s != nil {
		// Serialize the directory allocation and the one-time session bind.
		// Concurrent empty announcements cannot allocate competing Core handles.
		s.announceMu.Lock()
		defer s.announceMu.Unlock()
		b := s.bindingValue()
		if b.instance == "" && presence.Instance != "" {
			return appChange(legacy.ApplicationOutcomeStale, "instance")
		}
		if b.manifestRevision != k.revision || (b.instance != "" && presence.Instance != "" && presence.Instance != b.instance) {
			return appChange(legacy.ApplicationOutcomeStale, "instance")
		}
		if err := s.guard(ctx, b); err != nil {
			return appChange(legacy.ApplicationOutcomeUnavailable, "process")
		}
	}
	p, _ := c.peer.Process.Get()
	canonical := legacy.EncodeApplicationPresence(&presence)
	occurrence := fmt.Sprintf("%s/%d/%d/%d", desktop, p.PID, p.StartTime.UnixNano(), p.Generation)
	if s != nil {
		b := s.bindingValue()
		occurrence = fmt.Sprintf("%s/%d/%d/%d", b.desktop, b.pid, b.started.UnixNano(), b.generation)
	}
	binding := applicationV3ReceiptBinding{Application: presence.Application, Manifest: k.digest, Revision: k.revision, RuntimeGeneration: g.epoch, Instance: presence.Instance, InstanceEpoch: occurrence}
	previous, token, err := g.receipts.admit(ctx, applicationV3ReceiptRequest{Scope: applicationV3ReceiptScope{Account: c.subject.Account, Program: c.subject.Program, Method: "Announce"}, Key: key, CanonicalBody: canonical, Binding: binding})
	if err != nil {
		if errors.Is(err, errApplicationV3ReceiptConflict) {
			return appChange(legacy.ApplicationOutcomeInvalid, "key")
		}
		return appChange(legacy.ApplicationOutcomeUnavailable, "receipt")
	}
	if token == nil {
		if previous.State != "complete" || quiet() != nil {
			return appChange(legacy.ApplicationOutcomeUnavailable, "unknown")
		}
		result, err := legacy.DecodeApplicationChange(previous.Reply)
		if err != nil {
			return appChange(legacy.ApplicationOutcomeUnavailable, "receipt")
		}
		// Replay supplies no new process binding. Only the original, still-live
		// session may observe its already-frozen handle as owned.
		if s != nil && result.Outcome == legacy.ApplicationOutcomeApplied {
			b := s.bindingValue()
			if b.instance != result.Instance || s.guard(ctx, b) != nil {
				return appChange(legacy.ApplicationOutcomeUnavailable, "unknown")
			}
		}
		return *result
	}
	if quiet() != nil {
		return appChange(legacy.ApplicationOutcomeUnavailable, "authority")
	}
	if s != nil && s.bindingValue().instance != "" && presence.Instance == "" {
		result := appChange(legacy.ApplicationOutcomeStale, "instance")
		if err := g.receipts.finish(ctx, *token, legacy.EncodeApplicationChange(&result)); err != nil {
			return appChange(legacy.ApplicationOutcomeUnavailable, "receipt")
		}
		return result
	}
	result := g.directory.announceInSession(ctx, c.subject, desktop, presence)
	if s != nil && result.Outcome == legacy.ApplicationOutcomeApplied {
		if err := s.bindAnnouncedInstance(ctx, c.peer, presence.Application, result.Instance, c.recheck, c.matcher); err != nil {
			// The allocated lease remains an untrusted presence; a failed bind
			// cannot authorize a channel or be retried as a second effect.
			s.Close()
			result = appChange(legacy.ApplicationOutcomeUnavailable, "unknown")
		}
	}
	if err := g.receipts.finish(ctx, *token, legacy.EncodeApplicationChange(&result)); err != nil {
		return appChange(legacy.ApplicationOutcomeUnavailable, "receipt")
	}
	if quiet() != nil {
		return appChange(legacy.ApplicationOutcomeUnavailable, "authority")
	}
	return result
}
