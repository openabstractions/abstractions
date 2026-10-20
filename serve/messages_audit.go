package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	logging "github.com/openabstractions/abstraction-logging/go"
)

// The nonsecret projection may be retained beside canonical acceptance. It
// carries no subject/body text and confers no identity or read authority.
type messageAuditEntry struct {
	Reference                                                  *msg.MessageRef `json:",omitempty"`
	MetadataOnly                                               bool            `json:",omitempty"`
	Epoch, Digest, ID, Account, From, FromProgram, FromContext string
	To, ToProgram, ToContext, Kind, Outcome                    string
	ReturnContext                                              string `json:",omitempty"`
	At                                                         int64
	ExpiresUnixMs                                              int64  `json:"ExpiresAt"`
	UpdatedAt                                                  int64  `json:",omitempty"`
	Version                                                    uint64 `json:",omitempty"`
	// A receiver start row names the woken conversation's folder label and
	// the start permit it ran under, which the person can revoke there.
	ToFolder, PermitProgram, PermitAction, PermitResource string `json:",omitempty"`
	// Origin is the chain of question IDs that woke the sending turn,
	// oldest first and comma-separated; IDs never hold a comma.
	Origin string `json:",omitempty"`
	// Delivery is how the woken session received the turn (start rows).
	Delivery                         string `json:",omitempty"`
	DeliveryState, UndeliveredReason string `json:",omitempty"`
	DeliveryAt                       int64  `json:",omitempty"`
}

type messageAudit struct {
	mu     sync.Mutex
	key    [32]byte
	epoch  string
	sink   logging.Sink
	closed bool
}

func newMessageAudit(sink logging.Sink) (*messageAudit, error) {
	return makeMessageAudit(sink, rand.Reader)
}

func makeMessageAudit(sink logging.Sink, entropy io.Reader) (*messageAudit, error) {
	if sink == nil || entropy == nil {
		return nil, errors.New("messages: audit initialization unavailable")
	}
	var seed [48]byte
	if _, err := io.ReadFull(entropy, seed[:]); err != nil {
		clear(seed[:])
		return nil, errors.New("messages: audit entropy unavailable")
	}
	a := &messageAudit{sink: sink, epoch: hex.EncodeToString(seed[32:])}
	copy(a.key[:], seed[:32])
	clear(seed[:])
	return a, nil
}

// prepare performs no logging I/O. Its caller publishes this projection with
// the canonical record, then appends it outside native/directory/CAS locks.
func (a *messageAudit) prepare(record *messageRecord) (*messageAuditEntry, error) {
	if a == nil || record == nil {
		return nil, errors.New("messages: audit unavailable")
	}
	request, err := decodeRetainedMessageRequest(record.Spec)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errors.New("messages: audit is closed")
	}
	h := hmac.New(sha256.New, a.key[:])
	h.Write([]byte("openabstractions.messages.audit.content@1\x00"))
	var size [8]byte
	for _, text := range []string{request.Subject, request.Body} {
		binary.BigEndian.PutUint64(size[:], uint64(len(text)))
		h.Write(size[:])
		h.Write([]byte(text))
	}
	outcome := "sent"
	if record.Held {
		outcome = "held"
	}
	entry := &messageAuditEntry{Epoch: a.epoch, Digest: hex.EncodeToString(h.Sum(nil)), ID: record.ID, Account: record.Actor.Account, From: messageSenderLabel(record.Actor), FromProgram: record.Actor.Program, FromContext: record.Actor.Context, To: request.To, ToProgram: record.Receiver, ToContext: request.Context, Kind: request.Kind.String(), Outcome: outcome, At: record.AcceptedAt, ExpiresUnixMs: record.ExpiresUnixMs, Origin: strings.Join(record.Origin, ",")}
	if record.Actor.Chat != nil || record.Actor.Remote != nil && record.Actor.Remote.ReturnContext != "" {
		entry.ReturnContext = record.Actor.returnContext()
	}
	return entry, nil
}

func (a *messageAudit) append(entry *messageAuditEntry) error {
	if a == nil || entry == nil {
		return errors.New("messages: audit unavailable")
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return errors.New("messages: audit is closed")
	}
	sink := a.sink
	a.mu.Unlock()
	attrs := map[string]string{
		"oa_message": "1", "message_id": entry.ID, "account": entry.Account,
		"from": entry.From, "from_program": entry.FromProgram, "from_context": entry.FromContext,
		"to": entry.To, "to_program": entry.ToProgram, "to_context": entry.ToContext,
		"kind": entry.Kind, "outcome": entry.Outcome, "accepted_at": strconv.FormatInt(entry.At, 10), "expires_at": strconv.FormatInt(entry.ExpiresUnixMs, 10),
		"audit_epoch": entry.Epoch, "content_hmac": entry.Digest,
		"updated_at":    strconv.FormatInt(entry.UpdatedAt, 10),
		"state_version": strconv.FormatUint(entry.Version, 10),
	}
	if entry.MetadataOnly {
		attrs["metadata_only"] = "1"
	}
	if entry.Reference != nil {
		attrs["foreign_peer_key"] = entry.Reference.OwnerPeerKey
		attrs["foreign_message_id"] = entry.Reference.MessageID
	}
	if entry.ReturnContext != "" {
		attrs["return_context"] = entry.ReturnContext
	}
	if entry.Origin != "" {
		// The person sees which questions woke the turn that sent this.
		attrs["origin_chain"] = entry.Origin
	}
	for name, value := range map[string]string{"delivery": entry.Delivery, "to_folder": entry.ToFolder, "permit_program": entry.PermitProgram, "permit_action": entry.PermitAction, "permit_resource": entry.PermitResource} {
		if value != "" {
			attrs[name] = value
		}
	}
	if entry.DeliveryState != "" {
		attrs["delivery_state"] = entry.DeliveryState
	}
	if entry.UndeliveredReason != "" {
		attrs["undelivered_reason"] = entry.UndeliveredReason
	}
	if entry.DeliveryAt > 0 {
		attrs["delivery_at"] = strconv.FormatInt(entry.DeliveryAt, 10)
	}
	return sink.Write(logging.Record{Schema: logging.SchemaVersion, Time: logging.At(time.UnixMilli(max(entry.At, entry.UpdatedAt, entry.DeliveryAt))), Level: logging.LevelInfo, Msg: "Application message", Attrs: attrs})
}

// presenceLapsed records a receiver whose presence lease lapsed without a
// withdrawal, about that receiver's own program (LOG-P9), so the person sees
// the fault in that program's history. It carries no message content.
func (a *messageAudit) presenceLapsed(program, application, contextName string, at time.Time) error {
	if a == nil {
		return errors.New("messages: audit unavailable")
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return errors.New("messages: audit is closed")
	}
	sink := a.sink
	a.mu.Unlock()
	about, err := logging.AboutCaller(sink, program)
	if err != nil {
		return err
	}
	return about.Write(logging.Record{Schema: logging.SchemaVersion, Time: logging.At(at), Level: logging.LevelWarn, Msg: "Application inbox lapsed", Attrs: map[string]string{
		"oa_message_presence": "lapsed", "application": application, "program": program, "context": contextName,
		"lapsed_at": strconv.FormatInt(at.UnixMilli(), 10),
	}})
}

func (a *messageAudit) close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	clear(a.key[:])
	a.closed = true
}
