package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

var messagePriorMembers = map[string]string{
	"request_key":        "idempotency_key",
	"expires_at":         "expires_unix_ms",
	"answer_by":          "answer_by_unix_ms",
	"pairing_generation": "pairing_epoch",
}

// This projection belongs exclusively to immutable accepted storage. Public
// admission always uses the current generated codec. Token spans preserve body
// values, escaping, order and original canonical bytes used by custody hashes.
func messageRenameRetained(raw []byte, prior bool) ([]byte, error) {
	if len(raw) > messageRecordLimit {
		return nil, errors.New("messages: retained request bound")
	}
	names := messagePriorMembers
	if !prior {
		names = make(map[string]string, len(messagePriorMembers))
		for old, current := range messagePriorMembers {
			names[current] = old
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	type edit struct {
		start, end int
		value      string
	}
	var edits []edit
	var root map[string]bool
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 64 {
			return errors.New("messages: retained request depth")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			if depth == 0 {
				root = seen
			}
			for d.More() {
				before := int(d.InputOffset())
				token, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok || seen[key] {
					return errors.New("messages: duplicate retained member")
				}
				seen[key] = true
				end := int(d.InputOffset())
				if next, rename := names[key]; depth == 0 && rename {
					start := before + bytes.IndexByte(raw[before:end], '"')
					edits = append(edits, edit{start, end, `"` + next + `"`})
				}
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case json.Delim('['):
			for d.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := walk(0); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("messages: trailing retained request")
	}
	for _, next := range names {
		if root[next] {
			return nil, errors.New("messages: mixed retained schema")
		}
	}
	key, expiry := "request_key", "expires_at"
	if !prior {
		key, expiry = "idempotency_key", "expires_unix_ms"
	}
	if (!root[key] && !root["message_id"]) || !root[expiry] {
		return nil, errors.New("messages: incomplete retained schema")
	}
	var out []byte
	last := 0
	for _, e := range edits {
		out = append(out, raw[last:e.start]...)
		out = append(out, e.value...)
		last = e.end
	}
	out = append(out, raw[last:]...)
	return out, nil
}

func decodeRetainedMessageRequest(raw []byte) (*msg.SendRequest, error) {
	if len(raw) > messageRecordLimit {
		return nil, errors.New("messages: retained request bound")
	}
	if request, err := msg.DecodeSendRequest(raw); err == nil {
		return request, nil
	}
	current, err := messageRenameRetained(raw, true)
	if err != nil {
		return nil, err
	}
	return msg.DecodeSendRequest(current)
}

func messageRequestDigestMatches(digest string, current []byte) bool {
	if digest == messageDigest(string(current)) {
		return true
	}
	prior, err := messageRenameRetained(current, false)
	return err == nil && digest == messageDigest(string(prior))
}

func decodeRetainedMessageHeader(raw []byte) (*msg.Header, error) {
	if len(raw) > messageRecordLimit {
		return nil, errors.New("messages: retained header bound")
	}
	// Classify member names only. Neither map values nor its re-encoding become
	// accepted data; the token walk below still checks every duplicate and span.
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, err
	}
	prior := members["expires_at"] != nil || members["answer_by"] != nil
	if !prior {
		return msg.DecodeHeader(raw)
	}
	// Historical storage is an exact receiving format, independently of the
	// public Header's additive output policy. Current renamed members therefore
	// also refuse here, even when a grant reader could ignore the old spelling.
	for name := range members {
		switch name {
		case "message_id", "from", "from_context", "to", "context", "kind", "subject", "context_id", "expires_at", "reply_to", "answer_by", "task_state", "refused", "reference", "remote_origin", "reply_reference", "delivery", "undelivered", "count":
		default:
			return nil, errors.New("messages: unknown historical header member")
		}
	}
	current, err := messageRenameRetained(raw, true)
	if err != nil {
		return nil, err
	}
	return msg.DecodeHeader(current)
}

func messageRequestBytesEqual(retained, current []byte) bool {
	if bytes.Equal(retained, current) {
		return true
	}
	old, err := decodeRetainedMessageRequest(retained)
	if err != nil {
		return false
	}
	return bytes.Equal(msg.EncodeSendRequest(old), current)
}
