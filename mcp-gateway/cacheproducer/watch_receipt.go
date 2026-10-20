package cacheproducer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstractions/mcp-gateway/internal/setupio"
	"os"
	"path/filepath"
)

const claudeWatchRule = "mcp__OpenAbstractions__oa_messages_watch"
const claudeAsksWatchRule = "mcp__OpenAbstractions__oa_asks_watch"

type WatchReceipt struct {
	Version          int `json:"version"`
	CLI, Root, State string
	Before, After    string
	Undo             string
	Offset, Length   int
	Added            bool
	Status           string
	// A composed undo journals the surviving scope's replacement receipt before
	// changing settings. Recovery can complete either receipt write after CAS.
	Rebased       *WatchReceipt `json:",omitempty"`
	SiblingBefore string        `json:",omitempty"`
}

func WatchReceiptFile(state, rule string) string {
	if rule == claudeAsksWatchRule {
		return filepath.Join(state, "claude-asks-watch-permission.json")
	}
	return filepath.Join(state, "claude-watch-permission.json")
}

func ReadWatchReceipt(ctx context.Context, p string) ([]byte, WatchReceipt, error) {
	var r WatchReceipt
	if err := privatedir.CheckFile(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, r, err
	}
	b, err := cas.ReadLimitContext(ctx, p, claudeMarkerLimit)
	if err != nil || b == nil {
		return b, r, err
	}
	if _, err := claudeConfigObject(b); err != nil {
		return nil, r, errors.New("unknown ownership receipt")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || r.Version != 1 {
		return nil, r, errors.New("unknown ownership receipt")
	}
	switch r.Status {
	case "pending", "owned", "preexisting", "disabled", "undo-pending":
	default:
		return nil, r, errors.New("unknown ownership state")
	}
	return b, r, nil
}

func claudeConfigObject(raw []byte) (map[string]json.RawMessage, error) {
	return setupio.ConfigObject(raw)
}
