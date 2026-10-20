//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	mwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/machines"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-identity/remote"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

type remoteMessagesFixture struct {
	nodes     [2]*messagesObserveRun
	states    [2]*runtimePairingState
	keys      [2]string
	listeners [2]net.Listener
	audits    [2]*messageAuditSink
}

// Actual native kept agents, canonical private pairing books and the normal
// generated runtime/mTLS handler. The fixture supplies already-approved books;
// it performs no pairing UI or owner configuration operation.
func pairedMessagesFixture(t *testing.T) *remoteMessagesFixture {
	t.Helper()
	f := &remoteMessagesFixture{}
	var identities [2]tls.Certificate
	var certs [2]*x509.Certificate
	var keys [2][32]byte
	for i := range f.nodes {
		f.nodes[i] = messagesObserveFixture(t, true)
		h := f.nodes[i].reader.host
		h.report = func(err error) { t.Log(err) }
		f.audits[i] = &messageAuditSink{}
		if err := h.configureMessageAudit(f.audits[i]); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(h.messageAudit.close)
		h.owner = h.messageRights.owner
		catalog, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{})
		if err != nil {
			t.Fatal(err)
		}
		h.messageRights.policy.Definitions = catalog
		if err = h.messageRights.policy.EnableRevisionDecisions("remote-messages-fixture@1"); err != nil {
			t.Fatal(err)
		}
		f.states[i] = pairingStateFixture(t)
		h.messagePairing = f.states[i]
		h.messageRights.messagePairing = f.states[i]
		identities[i], _, err = f.states[i].identity(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		certs[i], err = x509.ParseCertificate(identities[i].Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		keys[i] = sha256.Sum256(certs[i].RawSubjectPublicKeyInfo)
		f.keys[i] = hex.EncodeToString(keys[i][:])
		f.listeners[i], err = net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
	}
	var proposals [2][]byte
	for i := range proposals {
		var err error
		proposals[i], err = preparePairingProposal(mwire.MachinePreparation{Request: mwire.MachineScopeKindOneHostInference, Offer: mwire.MachineScopeKindOneHostInference, OfferHost: "owned", NormalServiceAddress: f.listeners[i].Addr().String(), BindingName: "paired"}, remote.PairKeys{Role: byte(i + 1), LocalKey: keys[i], PeerKey: keys[1-i]})
		if err != nil {
			t.Fatal(err)
		}
	}
	transcript := sha256.Sum256([]byte("paired Messages native fixture"))
	for i := range f.nodes {
		state, h := f.states[i], f.nodes[i].reader.host
		consent := &pairingConsent{Operation: "fixture", Question: "fixture", Scope: "fixture", Program: "fixture", TextHash: strings.Repeat("a", 64), ExpectedRights: "fixture", Role: byte(i + 1), LocalKey: keys[i], PeerKey: keys[1-i], Transcript: transcript, Check: "123456", Initiator: proposals[0], Responder: proposals[1], NativeYes: true, TransportComplete: true}
		rev, _ := pairingStateRead(t, state)
		_, err := state.change(t.Context(), rev, func(d *pairingDocument) error {
			d.Peers[f.keys[1-i]] = pairingPeerRecord{Certificate: certs[1-i].Raw, Transcript: transcript, State: "active", ActivationKind: "rights_applied", RightsEffect: "fixture", MinimumRevision: "fixture", BindingName: "paired", PeerServiceAddress: f.listeners[1-i].Addr().String(), Consent: consent}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		_, services, config, _ := incomingFixture(t)
		roots := x509.NewCertPool()
		roots.AddCert(certs[1-i])
		config.TLS = &tls.Config{Certificates: []tls.Certificate{identities[i]}, ClientCAs: roots}
		config.Admit = state.admit
		services.registry = &registryHost{pairing: &runtimePairing{state: state, messageHost: h}}
		if err = h.messageRights.policy.RegisterAction(remoteServeAction); err != nil {
			t.Fatal(err)
		}
		if err = h.messageRights.policy.Set(rights.Subject{Account: h.owner, Program: "remote:" + f.keys[1-i]}, remoteServeAction, "account", true); err != nil {
			t.Fatal(err)
		}
		decision := h.messageRights.decideRemote(t.Context(), pairingStateAdmission(certs[1-i]), state.admit, remoteServeAction, "account", "")
		if decision.Outcome != rights.DecisionOutcomePermitted {
			t.Fatalf("fixture remote gate: %+v", decision)
		}
		prepared, err := fixturePrepareRemote(t.Context(), fixtureRemoteOptions(config), h.messageRights, services)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- prepared.server.Serve(ctx, f.listeners[i]) }()
		t.Cleanup(func() {
			cancel()
			select {
			case e := <-done:
				if e != nil && !errors.Is(e, context.Canceled) && !errors.Is(e, net.ErrClosed) {
					t.Error(e)
				}
			case <-time.After(5 * time.Second):
				t.Error("paired server did not join")
			}
		})
	}
	return f
}

func (f *remoteMessagesFixture) sender(t *testing.T, side int) (string, string) {
	t.Helper()
	n := f.nodes[side]
	instance := messages1608Presence(t, n.run, n.reader.call, "", "source")
	if opened, e := n.reader.InboxOpen(instance, "source", "ready"); e != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, e)
	}
	return instance, "source"
}
func (f *remoteMessagesFixture) request(side int, key string) msg.SendRequest {
	return msg.SendRequest{PeerKey: f.keys[1-side], To: "app:other-agent", Context: "target", Kind: msg.KindNote, Subject: "paired", Body: "private body", IdempotencyKey: key, ExpiresUnixMs: f.nodes[side].now.Add(time.Hour).UnixMilli()}
}
func (f *remoteMessagesFixture) permit(t *testing.T, side int, to string) {
	t.Helper()
	n := f.nodes[side]
	if e := n.reader.host.messageRights.policy.Set(n.self, ActionApplicationMessage, messagePeerResource(f.keys[1-side], to), true); e != nil {
		t.Fatal(e)
	}
	other := f.nodes[1-side]
	if e := other.reader.host.messageRights.policy.Set(rights.Subject{Account: other.reader.host.owner, Program: "remote:" + f.keys[side]}, ActionApplicationMessage, to, true); e != nil {
		t.Fatal(e)
	}
}

func TestRemoteSendAsksReceivingPerson(t *testing.T) {
	f := pairedMessagesFixture(t)
	instance, ctx := f.sender(t, 0)
	request := f.request(0, "held")
	// Existing local app permission cannot authorize the paired destination.
	if e := f.nodes[0].reader.host.messageRights.policy.Set(f.nodes[0].self, ActionApplicationMessage, request.To, true); e != nil {
		t.Fatal(e)
	}
	out, e := f.nodes[0].reader.Send(instance, ctx, request)
	if e != nil || out.Outcome == msg.OutcomeSent || !out.AwaitingPermission {
		t.Fatal(out, e)
	}
	messagesReceiverPersonAllow(t, f.nodes[0].run, messagePeerResource(f.keys[1], request.To))
	out, e = f.nodes[0].reader.Send(instance, ctx, request)
	if e != nil || out.Outcome != msg.OutcomeSent || !out.AwaitingPermission {
		t.Fatal(out, e)
	}
	entries, _, e := f.nodes[1].run.f.a.book.ApplicationHistoryScoped()
	if e != nil {
		t.Fatal(e)
	}
	var question asks.Record
	for _, entry := range entries {
		if entry.Record.Permission != nil {
			question = entry.Record
		}
	}
	if question.ID == "" || question.Permission.Targets[0].Program != "remote:"+f.keys[0] || question.PublisherTrusted {
		t.Fatal(question)
	}
	if _, e = f.nodes[1].run.f.a.authorizeAnswerer(t.Context(), answerer{subject: f.nodes[1].self}, question, f.nodes[1].run.f.a.scope, "allow"); e == nil {
		t.Fatal("assistant answered remote first-use")
	}
	// Repeated send reuses one held canonical receipt and one occurrence.
	again, _ := f.nodes[0].reader.Send(instance, ctx, request)
	if again.MessageID != out.MessageID {
		t.Fatal(out, again)
	}
	if !strings.Contains(question.Asker, "Unverified program claim") {
		t.Fatal("remote claim missing from trusted display", question)
	}
	messagesReceiverPersonAllow(t, f.nodes[1].run, request.To)
	released, e := f.nodes[0].reader.Send(instance, ctx, request)
	if e != nil || released.Outcome != msg.OutcomeSent || released.AwaitingPermission || released.Refused || released.MessageID != out.MessageID {
		t.Fatal(released, e)
	}
}

func TestMessagesRemoteForgedNamespaceCannotEscapeReceiverDenial(t *testing.T) {
	f := pairedMessagesFixture(t)
	f.permit(t, 0, "app:other-agent")
	client, _, err := f.nodes[0].reader.pairedTransport(f.keys[1])
	if err != nil {
		t.Fatal(err)
	}
	_, generation, _, err := messagePeerSnapshot(t.Context(), f.states[0], f.keys[1])
	if err != nil {
		t.Fatal(err)
	}
	request := f.request(0, "forged-namespace")
	request.PairingEpoch = generation
	request.Origin = &msg.OriginClaim{Namespace: strings.Repeat("a", 64), Application: "native-agent", ProgramLabel: "claim"}
	peer := rights.Subject{Account: f.nodes[1].reader.host.owner, Program: "remote:" + f.keys[0]}
	if err = f.nodes[1].reader.host.messageRights.policy.Set(peer, ActionApplicationMessage, request.To, false); err != nil {
		t.Fatal(err)
	}
	for _, namespace := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		request.Origin.Namespace = namespace
		out, e := client.Send("", "source", request)
		if e != nil || out.Outcome != msg.OutcomeForbidden || out.MessageID != "" || out.AwaitingPermission {
			t.Fatal(out, e)
		}
		if out.Refusal == nil || out.Refusal.Source != rights.RefusalSourceRights || out.Refusal.Decision != rights.RefusalDecisionDenied || out.Refusal.Ask != rights.RefusalAskNone || out.Refusal.Action != ActionApplicationMessage {
			t.Fatal("receiving authority detail lost through actual paired codec", out)
		}
	}
}

func TestRemoteSenderIsClaimedLabelOnly(t *testing.T) {
	f := pairedMessagesFixture(t)
	instance, ctx := f.sender(t, 0)
	f.permit(t, 0, "app:other-agent")
	request := f.request(0, "claims")
	out, e := f.nodes[0].reader.Send(instance, ctx, request)
	if e != nil || out.Outcome != msg.OutcomeSent || out.Reference == nil || out.Reference.OwnerPeerKey != f.keys[1] {
		t.Fatal(out, e)
	}
	record, _, e := f.nodes[1].store.readByID(t.Context(), out.MessageID)
	if e != nil || record == nil || record.Actor.Program != "remote:"+f.keys[0] || record.Actor.Remote == nil || record.Actor.ObservedProgram != "" {
		t.Fatal(record, e)
	}
	page := f.nodes[1].other.headersWait("target", "", 0)
	if page.Outcome != msg.OutcomePage || len(page.Headers) != 1 || page.Headers[0].RemoteOrigin == nil || page.Headers[0].RemoteOrigin.PeerKey != f.keys[0] || len(page.Messages) != 0 {
		t.Fatal(page)
	}
	if len(f.audits[1].records) == 0 {
		t.Fatal("remote canonical acceptance missed existing audit owner")
	}
	attrs := f.audits[1].records[0].Attrs
	if attrs["from_program"] != "remote:"+f.keys[0] || !strings.Contains(attrs["from"], "unverified program claim") || strings.Contains(attrs["from"], request.Body) {
		t.Fatal(attrs)
	}
}

func TestResendSameRequestKeyIdempotent(t *testing.T) {
	f := pairedMessagesFixture(t)
	instance, ctx := f.sender(t, 0)
	f.permit(t, 0, "app:other-agent")
	request := f.request(0, "same")
	first, _ := f.nodes[0].reader.Send(instance, ctx, request)
	again, _ := f.nodes[0].reader.Send(instance, ctx, request)
	if first.Outcome != msg.OutcomeSent || again.MessageID != first.MessageID {
		t.Fatal(first, again)
	}
	request.Body = "changed"
	conflict, _ := f.nodes[0].reader.Send(instance, ctx, request)
	if conflict.Outcome != msg.OutcomeConflict {
		t.Fatal(conflict)
	}
	// Expired key reuse is a fresh acceptance, matching the local store contract.
	now := time.UnixMilli(request.ExpiresUnixMs + 1)
	*f.nodes[0].now = now
	*f.nodes[1].now = now
	request.ExpiresUnixMs = now.Add(time.Hour).UnixMilli()
	fresh, err := f.nodes[0].reader.Send(instance, ctx, request)
	if err != nil || fresh.Outcome != msg.OutcomeSent || fresh.MessageID == first.MessageID {
		t.Fatal("expired receipt blocked fresh request", fresh, err)
	}
}

func TestSleepingPeerReadsUnreachable(t *testing.T) {
	f := pairedMessagesFixture(t)
	instance, ctx := f.sender(t, 0)
	f.permit(t, 0, "app:other-agent")
	f.listeners[1].Close()
	out, _ := f.nodes[0].reader.Send(instance, ctx, f.request(0, "offline"))
	if out.Outcome != msg.OutcomeUnavailable || out.Reason != "unreachable" {
		t.Fatal(out)
	}
	// A TCP endpoint that never completes authentication also proves that no
	// application frame was dispatched. The connect budget covers handshake.
	_, port, err := net.SplitHostPort(f.listeners[1].Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	stall, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		conn, e := stall.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		<-stop
	}()
	cleanup := func() {
		once.Do(func() { close(stop); stall.Close() })
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("owned stalled connection did not join")
		}
	}
	t.Cleanup(cleanup)
	started := time.Now()
	out, _ = f.nodes[0].reader.Send(instance, ctx, f.request(0, "handshake-offline"))
	cleanup()
	if out.Outcome != msg.OutcomeUnavailable || out.Reason != "unreachable" || time.Since(started) > 5*time.Second {
		t.Fatal("connect/handshake budget or pre-dispatch result", out, time.Since(started))
	}
}

func TestRemoteSendCannotStartReceiver(t *testing.T) {
	f := pairedMessagesFixture(t)
	instance, ctx := f.sender(t, 0)
	f.permit(t, 0, "app:other-agent")
	before := len(f.nodes[1].run.d.instances)
	out, _ := f.nodes[0].reader.Send(instance, ctx, f.request(0, "closed"))
	if out.Outcome != msg.OutcomeSent {
		t.Fatal(out)
	}
	if len(f.nodes[1].run.d.instances) != before || f.nodes[1].run.d.nativeInboxes != nil {
		t.Fatal("remote send created presence/inbox")
	}
}

func TestMessagesRemoteLeafKeepsBodyAndNativeOwnershipLocal(t *testing.T) {
	f := pairedMessagesFixture(t)
	instance, contextName := f.sender(t, 0)
	f.permit(t, 0, "app:other-agent")
	client, _, err := f.nodes[0].reader.pairedTransport(f.keys[1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.InboxOpen("forged", "target", "ready"); err == nil {
		t.Fatal("remote InboxOpen entered local native owner")
	}
	if _, err = client.Wait("forged", "target", msg.ViewBody, "", "", 0); err == nil {
		t.Fatal("remote body Wait entered local native owner")
	}
	if _, err = client.ReadForPerson("forged"); err == nil {
		t.Fatal("remote person reader entered local operator owner")
	}
	page, err := client.InboxesList(msg.InboxQuery{PeerKey: f.keys[0]})
	if err != nil || page.Outcome != msg.OutcomeForbidden || len(page.Inboxes) != 0 {
		t.Fatal("remote leaf forwarded", page, err)
	}
	request := f.request(0, "forged")
	request.Origin = &msg.OriginClaim{Namespace: strings.Repeat("a", 64), Application: "native-agent", ProgramLabel: "trusted-looking.exe"}
	if out, err := f.nodes[0].reader.Send(instance, contextName, request); err != nil || out.Outcome != msg.OutcomeForbidden {
		t.Fatal("local caller supplied origin", out, err)
	}
	if out, err := client.Send("fake-native-instance", contextName, request); err != nil || out.Outcome != msg.OutcomeForbidden {
		t.Fatal("remote claim impersonated native instance", out, err)
	}
	request.Origin = nil
	request.PairingEpoch = strings.Repeat("a", 64)
	if out, err := f.nodes[0].reader.Send(instance, contextName, request); err != nil || out.Outcome != msg.OutcomeForbidden {
		t.Fatal("native caller supplied runtime generation", out, err)
	}
	request.PairingEpoch = ""
	noCall := &messagesReceiver{host: f.nodes[0].reader.host, ctx: t.Context()}
	if out, err := noCall.Send(instance, contextName, request); err != nil || out.Outcome != msg.OutcomeForbidden {
		t.Fatal("unbound source forwarded", out, err)
	}
	node := f.nodes[0]
	enrollment := node.run.d.readAgentEnrollment(node.run.ctx, node.run.f.panel, "native-agent")
	if enrollment.Enrollment == nil {
		t.Fatal("sender enrollment unavailable", enrollment)
	}
	agent1478Set(t, node.run, "native-agent", enrollment.Enrollment.Revision, false)
	if out, err := node.reader.Send(instance, contextName, request); err != nil || out.Outcome != msg.OutcomeForbidden {
		t.Fatal("revoked native sender forwarded with retained peer permit", out, err)
	}
}

func TestMessagesRemotePairGenerationCannotReviveReceipt(t *testing.T) {
	f := pairedMessagesFixture(t)
	instance, contextName := f.sender(t, 0)
	f.permit(t, 0, "app:other-agent")
	request := f.request(0, "generation")
	accepted, err := f.nodes[0].reader.Send(instance, contextName, request)
	if err != nil || accepted.Outcome != msg.OutcomeSent {
		t.Fatal(accepted, err)
	}
	state := f.states[1]
	revision, _ := pairingStateRead(t, state)
	_, err = state.change(t.Context(), revision, func(d *pairingDocument) error {
		p := d.Peers[f.keys[0]]
		p.State = "inactive"
		p.RightsEffect = ""
		d.Peers[f.keys[0]] = p
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	record, key, err := f.nodes[1].store.readByID(t.Context(), accepted.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	current, permitted, err := f.nodes[1].reader.host.reconcileMessageState(t.Context(), key, record)
	if err != nil || permitted || !current.Refused {
		t.Fatal(current, permitted, err)
	}
	revision, _ = pairingStateRead(t, state)
	_, err = state.change(t.Context(), revision, func(d *pairingDocument) error {
		p := d.Peers[f.keys[0]]
		p.State = "active"
		p.RightsEffect = "fixture"
		p.Transcript = sha256.Sum256([]byte("another actual pairing occurrence"))
		p.Consent.Transcript = p.Transcript
		d.Peers[f.keys[0]] = p
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.nodes[0].reader.Send(instance, contextName, request)
	if err != nil || replay.Outcome != msg.OutcomeForbidden || replay.MessageID != "" {
		t.Fatal("re-pair revived old receipt", replay, err)
	}
	request.IdempotencyKey = "old-generation-fresh-key"
	if buffered, err := f.nodes[0].reader.Send(instance, contextName, request); err != nil || buffered.Outcome != msg.OutcomeForbidden {
		t.Fatal("old source generation entered new pairing", buffered, err)
	}
	page := f.nodes[1].other.headersWait("target", "", 0)
	if page.Outcome != msg.OutcomePage || len(page.Headers) != 0 {
		t.Fatal("removed pairing retained delivery", page)
	}
}

func TestMessagesRemoteExplicitReconciliationAcrossPublicationLoss(t *testing.T) {
	f := pairedMessagesFixture(t)
	instance, contextName := f.sender(t, 0)
	f.permit(t, 0, "app:other-agent")
	actor, _, info, ok := f.nodes[0].reader.owner(instance, contextName)
	if !ok {
		t.Fatal("native source ended")
	}
	remoteActor := messageActor{Account: f.nodes[1].reader.host.owner, Program: "remote:" + f.keys[0], Context: contextName, Remote: &messageRemoteActor{Namespace: messageNamespace(actor.Account, info.Image.Key)}}
	for _, boundary := range []string{"before_record", "record_published"} {
		t.Run(boundary, func(t *testing.T) {
			request := f.request(0, boundary)
			request.Body = boundary
			fired := false
			f.nodes[1].store.boundary = func(at string) error {
				if at == boundary && !fired {
					fired = true
					return errors.New("owned lost-publication fixture")
				}
				return nil
			}
			first, err := f.nodes[0].reader.Send(instance, contextName, request)
			f.nodes[1].store.boundary = nil
			if err != nil || !fired || first.Outcome != msg.OutcomeUnknown {
				t.Fatal(first, err, fired)
			}
			before, err := f.nodes[1].store.readRecord(t.Context(), f.nodes[1].store.key(remoteActor, request.IdempotencyKey))
			if err != nil {
				t.Fatal(err)
			}
			if (boundary == "record_published") != (before != nil) {
				t.Fatal("negative did not reach intended canonical boundary", before)
			}
			reconciled, err := f.nodes[0].reader.Send(instance, contextName, request)
			if err != nil || reconciled.Outcome != msg.OutcomeSent || before != nil && before.ID != reconciled.MessageID {
				t.Fatal(reconciled, err)
			}
			again, err := f.nodes[0].reader.Send(instance, contextName, request)
			if err != nil || again.MessageID != reconciled.MessageID {
				t.Fatal(again, err)
			}
		})
	}
}

func TestMessagesRemoteReturningReplyUsesQuestionOwnerFence(t *testing.T) {
	f := pairedMessagesFixture(t)
	instance, contextName := f.sender(t, 0)
	f.permit(t, 0, "app:other-agent")
	request := f.request(0, "question")
	request.Kind = msg.KindQuestion
	request.AnswerByUnixMs = f.nodes[0].now.Add(5 * time.Minute).UnixMilli()
	question, err := f.nodes[0].reader.Send(instance, contextName, request)
	if err != nil || question.Outcome != msg.OutcomeSent || question.Reference == nil {
		t.Fatal(question, err)
	}
	receiver := f.nodes[1].other
	pin, err := receiver.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	presence := receiver.host.directory.announceInSession(t.Context(), subject, session, fwire.ApplicationPresence{Application: "other-agent", LeaseMs: 60000, Interfaces: []fwire.ApplicationInterface{}, Contexts: []fwire.ApplicationContext{{Name: "target"}}})
	if presence.Outcome != fwire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	if opened, e := receiver.InboxOpen(presence.Instance, "target", "ready"); e != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, e)
	}
	if err = receiver.host.messageRights.policy.Set(subject, ActionApplicationMessage, messagePeerResource(f.keys[0], "app:native-agent"), true); err != nil {
		t.Fatal(err)
	}
	if err = f.nodes[0].reader.host.messageRights.policy.Set(rights.Subject{Account: f.nodes[0].reader.host.owner, Program: "remote:" + f.keys[1]}, ActionApplicationMessage, "app:native-agent", true); err != nil {
		t.Fatal(err)
	}
	reply := msg.SendRequest{PeerKey: f.keys[0], To: "app:native-agent", Context: contextName, Kind: msg.KindReply, Subject: "reply", Body: "reply body", IdempotencyKey: "remote-reply", ExpiresUnixMs: f.nodes[1].now.Add(time.Hour).UnixMilli(), ReplyReference: question.Reference}
	fired := false
	f.nodes[1].store.boundary = func(at string) error {
		if at == "remote_reply_returned" && !fired {
			fired = true
			return errors.New("owned crash after accepted return")
		}
		return nil
	}
	first, err := receiver.Send(presence.Instance, "target", reply)
	f.nodes[1].store.boundary = nil
	if err != nil || !fired || first.Outcome != msg.OutcomeUnknown {
		t.Fatal(first, err, fired)
	}
	stored, key, err := f.nodes[1].store.readByID(t.Context(), question.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.nodes[1].store.currentState(t.Context(), key, stored)
	if err != nil || state.RemoteReply == nil || state.RemoteReply.Completed {
		t.Fatal(state, err)
	}
	changed := reply
	changed.Body = "different answer"
	if out, _ := receiver.Send(presence.Instance, "target", changed); out.Outcome != msg.OutcomeConflict {
		t.Fatal("changed reply escaped uncertainty", out)
	}
	completed, err := receiver.Send(presence.Instance, "target", reply)
	if err != nil || completed.Outcome != msg.OutcomeSent || completed.Reference == nil || completed.Reference.OwnerPeerKey != f.keys[0] {
		t.Fatal(completed, err)
	}
	state, err = f.nodes[1].store.currentState(t.Context(), key, stored)
	if err != nil || state.TaskState != msg.TaskStateCompleted {
		t.Fatal(state, err)
	}
	page := f.nodes[0].reader.headersWait(contextName, "", 0)
	if page.Outcome != msg.OutcomePage || len(page.Headers) != 1 || page.Headers[0].ReplyReference == nil || *page.Headers[0].ReplyReference != *question.Reference {
		t.Fatal(page)
	}
}

func TestMessagesNativeRemoteReplyUsesQuestionOwnerFence(t *testing.T) {
	f := pairedMessagesFixture(t)
	instance, source := f.sender(t, 0)
	f.permit(t, 0, "app:other-agent")
	request := f.request(0, "native-question")
	request.Kind = msg.KindQuestion
	request.AnswerByUnixMs = f.nodes[0].now.Add(5 * time.Minute).UnixMilli()
	question, err := f.nodes[0].reader.Send(instance, source, request)
	if err != nil || question.Outcome != msg.OutcomeSent || question.Reference == nil {
		t.Fatal(question, err)
	}
	receiver := f.nodes[1].other
	pin, err := receiver.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	presence := receiver.host.directory.announceInSession(t.Context(), subject, session, fwire.ApplicationPresence{Application: "other-agent", LeaseMs: 60000, Interfaces: []fwire.ApplicationInterface{}, Contexts: []fwire.ApplicationContext{{Name: "target"}}})
	if presence.Outcome != fwire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	if opened, e := receiver.InboxOpen(presence.Instance, "target", ""); e != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, e)
	}
	if err = receiver.host.messageRights.policy.Set(subject, ActionApplicationMessage, messagePeerResource(f.keys[0], "app:native-agent"), true); err != nil {
		t.Fatal(err)
	}
	if err = f.nodes[0].reader.host.messageRights.policy.Set(rights.Subject{Account: f.nodes[0].reader.host.owner, Program: "remote:" + f.keys[1]}, ActionApplicationMessage, "app:native-agent", true); err != nil {
		t.Fatal(err)
	}
	fired := false
	f.nodes[1].store.boundary = func(at string) error {
		if at == "remote_reply_returned" && !fired {
			fired = true
			return errors.New("owned lost response")
		}
		return nil
	}
	first, err := receiver.Reply(presence.Instance, "target", question.MessageID, "reply body")
	f.nodes[1].store.boundary = nil
	if err != nil || !fired || first.Outcome != msg.OutcomeUnknown {
		t.Fatal(first, err, fired)
	}
	if changed, _ := receiver.Reply(presence.Instance, "target", question.MessageID, "changed"); changed.Outcome != msg.OutcomeConflict {
		t.Fatal(changed)
	}
	done, err := receiver.Reply(presence.Instance, "target", question.MessageID, "reply body")
	if err != nil || done.Outcome != msg.OutcomeSent || done.Reference == nil || done.Reference.OwnerPeerKey != f.keys[0] {
		t.Fatal(done, err)
	}
	*f.nodes[1].now = f.nodes[1].now.Add(6 * time.Minute)
	again, err := receiver.Reply(presence.Instance, "target", question.MessageID, "reply body")
	if err != nil || again.Outcome != msg.OutcomeSent || again.MessageID != done.MessageID || again.ContextID != done.ContextID {
		t.Fatal("retained paired receipt after answer deadline", again, err)
	}
	if err = receiver.host.messageRights.policy.Set(subject, ActionApplicationMessage, messagePeerResource(f.keys[0], "app:native-agent"), false); err != nil {
		t.Fatal(err)
	}
	if got, _ := receiver.Reply(presence.Instance, "target", question.MessageID, "reply body"); got.Outcome != msg.OutcomeForbidden {
		t.Fatal("revoked paired authority", got)
	}
}

func TestMessagesRemoteRefusalCoalescesAndCoolsDown(t *testing.T) {
	f := pairedMessagesFixture(t)
	instance, contextName := f.sender(t, 0)
	request := f.request(0, "refused")
	if e := f.nodes[0].reader.host.messageRights.policy.Set(f.nodes[0].self, ActionApplicationMessage, messagePeerResource(f.keys[1], request.To), true); e != nil {
		t.Fatal(e)
	}
	held, err := f.nodes[0].reader.Send(instance, contextName, request)
	if err != nil || held.Outcome != msg.OutcomeSent || !held.AwaitingPermission {
		t.Fatal(held, err)
	}
	book := f.nodes[1].run.f.a.book
	entries, _, err := book.ApplicationHistoryScoped()
	if err != nil {
		t.Fatal(err)
	}
	question := asks.Record{}
	pending := 0
	for _, entry := range entries {
		if entry.Record.Permission != nil && entry.Record.Pending() {
			pending++
			question = entry.Record
		}
	}
	if pending != 1 {
		t.Fatal("peer/action admitted multiple occurrences", pending)
	}
	other := request
	other.To = "app:native-agent"
	other.IdempotencyKey = "other-target"
	if e := f.nodes[0].reader.host.messageRights.policy.Set(f.nodes[0].self, ActionApplicationMessage, messagePeerResource(f.keys[1], other.To), true); e != nil {
		t.Fatal(e)
	}
	if result, _ := f.nodes[0].reader.Send(instance, contextName, other); result.Outcome == msg.OutcomeSent {
		t.Fatal("pending question broadened target", result)
	}
	if _, outcome, e := book.AnswerApplication(question.ID, "refuse"); e != nil || outcome != "answered" {
		t.Fatal(outcome, e)
	}
	refused, err := f.nodes[0].reader.Send(instance, contextName, request)
	if err != nil || refused.Outcome != msg.OutcomeSent || !refused.Refused || refused.AwaitingPermission || refused.MessageID != held.MessageID {
		t.Fatal("No left a live held request", refused, err)
	}
	request.IdempotencyKey = "after-no"
	if result, _ := f.nodes[0].reader.Send(instance, contextName, request); result.Outcome != msg.OutcomeForbidden {
		t.Fatal("No cooldown admitted another request", result)
	}
	entries, _, err = book.ApplicationHistoryScoped()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Record.Permission != nil && entry.Record.Pending() {
			t.Fatal("No cooldown raised a new popup", entry.Record)
		}
	}
}

func TestMessagesRemoteInboxFilteringAndOutageClearMetadata(t *testing.T) {
	f := pairedMessagesFixture(t)
	f.permit(t, 0, "app:other-agent")
	receiver := f.nodes[1].other
	pin, err := receiver.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	presence := receiver.host.directory.announceInSession(t.Context(), subject, session, fwire.ApplicationPresence{Application: "other-agent", LeaseMs: 60000, Interfaces: []fwire.ApplicationInterface{}, Contexts: []fwire.ApplicationContext{{Name: "target"}}})
	if presence.Outcome != fwire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	if opened, e := receiver.InboxOpen(presence.Instance, "target", "ready"); e != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, e)
	}
	query := msg.InboxQuery{PeerKey: f.keys[1], Receiver: "app:other-agent"}
	page, err := f.nodes[0].reader.InboxesList(query)
	if err != nil || page.Outcome != msg.OutcomePage || len(page.Inboxes) != 1 || page.Inboxes[0].Program != "app:other-agent" || page.Inboxes[0].Mine != nil {
		t.Fatal(page, err)
	}
	if e := f.nodes[0].reader.host.messageRights.policy.Set(f.nodes[0].self, ActionApplicationMessage, messagePeerResource(f.keys[1], "app:other-agent"), false); e != nil {
		t.Fatal(e)
	}
	page, err = f.nodes[0].reader.InboxesList(query)
	if err != nil || page.Outcome != msg.OutcomeForbidden || len(page.Inboxes) != 0 || page.Cursor != "" {
		t.Fatal("source grant bypassed", page, err)
	}
	if e := f.nodes[0].reader.host.messageRights.policy.Set(f.nodes[0].self, ActionApplicationMessage, messagePeerResource(f.keys[1], "app:other-agent"), true); e != nil {
		t.Fatal(e)
	}
	for _, side := range []int{0, 1} {
		t.Run([]string{"source", "receiver"}[side], func(t *testing.T) {
			path := filepath.Join(filepath.Dir(f.nodes[side].run.f.panel.Program), "decisions.json")
			raw, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(path, []byte("{"), 0600); e != nil {
				t.Fatal(e)
			}
			page, err := f.nodes[0].reader.InboxesList(query)
			if e = os.WriteFile(path, raw, 0600); e != nil {
				t.Fatal(e)
			}
			if err != nil || page.Outcome != msg.OutcomeUnavailable || len(page.Inboxes) != 0 || page.Cursor != "" {
				t.Fatal("outage leaked partial metadata", page, err)
			}
		})
	}
}

func TestMessagesRemoteInboxScopesHideForeignPagination(t *testing.T) {
	f := pairedMessagesFixture(t)
	receiver := f.nodes[1].other
	// Establish fixture permits before presence churn changes prepared inputs.
	for _, target := range []string{"app:other-agent", "app:native-agent"} {
		if e := receiver.host.messageRights.policy.Set(rights.Subject{Account: receiver.host.owner, Program: "remote:" + f.keys[0]}, ActionApplicationMessage, target, true); e != nil {
			t.Fatal(e)
		}
	}
	pin, err := receiver.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	for start := 0; start < 65; start += 32 {
		contexts := []fwire.ApplicationContext{}
		for i := start; i < start+32 && i < 65; i++ {
			contexts = append(contexts, fwire.ApplicationContext{Name: fmt.Sprintf("box-%02d", i)})
		}
		presence := receiver.host.directory.announceInSession(t.Context(), subject, session, fwire.ApplicationPresence{Application: "other-agent", LeaseMs: 60000, Interfaces: []fwire.ApplicationInterface{}, Contexts: contexts})
		if presence.Outcome != fwire.ApplicationOutcomeApplied {
			t.Fatal(presence)
		}
		for _, context := range contexts {
			if out, e := receiver.InboxOpen(presence.Instance, context.Name, "ready"); e != nil || out.Outcome != msg.OutcomeOpened {
				t.Fatal(out, e)
			}
		}
	}
	// The receiving runtime really has a next page; a source lacking this
	// target's permit must learn neither rows nor that cursor.
	client, generation, err := f.nodes[0].reader.pairedTransport(f.keys[1])
	if err != nil {
		t.Fatal(err)
	}
	remotePage, err := client.InboxesList(msg.InboxQuery{Receiver: "app:other-agent", PairingEpoch: generation})
	if err != nil || remotePage.Outcome != msg.OutcomePage || len(remotePage.Inboxes) != 64 || remotePage.Cursor == "" || len(remotePage.NeedsPermission) != 0 {
		t.Fatal(remotePage, err)
	}
	for _, inbox := range remotePage.Inboxes {
		if inbox.Reach == nil || *inbox.Reach != msg.InboxReachOpen {
			t.Fatal("paired live inbox reach", inbox)
		}
	}
	query := msg.InboxQuery{PeerKey: f.keys[1], Receiver: "app:other-agent"}
	hidden, err := f.nodes[0].reader.InboxesList(query)
	if err != nil || hidden.Outcome != msg.OutcomeForbidden || len(hidden.Inboxes) != 0 || hidden.Cursor != "" {
		t.Fatal("unpermitted target leaked pagination", hidden, err)
	}
	if e := f.nodes[0].reader.host.messageRights.policy.Set(f.nodes[0].self, ActionApplicationMessage, messagePeerResource(f.keys[1], query.Receiver), true); e != nil {
		t.Fatal(e)
	}
	first, err := f.nodes[0].reader.InboxesList(query)
	if err != nil || first.Outcome != msg.OutcomePage || len(first.Inboxes) != 64 || first.Cursor == "" || len(first.NeedsPermission) != 0 {
		t.Fatal(first, err)
	}
	// A different receiving app is visible to the paired runtime, but remains
	// outside this source app's exact target. Its churn cannot gap this cursor.
	instance := messages1608Presence(t, f.nodes[1].run, f.nodes[1].reader.call, "", "foreign")
	if out, e := f.nodes[1].reader.InboxOpen(instance, "foreign", "ready"); e != nil || out.Outcome != msg.OutcomeOpened {
		t.Fatal(out, e)
	}
	query.Cursor = first.Cursor
	last, err := f.nodes[0].reader.InboxesList(query)
	if err != nil || last.Outcome != msg.OutcomePage || len(last.Inboxes) != 1 || last.Inboxes[0].Context != "box-64" {
		t.Fatal("foreign receiver churn changed snapshot", last, err)
	}
	if err = f.nodes[0].reader.host.messageRights.policy.Set(f.nodes[0].otherSubject, ActionApplicationMessage, messagePeerResource(f.keys[1], query.Receiver), true); err != nil {
		t.Fatal(err)
	}
	if page, e := f.nodes[0].other.InboxesList(query); e != nil || page.Outcome != msg.OutcomeGap || len(page.Inboxes) != 0 {
		t.Fatal("cursor crossed native observers", page, e)
	}
}

type messagesPeerWithLocalDiscovery struct{ msg.Messages }

func (p *messagesPeerWithLocalDiscovery) InboxesList(query msg.InboxQuery) (msg.InboxPage, error) {
	page, err := p.Messages.InboxesList(query)
	if err == nil && page.Outcome == msg.OutcomePage {
		page.NeedsPermission = []msg.AgentApplication{{Program: "app:peer-private-agent", Title: "Peer private agent"}}
	}
	return page, err
}

func TestMessagesPairedInboxStripsPeerSuppliedDiscovery(t *testing.T) {
	f := pairedMessagesFixture(t)
	f.permit(t, 0, "app:other-agent")
	messagesStateOpen(t, f.nodes[1], f.nodes[1].other, "other-agent", "target")
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
			dispatcher.Handler = &messagesPeerWithLocalDiscovery{Messages: dispatcher.Handler}
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
			t.Error("discovery-supplying peer did not join")
		}
	})
	client, generation, err := f.nodes[0].reader.pairedTransport(f.keys[1])
	if err != nil {
		t.Fatal(err)
	}
	remotePage, err := client.InboxesList(msg.InboxQuery{Receiver: "app:other-agent", PairingEpoch: generation})
	if err != nil || remotePage.Outcome != msg.OutcomePage || len(remotePage.Inboxes) != 1 || remotePage.Inboxes[0].Context != "target" || len(remotePage.NeedsPermission) != 1 || remotePage.NeedsPermission[0].Program != "app:peer-private-agent" {
		t.Fatal("peer did not supply the valid extra discovery field", remotePage, err)
	}
	page, err := f.nodes[0].reader.InboxesList(msg.InboxQuery{PeerKey: f.keys[1], Receiver: "app:other-agent"})
	if err != nil || page.Outcome != msg.OutcomePage || len(page.NeedsPermission) != 0 || !messageInboxDisclosureEqual(remotePage.Inboxes, page.Inboxes) || page.Cursor != remotePage.Cursor || page.Reason != remotePage.Reason {
		t.Fatal("paired projection disclosed peer discovery or changed valid rows", page, err)
	}
}

// One isolated local fixture owns all twenty turns; no owner runtime is used.
func TestRemoteSendSurvivesBusyPeerMetadataRefresh(t *testing.T) {
	f := pairedMessagesFixture(t)
	f.permit(t, 0, "app:other-agent")
	instance, contextName := f.sender(t, 0)
	host := f.nodes[0].reader.host
	policy := host.messageRights
	policy.policy.RefreshMetaHolder = func(ctx context.Context) error {
		f.states[0].mu.Lock()
		defer f.states[0].mu.Unlock()
		return policy.refreshApplicationRoles(ctx)
	}
	for i := 0; i < 20; i++ {
		if i%2 == 0 {
			directory := host.directory
			directory.mu.Lock()
			err := directory.withRegistrationStorageLocked(t.Context(), func(map[string]fwire.ApplicationDescriptor) error { return nil })
			directory.mu.Unlock()
			if err != nil {
				t.Fatal("registration rebuild", err)
			}
		}
		request := f.request(0, fmt.Sprintf("busy-refresh-%d", i))
		out, err := f.nodes[0].reader.Send(instance, contextName, request)
		if err != nil || out.Outcome != msg.OutcomeSent || out.Refused || out.AwaitingPermission {
			t.Fatalf("turn %d: %+v %v", i, out, err)
		}
	}
	// A cached projection supplies no permission at the current send boundary.
	if err := policy.policy.Set(f.nodes[0].self, ActionApplicationMessage, messagePeerResource(f.keys[1], "app:other-agent"), false); err != nil {
		t.Fatal(err)
	}
	out, err := f.nodes[0].reader.Send(instance, contextName, f.request(0, "busy-revoked"))
	if err == nil && out.Outcome == msg.OutcomeSent && !out.Refused {
		t.Fatalf("revoked send admitted: %+v", out)
	}
	expired := f.request(0, "busy-expired")
	expired.ExpiresUnixMs = f.nodes[0].now.Add(-time.Second).UnixMilli()
	out, err = f.nodes[0].reader.Send(instance, contextName, expired)
	if err == nil && out.Outcome == msg.OutcomeSent && !out.Refused {
		t.Fatalf("expired send admitted: %+v", out)
	}
}
