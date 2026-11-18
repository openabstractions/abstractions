package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/remote"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// These fields are retained in the existing canonical record. Only peer key,
// local key and pairing generation are receiver-derived. Namespace/labels are
// claims; they never enter Rights or the pair quota key.
type messageRemoteActor struct {
	PeerKey, LocalKey, Generation, Namespace, ProgramLabel, ReceiverKey string
	PermissionQuestion                                                  string `json:",omitempty"`
	PeerLabel                                                           string `json:",omitempty"`
	ReturnContext                                                       string `json:",omitempty"`
}
type messageRemoteCall struct {
	peer                          remote.Peer
	key, local, generation, label string
	err                           error
}

func messagePeerResource(key, to string) string { return "peer:" + key + "/" + to }

// Display alone: the peer subject remains Actor.Program in audit and Rights.
func messageSenderLabel(actor messageActor) string {
	if actor.Remote == nil {
		return actor.Application
	}
	label := nativeMessageHeader(actor.Remote.PeerLabel)
	if label == "" {
		label = "Paired computer"
	}
	return label + " (unverified program claim " + fmt.Sprintf("%q", nativeMessageHeader(actor.Remote.ProgramLabel)) + ")"
}
func parseMessagePeerResource(resource string) (string, string, bool) {
	key, to, ok := strings.Cut(strings.TrimPrefix(resource, "peer:"), "/")
	_, valid := messageApplicationResource(to)
	return key, to, strings.HasPrefix(resource, "peer:") && ok && validPairingHex(key, 32) && valid
}
func messageNamespace(account, key string) string {
	return messageDigest("messages-origin@1\x00" + account + "\x00" + key)
}

func messagePeerSnapshot(ctx context.Context, s *runtimePairingState, key string) (local, generation, label string, err error) {
	if s == nil || !validPairingHex(key, 32) {
		return "", "", "", errPairingState
	}
	if !s.mu.TryLock() {
		return "", "", "", errPairingBusy
	}
	defer s.mu.Unlock()
	d, _, err := s.read(ctx)
	if err != nil {
		return "", "", "", err
	}
	p, ok := d.Peers[key]
	if !ok || p.State != "active" {
		return "", "", "", errPairingState
	}
	identity, err := pairingIdentity(d)
	if err != nil {
		return "", "", "", err
	}
	local = pairingKey(identity.Leaf)
	if key == local {
		return "", "", "", errPairingState
	}
	return local, hex.EncodeToString(p.Transcript[:]), p.Label, nil
}

func messageGeneration(ctx context.Context, s *runtimePairingState, key, generation string) error {
	_, current, _, err := messagePeerSnapshot(ctx, s, key)
	if err != nil {
		return err
	}
	if current != generation {
		return errPairingRevision
	}
	return nil
}

// Pairing -> directory -> canonical CAS is the receiving publication order.
// No network, policy or Book callback runs under this guard.
func withMessageGeneration(ctx context.Context, s *runtimePairingState, key, generation string, consume func() error) error {
	if s == nil || !s.mu.TryLock() {
		return errPairingBusy
	}
	defer s.mu.Unlock()
	d, _, err := s.read(ctx)
	if err != nil {
		return err
	}
	p, ok := d.Peers[key]
	if !ok || p.State != "active" || hex.EncodeToString(p.Transcript[:]) != generation {
		return errPairingRevision
	}
	return consume()
}

func (h *applicationsHost) remoteMessages(ctx context.Context, peer remote.Peer, admit func(remote.Peer) error) *msg.MessagesDispatcher {
	c := &messageRemoteCall{peer: peer, key: hex.EncodeToString(peer.Key[:])}
	if _, c.err = remote.SubjectOf(peer, admit); c.err == nil {
		c.local, c.generation, c.label, c.err = messagePeerSnapshot(ctx, h.messagePairing, c.key)
	}
	return &msg.MessagesDispatcher{Handler: &messagesReceiver{host: h, ctx: ctx, remote: c}}
}

func (r *messagesReceiver) remoteCurrent() error {
	if r.remote == nil || r.remote.err != nil || r.host == nil {
		return errPairingState
	}
	return messageGeneration(r.ctx, r.host.messagePairing, r.remote.key, r.remote.generation)
}

func (d *applicationDirectory) withMessageTarget(ctx context.Context, target applications.AgentEnrollment, image applicationAgentImage, consume func() error) error {
	if !d.lock(ctx) {
		return ctx.Err()
	}
	defer d.unlock()
	return d.withRegistrationReadLocked(ctx, func(legacy map[string]fwire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		entry, binding, ok := d.agentEntryLocked(legacy, target.Application)
		if !ok {
			return errors.New("messages: receiver removed")
		}
		view := agentEnrollmentView(target.Application, entry.Program, binding, d.owner, marks[target.Application])
		if view.State != applications.AgentEnrollmentStateEnrolled || view.Revision != target.Revision || view.Program != target.Program || marks[target.Application].ProgramKey != image.Key {
			return errors.New("messages: receiver changed")
		}
		return consume()
	})
}

func unsupportedRemoteWithdrawal() msg.SendResult {
	return msg.SendResult{Outcome: msg.OutcomeInvalid, Reason: "remote_withdrawal_unsupported"}
}

func (r *messagesReceiver) receiveRemoteMessage(instance, contextName string, request msg.SendRequest) msg.SendResult {
	if request.Withdraw != "" || request.WithdrawReference != nil {
		return unsupportedRemoteWithdrawal()
	}
	forbidden := msg.SendResult{Outcome: msg.OutcomeForbidden}
	if r.remoteCurrent() != nil || instance != "" || request.PeerKey != r.remote.local || request.PairingEpoch != r.remote.generation || request.Origin == nil || !validPairingHex(request.Origin.Namespace, 32) || !providerName.MatchString(request.Origin.Application) || request.Origin.ReturnContext != "" && !applicationContextName.MatchString(request.Origin.ReturnContext) || !applicationContextName.MatchString(contextName) || !nativeMessageText(request.Subject, request.Body, request.IdempotencyKey) || request.ReplyTo != "" || request.Withdraw != "" {
		return forbidden
	}
	if request.Kind == msg.KindReply {
		if request.ReplyReference == nil || request.ReplyReference.OwnerPeerKey != r.remote.key {
			return forbidden
		}
	} else if request.ReplyReference != nil {
		return forbidden
	}
	name, valid := messageApplicationResource(request.To)
	if !valid {
		return msg.SendResult{Outcome: msg.OutcomeInvalid}
	}
	store := r.host.messages
	if store == nil || r.host.messageRights == nil {
		return msg.SendResult{Outcome: msg.OutcomeUnavailable}
	}
	actor := messageActor{Account: r.host.owner, Program: "remote:" + r.remote.key, Application: request.Origin.Application, Context: contextName, Remote: &messageRemoteActor{PeerKey: r.remote.key, LocalKey: r.remote.local, Generation: r.remote.generation, Namespace: request.Origin.Namespace, ProgramLabel: nativeMessageHeader(request.Origin.ProgramLabel)}}
	actor.Remote.PeerLabel = nativeMessageHeader(r.remote.label)
	actor.Remote.ReturnContext = request.Origin.ReturnContext
	if previous, e := store.readRecord(r.ctx, store.key(actor, request.IdempotencyKey)); e != nil {
		return msg.SendResult{Outcome: msg.OutcomeUnavailable}
	} else if previous != nil && previous.ExpiresUnixMs > store.now().UnixMilli() && (previous.Actor.Remote == nil || previous.Actor.Remote.Generation != r.remote.generation) {
		return forbidden
	}
	if replay, found := store.lookupReceipt(r.ctx, actor, request); found {
		if r.remoteCurrent() != nil {
			return forbidden
		}
		out := r.projectSendReceipt(replay)
		if r.remoteCurrent() != nil {
			return msg.SendResult{Outcome: msg.OutcomeUnknown}
		}
		return out
	}
	now := store.now().UnixMilli()
	if reason := messageSendTimesReason(request, now); reason != "" {
		return msg.SendResult{Outcome: msg.OutcomeInvalid, Reason: reason}
	}
	var image applicationAgentImage
	view := r.host.directory.currentAgentEnrollmentImage(r.ctx, name, &image)
	if view.Enrollment == nil || view.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
		return forbidden
	}
	actor.Remote.ReceiverKey = image.Key
	decision := r.host.messageRights.decide(r.ctx, rights.Subject{Account: actor.Account, Program: actor.Program}, ActionApplicationMessage, request.To)
	held := false
	if decision.Outcome == rights.DecisionOutcomeNotGranted {
		var err error
		held, err = r.messageQuestion(rights.Subject{Account: actor.Account, Program: actor.Program}, request.To, r.remote.key, r.remote.label, request.Origin.ProgramLabel, &actor.Remote.PermissionQuestion)
		if err != nil {
			r.reportMessageError("remote permission", err)
			return msg.SendResult{Outcome: msg.OutcomeUnavailable, Reason: "authorization_pending", Refusal: messageRightsRefusal(decision)}
		}
	}
	if !held && decision.Outcome != rights.DecisionOutcomePermitted {
		forbidden.Refusal = messageRightsRefusal(decision)
		return forbidden
	}
	// attach-* contexts are retired (architect ruling 1, AB-1s).
	if messageAttachContext(request.Context) {
		return msg.SendResult{Outcome: msg.OutcomeUnavailable, Reason: "session_unknown"}
	}
	if err := r.host.resumeMessageBudget(r.ctx, actor, image.Program, request); err != nil {
		r.reportMessageError("remote message resume", err)
	}
	out := msg.SendResult{Outcome: msg.OutcomeUnknown}
	publicationStarted := false
	err := withMessageGeneration(r.ctx, r.host.messagePairing, r.remote.key, r.remote.generation, func() error {
		return r.host.directory.withMessageTarget(r.ctx, *view.Enrollment, image, func() error {
			publicationStarted = true
			out = store.accept(r.ctx, actor, image.Program, request, held, "")
			return nil
		})
	})
	if err != nil {
		if publicationStarted {
			return msg.SendResult{Outcome: msg.OutcomeUnknown}
		}
		return forbidden
	}
	if out.Outcome == msg.OutcomeExhausted {
		if _, e := r.host.progressPairNotification(r.ctx, actor, image.Program, request); e != nil {
			r.reportMessageError("remote pair notification pending", e)
		}
	}
	out = r.projectSendReceipt(out)
	if r.remoteCurrent() != nil {
		return msg.SendResult{Outcome: msg.OutcomeUnknown}
	}
	return out
}

func (h *applicationsHost) reconcileRemoteMessage(ctx context.Context, key string, record *messageRecord) (messageState, bool, error) {
	s := h.messages
	state, err := s.currentState(ctx, key, record)
	if err != nil {
		return state, false, err
	}
	request, err := decodeRetainedMessageRequest(record.Spec)
	if err != nil {
		return state, false, err
	}
	if record.Control {
		return state, false, errors.New("messages: remote withdrawal unsupported")
	}
	var image applicationAgentImage
	target := h.directory.currentAgentEnrollmentImage(ctx, strings.TrimPrefix(request.To, "app:"), &image)
	if target.Outcome == applications.OutcomeUnavailable {
		return state, false, errors.New("messages: remote target unavailable")
	}
	pairState := messageDeliveryPairState(ctx, h.messagePairing, record.Actor.Remote.PeerKey, record.Actor.Remote.LocalKey, record.Actor.Remote.Generation)
	if pairState == msg.DeliveryObservationOutcomeUnavailable {
		return state, false, errors.New("messages: current pairing unavailable")
	}
	targetCurrent := target.Enrollment != nil && target.Enrollment.State == applications.AgentEnrollmentStateEnrolled && samePrograms(image.Program, record.Receiver) && record.Actor.Remote.ReceiverKey == image.Key
	current := targetCurrent && pairState == msg.DeliveryObservationOutcomeObserved
	decision := h.messageRights.decide(ctx, rights.Subject{Account: record.Actor.Account, Program: record.Actor.Program}, ActionApplicationMessage, request.To)
	if decision.Outcome == rights.DecisionOutcomeUnavailable {
		return state, false, errors.New("messages: remote permission unavailable")
	}
	permitted := current && decision.Outcome == rights.DecisionOutcomePermitted
	personRefused := false
	if state.Held && record.Actor.Remote.PermissionQuestion != "" {
		if h.directory.agentQuestions == nil || h.directory.agentQuestions.a == nil {
			return state, false, errors.New("messages: question owner unavailable")
		}
		a := h.directory.agentQuestions.a
		found, e := a.book.WithApplicationRecordContext(ctx, record.Actor.Remote.PermissionQuestion, func(question asks.Record, scope string, _ []string) error {
			permission, valid := asks.PermissionForRecord(h.owner, question)
			if scope != a.scope || !valid || len(permission.Targets) != 1 || permission.Targets[0].Program != record.Actor.Program || permission.Targets[0].Action != ActionApplicationMessage || permission.Targets[0].Resource != request.To {
				return errors.New("messages: held permission occurrence changed")
			}
			personRefused = !question.Pending() && question.Option == "refuse"
			return nil
		})
		if e != nil || !found {
			return state, false, errors.New("messages: held permission occurrence unavailable")
		}
	}
	op := ""
	if record.Refused || personRefused || !current || decision.Outcome == rights.DecisionOutcomeDenied || (!state.Held && decision.Outcome == rights.DecisionOutcomeNotGranted) {
		op = "refuse"
		permitted = false
	} else if state.Held && permitted {
		op = "release"
	}
	if op != "" {
		if op == "refuse" && !record.Alias {
			reason := msg.UndeliveredReasonRefused
			if !targetCurrent {
				reason = msg.UndeliveredReasonReceiverRemoved
			}
			if pairState == msg.DeliveryObservationOutcomeForbidden {
				reason = msg.UndeliveredReasonPeerRemoved
			}
			if err = s.finishDelivery(ctx, key, record.Token, msg.DeliveryUndelivered, reason); err != nil {
				return state, false, err
			}
		}
		if err = s.transitionState(ctx, key, record, op); err != nil {
			if errors.Is(err, errMessageEventsFull) {
				state.Refused = op == "refuse"
				return state, false, nil
			}
			return state, false, err
		}
		state, err = s.currentState(ctx, key, record)
	}
	if err == nil && request.Kind == msg.KindQuestion && !messageTerminal(state.TaskState) {
		intent, e := s.questionTerminalIntent(ctx, state)
		if e != nil {
			return state, false, e
		}
		if intent == "" && request.AnswerByUnixMs <= s.now().UnixMilli() {
			intent = "fail"
		}
		if intent != "" {
			if err = s.transitionState(ctx, key, record, intent); err == nil {
				state, err = s.currentState(ctx, key, record)
			}
		}
	}
	if err == nil && state.Refused && !record.Alias && !record.Control {
		err = s.finishDelivery(ctx, key, record.Token, msg.DeliveryUndelivered, msg.UndeliveredReasonRefused)
	}
	return state, permitted, err
}

func (r *messagesReceiver) pairedTransport(key string) (*msg.MessagesClient, string, error) {
	local, generation, _, err := messagePeerSnapshot(r.ctx, r.host.messagePairing, key)
	if err != nil {
		return nil, "", err
	}
	if r.selectedPeer != "" && (key != r.selectedPeer || generation != r.selectedGeneration) {
		return nil, "", errPairingRevision
	}
	config, address, err := r.host.messagePairing.outgoingTLS(r.ctx, key)
	if err != nil {
		return nil, "", err
	}
	transport, err := (remote.Dialer{TLS: config, Timeout: 30 * time.Second, MaxFrame: remoteRequestBytes, AdmitContext: func(dialCtx context.Context, peer remote.Peer) error {
		if hex.EncodeToString(peer.Key[:]) != key {
			return remote.ErrTrust
		}
		if err := r.host.messagePairing.admitContext(dialCtx, peer); err != nil {
			return err
		}
		return messageGeneration(dialCtx, r.host.messagePairing, key, generation)
	}}).Client(address)
	if err != nil {
		return nil, "", err
	}
	// The existing framing owner retains the whole-call deadline. Limit its
	// authenticated dial separately so native metadata pages can complete.
	dial := transport.Dialer
	transport.Dialer = func(ctx context.Context, endpoint string) (net.Conn, error) {
		connecting, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		conn, e := dial(connecting, endpoint)
		if e != nil {
			return nil, fmt.Errorf("%w: %w", errMessageBeforeDispatch, e)
		}
		return conn, nil
	}
	_ = local
	return msg.NewMessagesClient(transport.WithContext(r.ctx)), generation, nil
}

var errMessageBeforeDispatch = errors.New("messages: authenticated connection not established")

func messageForwardFailure(err error) msg.SendResult {
	if errors.Is(err, errMessageBeforeDispatch) {
		return msg.SendResult{Outcome: msg.OutcomeUnavailable, Reason: "unreachable"}
	}
	var refusal *msg.ServiceError
	if errors.As(err, &refusal) {
		switch string(refusal.Code) {
		case "forbidden", "unsupported", "unknown_service", "unknown_method", "wrong_mode":
			return msg.SendResult{Outcome: msg.OutcomeForbidden, Reason: "receiving_refused"}
		case "unavailable":
			return msg.SendResult{Outcome: msg.OutcomeUnavailable, Reason: "receiving_unavailable"}
		}
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return msg.SendResult{Outcome: msg.OutcomeUnavailable, Reason: "unreachable"}
	}
	return msg.SendResult{Outcome: msg.OutcomeUnknown, Reason: "unreachable"}
}

func (r *messagesReceiver) sendPairedMessage(instance, contextName string, request msg.SendRequest) msg.SendResult {
	return r.sendPairedMessageWithReceipt(instance, contextName, request, false, false)
}

func (r *messagesReceiver) sendPairedMessageWithReceipt(instance, contextName string, request msg.SendRequest, reconcile, receiptOnly bool) msg.SendResult {
	return r.sendPairedMessageWithChat(instance, contextName, request, reconcile, receiptOnly, nil)
}

func (r *messagesReceiver) sendPairedMessageWithChat(instance, contextName string, request msg.SendRequest, reconcile, receiptOnly bool, chat *messageChatRoute) msg.SendResult {
	if request.Withdraw != "" || request.WithdrawReference != nil {
		return unsupportedRemoteWithdrawal()
	}
	actor, subject, info, ok := r.owner(instance, contextName)
	if !ok || !validPairingHex(request.PeerKey, 32) || request.Origin != nil || request.PairingEpoch != "" || request.ReplyTo != "" {
		return msg.SendResult{Outcome: msg.OutcomeForbidden}
	}
	if r.host.messages == nil || r.host.messageRights == nil {
		return msg.SendResult{Outcome: msg.OutcomeUnavailable}
	}
	if _, valid := messageApplicationResource(request.To); !valid || !nativeMessageText(request.Subject, request.Body, request.IdempotencyKey) || !applicationContextName.MatchString(request.Context) {
		return msg.SendResult{Outcome: msg.OutcomeInvalid}
	}
	if chat != nil {
		actor.Chat = chat
		if !r.host.directory.currentChatRoute(r.ctx, actor) {
			return msg.SendResult{Outcome: msg.OutcomeForbidden}
		}
	}
	sourceRequest := request
	local, generation, label, err := messagePeerSnapshot(r.ctx, r.host.messagePairing, request.PeerKey)
	if r.selectedPeer != "" && (request.PeerKey != r.selectedPeer || generation != r.selectedGeneration) {
		return msg.SendResult{Outcome: msg.OutcomeForbidden}
	}
	if err != nil {
		return msg.SendResult{Outcome: msg.OutcomeUnavailable, Reason: "peer_unavailable"}
	}
	namespaceKey := info.Image.Key
	if chat != nil {
		namespaceKey += "\x00" + contextName + "\x00" + chat.occurrence()
	}
	request.Origin = &msg.OriginClaim{Namespace: messageNamespace(actor.Account, namespaceKey), Application: actor.Application, ProgramLabel: nativeMessageHeader(filepath.Base(actor.Program))}
	if chat != nil {
		request.Origin.ReturnContext = chat.ReturnContext
	}
	request.PairingEpoch = generation
	var question *messageRecord
	var qkey string
	var retained *msg.SendResult
	if request.Kind == msg.KindReply {
		ref := request.ReplyReference
		if ref == nil || ref.OwnerPeerKey != local || request.WithdrawReference != nil {
			return msg.SendResult{Outcome: msg.OutcomeForbidden}
		}
		question, qkey, err = r.host.messages.readByID(r.ctx, ref.MessageID)
		if err != nil || question == nil || question.Actor.Account != actor.Account || question.Actor.Remote == nil || question.Actor.Remote.PeerKey != request.PeerKey || !samePrograms(question.Receiver, actor.Program) || question.Actor.Remote.ReceiverKey != info.Image.Key {
			return msg.SendResult{Outcome: msg.OutcomeForbidden}
		}
		original, e := decodeRetainedMessageRequest(question.Spec)
		if e != nil || original.Kind != msg.KindQuestion || original.Context != contextName || request.To != "app:"+question.Actor.Application || request.Context != question.Actor.returnContext() || question.Actor.Remote.Generation != generation {
			return msg.SendResult{Outcome: msg.OutcomeForbidden}
		}
		if reconcile {
			state, e := r.host.messages.currentState(r.ctx, qkey, question)
			if e != nil {
				return msg.SendResult{Outcome: msg.OutcomeUnknown}
			}
			if prior := state.RemoteReply; prior != nil && prior.Fingerprint == messageDigest(string(msg.EncodeSendRequest(&request))) && (prior.Completed || prior.Failed) && prior.Reference != nil && prior.Reference.OwnerPeerKey == request.PeerKey && prior.ContextID != "" {
				retained = &msg.SendResult{Outcome: msg.OutcomeSent, MessageID: prior.Reference.MessageID, ContextID: prior.ContextID, Reference: prior.Reference, Refused: prior.Failed}
			}
		}
		if original.AnswerByUnixMs <= r.host.messages.now().UnixMilli() && retained == nil {
			// An uncertain prior reply keeps its fence; absence is not proved by
			// the deadline and cannot authorize a fresh late remote send.
			return msg.SendResult{Outcome: msg.OutcomeUnknown}
		}
	} else if request.ReplyReference != nil {
		return msg.SendResult{Outcome: msg.OutcomeInvalid}
	}
	resource := messagePeerResource(request.PeerKey, request.To)
	decision := r.host.messageRights.decide(r.ctx, subject, ActionApplicationMessage, resource)
	if decision.Outcome == rights.DecisionOutcomeNotGranted {
		held, e := r.messageQuestion(subject, resource, request.PeerKey, label, filepath.Base(actor.Program), nil)
		if e != nil || held {
			if e != nil {
				r.reportMessageError("paired permission", e)
			}
			return msg.SendResult{Outcome: msg.OutcomeUnavailable, AwaitingPermission: held, Reason: "authorization_pending", Refusal: messageRightsRefusal(decision)}
		}
		decision = r.host.messageRights.decide(r.ctx, subject, ActionApplicationMessage, resource)
	}
	if decision.Outcome != rights.DecisionOutcomePermitted {
		return msg.SendResult{Outcome: msg.OutcomeForbidden, Refusal: messageRightsRefusal(decision)}
	}
	// Retained custody fences conflicting input before another dispatch, but
	// never substitutes for the destination's current explicit receipt.
	if prior, found := r.host.messages.lookupForwardReceipt(r.ctx, actor, sourceRequest, local, generation); found && (prior.Outcome == msg.OutcomeConflict || prior.Outcome == msg.OutcomeForbidden) {
		return prior
	}
	if retained != nil {
		return r.host.messages.acceptForward(r.ctx, actor, sourceRequest, *retained, local, generation)
	}
	if receiptOnly {
		return msg.SendResult{Outcome: msg.OutcomeUnknown}
	}
	if question != nil {
		err = withMessageGeneration(r.ctx, r.host.messagePairing, request.PeerKey, generation, func() error {
			allowed, e := r.host.directory.withNativeMessageOwner(r.ctx, r.call, instance, contextName, nil, func(_ *nativeInbox) error { return r.host.messages.reserveRemoteReply(r.ctx, qkey, question, request) })
			if e == nil && !allowed {
				e = errors.New("messages: native reply owner ended")
			}
			return e
		})
		if err != nil {
			return msg.SendResult{Outcome: msg.OutcomeConflict}
		}
		if err = r.host.messages.at("remote_reply_reserved"); err != nil {
			return msg.SendResult{Outcome: msg.OutcomeUnknown}
		}
	} else if request.ReplyReference != nil {
		return msg.SendResult{Outcome: msg.OutcomeInvalid}
	}
	client, _, err := r.pairedTransport(request.PeerKey)
	if err != nil {
		return msg.SendResult{Outcome: msg.OutcomeUnavailable, Reason: "peer_unavailable"}
	}
	if _, _, _, ok = r.owner(instance, contextName); !ok || messageGeneration(r.ctx, r.host.messagePairing, request.PeerKey, generation) != nil {
		return msg.SendResult{Outcome: msg.OutcomeForbidden}
	}
	if chat != nil {
		actor.Chat = chat
		allowed, err := r.host.directory.withNativeMessageOwner(r.ctx, r.call, instance, contextName, nil, func(*nativeInbox) error {
			if !r.host.directory.currentChatRouteLocked(actor) {
				return errors.New("messages: canonical chat route unavailable")
			}
			return nil
		})
		if err != nil || !allowed {
			return msg.SendResult{Outcome: msg.OutcomeForbidden}
		}
	}
	out, err := client.Send("", contextName, request)
	if err != nil {
		r.reportMessageError("paired forwarding", err)
		return messageForwardFailure(err)
	}
	if question != nil {
		if err = r.host.messages.at("remote_reply_returned"); err != nil {
			return msg.SendResult{Outcome: msg.OutcomeUnknown}
		}
	}
	if messageGeneration(r.ctx, r.host.messagePairing, request.PeerKey, generation) != nil {
		return msg.SendResult{Outcome: msg.OutcomeUnknown}
	}
	if out.Outcome == msg.OutcomeSent && question != nil && !out.AwaitingPermission {
		if err = withMessageGeneration(r.ctx, r.host.messagePairing, request.PeerKey, generation, func() error {
			allowed, e := r.host.directory.withNativeMessageOwner(r.ctx, r.call, instance, contextName, nil, func(_ *nativeInbox) error {
				return r.host.messages.finishRemoteReply(r.ctx, qkey, question, request, out)
			})
			if e == nil && !allowed {
				e = errors.New("messages: native reply owner ended")
			}
			return e
		}); err != nil {
			return msg.SendResult{Outcome: msg.OutcomeUnknown}
		}
	}
	if out.Outcome == msg.OutcomeSent {
		return r.host.messages.acceptForward(r.ctx, actor, sourceRequest, out, local, generation)
	}
	return out
}

func (r *messagesReceiver) remoteInboxes(query msg.InboxQuery) msg.InboxPage {
	var firstRows, secondRows []msg.Inbox
	first := r.remoteInboxSnapshot(query, &firstRows)
	if first.Outcome != msg.OutcomePage {
		return first
	}
	second := r.remoteInboxSnapshot(query, &secondRows)
	if second.Outcome != msg.OutcomePage {
		return second
	}
	if !messageInboxDisclosureEqual(firstRows, secondRows) {
		return messageEmptyInboxes(msg.OutcomeGap)
	}
	return second
}

func (r *messagesReceiver) remoteInboxSnapshot(query msg.InboxQuery, captured *[]msg.Inbox) msg.InboxPage {
	if query.PeerKey != "" || r.remoteCurrent() != nil || query.PairingEpoch != r.remote.generation {
		return messageEmptyInboxes(msg.OutcomeForbidden)
	}
	name, valid := messageApplicationResource(query.Receiver)
	if !valid {
		return messageEmptyInboxes(msg.OutcomeInvalid)
	}
	decision := r.host.messageRights.decide(r.ctx, rights.Subject{Account: r.host.owner, Program: "remote:" + r.remote.key}, ActionApplicationMessage, query.Receiver)
	if decision.Outcome == rights.DecisionOutcomeUnavailable {
		return messageEmptyInboxes(msg.OutcomeUnavailable)
	}
	if decision.Outcome != rights.DecisionOutcomePermitted {
		return messageEmptyInboxes(msg.OutcomeForbidden)
	}
	rows := []msg.Inbox{}
	d := r.host.directory
	if !d.lock(r.ctx) {
		return messageEmptyInboxes(msg.OutcomeUnavailable)
	}
	if d.nativeInboxesClosed || d.nativeInboxes != nil && d.nativeInboxes.closed {
		d.unlock()
		return messageEmptyInboxes(msg.OutcomeUnavailable)
	}
	var snapshots []nativeInbox
	if b := d.nativeInboxes; b != nil && !b.closed {
		var err error
		snapshots, err = b.snapshotLocked(name)
		if err != nil {
			d.unlock()
			return messageEmptyInboxes(msg.OutcomeUnavailable)
		}
		snapshots = d.receivingNativeSnapshotsLocked(snapshots)
	}
	d.unlock()
	defer closeNativeInboxSnapshots(snapshots)
	starts := map[string]messageInboxStart{}
	for _, row := range snapshots {
		peer, _, _, _, err := nativeInboxPeer(row.pin, d.now())
		if err != nil {
			continue
		}
		if info, ok, e := d.captureApplicationAgent(r.ctx, peer, row.application); e != nil {
			return messageEmptyInboxes(msg.OutcomeUnavailable)
		} else if !ok || row.admission != nil && !sameNativeInboxAdmission(*row.admission, info) {
			continue
		}
		watching := r.host.messageIsWatching(r.ctx, row.subject.Account, row.subject.Program, row.context)
		starts[messageInboxStartKey("app:"+row.application, row.context)] = messageInboxStart{at: row.opened, word: messageStartedWord}
		rows = append(rows, msg.Inbox{Program: "app:" + row.application, Context: row.context, ChatTitle: row.title, Status: nativeMessageHeader(row.status), Revision: row.revision, LastSeenUnixMs: row.seen.UnixMilli(), Watching: watching, Reach: new(r.host.messageInboxReach(row.application, row.subject.Program, row.context, row.status, watching))})
	}

	for _, row := range rows {
		// A paired listing's existing sessions carry their hourly last-active time.
		if key := messageInboxStartKey(row.Program, row.Context); starts[key] == (messageInboxStart{}) {
			starts[key] = messageInboxStart{at: time.UnixMilli(row.LastSeenUnixMs), word: messageLastActiveWord}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Program != rows[j].Program {
			return rows[i].Program < rows[j].Program
		}
		return rows[i].Context < rows[j].Context
	})
	rows = messageListedInboxTitles(rows, starts, d.now())
	if r.remoteCurrent() != nil {
		return messageEmptyInboxes(msg.OutcomeForbidden)
	}
	decision = r.host.messageRights.decide(r.ctx, rights.Subject{Account: r.host.owner, Program: "remote:" + r.remote.key}, ActionApplicationMessage, query.Receiver)
	if decision.Outcome == rights.DecisionOutcomeUnavailable {
		return messageEmptyInboxes(msg.OutcomeUnavailable)
	}
	if decision.Outcome != rights.DecisionOutcomePermitted {
		return messageEmptyInboxes(msg.OutcomeForbidden)
	}
	*captured = rows
	return messageRemoteInboxPage(rows, d.epoch+":"+r.remote.key+":"+r.remote.generation+":"+query.Receiver, query.Cursor)
}

func (r *messagesReceiver) pairedInboxes(query msg.InboxQuery) msg.InboxPage {
	if !validPairingHex(query.PeerKey, 32) {
		return messageEmptyInboxes(msg.OutcomeInvalid)
	}
	if _, valid := messageApplicationResource(query.Receiver); !valid {
		return messageEmptyInboxes(msg.OutcomeInvalid)
	}
	observer, outcome := r.messageObserver("")
	if observer == nil {
		return messageEmptyInboxes(outcome)
	}
	defer observer.pin.Close()
	resource := messagePeerResource(query.PeerKey, query.Receiver)
	decision := r.host.messageRights.decide(r.ctx, observer.subject, ActionApplicationMessage, resource)
	if decision.Outcome == rights.DecisionOutcomeUnavailable {
		return messageEmptyInboxes(msg.OutcomeUnavailable)
	}
	if decision.Outcome != rights.DecisionOutcomePermitted {
		return messageEmptyInboxes(msg.OutcomeForbidden)
	}
	client, generation, err := r.pairedTransport(query.PeerKey)
	if err != nil {
		return msg.InboxPage{Outcome: msg.OutcomeUnavailable, Inboxes: []msg.Inbox{}, Reason: "peer_unavailable"}
	}
	scope := messageDigest(r.host.directory.epoch + "\x00" + observer.actor.Account + "\x00" + observer.admission.Image.Key + "\x00" + query.PeerKey + "\x00" + generation + "\x00" + query.Receiver)
	cursor := ""
	if query.Cursor != "" {
		prefix, tail, found := strings.Cut(query.Cursor, ".")
		raw, e := base64.RawURLEncoding.DecodeString(tail)
		if !found || prefix != scope || e != nil || len(raw) > 128 {
			return messageEmptyInboxes(msg.OutcomeGap)
		}
		cursor = string(raw)
	}
	page, err := client.InboxesList(msg.InboxQuery{Cursor: cursor, PairingEpoch: generation, Receiver: query.Receiver})
	if err != nil {
		failure := messageForwardFailure(err)
		return msg.InboxPage{Outcome: msg.OutcomeUnavailable, Inboxes: []msg.Inbox{}, Reason: failure.Reason}
	}
	// Names-only discovery belongs to the local kept-agent roster. A peer's
	// optional field cannot enable discovery through this paired projection.
	page.NeedsPermission = nil
	for i, row := range page.Inboxes {
		if row.Program != query.Receiver {
			return messageEmptyInboxes(msg.OutcomeUnavailable)
		}
		// A peer's native ownership flag never describes this local caller.
		page.Inboxes[i].Mine = nil
	}
	if observer.check() != nil || messageGeneration(r.ctx, r.host.messagePairing, query.PeerKey, generation) != nil {
		return messageEmptyInboxes(msg.OutcomeUnavailable)
	}
	decision = r.host.messageRights.decide(r.ctx, observer.subject, ActionApplicationMessage, resource)
	if decision.Outcome == rights.DecisionOutcomeUnavailable {
		return messageEmptyInboxes(msg.OutcomeUnavailable)
	}
	if decision.Outcome != rights.DecisionOutcomePermitted {
		return messageEmptyInboxes(msg.OutcomeForbidden)
	}
	if page.Cursor != "" {
		page.Cursor = scope + "." + base64.RawURLEncoding.EncodeToString([]byte(page.Cursor))
	}
	return page
}

func messageRemoteInboxPage(rows []msg.Inbox, scope, cursor string) msg.InboxPage {
	raw, _ := json.Marshal(messageInboxSnapshotRows(rows))
	digest := messageDigest(scope + "\x00" + string(raw))
	offset := 0
	if cursor != "" {
		var tail string
		var ok bool
		var prefix string
		prefix, tail, ok = strings.Cut(cursor, ".")
		if !ok || prefix != digest {
			return messageEmptyInboxes(msg.OutcomeGap)
		}
		if _, err := fmt.Sscanf(tail, "%d", &offset); err != nil || offset < 0 || offset > len(rows) || fmt.Sprint(offset) != tail {
			return messageEmptyInboxes(msg.OutcomeGap)
		}
	}
	end := offset + 64
	if end > len(rows) {
		end = len(rows)
	}
	page := msg.InboxPage{Outcome: msg.OutcomePage, Inboxes: append([]msg.Inbox{}, rows[offset:end]...)}
	if end < len(rows) {
		page.Cursor = digest + "." + fmt.Sprint(end)
	}
	return page
}
