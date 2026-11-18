package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	watch "github.com/openabstractions/abstraction-watch/go"
)

const messageRecordLimit = 96 << 10
const messageIndexLimit = 4 << 20

// A pair index contains small recoverable pointers, never message bodies.
// Actual messages are bounded by20/hour and7 days. Fresh-key aliases are
// indexed separately in bounded expiry pages and never enlarge this array
// after publication. Pending pointers are reconciled before fresh admission.
const messageIndexEntries = 4096

type messageActor struct {
	Account, Program, Application, Context string
	ObservedProgram                        string              `json:",omitempty"`
	Remote                                 *messageRemoteActor `json:",omitempty"`
	Chat                                   *messageChatRoute   `json:",omitempty"`
}

// Immutable acceptance provenance. SourceInstance fences reconciliation to
// the actual source occurrence; ReturnContext is routing, never body authority.
type messageChatRoute struct {
	SourceInstance string
	SourceProcess  string `json:",omitempty"`
	ReturnContext  string
}

func (r *messageChatRoute) occurrence() string {
	if r.SourceProcess != "" {
		return r.SourceProcess
	}
	return r.SourceInstance
}

func (a messageActor) returnContext() string {
	if a.Chat != nil {
		return a.Chat.ReturnContext
	}
	if a.Remote != nil && a.Remote.ReturnContext != "" {
		return a.Remote.ReturnContext
	}
	return a.Context
}

type messageRecord struct {
	Forward                     *messageForwardRoute `json:",omitempty"`
	SenderRemoved               bool                 `json:",omitempty"`
	Lost                        bool                 `json:",omitempty"`
	LostDigest                  string               `json:",omitempty"`
	Profile                     string
	Pair                        string
	Actor                       messageActor
	Receiver                    string
	Token, ID, ContextID        string
	Spec                        []byte // Exact generated SendRequest encoding, preserved through storage.
	AcceptedAt                  int64
	ExpiresUnixMs               int64 `json:"ExpiresAt"`
	Held, Acked, Refused, Alias bool
	Control                     bool
	Terminal                    bool
	TargetKey, TargetToken      string
	Audit                       *messageAuditEntry    `json:",omitempty"`
	AuditDone                   bool                  `json:",omitempty"`
	AuditOutcome                string                `json:",omitempty"`
	WaitingNotice               *messageNotice        `json:",omitempty"`
	PairNoticeDay               int64                 `json:",omitempty"`
	Start                       *messageStartAttempt  `json:",omitempty"`
	Delivery                    *messageDeliveryState `json:",omitempty"`
	// Origin is the chain of question IDs that woke the turn which sent this
	// message, oldest first; empty for a turn OA did not wake.
	Origin []string `json:",omitempty"`
}

type messagePointer struct {
	Key, Token, ID, Context, Content string
	AcceptedAt                       int64
	ExpiresUnixMs                    int64 `json:"ExpiresAt"`
	Pending, Queued, Alias, Expired  bool
	EventVersion                     uint64
	EventChange                      string
	DeliveryReport                   string `json:",omitempty"`
}

type messagePair struct {
	Profile  string
	Pointers []messagePointer
	messageBudget
	Starts []messageStartCharge `json:",omitempty"`
	Chats  []messageChatBudget  `json:",omitempty"`
}

// messageStore owns one host's finite operations. Cross-process serialization
// and publication use the existing CAS owner. A callback never waits for a
// message, invokes enrollment or calls policy while holding a CAS lock.
type messageStore struct {
	mu         sync.RWMutex
	noteMu     sync.Mutex
	noteOwners map[string]bool
	noteChange chan struct{}
	eventMu    sync.Mutex // Excludes recovery of a live canonical event reservation.
	dir        string
	ownerNonce string
	now        func() time.Time
	idNonce    func() (string, error) // Inert allocator entropy seam; nil uses applicationToken.
	boundary   func(string) error     // Inert crash-boundary receiving seam; nil normally.
	inspect    func(string, int)      // Bounded-work receiving observations; nil normally.
	gate       *watch.Gate
	audit      *messageAudit
	// wakeIndex maps each projected wake key to its record while the record is
	// retained; messageWakeIndexed bounds it. TurnWake falls back to the
	// durable wake order files after a restart.
	wakeMu    sync.Mutex
	wakeIndex map[string]messageWakeTarget
	// changes counts every gate advance: a stored message, a rights edit or a
	// directory edit. Progress skips a settled record while it is unchanged.
	changes atomic.Uint64
}

// advance publishes a change to message waiters and to the progress worker.
func (s *messageStore) advance() {
	s.changes.Add(1)
	s.gate.Advance()
}

func messageDigest(s string) string {
	v := sha256.Sum256([]byte(s))
	return hex.EncodeToString(v[:])
}

func messageTargetValid(key, token string) bool {
	return key == "" && token == "" || agentHex(key, 32) && agentHex(token, 24)
}

func openMessageStore(root string, now func() time.Time) (*messageStore, error) {
	dir := filepath.Join(root, "messages")
	for _, name := range []string{"keys", "pairs", "sequences", "cursors", "expiry", "queues", "ids", "states", "notices", "threads"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
			return nil, err
		}
	}
	store := &messageStore{dir: dir, now: now, gate: watch.NewGate(watch.DefaultWaiters)}
	if err := store.initializeOwner(); err != nil {
		return nil, err
	}
	return store, nil
}

func messageStoredJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("messages: trailing stored data")
	}
	return nil
}

func (s *messageStore) key(actor messageActor, key string) string {
	if actor.Remote != nil {
		return messageDigest(actor.Account + "\x00" + actor.Program + "\x00" + actor.Remote.Namespace + "\x00" + actor.Context + "\x00" + key)
	}
	return messageDigest(actor.Account + "\x00" + actor.Program + "\x00" + actor.Context + "\x00" + key)
}

func (s *messageStore) recordPath(key string) string {
	return filepath.Join(s.dir, "keys", key+".json")
}

func (s *messageStore) pairPath(actor messageActor, receiver string) string {
	return filepath.Join(s.dir, "pairs", messagePairKey(actor, receiver)+".json")
}

func messageReceiverScope(actor messageActor, receiver string) string {
	if actor.Remote != nil {
		return actor.Remote.ReceiverKey
	}
	return receiver
}

func messagePairKey(actor messageActor, receiver string) string {
	return messageDigest(actor.Account + "\x00" + actor.Program + "\x00" + messageReceiverScope(actor, receiver))
}

func (s *messageStore) readRecord(ctx context.Context, key string) (*messageRecord, error) {
	if s.inspect != nil {
		s.inspect("record_read", 1)
	}
	if !agentHex(key, 32) {
		return nil, errors.New("messages: invalid stored key")
	}
	raw, err := cas.ReadLimitContext(ctx, s.recordPath(key), messageRecordLimit)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	var r messageRecord
	var profile struct{ Profile string }
	if err = json.Unmarshal(raw, &profile); err != nil {
		return nil, err
	}
	if profile.Profile == "messages-expired@1" {
		var tombstone messageExpiredRecord
		if err = messageStoredJSON(raw, &tombstone); err != nil || !agentHex(tombstone.Token, 24) || tombstone.ExpiresUnixMs > s.now().UnixMilli() && !tombstone.Lost || !messageTargetValid(tombstone.TargetKey, tombstone.TargetToken) || tombstone.Lost && (!agentHex(tombstone.LostDigest, 32) || tombstone.Delivery == nil || tombstone.Delivery.State == msg.DeliveryQueued) {
			return nil, errors.New("messages: invalid expired record")
		}
		return &messageRecord{Profile: tombstone.Profile, Pair: tombstone.Pair, Token: tombstone.Token, ID: tombstone.ID, ContextID: tombstone.ContextID, AcceptedAt: tombstone.AcceptedAt, ExpiresUnixMs: tombstone.ExpiresUnixMs, Alias: tombstone.Alias, Control: tombstone.Control, Terminal: tombstone.Terminal, TargetKey: tombstone.TargetKey, TargetToken: tombstone.TargetToken, Audit: tombstone.Audit, AuditDone: tombstone.AuditDone, AuditOutcome: tombstone.AuditOutcome, Start: tombstone.Start, Delivery: tombstone.Delivery, Lost: tombstone.Lost, LostDigest: tombstone.LostDigest, Actor: tombstone.Actor, Held: tombstone.Held, Refused: tombstone.Refused, SenderRemoved: tombstone.SenderRemoved}, nil
	}
	if profile.Profile == messageForwardProfile {
		return readForwardRecord(raw, key)
	}
	if err = messageStoredJSON(raw, &r); err != nil || r.Profile != "messages-record@1" || r.Forward != nil || !agentHex(r.Token, 24) || !agentHex(r.Pair, 32) || r.ID == "" || r.ContextID == "" || r.AcceptedAt <= 0 || r.ExpiresUnixMs <= r.AcceptedAt || !messageTargetValid(r.TargetKey, r.TargetToken) {
		return nil, errors.New("messages: invalid canonical record")
	}
	request, err := decodeRetainedMessageRequest(r.Spec)
	if err != nil || request.ExpiresUnixMs != r.ExpiresUnixMs || s.key(r.Actor, request.IdempotencyKey) != key || r.Control != (request.Withdraw != "") || (r.Terminal && !r.Control && request.Kind != msg.KindReply) {
		return nil, errors.New("messages: invalid canonical spec")
	}
	if r.Actor.Chat != nil && (r.Actor.Remote != nil || r.Actor.Chat.SourceInstance == "" || len(r.Actor.Chat.SourceInstance) > 128 || !applicationContextName.MatchString(r.Actor.Chat.ReturnContext)) {
		return nil, errors.New("messages: invalid canonical chat route")
	}
	if r.Actor.Chat != nil && r.Actor.Chat.SourceProcess != "" && !agentHex(r.Actor.Chat.SourceProcess, 32) {
		return nil, errors.New("messages: invalid native source lifetime")
	}
	if !validMessageOrigin(r.Origin) {
		return nil, errors.New("messages: invalid origin chain")
	}
	if r.Start != nil && !validMessageStart(r.Start, request.Kind) {
		return nil, errors.New("messages: invalid startup attempt")
	}
	if remote := r.Actor.Remote; remote != nil {
		if request.Origin == nil || remote.ReturnContext != request.Origin.ReturnContext || remote.ReturnContext != "" && !applicationContextName.MatchString(remote.ReturnContext) {
			return nil, errors.New("messages: invalid remote return route")
		}
		if (remote.ReceiverKey == "" || len(remote.ReceiverKey) > 256) || !validPairingHex(remote.PeerKey, 32) || !validPairingHex(remote.LocalKey, 32) || remote.PeerKey == remote.LocalKey || !validPairingHex(remote.Generation, 32) || !validPairingHex(remote.Namespace, 32) || r.Actor.Program != "remote:"+remote.PeerKey || r.Actor.ObservedProgram != "" || request.PeerKey != remote.LocalKey || request.Origin == nil || request.Origin.Namespace != remote.Namespace || request.Origin.Application != r.Actor.Application || !providerName.MatchString(r.Actor.Application) || len(remote.ProgramLabel) > 256 || nativeMessageHeader(remote.ProgramLabel) != remote.ProgramLabel {
			return nil, errors.New("messages: invalid remote provenance")
		}
		if request.PairingEpoch != remote.Generation {
			return nil, errors.New("messages: canonical pairing generation mismatch")
		}
		if r.Pair != messagePairKey(r.Actor, r.Receiver) || r.Control || request.WithdrawReference != nil || r.TargetKey != "" || r.TargetToken != "" {
			return nil, errors.New("messages: invalid remote receiver or foreign correlation")
		}
	} else if request.PeerKey != "" || request.Origin != nil || request.PairingEpoch != "" || request.ReplyReference != nil || request.WithdrawReference != nil {
		return nil, errors.New("messages: remote fields in local record")
	}
	return &r, nil
}

func readMessagePair(raw []byte) (messagePair, error) {
	p := messagePair{Profile: "messages-pair@1", Pointers: []messagePointer{}}
	if raw == nil {
		return p, nil
	}
	if err := messageStoredJSON(raw, &p); err != nil || p.Profile != "messages-pair@1" || p.Pointers == nil || len(p.Pointers) > messageIndexEntries || !p.messageBudget.valid() || !validMessageChatBudgets(p.Chats) {
		return p, errors.New("messages: invalid pair index")
	}
	seen := map[string]bool{}
	if len(p.Starts) > 20 {
		return p, errors.New("messages: invalid startup charges")
	}
	for _, charge := range p.Starts {
		if !agentHex(charge.Key, 32) || !agentHex(charge.Token, 24) || charge.At <= 0 || seen[charge.Token] {
			return p, errors.New("messages: invalid startup charge")
		}
		seen[charge.Token] = true
	}
	seen = map[string]bool{}
	for _, ref := range p.Pointers {
		if !agentHex(ref.Key, 32) || ref.Token == "" || ref.ID == "" || seen[ref.Token] || ref.ExpiresUnixMs <= ref.AcceptedAt {
			return p, errors.New("messages: invalid pair pointer")
		}
		seen[ref.Token] = true
	}
	return p, nil
}

func (s *messageStore) at(boundary string) error {
	if s.boundary != nil {
		return s.boundary(boundary)
	}
	return nil
}

func (s *messageStore) sequence(ctx context.Context, actor messageActor) (string, error) {
	var next uint64
	// One account-owned sequence also orders each sender and prevents a reused
	// registration label or native-image replacement from reusing an old ID.
	path := filepath.Join(s.dir, "sequences", messageDigest(actor.Account)+".json")
	err := cas.ChangeLimitContext(ctx, path, 4096, func(raw []byte) ([]byte, error) {
		if raw != nil {
			if err := messageStoredJSON(raw, &next); err != nil {
				return nil, err
			}
		}
		if next == ^uint64(0) {
			return nil, errors.New("messages: sequence exhausted")
		}
		next++
		return json.Marshal(next)
	})
	if err != nil {
		return "", err
	}
	// Pairing can survive a Messages reset. The existing counter orders this
	// store's allocations; randomness prevents an old qualified locator from
	// naming a later acceptance after that counter is reset.
	nonceSource := s.idNonce
	if nonceSource == nil {
		nonceSource = applicationToken
	}
	nonce, err := nonceSource()
	if err != nil {
		return "", err
	}
	if !agentHex(nonce, 24) {
		return "", errors.New("messages: id randomness unavailable")
	}
	return actor.Application + "-" + strconv.FormatUint(next, 10) + "-" + nonce[:32], nil
}

func messageReceipt(r *messageRecord) msg.SendResult {
	out := msg.SendResult{Outcome: msg.OutcomeSent, MessageID: r.ID, ContextID: r.ContextID, AwaitingPermission: r.Held}
	if r.Actor.Remote != nil {
		out.Reference = &msg.MessageRef{OwnerPeerKey: r.Actor.Remote.LocalKey, MessageID: r.ID}
	}
	return out
}

func messageSame(r *messageRecord, actor messageActor, spec []byte) bool {
	// Kept-image canonical Program/context owns the key. Registration labels,
	// caller spelling and the target's later rebindings do not retarget or
	// invalidate an already accepted receipt; its original provenance stays.
	chatSame := r.Actor.Chat == nil && actor.Chat == nil || r.Actor.Chat != nil && actor.Chat != nil && r.Actor.Chat.ReturnContext == actor.Chat.ReturnContext && (r.Actor.Chat.SourceProcess != "" && actor.Chat.SourceProcess != "" && r.Actor.Chat.SourceProcess == actor.Chat.SourceProcess || (r.Actor.Chat.SourceProcess == "" || actor.Chat.SourceProcess == "") && r.Actor.Chat.SourceInstance == actor.Chat.SourceInstance)
	return r.Actor.Account == actor.Account && r.Actor.Program == actor.Program && r.Actor.Context == actor.Context && chatSame && (messageRequestBytesEqual(r.Spec, spec) || r.Lost && messageRequestDigestMatches(r.LostDigest, spec) || r.Forward != nil && messageRequestDigestMatches(r.Forward.Digest, spec))
}

// recoverPair runs before fresh admission under this pair's CAS lock. Missing
// pending records are canceled; their original publisher must recheck the
// reservation under this same lock before publishing. A completed canonical
// record repairs a lost index update. Only pending/current queue records are
// read; acknowledged retained history stays in compact pointer metadata.
func (s *messageStore) recoverPair(ctx context.Context, p *messagePair, now int64) error {
	if s.inspect != nil {
		s.inspect("pair_pointers", len(p.Pointers))
	}
	out := p.Pointers[:0]
	for _, ref := range p.Pointers {
		if ref.Pending {
			r, err := s.recordForPointer(ctx, ref)
			if err != nil {
				return err
			}
			if r == nil || r.Token != ref.Token {
				if ref.ExpiresUnixMs <= now {
					ref.Queued = false
					if !ref.Pending && now-ref.AcceptedAt < int64(time.Hour/time.Millisecond) {
						out = append(out, ref)
					}
					continue
				}
				if !ref.Pending {
					return errors.New("messages: accepted pointer lost canonical record")
				}
				continue
			}
			ref.Pending = false
			ref.Queued = !r.Lost && !r.Control && !r.Acked && !r.Refused && r.ExpiresUnixMs > now
			ref.Alias = r.Alias
		}
		if ref.Alias {
			continue
		}
		if ref.ExpiresUnixMs <= now {
			ref.Queued = false
			if now-ref.AcceptedAt >= int64(time.Hour/time.Millisecond) {
				continue
			}
		}
		out = append(out, ref)
	}
	p.Pointers = out
	p.Hour, p.Count = now/int64(time.Hour/time.Millisecond), 0
	// The person's Resume starts a fresh count (architect ruling 2026-11-09).
	starts := p.Starts[:0]
	for _, charge := range p.Starts {
		if charge.At/int64(time.Hour/time.Millisecond) == p.Hour {
			starts = append(starts, charge)
			if charge.At >= p.ResumedAt {
				p.Count++
			}
		}
	}
	p.Starts = starts
	for _, ref := range out {
		if !ref.Pending && ref.AcceptedAt/int64(time.Hour/time.Millisecond) == p.Hour && ref.AcceptedAt >= p.ResumedAt {
			p.Count++
		}
	}
	// A same-app pair is budgeted per chat pair; its program-level count
	// only records the hour's traffic.
	p.Count = min(p.Count, messageBudgetPerHour)
	return nil
}

func (s *messageStore) expireRecord(ctx context.Context, ref messagePointer) error {
	var deliveryErr error
	if s.inspect != nil {
		s.inspect("expire_record", 1)
	}
	// A bounded tombstone retains no subject/body/key. A later expired-key reuse
	// replaces it through the canonical record CAS; old pointer tokens cannot
	// expire that new acceptance.
	before, err := s.recordForPointer(ctx, ref)
	if err != nil {
		return err
	}
	if before != nil && before.Token == ref.Token && before.Forward != nil {
		// Forwarding custody contains no body. Observation runs after this expiry
		// transaction releases its head, through the existing progress roster.
		return nil
	}
	if before != nil && before.Token == ref.Token && before.Delivery != nil && !before.Delivery.Reported {
		if err := s.finishDelivery(ctx, ref.Key, ref.Token, before.Delivery.State, before.Delivery.Reason); err != nil {
			deliveryErr = err
		}
		before, err = s.readRecord(ctx, ref.Key)
		if err != nil {
			return err
		}
	}
	if before != nil && before.Token == ref.Token && before.Delivery == nil && before.ExpiresUnixMs <= s.now().UnixMilli() && before.Profile == "messages-record@1" && !before.Alias && !before.Control && (s.questionAnswered(ctx, ref.Key, before) || s.handedToReceiver(ctx, ref.Key, ref.Token)) {
		// A question its receiver answered reached it before expiry, as did a
		// body a Wait returned to the receiving inbox's owner (owner R34,
		// 2026-11-12: a note the receiving assistant acted on expired undelivered
		// because the model never passed the page's cursor back).
		if err := s.finishAnsweredDelivery(ctx, ref.Key, ref.Token); err != nil {
			deliveryErr = err
		}
		before, err = s.readRecord(ctx, ref.Key)
		if err != nil {
			return err
		}
	}
	if before != nil && before.Token == ref.Token && before.ExpiresUnixMs <= s.now().UnixMilli() && before.Profile == "messages-record@1" && !before.Alias && !before.Control {
		if err := s.finishDelivery(ctx, ref.Key, ref.Token, msg.DeliveryUndelivered, msg.UndeliveredReasonExpired); err != nil {
			if errors.Is(err, errMessageStateInvalid) {
				err = s.finishDelivery(ctx, ref.Key, ref.Token, msg.DeliveryUndelivered, msg.UndeliveredReasonLost)
			}
			deliveryErr = err
		}
		before, err = s.readRecord(ctx, ref.Key)
		if err != nil {
			return err
		}
		if deliveryErr != nil && (before == nil || before.Delivery == nil) {
			return deliveryErr
		}
	}
	terminal := false
	var audit *messageAuditEntry
	if before != nil && before.Token == ref.Token {
		if before.Audit != nil {
			audit, err = s.auditProjection(ctx, ref.Key, before)
			if err != nil {
				return err
			}
		}
		terminal, err = s.retainTerminal(ctx, before, ref.Key)
		if err != nil {
			if !(before.Delivery != nil && before.Delivery.State == msg.DeliveryUndelivered && before.Delivery.Reason == msg.UndeliveredReasonLost && errors.Is(err, errMessageStateInvalid)) {
				return err
			}
		}
	}
	err = cas.ChangeLimitContext(ctx, s.recordPath(ref.Key), messageRecordLimit, func(raw []byte) ([]byte, error) {
		if raw == nil {
			return nil, nil
		}
		var r messageRecord
		if err := messageStoredJSON(raw, &r); err != nil {
			return nil, err
		}
		if r.Token != ref.Token {
			return raw, nil
		}
		if r.Profile == "messages-expired@1" {
			return raw, nil
		}
		if r.ExpiresUnixMs > s.now().UnixMilli() {
			return nil, errors.New("messages: expiry hint precedes canonical retention")
		}
		if audit != nil {
			r.Audit = audit
			r.AuditDone = r.AuditDone && (r.AuditOutcome == audit.Outcome || r.AuditOutcome == "" && before.Audit.Outcome == audit.Outcome)
		}
		return json.Marshal(messageExpiredRecord{Profile: "messages-expired@1", Pair: r.Pair, Token: r.Token, ID: r.ID, AcceptedAt: r.AcceptedAt, ExpiresUnixMs: r.ExpiresUnixMs, Alias: r.Alias, Control: r.Control, Terminal: terminal, TargetKey: r.TargetKey, TargetToken: r.TargetToken, Audit: r.Audit, AuditDone: r.AuditDone, AuditOutcome: r.AuditOutcome, Start: r.Start, Delivery: r.Delivery, Actor: r.Actor, ContextID: r.ContextID, Held: r.Held, Refused: r.Refused, SenderRemoved: r.SenderRemoved})
	})
	if errors.Is(err, cas.ErrNoValue) {
		return s.expireWakeOrder(ctx, ref.Key, ref.Token) // Abandoned reservation published no body.
	}
	if err == nil {
		if err := s.expireWakeOrder(ctx, ref.Key, ref.Token); err != nil {
			return err
		}
		return deliveryErr
	}
	return err
}

type messageExpiredRecord struct {
	SenderRemoved                       bool                  `json:",omitempty"`
	Held, Refused                       bool                  `json:",omitempty"`
	Lost                                bool                  `json:",omitempty"`
	LostDigest                          string                `json:",omitempty"`
	Actor                               messageActor          `json:",omitempty"`
	Delivery                            *messageDeliveryState `json:",omitempty"`
	Start                               *messageStartAttempt  `json:",omitempty"`
	Profile, Pair, Token, ID, ContextID string
	AcceptedAt                          int64
	ExpiresUnixMs                       int64 `json:"ExpiresAt"`
	Alias                               bool
	Control, Terminal                   bool
	TargetKey, TargetToken              string
	Audit                               *messageAuditEntry `json:",omitempty"`
	AuditDone                           bool               `json:",omitempty"`
	AuditOutcome                        string             `json:",omitempty"`
}

// accept receives facts already admitted by the native/registration/policy
// owners. It publishes no process, question or grant. A canonical record is
// the only acceptance authority; indices and sequence reservations are hints.
func (s *messageStore) accept(ctx context.Context, actor messageActor, receiver string, request msg.SendRequest, held bool, contextID string) msg.SendResult {
	return s.acceptReply(ctx, actor, receiver, request, held, contextID, nil)
}

func (s *messageStore) acceptReply(ctx context.Context, actor messageActor, receiver string, request msg.SendRequest, held bool, contextID string, question *messageReplyTarget) msg.SendResult {
	key := s.key(actor, request.IdempotencyKey)
	// Native admission verifies the source and current return route before
	// calling the store. Require a proven process occurrence for typed notes.
	typed := actor.Chat
	freshNoteOwner := actor.Remote == nil && (typed == nil || typed.SourceInstance != "" && len(typed.SourceInstance) <= 128 && agentHex(typed.SourceProcess, 32) && applicationContextName.MatchString(typed.ReturnContext))
	if request.Kind == msg.KindNote && !held && question == nil && request.Withdraw == "" && freshNoteOwner {
		result, handled := s.acceptFreshNote(ctx, actor, receiver, request, contextID, key)
		if handled {
			return result
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := s.readRecord(ctx, key)
	if err != nil {
		return msg.SendResult{Outcome: msg.OutcomeUnknown}
	}
	return s.acceptRecord(ctx, actor, receiver, request, held, contextID, question, key, old)
}

func (s *messageStore) acceptFreshNote(ctx context.Context, actor messageActor, receiver string, request msg.SendRequest, contextID, key string) (msg.SendResult, bool) {
	// Pair and inbox reservations can be recovered before a canonical write.
	// Acquire all ownership before taking the shared store lock. Release
	// both before retrying an existing receipt through exclusive custody.
	keys := [3]string{
		"key\x00" + key,
		"pair\x00" + messagePairKey(actor, receiver),
		"queue\x00" + s.queuePath(actor.Account, receiver, request.Context),
	}
	owned, err := s.reserveNoteOwners(ctx, keys)
	if err != nil {
		return msg.SendResult{Outcome: msg.OutcomeUnknown}, true
	}
	if !owned {
		return msg.SendResult{}, false
	}
	defer s.releaseNoteOwners(keys)
	s.mu.RLock()
	defer s.mu.RUnlock()
	old, err := s.readRecord(ctx, key)
	if err != nil {
		return msg.SendResult{Outcome: msg.OutcomeUnknown}, true
	}
	if old != nil {
		return msg.SendResult{}, false
	}
	return s.acceptRecord(ctx, actor, receiver, request, false, contextID, nil, key, nil), true
}

func (s *messageStore) reserveNoteOwners(ctx context.Context, keys [3]string) (bool, error) {
	const maxActiveNotes = 64
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		s.noteMu.Lock()
		if len(s.noteOwners) >= 3*maxActiveNotes {
			s.noteMu.Unlock()
			return false, nil
		}
		if !s.noteOwners[keys[0]] && !s.noteOwners[keys[1]] && !s.noteOwners[keys[2]] {
			if s.noteOwners == nil {
				s.noteOwners = make(map[string]bool)
			}
			for _, key := range keys {
				s.noteOwners[key] = true
			}
			s.noteMu.Unlock()
			return true, nil
		}
		if s.noteChange == nil {
			s.noteChange = make(chan struct{})
		}
		changed := s.noteChange
		s.noteMu.Unlock()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-changed:
		}
	}
}

func (s *messageStore) releaseNoteOwners(keys [3]string) {
	s.noteMu.Lock()
	defer s.noteMu.Unlock()
	for _, key := range keys {
		delete(s.noteOwners, key)
	}
	if s.noteChange != nil {
		close(s.noteChange)
		s.noteChange = nil
	}
}

func (s *messageStore) acceptRecord(ctx context.Context, actor messageActor, receiver string, request msg.SendRequest, held bool, contextID string, question *messageReplyTarget, key string, old *messageRecord) msg.SendResult {
	unknown := msg.SendResult{Outcome: msg.OutcomeUnknown}
	spec := msg.EncodeSendRequest(&request)
	var err error
	now := s.now().UnixMilli()
	if old != nil && old.ExpiresUnixMs <= now && old.Audit != nil {
		entry, e := s.auditProjection(ctx, key, old)
		if e != nil || old.AuditOutcome != entry.Outcome && !(old.AuditOutcome == "" && old.Audit.Outcome == entry.Outcome) {
			return unknown
		}
	}
	// Pending metadata must survive expired-key reuse until its attributed log
	// append is confirmed. The host repairs it outside native/CAS admission.
	if old != nil && old.ExpiresUnixMs <= now && old.Audit != nil && !old.AuditDone {
		return unknown
	}
	if old != nil && old.ExpiresUnixMs <= now && old.Delivery != nil && !old.Delivery.Reported {
		return unknown
	}
	if old != nil && old.ExpiresUnixMs > now {
		if !messageSame(old, actor, spec) {
			return msg.SendResult{Outcome: msg.OutcomeConflict}
		}
		if err := s.retainAcceptedRoute(ctx, key, old); err != nil {
			return unknown
		}
		return messageReceipt(old)
	}
	if old != nil {
		if _, err = s.retainTerminal(ctx, old, key); err != nil {
			return unknown
		}
		if !agentHex(old.Pair, 32) {
			return unknown
		}
		// Resolve the old pointer before this expired key can be reused for a
		// different pair. This happens before taking the new pair's lock.
		if err = cas.ChangeLimitContext(ctx, filepath.Join(s.dir, "pairs", old.Pair+".json"), messageIndexLimit, func(raw []byte) ([]byte, error) {
			p, err := readMessagePair(raw)
			if err != nil {
				return nil, err
			}
			if err = s.recoverPair(ctx, &p, now); err != nil {
				return nil, err
			}
			return json.Marshal(p)
		}); err != nil {
			return unknown
		}
	}
	if request.ExpiresUnixMs <= now || request.ExpiresUnixMs > now+int64(7*24*time.Hour/time.Millisecond) {
		return msg.SendResult{Outcome: msg.OutcomeInvalid}
	}
	id, err := s.sequence(ctx, actor)
	if err != nil {
		return msg.SendResult{Outcome: msg.OutcomeUnavailable}
	}
	if s.at("id_reserved") != nil {
		return unknown
	}
	if contextID == "" {
		contextID = id
	}
	token, err := applicationToken()
	if err != nil {
		return msg.SendResult{Outcome: msg.OutcomeUnavailable}
	}
	contentRequest := request
	contentRequest.IdempotencyKey = "content"
	contentSpec := msg.EncodeSendRequest(&contentRequest)
	content := messageDigest(actor.Context + "\x00" + string(contentSpec))
	priorSpec, _ := messageRenameRetained(contentSpec, false)
	priorContent := messageDigest(actor.Context + "\x00" + string(priorSpec))
	if actor.Chat != nil {
		content = messageDigest(content + "\x00" + actor.Chat.occurrence() + "\x00" + actor.Chat.ReturnContext)
		priorContent = messageDigest(priorContent + "\x00" + actor.Chat.occurrence() + "\x00" + actor.Chat.ReturnContext)
	}
	ref := messagePointer{Key: key, Token: token, ID: id, Context: request.Context, Content: content, AcceptedAt: now, ExpiresUnixMs: request.ExpiresUnixMs, Pending: true, Queued: true}
	if request.Withdraw != "" {
		ref.Queued = false
	}
	path := s.pairPath(actor, receiver)
	scope := messageBudgetScope(actor, receiver, request)
	result := unknown
	reserved := false
	var original *messageRecord
	err = cas.ChangeLimitContext(ctx, path, messageIndexLimit, func(raw []byte) ([]byte, error) {
		p, err := readMessagePair(raw)
		if err != nil {
			return nil, err
		}
		if err = s.recoverPair(ctx, &p, now); err != nil {
			return nil, err
		}
		for _, prior := range p.Pointers {
			if (prior.Content == content || prior.Content == priorContent) && prior.ExpiresUnixMs > now && now-prior.AcceptedAt < int64(10*time.Minute/time.Millisecond) {
				r, err := s.readRecord(ctx, prior.Key)
				if err != nil || r == nil {
					return nil, errors.New("messages: content replay unavailable")
				}
				original = r
				break
			}
		}
		if budget := p.budget(scope, now); original == nil && (budget == nil || budget.full(now)) {
			// Every kept chat pair is still in use: a new pair waits for the
			// next hour, when an idle one can give way.
			result = msg.SendResult{Outcome: msg.OutcomeExhausted, Reason: messageChatPairsFull, RetryAfterMs: (now/messageHourMs+1)*messageHourMs - now}
			if budget != nil {
				result.Reason, result.RetryAfterMs = "", budget.refuse(now)
			}
			return json.Marshal(p)
		}
		if len(p.Pointers) >= messageIndexEntries {
			return nil, errors.New("messages: derived retention bound exceeded")
		}
		if original != nil {
			ref.Alias, ref.Queued = true, false
			ref.ID = original.ID
			ref.ExpiresUnixMs = original.ExpiresUnixMs
		}
		p.Pointers = append(p.Pointers, ref)
		reserved = true
		return json.Marshal(p)
	})
	if err != nil || !reserved {
		return result
	}
	if original == nil && request.Withdraw == "" {
		queued, err := s.reserveQueue(ctx, actor, receiver, ref)
		if err != nil {
			return unknown
		}
		if !queued {
			return msg.SendResult{Outcome: msg.OutcomeExhausted, RetryAfterMs: 1000}
		}
	}
	if s.at("reserved") != nil {
		return unknown
	}
	accepted := &messageRecord{Profile: "messages-record@1", Pair: messagePairKey(actor, receiver), Actor: actor, Receiver: receiver, Token: token, ID: id, ContextID: contextID, Spec: spec, AcceptedAt: now, ExpiresUnixMs: request.ExpiresUnixMs, Held: held}
	accepted.Control = request.Withdraw != ""
	if actor.Remote == nil {
		accepted.Origin = messageOriginFrom(ctx)
	}
	accepted.Terminal = accepted.Control || (request.Kind == msg.KindReply && !held)
	if question != nil {
		accepted.TargetKey, accepted.TargetToken = question.key, question.token
	}
	if original != nil {
		accepted.ID, accepted.ContextID = original.ID, original.ContextID
		accepted.ExpiresUnixMs, accepted.Held, accepted.Alias = original.ExpiresUnixMs, original.Held, true
		accepted.Terminal = original.Terminal
		accepted.TargetKey, accepted.TargetToken = "", ""
	}
	if s.audit != nil && !accepted.Alias && !accepted.Control {
		accepted.Audit, err = s.audit.prepare(accepted)
		if err != nil {
			return unknown
		}
	}
	if question != nil && !accepted.Alias {
		if err = s.reserveReply(ctx, question, key, token); err != nil {
			return unknown
		}
		if err = s.at("reply_reserved"); err != nil {
			return unknown
		}
	}
	err = cas.ChangeLimitContext(ctx, path, messageIndexLimit, func(raw []byte) ([]byte, error) {
		p, err := readMessagePair(raw)
		if err != nil {
			return nil, err
		}
		index := -1
		for i := range p.Pointers {
			if p.Pointers[i].Token == token && p.Pointers[i].Pending {
				index = i
				break
			}
		}
		if index < 0 {
			return nil, errors.New("messages: reservation reconciled before publication")
		}
		if err = s.at("before_record"); err != nil {
			return nil, err
		}
		if s.now().UnixMilli() >= request.ExpiresUnixMs {
			return nil, errors.New("messages: reservation expired before publication")
		}
		publish := func() error {
			if err := s.appendExpiry(ctx, ref); err != nil {
				return err
			}
			if err := s.at("expiry_linked"); err != nil {
				return err
			}
			if !accepted.Alias {
				if err := s.publishID(ctx, accepted, key); err != nil {
					return err
				}
			}
			writeRecord := func() error {
				return cas.ChangeLimitContext(ctx, s.recordPath(key), messageRecordLimit, func(current []byte) ([]byte, error) {
					if s.now().UnixMilli() >= accepted.ExpiresUnixMs {
						return nil, errors.New("messages: acceptance expired at canonical publication")
					}
					if current != nil {
						var previous struct {
							Profile       string
							ExpiresUnixMs int64 `json:"ExpiresAt"`
							Audit         *messageAuditEntry
							AuditDone     bool
							Delivery      *messageDeliveryState
						}
						if err := json.Unmarshal(current, &previous); err != nil || previous.ExpiresUnixMs > now || previous.Audit != nil && !previous.AuditDone || previous.Delivery != nil && !previous.Delivery.Reported {
							return nil, errors.New("messages: key publication raced")
						}
					}
					return json.Marshal(accepted)
				})
			}
			var err error
			if question != nil && !accepted.Alias {
				err = s.publishReply(ctx, question, key, token, writeRecord)
			} else {
				err = writeRecord()
			}
			if err != nil {
				return err
			}
			if err := s.retainAcceptedRoute(ctx, key, accepted); err != nil {
				return err
			}
			if err := s.at("acceptance_route_published"); err != nil {
				return err
			}
			return s.at("record_published")
		}
		if accepted.Alias || accepted.Control {
			err = publish()
		} else {
			err = s.publishQueue(ctx, actor, receiver, ref, publish)
		}
		if err != nil {
			return nil, err
		}
		if accepted.Alias {
			p.Pointers = append(p.Pointers[:index], p.Pointers[index+1:]...)
		} else {
			p.Pointers[index].Pending = false
			p.Count = min(p.Count+1, messageBudgetPerHour)
			if scope != "" {
				if budget := p.budget(scope, s.now().UnixMilli()); budget != nil {
					budget.Count = min(budget.Count+1, messageBudgetPerHour)
				}
			}
		}
		return json.Marshal(p)
	})
	if err != nil || s.at("index_published") != nil {
		return unknown
	}
	if !accepted.Alias {
		s.advance()
	}
	return messageReceipt(accepted)
}

func (s *messageStore) lookupReceipt(ctx context.Context, actor messageActor, request msg.SendRequest) (msg.SendResult, bool) {
	r, err := s.readRecord(ctx, s.key(actor, request.IdempotencyKey))
	if err != nil {
		return msg.SendResult{Outcome: msg.OutcomeUnknown}, true
	}
	if r == nil || r.ExpiresUnixMs <= s.now().UnixMilli() {
		return msg.SendResult{}, false
	}
	if !messageSame(r, actor, msg.EncodeSendRequest(&request)) {
		return msg.SendResult{Outcome: msg.OutcomeConflict}, true
	}
	if err := s.retainAcceptedRoute(ctx, s.key(actor, request.IdempotencyKey), r); err != nil {
		return msg.SendResult{Outcome: msg.OutcomeUnknown}, true
	}
	return messageReceipt(r), true
}
