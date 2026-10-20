package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	cas "github.com/openabstractions/abstraction-cas/go"
)

type messageIDPointer struct {
	Profile, ID, Key, Token string
	ExpiresUnixMs           int64 `json:"ExpiresAt"`
}

func (s *messageStore) publishID(ctx context.Context, record *messageRecord, key string) error {
	ref := messageIDPointer{Profile: "messages-id@1", ID: record.ID, Key: key, Token: record.Token, ExpiresUnixMs: record.ExpiresUnixMs}
	return cas.ChangeLimitContext(ctx, filepath.Join(s.dir, "ids", messageDigest(record.ID)+".json"), 4096, func(raw []byte) ([]byte, error) {
		if raw != nil {
			var existing messageIDPointer
			if err := messageStoredJSON(raw, &existing); err != nil {
				return nil, err
			}
			if existing != ref {
				return nil, errors.New("messages: assigned id collision")
			}
			return raw, nil
		}
		return json.Marshal(ref)
	})
}

// An ID is a locator only. Callers still prove native inbox, conversation and
// exact permission authority. An interrupted locator publication proves no
// acceptance without the matching canonical key record and token.
func (s *messageStore) readByID(ctx context.Context, id string) (*messageRecord, string, error) {
	r, key, err := s.readRetainedByID(ctx, id)
	if r != nil && r.ExpiresUnixMs <= s.now().UnixMilli() {
		return nil, "", nil
	}
	return r, key, err
}

func (s *messageStore) readRetainedByID(ctx context.Context, id string) (*messageRecord, string, error) {
	if len(id) < 1 || len(id) > 128 {
		return nil, "", nil
	}
	raw, err := cas.ReadLimitContext(ctx, filepath.Join(s.dir, "ids", messageDigest(id)+".json"), 4096)
	if err != nil || raw == nil {
		return nil, "", err
	}
	var ref messageIDPointer
	if err = messageStoredJSON(raw, &ref); err != nil || ref.Profile != "messages-id@1" || ref.ID != id || !agentHex(ref.Key, 32) || ref.Token == "" {
		return nil, "", errors.New("messages: invalid id pointer")
	}
	r, err := s.readRecord(ctx, ref.Key)
	if err != nil {
		return nil, "", err
	}
	if r == nil || r.Token != ref.Token || r.ID != id {
		return nil, "", nil
	}
	return r, ref.Key, nil
}
