package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	cas "github.com/openabstractions/abstraction-cas/go"
)

func TestMessagesChatAllocationScopesRestartAndResetWithoutReadingVendorIdentity(t *testing.T) {
	s, _, actor, _ := messagesStoreFixture(t)
	ctx := context.Background()
	original, err := s.contextForChat(ctx, actor.Account, actor.Program, "same-host-session")
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := openMessageStore(filepath.Dir(s.dir), s.now)
	if err != nil {
		t.Fatal(err)
	}
	stable, err := restarted.contextForChat(ctx, actor.Account, actor.Program, "same-host-session")
	if err != nil || stable != original || s.ownerNonce != restarted.ownerNonce {
		t.Fatal("restart changed owned address", original, stable, err)
	}
	reset, err := openMessageStore(t.TempDir(), s.now)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := reset.contextForChat(ctx, actor.Account, actor.Program, "same-host-session")
	if err != nil || changed == original || s.ownerNonce == reset.ownerNonce {
		t.Fatal("new store reused a prior owner address", original, changed, err)
	}
	oldVendorHighWater := map[string]string{"same-host-session\x00" + original: "prior-key"}
	if oldVendorHighWater["same-host-session\x00"+changed] != "" {
		t.Fatal("prior vendor high-water suppressed fresh owner context")
	}
	other, err := reset.contextForChat(ctx, actor.Account, actor.Program, "different-host-session")
	if err != nil || other == changed {
		t.Fatal(other, err)
	}
}

func TestMessagesRetainedOwnerWithoutCounterRefusesInsteadOfRestartingOrder(t *testing.T) {
	s, _, actor, _ := messagesStoreFixture(t)
	ctx := context.Background()
	if _, err := s.contextForChat(ctx, actor.Account, actor.Program, "host"); err != nil {
		t.Fatal(err)
	}
	if err := cas.ChangeLimitContext(ctx, s.ownerPath(), 4096, func(raw []byte) ([]byte, error) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
		delete(fields, "Next")
		return json.Marshal(fields)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.contextForChat(ctx, actor.Account, actor.Program, "host"); err == nil {
		t.Fatal("retained owner silently recreated missing counter")
	}
	if _, err := openMessageStore(filepath.Dir(s.dir), s.now); err == nil {
		t.Fatal("reopen silently recreated missing counter")
	}
}

func TestMessagesDurableChatAssociationKeepsItsExactOpaqueAddress(t *testing.T) {
	s, _, actor, _ := messagesStoreFixture(t)
	ctx := context.Background()
	chat := "retained-host"
	key := messageDigest(actor.Account + "\x00" + actor.Program + "\x00" + chat)
	legacy := "native-11111111111111111111111111111111"
	// A retained owner-created association is custody. An incoming queue or
	// caller-supplied opaque label is never used to manufacture this record.
	path := filepath.Join(s.dir, "sequences", "chat-"+key+".json")
	if err := cas.ChangeLimitContext(ctx, path, 1024, func([]byte) ([]byte, error) {
		return json.Marshal(messageChatBindingRecord{Profile: "messages-chat@1", Owner: s.ownerNonce, Context: legacy})
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.contextForChat(ctx, actor.Account, actor.Program, chat)
	if err != nil || got != legacy {
		t.Fatal("retained association address was recalculated", got, err)
	}
}
