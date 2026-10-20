//go:build windows

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/remote"
)

type forwardPollOldMessages struct{ msg.Messages }

func (*forwardPollOldMessages) ObserveDelivery(msg.MessageRef, string) (msg.ObserveDeliveryResult, error) {
	return msg.ObserveDeliveryResult{}, &fwire.ServiceError{Code: fwire.ServiceErrorCode("unsupported"), Message: "fixture old peer"}
}

func forwardPollAcceptance(t *testing.T, f *remoteMessagesFixture, name string) (msg.SendResult, *messageRecord, string) {
	t.Helper()
	instance, contextName := f.sender(t, 0)
	request := f.request(0, name)
	request.Subject, request.Body = name, "private "+name
	request.NotifyDelivered = new(true)
	actor, _, _, ok := f.nodes[0].reader.owner(instance, contextName)
	if !ok {
		t.Fatal("missing source actor")
	}
	out, err := f.nodes[0].reader.Send(instance, contextName, request)
	if err != nil || out.Outcome != msg.OutcomeSent || out.Reference == nil {
		t.Fatal(out, err)
	}
	s := f.nodes[0].store
	key := s.forwardKey(actor, f.keys[1], request.IdempotencyKey)
	r, err := s.readRecord(t.Context(), key)
	if err != nil || r == nil || r.Forward == nil {
		t.Fatal("missing genuine forwarding custody", r, err)
	}
	if !r.Alias || !agentHex(r.TargetKey, 32) || !agentHex(r.TargetToken, 24) {
		t.Fatal("missing canonical forwarding target", r)
	}
	canonical, err := s.readRecord(t.Context(), r.TargetKey)
	if err != nil || canonical == nil || canonical.Token != r.TargetToken || canonical.Alias || canonical.Forward == nil || canonical.Forward.Reference != *out.Reference {
		t.Fatal("invalid canonical forwarding custody", canonical, err)
	}
	key, r = r.TargetKey, canonical
	return out, r, key
}

func TestMessagesForwardPollBodyAckOwnReport(t *testing.T) {
	f := pairedMessagesFixture(t)
	f.permit(t, 0, "app:other-agent")
	out, source, key := forwardPollAcceptance(t, f, "forward-body-ack")
	h := f.nodes[0].reader.host
	if err := h.observeForwardDelivery(t.Context(), key, source); err != nil {
		t.Fatal(err)
	}
	current, err := h.messages.readRecord(t.Context(), key)
	if err != nil || current.Delivery != nil {
		t.Fatal("queued became terminal", current, err)
	}
	destination := f.nodes[1].store
	r, destinationKey, err := destination.readByID(t.Context(), out.MessageID)
	if err != nil || r == nil {
		t.Fatal(r, err)
	}
	if err = destination.acknowledge(t.Context(), messagePointer{Key: destinationKey, Token: r.Token}); err != nil {
		t.Fatal(err)
	}
	if err = h.observeForwardDelivery(t.Context(), key, source); err != nil {
		t.Fatal(err)
	}
	current, err = h.messages.readRecord(t.Context(), key)
	if err != nil || current.Delivery == nil || current.Delivery.State != msg.DeliveryDelivered {
		t.Fatal(current, err)
	}
	refs, err := h.messages.messageEventCandidates(t.Context(), source.Actor, h.messages.stateEventCheckFor(source.Actor))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ref := range refs {
		if ref.DeliveryReport == "" {
			continue
		}
		header, err := msg.DecodeHeader([]byte(ref.DeliveryReport))
		if err != nil {
			t.Fatal(err)
		}
		if header.Delivery != nil && *header.Delivery == msg.DeliveryDelivered {
			found = true
		}
	}
	if !found {
		t.Fatal("source sender report missing")
	}
}

func TestMessagesForwardFreshKeyAliasHasOneCanonicalReport(t *testing.T) {
	f := pairedMessagesFixture(t)
	f.permit(t, 0, "app:other-agent")
	instance, contextName := f.sender(t, 0)
	request := f.request(0, "forward-alias-first")
	request.NotifyDelivered = new(true)
	actor, _, _, ok := f.nodes[0].reader.owner(instance, contextName)
	if !ok {
		t.Fatal("missing source")
	}
	first, err := f.nodes[0].reader.Send(instance, contextName, request)
	if err != nil || first.Outcome != msg.OutcomeSent || first.Reference == nil {
		t.Fatal(first, err)
	}
	secondRequest := request
	secondRequest.IdempotencyKey = "forward-alias-second"
	second, err := f.nodes[0].reader.Send(instance, contextName, secondRequest)
	if err != nil || second.Outcome != msg.OutcomeSent || second.Reference == nil || *second.Reference != *first.Reference {
		t.Fatal(first, second, err)
	}
	source := f.nodes[0].store
	a, err := source.readRecord(t.Context(), source.forwardKey(actor, f.keys[1], request.IdempotencyKey))
	if err != nil {
		t.Fatal(err)
	}
	b, err := source.readRecord(t.Context(), source.forwardKey(actor, f.keys[1], secondRequest.IdempotencyKey))
	if err != nil || a == nil || b == nil || !a.Alias || !b.Alias || a.TargetKey != b.TargetKey || a.TargetToken != b.TargetToken {
		t.Fatal(a, b, err)
	}
	canonical, err := source.readRecord(t.Context(), a.TargetKey)
	if err != nil || canonical == nil || canonical.Alias || len(canonical.Spec) != 0 {
		t.Fatal(canonical, err)
	}
	destination := f.nodes[1].store
	r, key, err := destination.readByID(t.Context(), first.MessageID)
	if err != nil || r == nil {
		t.Fatal(r, err)
	}
	if err = destination.acknowledge(t.Context(), messagePointer{Key: key, Token: r.Token}); err != nil {
		t.Fatal(err)
	}
	h := f.nodes[0].reader.host
	for range 2 {
		if err = h.observeForwardDelivery(t.Context(), a.TargetKey, canonical); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := source.messageEventCandidates(t.Context(), actor, source.stateEventCheckFor(actor))
	if err != nil || len(refs) != 1 {
		t.Fatal("aliases duplicated source terminal report", refs, err)
	}
	header, err := msg.DecodeHeader([]byte(refs[0].DeliveryReport))
	if err != nil || header.Count == nil || *header.Count != 1 || header.Reference == nil || *header.Reference != *first.Reference {
		t.Fatal(header, err)
	}
}

func TestMessagesForwardProgressRecoversCustodyAfterRestart(t *testing.T) {
	f := pairedMessagesFixture(t)
	f.permit(t, 0, "app:other-agent")
	out, source, key := forwardPollAcceptance(t, f, "forward-progress-restart")
	destination := f.nodes[1].store
	r, destinationKey, err := destination.readByID(t.Context(), out.MessageID)
	if err != nil || r == nil {
		t.Fatal(r, err)
	}
	if err = destination.acknowledge(t.Context(), messagePointer{Key: destinationKey, Token: r.Token}); err != nil {
		t.Fatal(err)
	}
	h := f.nodes[0].reader.host
	reopened, err := openMessageStore(filepath.Dir(h.messages.dir), h.messages.now)
	if err != nil {
		t.Fatal(err)
	}
	h.messages = reopened
	if err = h.progressMessages(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err := reopened.readRecord(t.Context(), key)
	if err != nil || current == nil || current.Delivery == nil || current.Delivery.State != msg.DeliveryDelivered {
		t.Fatal(current, err)
	}
	if len(current.Spec) != 0 || current.Forward.Reference != *out.Reference {
		t.Fatal("forward restart retained body or changed locator", current)
	}
	refs, err := reopened.messageEventCandidates(t.Context(), source.Actor, reopened.stateEventCheckFor(source.Actor))
	if err != nil || len(refs) != 1 || refs[0].DeliveryReport == "" {
		t.Fatal(refs, err)
	}
}

func TestMessagesForwardRemovedSenderKeepsOnlyActivityOutcome(t *testing.T) {
	f := pairedMessagesFixture(t)
	f.permit(t, 0, "app:other-agent")
	out, source, key := forwardPollAcceptance(t, f, "forward-sender-removed")
	node := f.nodes[0]
	enrollment := node.run.d.readAgentEnrollment(node.run.ctx, node.run.f.panel, "native-agent")
	if enrollment.Enrollment == nil {
		t.Fatal(enrollment)
	}
	agent1478Set(t, node.run, "native-agent", enrollment.Enrollment.Revision, false)
	destination := f.nodes[1].store
	r, destinationKey, err := destination.readByID(t.Context(), out.MessageID)
	if err != nil || r == nil {
		t.Fatal(r, err)
	}
	if err = destination.acknowledge(t.Context(), messagePointer{Key: destinationKey, Token: r.Token}); err != nil {
		t.Fatal(err)
	}
	h := node.reader.host
	if err = h.observeForwardDelivery(t.Context(), key, source); err != nil {
		t.Fatal(err)
	}
	current, err := h.messages.readRecord(t.Context(), key)
	if err != nil || current == nil || !current.SenderRemoved || current.Delivery == nil || current.Delivery.State != msg.DeliveryDelivered || current.Delivery.Report != "" || !current.Delivery.Reported || current.Audit == nil || !current.Audit.MetadataOnly {
		t.Fatal(current, err)
	}
	refs, err := h.messages.messageEventCandidates(t.Context(), source.Actor, h.messages.stateEventCheckFor(source.Actor))
	if err != nil || len(refs) != 0 {
		t.Fatal("removed sender gained native report", refs, err)
	}
}

func TestMessagesForwardRemovedSenderSurvivesCanonicalDamage(t *testing.T) {
	for _, damage := range []string{"missing", "malformed", "projection_pending"} {
		t.Run(damage, func(t *testing.T) {
			f := pairedMessagesFixture(t)
			f.permit(t, 0, "app:other-agent")
			out, source, key := forwardPollAcceptance(t, f, "forward-removed-damaged")
			s := f.nodes[0].store
			if err := s.markSenderRemoved(t.Context(), key, source.Token); err != nil {
				t.Fatal(err)
			}
			var err error
			if damage == "projection_pending" {
				raw, e := json.Marshal(source)
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(s.recordPath(key), raw, 0600)
			} else if damage == "missing" {
				err = os.Remove(s.recordPath(key))
			} else {
				err = os.WriteFile(s.recordPath(key), []byte("{broken"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := openMessageStore(filepath.Dir(s.dir), s.now)
			if err != nil {
				t.Fatal(err)
			}
			h := f.nodes[0].reader.host
			h.messages = reopened
			recovered, err := reopened.recordForPointer(t.Context(), messagePointer{Key: key, Token: source.Token})
			if err != nil || recovered == nil || !recovered.SenderRemoved && damage != "projection_pending" || recovered.Delivery != nil || recovered.Forward == nil || len(recovered.Spec) != 0 {
				t.Fatal(recovered, err)
			}
			destination := f.nodes[1].store
			r, destinationKey, err := destination.readByID(t.Context(), out.MessageID)
			if err != nil || r == nil {
				t.Fatal(r, err)
			}
			if err = destination.acknowledge(t.Context(), messagePointer{Key: destinationKey, Token: r.Token}); err != nil {
				t.Fatal(err)
			}
			if err = h.observeForwardDelivery(t.Context(), key, recovered); err != nil {
				t.Fatal(err)
			}
			current, err := reopened.readRecord(t.Context(), key)
			if err != nil || current == nil || !current.SenderRemoved || current.Delivery == nil || current.Delivery.State != msg.DeliveryDelivered || current.Delivery.Report != "" || !current.Delivery.Reported || current.Audit == nil || !current.Audit.MetadataOnly {
				t.Fatal(current, err)
			}
			if err = h.flushMessageAudit(t.Context(), key, source.Token); err != nil {
				t.Fatal(err)
			}
			current, err = reopened.readRecord(t.Context(), key)
			if err != nil || !current.AuditDone {
				t.Fatal("forward Activity pending", current, err)
			}
			refs, err := reopened.messageEventCandidates(t.Context(), source.Actor, reopened.stateEventCheckFor(source.Actor))
			if err != nil || len(refs) != 0 {
				t.Fatal("recovered removed sender gained native report", refs, err)
			}
		})
	}
}

func TestMessagesForwardReceiverRemovalSurvivesFullTaskEventLog(t *testing.T) {
	f := pairedMessagesFixture(t)
	f.permit(t, 0, "app:other-agent")
	out, source, key := forwardPollAcceptance(t, f, "forward-receiver-removed-full")
	node := f.nodes[1]
	r, destinationKey, err := node.store.readByID(t.Context(), out.MessageID)
	if err != nil || r == nil {
		t.Fatal(r, err)
	}
	seedMessagesFullDeliveryLog(t, node.store, r.Actor, false)
	enrollment := node.run.d.readAgentEnrollment(node.run.ctx, node.run.f.panel, "other-agent")
	if enrollment.Enrollment == nil {
		t.Fatal(enrollment)
	}
	agent1478Set(t, node.run, "other-agent", enrollment.Enrollment.Revision, false)
	state, permitted, err := node.reader.host.reconcileMessageState(t.Context(), destinationKey, r)
	if err != nil || permitted || !state.Refused {
		t.Fatal(state, permitted, err)
	}
	current, err := node.store.readRecord(t.Context(), destinationKey)
	if err != nil || current.Delivery == nil || current.Delivery.Reason != msg.UndeliveredReasonReceiverRemoved || !current.Refused {
		t.Fatal(current, err)
	}
	refs, err := node.store.queueCandidates(t.Context(), current.Actor.Account, current.Receiver, "target")
	if err != nil || len(refs) != 0 {
		t.Fatal("removed receiver body remained deliverable", refs, err)
	}
	if err = f.nodes[0].reader.host.observeForwardDelivery(t.Context(), key, source); err != nil {
		t.Fatal(err)
	}
	forward, err := f.nodes[0].store.readRecord(t.Context(), key)
	if err != nil || forward.Delivery == nil || forward.Delivery.Reason != msg.UndeliveredReasonReceiverRemoved {
		t.Fatal(forward, err)
	}
}

func TestMessagesForwardPollUnreachableStaysUnconfirmed(t *testing.T) {
	f := pairedMessagesFixture(t)
	f.permit(t, 0, "app:other-agent")
	_, source, key := forwardPollAcceptance(t, f, "forward-offline")
	if err := f.listeners[1].Close(); err != nil {
		t.Fatal(err)
	}
	h := f.nodes[0].reader.host
	if err := h.observeForwardDelivery(t.Context(), key, source); err != nil {
		t.Fatal(err)
	}
	r, err := h.messages.readRecord(t.Context(), key)
	if err != nil || r.Delivery != nil {
		t.Fatal(r, err)
	}
	*f.nodes[0].now = time.UnixMilli(source.ExpiresUnixMs + 1)
	if err = h.observeForwardDelivery(t.Context(), key, source); err != nil {
		t.Fatal(err)
	}
	r, err = h.messages.readRecord(t.Context(), key)
	if err != nil || r.Delivery == nil || r.Delivery.State != msg.DeliveryUndelivered || r.Delivery.Reason != msg.UndeliveredReasonUnconfirmed {
		t.Fatal(r, err)
	}
}

func TestMessagesForwardPollActualPairRemoval(t *testing.T) {
	f := pairedMessagesFixture(t)
	f.permit(t, 0, "app:other-agent")
	_, source, key := forwardPollAcceptance(t, f, "forward-pair-removed")
	revision, _ := pairingStateRead(t, f.states[0])
	_, err := f.states[0].change(t.Context(), revision, func(d *pairingDocument) error {
		p := d.Peers[f.keys[1]]
		p.State, p.RightsEffect = "inactive", ""
		d.Peers[f.keys[1]] = p
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	h := f.nodes[0].reader.host
	if err = h.observeForwardDelivery(t.Context(), key, source); err != nil {
		t.Fatal(err)
	}
	r, err := h.messages.readRecord(t.Context(), key)
	if err != nil || r.Delivery == nil || r.Delivery.Reason != msg.UndeliveredReasonPeerRemoved {
		t.Fatal(r, err)
	}
}

func TestMessagesForwardPollUnretainedReferenceNeverUsesTransport(t *testing.T) {
	// A nil pairing owner would make an attempted transport unavailable. A fake
	// supplied record has no corresponding canonical custody and is a no-op.
	s, _, actor, _ := messagesStoreFixture(t)
	h := &applicationsHost{owner: actor.Account, messages: s}
	fake := &messageRecord{Profile: messageForwardProfile, Token: "invented", Actor: actor, Forward: &messageForwardRoute{}}
	if err := h.observeForwardDelivery(t.Context(), messageDigest("missing forward"), fake); err != nil {
		t.Fatal(err)
	}
}

func TestMessagesForwardPollPersonRefusalWithoutSendReplay(t *testing.T) {
	f := pairedMessagesFixture(t)
	if err := f.nodes[0].reader.host.messageRights.policy.Set(f.nodes[0].self, ActionApplicationMessage, messagePeerResource(f.keys[1], "app:other-agent"), true); err != nil {
		t.Fatal(err)
	}
	_, source, key := forwardPollAcceptance(t, f, "forward-person-refusal")
	book := f.nodes[1].run.f.a.book
	entries, _, err := book.ApplicationHistoryScoped()
	if err != nil {
		t.Fatal(err)
	}
	question := ""
	for _, entry := range entries {
		if entry.Record.Permission != nil && entry.Record.Pending() {
			question = entry.Record.ID
		}
	}
	if question == "" {
		t.Fatal("missing receiving permission question")
	}
	if _, outcome, err := book.AnswerApplication(question, "refuse"); err != nil || outcome != "answered" {
		t.Fatal(outcome, err)
	}
	h := f.nodes[0].reader.host
	ctx, cancel := context.WithTimeout(t.Context(), runtimeWait)
	defer cancel()
	// One bounded observation may leave delivery unconfirmed while the pair
	// is busy. Retry observation only. The original Send remains untouched.
	for {
		if err := h.observeForwardDelivery(ctx, key, source); err != nil {
			t.Fatal(err)
		}
		r, err := h.messages.readRecord(ctx, key)
		if err != nil || r == nil {
			t.Fatalf("retained forward record: %v", err)
		}
		if r.Delivery != nil {
			if r.Delivery.State != msg.DeliveryUndelivered || r.Delivery.Reason != msg.UndeliveredReasonRefused {
				t.Fatalf("unexpected terminal delivery: %+v", r.Delivery)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("person refusal remained unconfirmed: %v", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestMessagesForwardPollTimeoutIsBoundedAndUnconfirmed(t *testing.T) {
	f := pairedMessagesFixture(t)
	f.permit(t, 0, "app:other-agent")
	_, source, key := forwardPollAcceptance(t, f, "forward-stalled-handshake")
	address := f.listeners[1].Addr().String()
	if err := f.listeners[1].Close(); err != nil {
		t.Fatal(err)
	}
	// loopback-scan: judged the address is the fixture's own 127.0.0.1 listener, closed and rebound
	stall, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		connection, err := stall.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		<-stop
	}()
	t.Cleanup(func() {
		close(stop)
		stall.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("stalled listener did not join")
		}
	})
	h := f.nodes[0].reader.host
	started := time.Now()
	if err := h.observeForwardDelivery(t.Context(), key, source); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatal("observation exceeded bounded transport budget", elapsed)
	}
	r, err := h.messages.readRecord(t.Context(), key)
	if err != nil || r.Delivery != nil {
		t.Fatal("timeout inferred terminal delivery", r, err)
	}
}

func TestMessagesForwardPollOldPeerUnsupported(t *testing.T) {
	f := pairedMessagesFixture(t)
	f.permit(t, 0, "app:other-agent")
	_, source, key := forwardPollAcceptance(t, f, "forward-old-method")
	address := f.listeners[1].Addr().String()
	if err := f.listeners[1].Close(); err != nil {
		t.Fatal(err)
	}
	// loopback-scan: judged the address is the fixture's own 127.0.0.1 listener, closed and rebound
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	identity, _, err := f.states[1].identity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sourceIdentity, _, err := f.states[0].identity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sourceCertificate, err := x509.ParseCertificate(sourceIdentity.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(sourceCertificate)
	server := remote.Server{TLS: &tls.Config{Certificates: []tls.Certificate{identity}, ClientCAs: roots}, Admit: f.states[1].admit,
		Handler: func(ctx context.Context, peer remote.Peer, frame []byte) ([]byte, error) {
			dispatcher := f.nodes[1].reader.host.remoteMessages(ctx, peer, f.states[1].admit)
			dispatcher.Handler = &forwardPollOldMessages{Messages: dispatcher.Handler}
			return fwire.ServeEndpointRemote(frame, "openabstractions", "", func(string, string, string) error { return nil }, dispatcher)
		}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("old-peer server did not join")
		}
	})
	h := f.nodes[0].reader.host
	if err := h.observeForwardDelivery(t.Context(), key, source); err != nil {
		t.Fatal(err)
	}
	r, err := h.messages.readRecord(t.Context(), key)
	if err != nil || r.Delivery != nil {
		t.Fatal("unsupported method inferred terminal delivery", r, err)
	}
	*f.nodes[0].now = time.UnixMilli(source.ExpiresUnixMs + 1)
	if err = h.observeForwardDelivery(t.Context(), key, source); err != nil {
		t.Fatal(err)
	}
	r, err = h.messages.readRecord(t.Context(), key)
	if err != nil || r.Delivery == nil || r.Delivery.State != msg.DeliveryUndelivered || r.Delivery.Reason != msg.UndeliveredReasonUnconfirmed {
		t.Fatal(r, err)
	}
}
