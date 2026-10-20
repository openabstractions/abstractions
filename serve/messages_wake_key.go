package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
)

// A token-bound sidecar in the existing states directory retains first visible
// eligibility order. Existing message/state bytes stay readable by old binaries.
// Retained records acquire it lazily when first observed eligible. A recoverable
// reservation in the existing sequences directory publishes the prior item's
// state before another order can be reserved. Acceptance ID reservations and
// wall-clock milliseconds do not determine visibility order.
type messageWakeReservation struct {
	Key, Token, Binding string
	Order               uint64
}

type messageWakeOrder struct {
	Binding string
	Order   uint64
}

func (s *messageStore) wakeOrderPath(key, token string) string {
	return filepath.Join(s.dir, "states", key+"-"+token+"-wake.json")
}

func readMessageWakeOrder(raw []byte, binding string) (uint64, error) {
	if raw == nil {
		return 0, nil
	}
	var order messageWakeOrder
	if err := messageStoredJSON(raw, &order); err != nil {
		return 0, err
	}
	if order.Binding != binding || order.Order == 0 {
		return 0, errors.New("messages: invalid token-bound wake order")
	}
	return order.Order, nil
}

func messageWakeBinding(actor messageActor) string {
	return messageDigest(actor.Account + "\x00" + actor.Program + "\x00" + actor.Context)
}

func (s *messageStore) publishWakeOrder(ctx context.Context, binding string, pending messageWakeReservation) error {
	r, err := s.readRecord(ctx, pending.Key)
	if err != nil {
		return errors.New("messages: wake reservation lost canonical acceptance")
	}
	if r == nil || r.Token != pending.Token || r.ExpiresUnixMs <= s.now().UnixMilli() {
		// Expired acceptance can disappear during crash recovery. Its reserved
		// ordinal remains consumed; it never targets a replacement record.
		return nil
	}
	err = cas.ChangeLimitContext(ctx, s.wakeOrderPath(pending.Key, pending.Token), 1024, func(raw []byte) ([]byte, error) {
		current, err := s.readRecord(ctx, pending.Key)
		if err != nil {
			return nil, err
		}
		if current == nil || current.Token != pending.Token || current.ExpiresUnixMs <= s.now().UnixMilli() {
			return raw, nil
		}
		order, err := readMessageWakeOrder(raw, binding)
		if err != nil {
			return nil, err
		}
		if order != 0 && order != pending.Order {
			return nil, errors.New("messages: wake reservation conflicts")
		}
		return json.Marshal(messageWakeOrder{Binding: binding, Order: pending.Order})
	})
	if errors.Is(err, cas.ErrNoValue) {
		return nil
	}
	return err
}

func (s *messageStore) expireWakeOrder(ctx context.Context, key, token string) error {
	for _, path := range []string{s.wakeOrderPath(key, token), s.wakeShownPath(key, token)} {
		release, err := cas.LockContext(ctx, path, false)
		if err != nil {
			return err
		}
		err = os.Remove(path)
		release()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// A token-bound sidecar beside the wake order records that a body Wait
// answered this message's inbox owner with it (architect ruling 2026-11-09).
// A shown message projects no Page.wake for the 30-minute wake lease. One
// still unacknowledged after the lease wakes once more; once a Wait hands it
// again it wakes nothing further. ACK, unread count and delivery stay with
// the cursor's after.
type messageWakeShown struct {
	Binding string
	At      int64
	Rewoken bool `json:",omitempty"`
}

const messageWakeShownLease = 30 * time.Minute

func (s *messageStore) wakeShownPath(key, token string) string {
	return filepath.Join(s.dir, "states", key+"-"+token+"-shown.json")
}

func (s *messageStore) markWakeShown(ctx context.Context, binding, key, token string) error {
	now := s.now().UnixMilli()
	err := cas.ChangeLimitContext(ctx, s.wakeShownPath(key, token), 1024, func(raw []byte) ([]byte, error) {
		if raw != nil {
			var shown messageWakeShown
			if err := messageStoredJSON(raw, &shown); err == nil && shown.Binding == binding {
				// A hand-over after the lease spent the one later wake.
				if !shown.Rewoken && now-shown.At >= messageWakeShownLease.Milliseconds() {
					shown.Rewoken, shown.At = true, now
					return json.Marshal(shown)
				}
				return raw, nil
			}
		}
		// An expired acceptance gets no sidecar its expiry would not remove.
		current, err := s.readRecord(ctx, key)
		if err != nil {
			return nil, err
		}
		if current == nil || current.Token != token || current.ExpiresUnixMs <= now {
			return raw, nil
		}
		return json.Marshal(messageWakeShown{Binding: binding, At: now})
	})
	if errors.Is(err, cas.ErrNoValue) {
		return nil
	}
	return err
}

// handedToReceiver reports that a body Wait returned this acceptance to its
// receiving inbox's native owner: markWakeShown writes the token-bound mark
// only after that Wait's final ownership recheck, for the record's own
// receiving binding, and expiry removes it after the terminal delivery.
func (s *messageStore) handedToReceiver(ctx context.Context, key, token string) bool {
	raw, err := cas.ReadLimitContext(ctx, s.wakeShownPath(key, token), 1024)
	if err != nil || raw == nil {
		return false
	}
	var shown messageWakeShown
	return messageStoredJSON(raw, &shown) == nil && shown.Binding != "" && shown.At > 0
}

// wakeShown reports whether a shown mark suppresses this message's wake now.
// A missing, unreadable or foreign mark suppresses nothing.
func (s *messageStore) wakeShown(ctx context.Context, binding, key, token string) bool {
	raw, err := cas.ReadLimitContext(ctx, s.wakeShownPath(key, token), 1024)
	if err != nil || raw == nil {
		return false
	}
	var shown messageWakeShown
	if err := messageStoredJSON(raw, &shown); err != nil || shown.Binding != binding {
		return false
	}
	return shown.Rewoken || s.now().UnixMilli()-shown.At < messageWakeShownLease.Milliseconds()
}

func (s *messageStore) wakeForRecord(ctx context.Context, actor messageActor, key string, r *messageRecord) (string, error) {
	binding := messageWakeBinding(actor)
	// A record keeps its token-bound order once published. Every headers
	// read reads it without taking the store owner's lock; only a record
	// without an order reserves one under that lock.
	orderRaw, err := cas.ReadLimitContext(ctx, s.wakeOrderPath(key, r.Token), 1024)
	if err != nil {
		return "", err
	}
	storedOrder, err := readMessageWakeOrder(orderRaw, binding)
	if err != nil {
		return "", err
	}
	if storedOrder != 0 {
		return s.wakeKey(binding, storedOrder, key, r), nil
	}
	var reservation messageWakeReservation
	var order uint64
	err = s.changeOwner(ctx, func(head *messageStoreOwner) error {
		if head.Pending != nil {
			p := *head.Pending
			if !agentHex(p.Key, 32) || !agentHex(p.Token, 24) || !agentHex(p.Binding, 32) || p.Order == 0 || p.Order != *head.Next {
				return errors.New("messages: invalid pending wake order")
			}
			if err := s.publishWakeOrder(ctx, p.Binding, p); err != nil {
				return err
			}
			head.Pending = nil
		}
		orderRaw, err := cas.ReadLimitContext(ctx, s.wakeOrderPath(key, r.Token), 1024)
		if err != nil {
			return err
		}
		storedOrder, err := readMessageWakeOrder(orderRaw, binding)
		if err != nil {
			return err
		}
		if storedOrder != 0 {
			if storedOrder > *head.Next {
				return errors.New("messages: wake order exceeds durable sequence")
			}
			order = storedOrder
			return nil
		}
		if *head.Next == ^uint64(0) {
			return errors.New("messages: wake sequence exhausted")
		}
		*head.Next++
		reservation = messageWakeReservation{Key: key, Token: r.Token, Binding: binding, Order: *head.Next}
		head.Pending = &reservation
		order = *head.Next
		return nil
	})
	if err != nil {
		return "", err
	}
	if reservation.Order != 0 {
		if err := s.at("wake_order_reserved"); err != nil {
			return "", err
		}
		if err := s.publishWakeOrder(ctx, binding, reservation); err != nil {
			return "", err
		}
	}
	return s.wakeKey(binding, order, key, r), nil
}

func (s *messageStore) wakeKey(binding string, order uint64, key string, r *messageRecord) string {
	wake := fmt.Sprintf("%s-%020d", binding, order)
	s.indexWake(wake, messageWakeTarget{key: key, token: r.Token, expires: r.ExpiresUnixMs})
	return wake
}
