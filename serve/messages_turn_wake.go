package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// AB-1s architect ruling 2026-11-09, open-chat wake chains. An open chat's
// wake is a key serve projected on that inbox's own headers. When the gateway
// confirms that a turn consumed the key's pointer, it states the key through
// TurnWake; every message the inbox sends while that row lives carries the
// chain of the message the key was projected for, and the existing hop limit
// stops two open chats from waking each other forever. A person's turn or the
// turn's end clears the row with an empty key.

// messageWakeIndexed bounds the in-memory key index; past it, expired rows go
// first and then the whole index, which the durable order files back.
const messageWakeIndexed = 4096

var errMessageWakeFuture = errors.New("messages: wake key past the durable sequence")

type messageWakeTarget struct {
	key, token string
	expires    int64
}

// parseMessageWakeKey splits a projected key into its binding and order, the
// shape wakeForRecord writes: 64 hex digits, a hyphen and 20 decimal digits.
func parseMessageWakeKey(wake string) (string, uint64, bool) {
	binding, digits, ok := strings.Cut(wake, "-")
	if !ok || !agentHex(binding, 32) || len(digits) != 20 {
		return "", 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return "", 0, false
		}
	}
	order, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || order == 0 {
		return "", 0, false
	}
	return binding, order, true
}

func (s *messageStore) indexWake(wake string, target messageWakeTarget) {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	if s.wakeIndex == nil {
		s.wakeIndex = map[string]messageWakeTarget{}
	}
	if _, ok := s.wakeIndex[wake]; !ok && len(s.wakeIndex) >= messageWakeIndexed {
		now := s.now().UnixMilli()
		for k, v := range s.wakeIndex {
			if v.expires <= now {
				delete(s.wakeIndex, k)
			}
		}
		if len(s.wakeIndex) >= messageWakeIndexed {
			clear(s.wakeIndex)
		}
	}
	s.wakeIndex[wake] = target
}

func (s *messageStore) indexedWake(wake string) (messageWakeTarget, bool) {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	target, ok := s.wakeIndex[wake]
	return target, ok
}

// scanWakeOrder finds the retained record whose durable wake order file holds
// this binding and order.
func (s *messageStore) scanWakeOrder(ctx context.Context, binding string, order uint64) (messageWakeTarget, bool, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "states"))
	if err != nil {
		return messageWakeTarget{}, false, err
	}
	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), "-wake.json")
		if !ok {
			continue
		}
		key, token, ok := strings.Cut(name, "-")
		if !ok || !messageTargetValid(key, token) || key == "" {
			continue
		}
		raw, err := cas.ReadLimitContext(ctx, s.wakeOrderPath(key, token), 1024)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, cas.ErrNoValue) || raw == nil && err == nil {
			continue
		}
		if err != nil {
			return messageWakeTarget{}, false, err
		}
		var stored messageWakeOrder
		if messageStoredJSON(raw, &stored) != nil || stored.Binding != binding || stored.Order != order {
			continue
		}
		return messageWakeTarget{key: key, token: token}, true, nil
	}
	return messageWakeTarget{}, false, nil
}

// wakeChainFor is the chain a turn woken by this own-binding key carries: the
// chain of the record the key was projected for plus that record's ID. A key
// past the durable sequence was never projected. A key whose record is gone
// sets a chain at the hop limit, so the turn wakes nobody.
func (s *messageStore) wakeChainFor(ctx context.Context, wake, binding string, order uint64) ([]string, error) {
	raw, err := cas.ReadLimitContext(ctx, s.ownerPath(), 4096)
	if err != nil {
		return nil, err
	}
	owner, err := readMessageStoreOwner(raw)
	if err != nil {
		return nil, err
	}
	if owner.Nonce != s.ownerNonce {
		return nil, errors.New("messages: store owner changed")
	}
	if order > *owner.Next {
		return nil, errMessageWakeFuture
	}
	target, found := s.indexedWake(wake)
	if !found {
		if target, found, err = s.scanWakeOrder(ctx, binding, order); err != nil {
			return nil, err
		}
	}
	if found {
		r, err := s.readRecord(ctx, target.key)
		if err != nil {
			return nil, err
		}
		if r != nil && r.Token == target.token && r.ExpiresUnixMs > s.now().UnixMilli() {
			s.indexWake(wake, messageWakeTarget{key: target.key, token: r.Token, expires: r.ExpiresUnixMs})
			chain := messageWakeChain(r)
			if len(chain) > messageWakeHopLimit {
				chain = chain[len(chain)-messageWakeHopLimit:]
			}
			return chain, nil
		}
	}
	return messageWakeLimitChain(wake), nil
}

// messageWakeLimitChain is a stored chain already at the hop limit for a wake
// whose message is no longer retained. Activity shows the key it came from.
func messageWakeLimitChain(wake string) []string {
	return slices.Repeat([]string{"wake:" + wake}, messageWakeHopLimit)
}

// TurnWake records which projected wake started the caller's current turn, or
// ends that turn's chain with an empty key. Admission is Send's: the retained
// native owner of instance and source_context. A key must carry the caller's
// own wake binding, so a peer can neither set nor clear another inbox's chain.
func (r *messagesReceiver) TurnWake(instance, sourceContext, wake string) (msg.TurnWakeResult, error) {
	result := func(outcome msg.Outcome) (msg.TurnWakeResult, error) {
		return msg.TurnWakeResult{Outcome: outcome}, nil
	}
	if r.remote != nil || r.call == nil || r.selectedPeer != "" {
		return result(msg.OutcomeForbidden)
	}
	if r.host == nil || r.host.messages == nil || r.host.directory == nil {
		return result(msg.OutcomeUnavailable)
	}
	var binding string
	var order uint64
	if wake != "" {
		var ok bool
		if binding, order, ok = parseMessageWakeKey(wake); !ok {
			return result(msg.OutcomeInvalid)
		}
	}
	actor, _, _, ok := r.owner(instance, sourceContext)
	if !ok {
		return result(msg.OutcomeForbidden)
	}
	d := r.host.directory
	if wake == "" {
		d.messageWakes.endTurn(actor.Application, actor.Context)
		return result(msg.OutcomePage)
	}
	if binding != messageWakeBinding(actor) {
		return result(msg.OutcomeForbidden)
	}
	chain, err := r.host.messages.wakeChainFor(r.ctx, wake, binding, order)
	if errors.Is(err, errMessageWakeFuture) {
		return result(msg.OutcomeInvalid)
	}
	if err != nil {
		r.reportMessageError("turn wake", err)
		return result(msg.OutcomeUnavailable)
	}
	d.messageWakes.turn(actor.Application, actor.Context, chain, d.now())
	return result(msg.OutcomePage)
}
