package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	cas "github.com/openabstractions/abstraction-cas/go"
)

// This index owns expiry work for canonical records and fresh-key aliases.
// Each head/page is bounded independently of retained history. Operations
// append one reference; resumable expiry examines at most one64-entry slice.
// Neither startup nor Wait constructs a retained-history map or scans it.
const messageExpiryPageEntries = 128
const messageExpiryWork = 64

type messageExpiryHead struct {
	Profile, First, Last, Page string
	Offset                     int
}
type messageExpiryPage struct {
	Profile, Next string
	Entries       []messagePointer
}

func (s *messageStore) expiryPagePath(token string) string {
	return filepath.Join(s.dir, "expiry", token+".json")
}
func readMessageExpiryHead(raw []byte) (messageExpiryHead, error) {
	h := messageExpiryHead{Profile: "messages-expiry-head@1"}
	if raw == nil {
		return h, nil
	}
	if err := messageStoredJSON(raw, &h); err != nil || h.Profile != "messages-expiry-head@1" || h.Offset < 0 || h.Offset > messageExpiryPageEntries {
		return h, errors.New("messages: invalid expiry head")
	}
	for _, token := range []string{h.First, h.Last, h.Page} {
		if token != "" && !agentHex(token, 24) {
			return h, errors.New("messages: invalid expiry page identity")
		}
	}
	return h, nil
}
func readMessageExpiryPage(raw []byte) (messageExpiryPage, error) {
	p := messageExpiryPage{Profile: "messages-expiry-page@1", Entries: []messagePointer{}}
	if raw == nil {
		return p, nil
	}
	if err := messageStoredJSON(raw, &p); err != nil || p.Profile != "messages-expiry-page@1" || p.Entries == nil || len(p.Entries) > messageExpiryPageEntries || p.Next != "" && !agentHex(p.Next, 24) {
		return p, errors.New("messages: invalid expiry page")
	}
	for _, ref := range p.Entries {
		if !agentHex(ref.Key, 32) || ref.Token == "" || ref.ExpiresUnixMs <= 0 {
			return p, errors.New("messages: invalid expiry reference")
		}
	}
	return p, nil
}

func (s *messageStore) appendExpiry(ctx context.Context, ref messagePointer) error {
	completed := false
	err := cas.ChangeLimitContext(ctx, filepath.Join(s.dir, "expiry", "head.json"), 4096, func(raw []byte) ([]byte, error) {
		h, err := readMessageExpiryHead(raw)
		if err != nil {
			return nil, err
		}
		if h.Last != "" {
			linked := ""
			appended := false
			err = cas.ChangeLimitContext(ctx, s.expiryPagePath(h.Last), 128<<10, func(raw []byte) ([]byte, error) {
				if raw == nil {
					return nil, errors.New("messages: referenced expiry tail missing")
				}
				p, err := readMessageExpiryPage(raw)
				if err != nil {
					return nil, err
				}
				if s.inspect != nil {
					s.inspect("expiry_page_entries", len(p.Entries))
				}
				if p.Next != "" {
					linked = p.Next
					return raw, nil
				}
				for _, existing := range p.Entries {
					if existing.Key == ref.Key && existing.Token == ref.Token {
						appended = true
						return raw, nil
					}
				}
				if len(p.Entries) < messageExpiryPageEntries {
					p.Entries = append(p.Entries, ref)
					appended = true
					return json.Marshal(p)
				}
				token, err := applicationToken()
				if err != nil {
					return nil, err
				}
				newPage := messageExpiryPage{Profile: "messages-expiry-page@1", Entries: []messagePointer{ref}}
				if err = cas.ChangeLimitContext(ctx, s.expiryPagePath(token), 128<<10, func(current []byte) ([]byte, error) {
					if current != nil {
						return nil, errors.New("messages: expiry identity collision")
					}
					return json.Marshal(newPage)
				}); err != nil {
					return nil, err
				}
				p.Next, linked, appended = token, token, true
				return json.Marshal(p)
			})
			if err != nil {
				return nil, err
			}
			if linked != "" {
				h.Last = linked
			}
			if !appended {
				// One crashed link is repaired per call. The pending acceptance
				// remains discoverable; no canonical record is published here.
				return json.Marshal(h)
			}
			completed = true
			return json.Marshal(h)
		}
		token, err := applicationToken()
		if err != nil {
			return nil, err
		}
		p := messageExpiryPage{Profile: "messages-expiry-page@1", Entries: []messagePointer{ref}}
		if err = cas.ChangeLimitContext(ctx, s.expiryPagePath(token), 128<<10, func(current []byte) ([]byte, error) {
			if current != nil {
				return nil, errors.New("messages: expiry identity collision")
			}
			return json.Marshal(p)
		}); err != nil {
			return nil, err
		}
		h.First, h.Last, h.Page = token, token, token
		completed = true
		return json.Marshal(h)
	})
	if err == nil && !completed {
		return errors.New("messages: expiry link repair pending")
	}
	return err
}

// sweepExpiry performs bounded cleanup. Expiry is checked separately before
// every disclosure; cleanup progress cannot extend logical retention.
func (s *messageStore) sweepExpiry(ctx context.Context) error {
	_, err := s.expiryProgress(ctx)
	return err
}

func (s *messageStore) expiryProgress(ctx context.Context) ([]messagePointer, error) {
	var progress []messagePointer
	err := cas.ChangeLimitContext(ctx, filepath.Join(s.dir, "expiry", "head.json"), 4096, func(raw []byte) ([]byte, error) {
		h, err := readMessageExpiryHead(raw)
		if err != nil || h.First == "" {
			return raw, err
		}
		if h.Page == "" {
			h.Page, h.Offset = h.First, 0
		}
		next, allDone := "", false
		err = cas.ChangeLimitContext(ctx, s.expiryPagePath(h.Page), 128<<10, func(raw []byte) ([]byte, error) {
			if raw == nil {
				return nil, os.ErrNotExist
			}
			p, err := readMessageExpiryPage(raw)
			if err != nil || h.Offset > len(p.Entries) {
				return nil, errors.New("messages: invalid expiry cursor")
			}
			end := min(len(p.Entries), h.Offset+messageExpiryWork)
			for i := h.Offset; i < end; i++ {
				if !p.Entries[i].Expired && !p.Entries[i].Alias {
					progress = append(progress, p.Entries[i])
				}
				if p.Entries[i].ExpiresUnixMs <= s.now().UnixMilli() && !p.Entries[i].Expired {
					if err := s.expireRecord(ctx, p.Entries[i]); err != nil {
						return nil, err
					}
					record, err := s.readRecord(ctx, p.Entries[i].Key)
					if err != nil {
						return nil, err
					}
					p.Entries[i].Expired = record == nil || record.Token != p.Entries[i].Token || (record.Forward == nil || record.Alias || record.Delivery != nil) && (record.Audit == nil || record.AuditDone) && (record.Delivery == nil || record.Delivery.Reported)
				}
			}
			h.Offset = end
			allDone = true
			for _, ref := range p.Entries {
				allDone = allDone && ref.Expired
			}
			if end == len(p.Entries) {
				next = p.Next
				h.Offset = 0
			}
			return json.Marshal(p)
		})
		if err != nil {
			return nil, err
		}
		if allDone && h.Page == h.First && h.Offset == 0 {
			h.First = next
			if h.Last == h.Page {
				h.Last = ""
			}
		}
		if h.Offset == 0 {
			h.Page = next
			if h.Page == "" {
				h.Page = h.First
			}
		}
		return json.Marshal(h)
	})
	return progress, err
}
