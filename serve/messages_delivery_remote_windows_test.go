//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/fileid"
)

func TestMessagesObserveDeliveryPairedCanonical(t *testing.T) {
	f := pairedMessagesFixture(t)
	instance, contextName := f.sender(t, 0)
	f.permit(t, 0, "app:other-agent")
	client, generation, err := f.nodes[0].reader.pairedTransport(f.keys[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, terminal := range []string{"processed", "acked", "refused", "expired", "lost"} {
		t.Run(terminal, func(t *testing.T) {
			request := f.request(0, "observe-"+terminal)
			request.Subject = "observe " + terminal
			request.Body = "private observation body " + terminal
			request.Kind = msg.KindQuestion
			request.AnswerByUnixMs = f.nodes[0].now.Add(time.Minute).UnixMilli()
			accepted, err := f.nodes[0].reader.Send(instance, contextName, request)
			if err != nil || accepted.Outcome != msg.OutcomeSent || accepted.Reference == nil {
				t.Fatal(accepted, err)
			}
			check := func(want msg.Delivery, reason msg.UndeliveredReason) {
				t.Helper()
				out, err := client.ObserveDelivery(*accepted.Reference, generation)
				if err != nil || out.Outcome != msg.DeliveryObservationOutcomeObserved || out.Delivery == nil || *out.Delivery != want || reason == "" && out.Undelivered != nil || reason != "" && (out.Undelivered == nil || *out.Undelivered != reason) {
					t.Fatal(out, err)
				}
				raw, err := json.Marshal(out)
				if err != nil || strings.Contains(string(raw), request.Body) || strings.Contains(string(raw), accepted.MessageID) {
					t.Fatal("observation exposed content or locator", string(raw), err)
				}
			}
			check(msg.DeliveryQueued, "")
			s := f.nodes[1].store
			record, key, err := s.readByID(t.Context(), accepted.MessageID)
			if err != nil || record == nil {
				t.Fatal(record, err)
			}
			switch terminal {
			case "processed":
				if err = s.transitionState(t.Context(), key, record, "complete"); err != nil {
					t.Fatal(err)
				}
				check(msg.DeliveryQueued, "")
			case "acked":
				if err = s.acknowledge(t.Context(), messagePointer{Key: key, Token: record.Token}); err != nil {
					t.Fatal(err)
				}
				check(msg.DeliveryDelivered, "")
				if err = s.finishDelivery(t.Context(), key, record.Token, msg.DeliveryUndelivered, msg.UndeliveredReasonRefused); err != nil {
					t.Fatal(err)
				}
				check(msg.DeliveryDelivered, "")
			case "lost":
				if err = os.Remove(s.recordPath(key)); err != nil {
					t.Fatal(err)
				}
				check(msg.DeliveryUndelivered, msg.UndeliveredReasonLost)
			default:
				reason := msg.UndeliveredReasonRefused
				if terminal == "expired" {
					reason = msg.UndeliveredReasonExpired
				}
				if err = s.finishDelivery(t.Context(), key, record.Token, msg.DeliveryUndelivered, reason); err != nil {
					t.Fatal(err)
				}
				check(msg.DeliveryUndelivered, reason)
			}
		})
	}
	for _, ref := range []msg.MessageRef{{OwnerPeerKey: f.keys[0], MessageID: "foreign"}, {OwnerPeerKey: strings.Repeat("f", 64), MessageID: "foreign"}} {
		out, err := client.ObserveDelivery(ref, generation)
		if err != nil || out.Outcome != msg.DeliveryObservationOutcomeForbidden || out.Delivery != nil || out.Undelivered != nil {
			t.Fatal(out, err)
		}
	}
	out, err := client.ObserveDelivery(msg.MessageRef{OwnerPeerKey: f.keys[1], MessageID: "unknown"}, generation)
	if err != nil || out.Outcome != msg.DeliveryObservationOutcomeUnknown || out.Delivery != nil {
		t.Fatal(out, err)
	}
}

func TestMessagesObserveDeliveryPairAuthority(t *testing.T) {
	f := pairedMessagesFixture(t)
	instance, contextName := f.sender(t, 0)
	f.permit(t, 0, "app:other-agent")
	accepted, err := f.nodes[0].reader.Send(instance, contextName, f.request(0, "observe-authority"))
	if err != nil || accepted.Reference == nil {
		t.Fatal(accepted, err)
	}
	local, generation, label, err := messagePeerSnapshot(t.Context(), f.states[1], f.keys[0])
	if err != nil {
		t.Fatal(err)
	}
	r := &messagesReceiver{host: f.nodes[1].reader.host, ctx: t.Context(), remote: &messageRemoteCall{key: f.keys[0], local: local, generation: generation, label: label}}
	assertForbidden := func(receiver *messagesReceiver, gen string) {
		t.Helper()
		out, err := receiver.ObserveDelivery(*accepted.Reference, gen)
		if err != nil || out.Outcome != msg.DeliveryObservationOutcomeForbidden || out.Delivery != nil || out.Undelivered != nil {
			t.Fatal(out, err)
		}
	}
	assertForbidden(f.nodes[1].reader, generation)
	assertForbidden(r, strings.Repeat("e", 64))
	f.states[1].mu.Lock()
	busy, busyErr := r.ObserveDelivery(*accepted.Reference, generation)
	f.states[1].mu.Unlock()
	if busyErr != nil || busy.Outcome != msg.DeliveryObservationOutcomeUnavailable || busy.Delivery != nil {
		t.Fatal(busy, busyErr)
	}
	s := f.nodes[1].store
	canonical, _, err := s.readByID(t.Context(), accepted.MessageID)
	if err != nil || canonical == nil {
		t.Fatal(canonical, err)
	}
	foreignActor := canonical.Actor
	foreignRemote := *foreignActor.Remote
	foreignActor.Remote = &foreignRemote
	foreignRemote.PeerKey = strings.Repeat("f", 64)
	foreignActor.Program = "remote:" + foreignRemote.PeerKey
	foreignRequest := remoteStoreRequest(*f.nodes[1].now, foreignActor, "other-peer-observation")
	foreign := s.accept(t.Context(), foreignActor, canonical.Receiver, foreignRequest, false, "")
	if foreign.Outcome != msg.OutcomeSent || foreign.Reference == nil {
		t.Fatal(foreign)
	}
	foreignOut, err := r.ObserveDelivery(*foreign.Reference, generation)
	if err != nil || foreignOut.Outcome != msg.DeliveryObservationOutcomeForbidden || foreignOut.Delivery != nil {
		t.Fatal(foreignOut, err)
	}
	// An authenticated peer cannot observe a record accepted from another peer.
	reverse, reverseGeneration, err := f.nodes[1].reader.pairedTransport(f.keys[0])
	if err != nil {
		t.Fatal(err)
	}
	out, err := reverse.ObserveDelivery(msg.MessageRef{OwnerPeerKey: f.keys[0], MessageID: accepted.MessageID}, reverseGeneration)
	if err != nil || out.Outcome != msg.DeliveryObservationOutcomeUnknown || out.Delivery != nil {
		t.Fatal(out, err)
	}
	for _, state := range []string{"inactive", "active"} {
		revision, _ := pairingStateRead(t, f.states[1])
		_, err = f.states[1].change(t.Context(), revision, func(d *pairingDocument) error {
			p := d.Peers[f.keys[0]]
			p.State = state
			if state == "inactive" {
				p.RightsEffect = ""
			} else {
				p.RightsEffect = "fixture"
				p.Transcript = sha256.Sum256([]byte("replacement observation generation"))
				p.Consent.Transcript = p.Transcript
			}
			d.Peers[f.keys[0]] = p
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		assertForbidden(r, generation)
	}
	_, newGeneration, _, err := messagePeerSnapshot(t.Context(), f.states[1], f.keys[0])
	if err != nil {
		t.Fatal(err)
	}
	r.remote.generation = newGeneration
	assertForbidden(r, newGeneration)
}

func TestMessagesObserveDeliveryPairStorageUnavailable(t *testing.T) {
	for _, damage := range []string{"malformed", "identity-replaced"} {
		t.Run(damage, func(t *testing.T) {
			state := pairingStateFixture(t)
			if damage == "malformed" {
				if err := os.WriteFile(state.path, []byte("malformed pairing fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				raw, err := os.ReadFile(state.path)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(state.path+".replacement", raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(state.path); err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(state.path+".replacement", state.path); err != nil {
					t.Fatal(err)
				}
			}
			if got := messageDeliveryPairState(t.Context(), state, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)); got != msg.DeliveryObservationOutcomeUnavailable {
				t.Fatal(got)
			}
		})
	}
}

func TestMessagesObserveDeliveryQueryAloneExpires(t *testing.T) {
	f := pairedMessagesFixture(t)
	instance, contextName := f.sender(t, 0)
	f.permit(t, 0, "app:other-agent")
	request := f.request(0, "observe-expiry-alone")
	accepted, err := f.nodes[0].reader.Send(instance, contextName, request)
	if err != nil || accepted.Reference == nil {
		t.Fatal(accepted, err)
	}
	client, generation, err := f.nodes[0].reader.pairedTransport(f.keys[1])
	if err != nil {
		t.Fatal(err)
	}
	*f.nodes[1].now = time.UnixMilli(request.ExpiresUnixMs + 1)
	out, err := client.ObserveDelivery(*accepted.Reference, generation)
	if err != nil || out.Outcome != msg.DeliveryObservationOutcomeObserved || out.Delivery == nil || *out.Delivery != msg.DeliveryUndelivered || out.Undelivered == nil || *out.Undelivered != msg.UndeliveredReasonExpired {
		t.Fatal(out, err)
	}
}

func TestMessagesObserveDeliveryQueryAloneRefuses(t *testing.T) {
	f := pairedMessagesFixture(t)
	instance, contextName := f.sender(t, 0)
	request := f.request(0, "observe-refusal-alone")
	if err := f.nodes[0].reader.host.messageRights.policy.Set(f.nodes[0].self, ActionApplicationMessage, messagePeerResource(f.keys[1], request.To), true); err != nil {
		t.Fatal(err)
	}
	accepted, err := f.nodes[0].reader.Send(instance, contextName, request)
	if err != nil || accepted.Reference == nil || !accepted.AwaitingPermission {
		t.Fatal(accepted, err)
	}
	book := f.nodes[1].run.f.a.book
	entries, _, err := book.ApplicationHistoryScoped()
	if err != nil {
		t.Fatal(err)
	}
	var question asks.Record
	for _, entry := range entries {
		if entry.Record.Permission != nil && entry.Record.Pending() {
			question = entry.Record
		}
	}
	if question.ID == "" {
		t.Fatal("missing receiving permission question")
	}
	if _, outcome, err := book.AnswerApplication(question.ID, "refuse"); err != nil || outcome != "answered" {
		t.Fatal(outcome, err)
	}
	client, generation, err := f.nodes[0].reader.pairedTransport(f.keys[1])
	if err != nil {
		t.Fatal(err)
	}
	out, err := client.ObserveDelivery(*accepted.Reference, generation)
	if err != nil || out.Outcome != msg.DeliveryObservationOutcomeObserved || out.Delivery == nil || *out.Delivery != msg.DeliveryUndelivered || out.Undelivered == nil || *out.Undelivered != msg.UndeliveredReasonRefused {
		t.Fatal(out, err)
	}
}

func TestMessagesObserveDeliveryDestinationResetNeverAliasesOldReference(t *testing.T) {
	f := pairedMessagesFixture(t)
	instance, contextName := f.sender(t, 0)
	f.permit(t, 0, "app:other-agent")
	old, err := f.nodes[0].reader.Send(instance, contextName, f.request(0, "before-destination-reset"))
	if err != nil || old.Outcome != msg.OutcomeSent || old.Reference == nil {
		t.Fatal(old, err)
	}
	client, generation, err := f.nodes[0].reader.pairedTransport(f.keys[1])
	if err != nil {
		t.Fatal(err)
	}
	destination := f.nodes[1]
	fixtureRoot, err := filepath.EvalSymlinks(destination.run.state)
	if err != nil {
		t.Fatal(err)
	}
	// The Go runner can give t.TempDir a different root from os.TempDir.
	// Compare against the directory containing this test's other allocations.
	temporaryRoot := filepath.Dir(t.TempDir())
	if same, err := fileid.SameFile(temporaryRoot, filepath.Dir(fixtureRoot)); err != nil || !same {
		t.Fatal("fixture state escaped the test's temporary root", fixtureRoot, err)
	}
	resetTarget, err := filepath.EvalSymlinks(destination.store.dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(resetTarget, filepath.Join(fixtureRoot, "messages")) {
		t.Fatal("unexpected fixture reset target", resetTarget)
	}
	if err := os.RemoveAll(resetTarget); err != nil {
		t.Fatal(err)
	}
	reopened, err := openMessageStore(destination.run.state, func() time.Time { return *destination.now })
	if err != nil {
		t.Fatal(err)
	}
	destination.store = reopened
	destination.reader.host.messages = reopened
	request := f.request(0, "after-destination-reset")
	request.Subject = "new acceptance after reset"
	request.Body = "new private body after reset"
	later, err := f.nodes[0].reader.Send(instance, contextName, request)
	if err != nil || later.Outcome != msg.OutcomeSent || later.Reference == nil || *later.Reference == *old.Reference {
		t.Fatal(old, later, err)
	}
	record, key, err := reopened.readByID(t.Context(), later.MessageID)
	if err != nil || record == nil {
		t.Fatal(record, err)
	}
	if err = reopened.acknowledge(t.Context(), messagePointer{Key: key, Token: record.Token}); err != nil {
		t.Fatal(err)
	}
	newObservation, err := client.ObserveDelivery(*later.Reference, generation)
	if err != nil || newObservation.Outcome != msg.DeliveryObservationOutcomeObserved || newObservation.Delivery == nil || *newObservation.Delivery != msg.DeliveryDelivered {
		t.Fatal(newObservation, err)
	}
	oldObservation, err := client.ObserveDelivery(*old.Reference, generation)
	if err != nil || oldObservation.Outcome != msg.DeliveryObservationOutcomeUnknown || oldObservation.Delivery != nil || oldObservation.Undelivered != nil {
		t.Fatal("old reference acquired later acceptance", oldObservation, err)
	}
}
