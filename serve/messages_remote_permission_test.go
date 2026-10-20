package main

import (
	"bytes"
	"context"
	"maps"
	"os"
	"slices"
	"testing"

	facade "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func messagePeerPermitFixture(t *testing.T) (*runtimePairingState, *runtimeRights, string, uint64) {
	t.Helper()
	ctx := context.Background()
	s := pairingStateFixture(t)
	cert, transcript := pairingStatePeer(t)
	rev, _ := pairingStateRead(t, s)
	rev, err := s.stage(ctx, rev, cert, transcript, "peer", "")
	if err != nil {
		t.Fatal(err)
	}
	key := pairingKey(cert)
	rev, err = s.activate(ctx, rev, key, func(context.Context, pairingPeerRecord) (string, error) { return "fixture", nil })
	if err != nil {
		t.Fatal(err)
	}
	d := &applicationDirectory{}
	d.roles.Store(&applicationRoles{})
	r := &runtimeRights{applications: d, messagePairing: s}
	r.refreshMessagePeerFacts(ctx)
	return s, r, key, rev
}

func TestMessagePeerPermitFencesPairingInput(t *testing.T) {
	ctx := context.Background()
	s, r, key, rev := messagePeerPermitFixture(t)
	d := r.applications
	guard, err := r.prepareMessagePeerPermit(ctx, wire.Subject{Program: "local"}, key)
	if err != nil {
		t.Fatal(err)
	}
	// An independent registration projection can publish while Rights waits
	// for its CAS. The prepared peer's canonical authority remains identical.
	original := d.roles.Load()
	updated := *original
	peerFacts := *original.messagePeers
	peerFacts.raw = bytes.Clone(peerFacts.raw)
	peerFacts.ids = slices.Clone(peerFacts.ids)
	peerFacts.active = maps.Clone(peerFacts.active)
	updated.messagePeers = &peerFacts
	d.roles.Store(&updated)
	if err := guard(); err != nil {
		t.Fatalf("unrelated role publication invalidated unchanged pairing: %v", err)
	}
	for _, variant := range []string{"unknown", "missing", "inactive", "path", "bytes", "identity"} {
		t.Run(variant, func(t *testing.T) {
			defer d.roles.Store(original)
			changed := *original
			facts := *original.messagePeers
			changed.messagePeers = &facts
			switch variant {
			case "unknown":
				d.roles.Store(nil)
			case "missing":
				changed.messagePeers = nil
			case "inactive":
				facts.active = map[string]bool{}
			case "path":
				facts.path += ".other"
			case "bytes":
				facts.raw = append(bytes.Clone(facts.raw), ' ')
			case "identity":
				facts.ids = nil
			}
			if variant != "unknown" {
				d.roles.Store(&changed)
			}
			if err := guard(); err == nil {
				t.Fatal("changed or unknown peer facts admitted")
			}
		})
	}
	rev, err = s.change(ctx, rev, func(d *pairingDocument) error {
		peer := d.Peers[key]
		peer.Label = "changed"
		d.Peers[key] = peer
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := guard(); err == nil {
		t.Fatal("prepared permit followed a changed pairing revision")
	}
	r.refreshMessagePeerFacts(ctx)
	guard, err = r.prepareMessagePeerPermit(ctx, wire.Subject{Program: "local"}, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.remove(ctx, rev, key, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := guard(); err == nil {
		t.Fatal("prepared permit followed a changed pairing document")
	}
	r.refreshMessagePeerFacts(ctx)
	if _, err := r.prepareMessagePeerPermit(ctx, wire.Subject{Program: "local"}, key); err == nil {
		t.Fatal("removed peer prepared a permit")
	}
}

func TestMessagePeerPermitRefusesReplacedCanonicalIdentity(t *testing.T) {
	ctx := context.Background()
	s, r, key, _ := messagePeerPermitFixture(t)
	// The identity guard must refuse even identical canonical bytes. An active
	// projection alone supplies no authority to replace the kept state file.
	guard, err := r.prepareMessagePeerPermit(ctx, wire.Subject{Program: "local"}, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.path, s.path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := guard(); err == nil {
		t.Fatal("identical replacement file admitted")
	}
}

func TestMessagePeerFactsBusyRefreshKeepsMetadataAndCurrentFence(t *testing.T) {
	ctx := context.Background()
	state, policy, key, revision := messagePeerPermitFixture(t)
	original := policy.applications.roles.Load()
	state.mu.Lock()
	policy.refreshMessagePeerFacts(ctx)
	state.mu.Unlock()
	if policy.applications.roles.Load() != original {
		t.Fatal("busy snapshot published peer absence")
	}
	guard, err := policy.prepareMessagePeerPermit(ctx, wire.Subject{Program: "local"}, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = guard(); err != nil {
		t.Fatal("unchanged canonical peer refused", err)
	}
	if _, err = state.remove(ctx, revision, key, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	policy.refreshMessagePeerFacts(ctx)
	state.mu.Unlock()
	if err = guard(); err == nil {
		t.Fatal("retained metadata authorized a revoked canonical pair")
	}
	policy.refreshMessagePeerFacts(ctx)
	if _, err = policy.prepareMessagePeerPermit(ctx, wire.Subject{Program: "local"}, key); err == nil {
		t.Fatal("removed peer remained eligible")
	}
	policy.applications.roles.Store(nil)
	state.mu.Lock()
	policy.refreshMessagePeerFacts(ctx)
	state.mu.Unlock()
	if _, err = policy.prepareMessagePeerPermit(ctx, wire.Subject{Program: "local"}, key); err == nil {
		t.Fatal("unknown projection admitted")
	}
}

func TestMessagePeerFactsSurviveRegistrationRebuilds(t *testing.T) {
	ctx := context.Background()
	_, policy, key, _ := messagePeerPermitFixture(t)
	directory := registration1459Directory(t, t.TempDir())
	directory.roleDecode = func([]byte, string) (bool, error) { return false, nil }
	if err := directory.refreshRoles(ctx); err != nil {
		t.Fatal(err)
	}
	roles := *directory.roles.Load()
	roles.messagePeers = policy.applications.roles.Load().messagePeers
	directory.roles.Store(&roles)
	policy.applications = directory
	for _, kind := range []string{"registration", "legacy"} {
		directory.mu.Lock()
		var err error
		if kind == "registration" {
			err = directory.withRegistrationStorageLocked(ctx, func(map[string]facade.ApplicationDescriptor) error { return nil })
		} else {
			err = directory.persistLocked(ctx, map[string]facade.ApplicationDescriptor{})
		}
		directory.mu.Unlock()
		if err != nil {
			t.Fatal(kind, err)
		}
		guard, err := policy.prepareMessagePeerPermit(ctx, wire.Subject{Program: "local"}, key)
		if err != nil {
			t.Fatal(kind, "lost independent peer metadata", err)
		}
		if err = guard(); err != nil {
			t.Fatal(kind, err)
		}
	}
}
