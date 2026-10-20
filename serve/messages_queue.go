package main

import (
	"context"
	"encoding/json"
	"errors"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"path/filepath"

	cas "github.com/openabstractions/abstraction-cas/go"
)

type messageQueue struct {
	Profile  string
	Pointers []messagePointer
}

func (s *messageStore) queuePath(account, receiver, contextName string) string {
	return filepath.Join(s.dir, "queues", messageDigest(account+"\x00"+receiver+"\x00"+contextName)+".json")
}

func readMessageQueue(raw []byte) (messageQueue, error) {
	q := messageQueue{Profile: "messages-queue@1", Pointers: []messagePointer{}}
	if raw == nil {
		return q, nil
	}
	if err := messageStoredJSON(raw, &q); err != nil || q.Profile != "messages-queue@1" || q.Pointers == nil || len(q.Pointers) > 50 {
		return q, errors.New("messages: invalid inbox queue")
	}
	for _, ref := range q.Pointers {
		if !agentHex(ref.Key, 32) || ref.Token == "" || ref.Alias {
			return q, errors.New("messages: invalid inbox pointer")
		}
	}
	return q, nil
}

func (s *messageStore) recoverQueue(ctx context.Context, q *messageQueue) error {
	out := q.Pointers[:0]
	for _, ref := range q.Pointers {
		r, err := s.recordForPointer(ctx, ref)
		if err != nil {
			return err
		}
		if r == nil || r.Token != ref.Token {
			if ref.Pending || ref.ExpiresUnixMs <= s.now().UnixMilli() {
				continue
			}
			return errors.New("messages: inbox lost canonical acceptance")
		}
		if r.Lost || r.Alias || r.Acked || r.Refused || r.ExpiresUnixMs <= s.now().UnixMilli() {
			continue
		}
		state, err := s.currentState(ctx, ref.Key, r)
		if err != nil {
			if errors.Is(err, errMessageStateInvalid) {
				if err := s.finishDelivery(ctx, ref.Key, ref.Token, msg.DeliveryUndelivered, msg.UndeliveredReasonLost); err != nil {
					return err
				}
				continue
			}
			return err
		}
		if state.Refused {
			continue
		}
		ref.Pending = false
		out = append(out, ref)
	}
	q.Pointers = out
	return nil
}

func (s *messageStore) reserveQueue(ctx context.Context, actor messageActor, receiver string, ref messagePointer) (bool, error) {
	reserved := false
	err := cas.ChangeLimitContext(ctx, s.queuePath(actor.Account, receiver, ref.Context), 64<<10, func(raw []byte) ([]byte, error) {
		q, err := readMessageQueue(raw)
		if err != nil {
			return nil, err
		}
		if err = s.recoverQueue(ctx, &q); err != nil {
			return nil, err
		}
		if len(q.Pointers) < 50 {
			q.Pointers = append(q.Pointers, ref)
			reserved = true
		}
		return json.Marshal(q)
	})
	return reserved, err
}

// Lock order: pair -> queue -> expiry head/page -> canonical record. Neither
// queue reads nor expiry cleanup acquire a pair lock. The short publication
// callback performs no policy/enrollment lookup or blocking observation.
func (s *messageStore) publishQueue(ctx context.Context, actor messageActor, receiver string, ref messagePointer, publish func() error) error {
	return cas.ChangeLimitContext(ctx, s.queuePath(actor.Account, receiver, ref.Context), 64<<10, func(raw []byte) ([]byte, error) {
		q, err := readMessageQueue(raw)
		if err != nil {
			return nil, err
		}
		for i := range q.Pointers {
			if q.Pointers[i].Token == ref.Token && q.Pointers[i].Pending {
				if err = publish(); err != nil {
					return nil, err
				}
				q.Pointers[i].Pending = false
				return json.Marshal(q)
			}
		}
		return nil, errors.New("messages: queue reservation reconciled before publication")
	})
}
