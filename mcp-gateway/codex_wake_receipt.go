package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	cas "github.com/openabstractions/abstraction-cas/go"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

var errCodexWakeStale = errors.New("Codex wake no longer exceeds submitted high-water mark")
var errCodexWakeBusy = errors.New("receiver:busy: prior OA pointer consumption is unconfirmed")

type codexWakeLedger struct {
	Receipts    map[string]string
	Outstanding map[string]codexWakeSlot
	Touched     map[string]int64 `json:",omitempty"`
	// Issued keeps, per thread slot, the last codexIssuedTickets Codex
	// tickets this ledger handed to the host, newest last. A read with one of
	// them belongs to a turn OA's pointer started, even after a newer pointer
	// replaced its slot (security review 2026-11-22, item 2).
	Issued map[string][]string `json:",omitempty"`
}

// codexIssuedTickets and codexIssuedSlots bound the issued-ticket history.
const (
	codexIssuedTickets = 4
	codexIssuedSlots   = 64
)

// Capacity may forget only a receipt with no live pointer. Ordinary helper
// exit/restart performs no cleanup. A capacity-evicted binding may wake once.
func evictCodexWakeReceipt(rows *codexWakeLedger, now int64, preserve string, pending func(codexWakeSlot, int64) bool) bool {
	oldest := ""
	for key := range rows.Receipts {
		if key == preserve {
			continue
		}
		live := false
		for _, slot := range rows.Outstanding {
			if slot.Binding == key && pending(slot, now) {
				live = true
				break
			}
		}
		if !live && (oldest == "" || rows.Touched[key] < rows.Touched[oldest] || rows.Touched[key] == rows.Touched[oldest] && key < oldest) {
			oldest = key
		}
	}
	if oldest == "" {
		return false
	}
	delete(rows.Receipts, oldest)
	delete(rows.Touched, oldest)
	for key, slot := range rows.Outstanding {
		if slot.Binding == oldest {
			delete(rows.Outstanding, key)
		}
	}
	return true
}

type codexWakeSlot struct {
	Binding, Ticket string
	Until           int64
	Prompt          string `json:",omitempty"`
	PriorWake       string `json:",omitempty"`
	UnknownUntil    int64  `json:",omitempty"`
	Accepted        bool   `json:",omitempty"`
	Repeat          bool   `json:",omitempty"`
	Codex           bool   `json:",omitempty"`
}
type codexWakeState struct {
	Load    func(context.Context) (string, error)
	Reserve func(context.Context, string) (string, error)
	Refresh func(context.Context, string, string) (string, error)
	Pointer func(context.Context, string, string) (string, error)
	// UndoUnstarted releases the exact reservation after a proven no-start.
	UndoUnstarted func(context.Context, string, string) error
	AcceptQueued  func(context.Context, string) error
	// ConfirmWake consumes this binding's pointer for a matching ticket.
	// ConfirmChatWake is used only after a successful body read by the
	// current typed native chat owner. It reconciles that chat's old-context
	// pointer. For Claude and OpenCode an empty ticket can consume whichever
	// pointer the thread holds. The Codex host queue requires the exact ticket.
	// Both return the wake key that started the reading turn: the key the
	// ticket reserved, or for a ticketless read the key of a held pointer
	// already handed to the adapter. The key is empty when the read woke no
	// turn.
	ConfirmWake     func(context.Context, string) (string, bool, error)
	ConfirmChatWake func(context.Context, string) (string, bool, error)
	// Held reports a Codex pointer queued to the host and not yet read with
	// its ticket. It is false for every other host.
	Held func(context.Context) (bool, error)
	// Issued reports a Codex ticket this ledger handed to the host for this
	// thread. A read with it belongs to a turn OA's pointer started.
	Issued func(context.Context, string) (bool, error)
}

const codexWakeLease = 30 * time.Minute
const codexUnknownQueueLease = 10 * time.Minute

func validCodexWakeTicket(ticket string) bool {
	if len(ticket) != 8 && len(ticket) != 64 {
		return false
	}
	for _, c := range ticket {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}

// codexQueuedWakePrompt is the pointer OA queues into a Codex thread. It
// tells the turn it is reply-only (S5 ruling, 2026-11-22): OA refuses its
// OA tools outside the list, while Codex's own tools follow Codex's approval
// policy.
func codexQueuedWakePrompt(ticket string) string {
	return "Check your OA inbox with oa_messages_wait (wake_ticket: " + ticket + "). This turn is reply-only unless the person allowed more: read the message, read files to answer and reply with oa_messages_reply."
}

// codexQueuedWakePromptBefore081 is the pointer before 0.8.1. A ledger that
// holds one stays valid.
func codexQueuedWakePromptBefore081(ticket string) string {
	return "Check your OA inbox with oa_messages_wait (wake_ticket: " + ticket + ")."
}

func shortWakeTicket(random io.Reader, retained []string) (string, error) {
	for {
		var nonce [4]byte
		if _, err := io.ReadFull(random, nonce[:]); err != nil {
			return "", err
		}
		ticket := hex.EncodeToString(nonce[:])
		collision := false
		for _, prior := range retained {
			collision = collision || prior == ticket
		}
		if !collision {
			return ticket, nil
		}
	}
}

// Busy is an observed display fact. Native admission, the ticket ledger and
// the runtime's wake projection each retain their separate ownership.
func (b *messagesBackend) publishWakeBusy(ctx context.Context, busy bool) {
	b.publishWakeStatus(ctx, func(s *messagesInboxStatus) { s.wakeBusy = busy })
}

// publishWakeUnconfirmed records the Codex receiver's claim, wake_busy or
// queue_unknown, that a queued wake is unconfirmed; an empty claim clears it.
// The presence status then tells Prepare and the listing the message is
// queued.
func (b *messagesBackend) publishWakeUnconfirmed(ctx context.Context, claim string) {
	b.publishWakeStatus(ctx, func(s *messagesInboxStatus) { s.wakeUnconfirmed = claim })
}

// A confirmed read consumes the held pointer: the thread is free and the wake
// it carried is confirmed.
func (b *messagesBackend) publishWakeConfirmed(ctx context.Context) {
	b.publishWakeStatus(ctx, func(s *messagesInboxStatus) { s.wakeBusy, s.wakeUnconfirmed = false, "" })
}

func (b *messagesBackend) publishWakeStatus(ctx context.Context, set func(*messagesInboxStatus)) {
	if b.status == nil {
		return
	}
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for !b.status.mu.TryLock() {
		select {
		case <-call.Done():
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
	set(b.status)
	b.status.mu.Unlock()
	if native, err := b.native(call); err == nil {
		_, _ = b.status.openContext(call, native, b.inboxInstance(), b.contextName, nil)
	}
}

func validMessageWake(value string) bool {
	if len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// The host thread and native context are independently bound facts. Neither
// substitutes for the other. Existing protected helper state and CAS retain at
// most256 binding receipts; a full or unreadable ledger refuses new delivery.
func codexWakeReceipt(ctx context.Context, application, nativeContext, thread string, clocks ...func() time.Time) (*codexWakeState, error) {
	return codexWakeReceiptWithRandom(ctx, application, nativeContext, thread, rand.Reader, clocks...)
}

func codexWakeReceiptWithRandom(ctx context.Context, application, nativeContext, thread string, random io.Reader, clocks ...func() time.Time) (*codexWakeState, error) {
	if !codexQueueThread.MatchString(thread) {
		return nil, errors.New("Codex durable wake binding unavailable")
	}
	return newChatWakeReceipt(ctx, application, nativeContext, thread, random, true, clocks...)
}

// useWakeState gives a chat receiver its thread's wake ledger: confirm after
// a body read, the fixed pointer, and a reservation that refreshes the ticket
// it is given or reserves a new one. Every agent's chat binding wires it here.
func (b *messagesBackend) useWakeState(state *codexWakeState) {
	b.wakeConfirm = state.ConfirmChatWake
	b.wakeHeld = state.Held
	b.wakeIssued = state.Issued
	b.wakePointer = state.Pointer
	b.wakeReserve = func(ctx context.Context, wake, ticket string) (string, error) {
		if ticket != "" {
			return state.Refresh(ctx, ticket, wake)
		}
		return state.Reserve(ctx, wake)
	}
}

func chatWakeReceiptWithRandom(ctx context.Context, application, nativeContext, thread string, random io.Reader, clocks ...func() time.Time) (*codexWakeState, error) {
	return newChatWakeReceipt(ctx, application, nativeContext, thread, random, false, clocks...)
}

func newChatWakeReceipt(ctx context.Context, application, nativeContext, thread string, random io.Reader, codex bool, clocks ...func() time.Time) (*codexWakeState, error) {
	d, _ := ctx.Value(agentDiagnosticKey{}).(*agentDiagnostic)
	if d == nil {
		return nil, errors.New("Codex durable wake binding unavailable")
	}
	return newWakeReceiptAt(d.root, application, nativeContext, thread, random, codex, clocks...)
}

// newWakeReceiptAt opens the wake ledger in the state folder root. The helper
// passes its own state folder; the runtime's Codex start passes the recipe's,
// the same folder.
func newWakeReceiptAt(root setupIdentityRef, application, nativeContext, thread string, random io.Reader, codex bool, clocks ...func() time.Time) (*codexWakeState, error) {
	if root == nil || !agentRoutingContext.MatchString(nativeContext) {
		return nil, errors.New("Codex durable wake binding unavailable")
	}
	program, err := os.Executable()
	if err != nil {
		return nil, err
	}
	program = identity.NormalizeSubjectProgram(identity.CanonicalProgramPath(program))
	now := time.Now
	if len(clocks) != 0 {
		now = clocks[0]
	}
	sum := sha256.Sum256([]byte(program + "\x00" + application + "\x00" + nativeContext + "\x00" + thread))
	binding := hex.EncodeToString(sum[:])
	threadSum := sha256.Sum256([]byte(nativecli.AgentApplicationBase(application) + "\x00" + thread))
	threadSlot := hex.EncodeToString(threadSum[:])
	// Another chat may share the ledger. Preserve each live Codex pointer
	// regardless of which adapter next prunes or reserves. An accepted pointer
	// holds the slot for codexWakeLease from its acceptance; after that the
	// next new message queues its own (architect ruling 1, 2026-11-22).
	pending := func(slot codexWakeSlot, at int64) bool {
		if slot.UnknownUntil != 0 {
			return slot.UnknownUntil > at
		}
		return slot.Until > at
	}

	path := filepath.Join(root.FileID().Path(), "codex-wake-receipts.json")
	decode := func(raw []byte) (codexWakeLedger, error) {
		rows := codexWakeLedger{Receipts: map[string]string{}, Outstanding: map[string]codexWakeSlot{}}
		if len(raw) != 0 {
			if err := json.Unmarshal(raw, &rows); err != nil {
				return rows, err
			}
		}
		if rows.Receipts == nil || rows.Outstanding == nil || len(rows.Receipts) > 256 || len(rows.Outstanding) > 256 {
			return rows, errors.New("Codex wake receipt bound exceeded")
		}
		if rows.Touched == nil {
			rows.Touched = map[string]int64{}
		}
		if len(rows.Issued) > codexIssuedSlots {
			return rows, errors.New("Codex issued ticket bound exceeded")
		}
		for slot, tickets := range rows.Issued {
			if len(slot) != 64 || len(tickets) == 0 || len(tickets) > codexIssuedTickets {
				return rows, errors.New("invalid Codex issued tickets")
			}
			for _, ticket := range tickets {
				if !validCodexWakeTicket(ticket) {
					return rows, errors.New("invalid Codex issued ticket")
				}
			}
		}
		if len(rows.Touched) > 256 {
			return rows, errors.New("Codex wake timestamp bound exceeded")
		}
		for key, stamp := range rows.Touched {
			if rows.Receipts[key] == "" || stamp < 0 {
				return rows, errors.New("invalid Codex wake timestamp")
			}
		}
		for key, value := range rows.Receipts {
			if len(key) != 64 || !validMessageWake(value) || value == "" {
				return rows, errors.New("invalid Codex wake receipt")
			}
		}
		for slot, owner := range rows.Outstanding {
			if owner.UnknownUntil < 0 || owner.UnknownUntil != 0 && (!owner.Codex || owner.Prompt == "") {
				return rows, errors.New("invalid Codex queue acceptance lease")
			}
			if owner.Accepted && (!owner.Codex || owner.Prompt == "" || owner.UnknownUntil != 0) {
				return rows, errors.New("invalid Codex queue acceptance")
			}
			currentCodexPrompt := owner.Codex && len(owner.Ticket) == 8 && (owner.Prompt == codexQueuedWakePrompt(owner.Ticket) || owner.Prompt == codexQueuedWakePromptBefore081(owner.Ticket))
			if len(slot) != 64 || rows.Receipts[owner.Binding] == "" || !validCodexWakeTicket(owner.Ticket) || owner.Until <= 0 || len(owner.Prompt) > 512 || !currentCodexPrompt && !validWakePointer(owner.Prompt, owner.Ticket, owner.Repeat) || !validMessageWake(owner.PriorWake) || owner.PriorWake >= rows.Receipts[owner.Binding] {
				return rows, errors.New("invalid Codex outstanding pointer")
			}
		}
		return rows, nil
	}
	encode := func(rows *codexWakeLedger) ([]byte, error) {
		for {
			raw, err := json.Marshal(rows)
			if err != nil || len(raw) <= 64<<10 {
				return raw, err
			}
			if !evictCodexWakeReceipt(rows, now().UnixMilli(), binding, pending) {
				return nil, errors.New("Codex wake receipt byte bound exceeded")
			}
		}
	}
	current := func(call context.Context) error {
		if !setupIdentitiesCurrent(call, []setupIdentityRef{root}) {
			return errors.New("Codex wake state changed")
		}
		return nil
	}
	load := func(call context.Context) (string, error) {
		if err := current(call); err != nil {
			return "", err
		}
		raw, err := cas.ReadLimitContext(call, path, 64<<10)
		if err != nil {
			return "", err
		}
		rows, err := decode(raw)
		return rows.Receipts[binding], err
	}
	claim := func(call context.Context, wake, existing string) (string, error) {
		if err := current(call); err != nil {
			return "", err
		}
		if wake == "" || !validMessageWake(wake) {
			return "", nil
		}
		ticket := ""
		claimed := false
		err := cas.ChangeLimitContext(call, path, 64<<10, func(raw []byte) ([]byte, error) {
			rows, err := decode(raw)
			if err != nil {
				return nil, err
			}
			if existing != "" {
				held, ok := rows.Outstanding[threadSlot]
				if !ok || held.Binding != binding || held.Ticket != existing || !pending(held, now().UnixMilli()) || wake < rows.Receipts[binding] {
					return raw, nil
				}
				rows.Receipts[binding], ticket, claimed = wake, held.Ticket, true
				rows.Touched[binding] = max(0, now().UnixMilli())
				return encode(&rows)
			}
			if wake <= rows.Receipts[binding] {
				return raw, nil
			}
			if held, ok := rows.Outstanding[threadSlot]; ok && pending(held, now().UnixMilli()) {
				return nil, errCodexWakeBusy
			}
			retained := []string{}
			if prior, ok := rows.Outstanding[threadSlot]; ok {
				retained = append(retained, prior.Ticket)
			}
			for key, slot := range rows.Outstanding {
				if !pending(slot, now().UnixMilli()) {
					delete(rows.Outstanding, key)
				}
			}
			if rows.Receipts[binding] == "" && len(rows.Receipts) == 256 && !evictCodexWakeReceipt(&rows, now().UnixMilli(), binding, pending) {
				return nil, errors.New("Codex wake receipt bound exceeded")
			}
			_, hasSlot := rows.Outstanding[threadSlot]
			if !hasSlot && len(rows.Outstanding) == 256 {
				return nil, errors.New("Codex wake receipt bound exceeded")
			}
			ticket, err = shortWakeTicket(random, retained)
			if err != nil {
				return nil, err
			}
			prior := rows.Receipts[binding]
			slot := codexWakeSlot{Binding: binding, Ticket: ticket, Until: now().Add(codexWakeLease).UnixMilli(), Repeat: prior != ""}
			if codex {
				slot.PriorWake = prior
			}
			rows.Receipts[binding], rows.Outstanding[threadSlot], claimed = wake, slot, true
			rows.Touched[binding] = max(0, now().UnixMilli())
			return encode(&rows)
		})
		if !claimed || err != nil {
			return "", err
		}
		return ticket, nil
	}
	undoUnstarted := func(call context.Context, ticket, wake string) error {
		if err := current(call); err != nil {
			return err
		}
		return cas.ChangeLimitContext(call, path, 64<<10, func(raw []byte) ([]byte, error) {
			rows, err := decode(raw)
			if err != nil {
				return nil, err
			}
			held, ok := rows.Outstanding[threadSlot]
			if !ok || held.Binding != binding || held.Ticket != ticket || rows.Receipts[binding] != wake {
				return raw, nil
			}
			delete(rows.Outstanding, threadSlot)
			if held.PriorWake == "" {
				delete(rows.Receipts, binding)
				delete(rows.Touched, binding)
			} else {
				rows.Receipts[binding] = held.PriorWake
			}
			return encode(&rows)
		})
	}
	acceptQueued := func(call context.Context, ticket string) error {
		if err := current(call); err != nil {
			return err
		}
		err := cas.ChangeLimitContext(call, path, 64<<10, func(raw []byte) ([]byte, error) {
			rows, err := decode(raw)
			if err != nil {
				return nil, err
			}
			held, ok := rows.Outstanding[threadSlot]
			if !ok || held.Binding != binding || held.Ticket != ticket || held.UnknownUntil == 0 {
				return raw, nil
			}
			held.UnknownUntil = 0
			held.Accepted = true
			held.Until = now().Add(codexWakeLease).UnixMilli()
			rows.Outstanding[threadSlot] = held
			return encode(&rows)
		})
		if errors.Is(err, cas.ErrNoValue) {
			return nil
		}
		return err
	}
	// heldCodex reports this thread's Codex pointer queued to the host and not
	// yet read with its ticket: a read now belongs to the turn it starts (S5
	// ruling, 2026-11-22). It changes nothing.
	heldCodex := func(call context.Context) (bool, error) {
		if !codex {
			return false, nil
		}
		if err := current(call); err != nil {
			return false, err
		}
		raw, err := cas.ReadLimitContext(call, path, 64<<10)
		if errors.Is(err, cas.ErrNoValue) || errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		rows, err := decode(raw)
		if err != nil {
			return false, err
		}
		held, ok := rows.Outstanding[threadSlot]
		return ok && held.Binding == binding && held.Prompt != "" && pending(held, now().UnixMilli()), nil
	}
	confirm := func(call context.Context, ticket string, currentChat bool) (string, bool, error) {
		if err := current(call); err != nil {
			return "", false, err
		}
		// A Codex inbox poll can run while the host still holds the queued
		// item. Only a read with its ticket confirms that item.
		anyHeld := ticket == "" && currentChat && !codex
		if !anyHeld && !validCodexWakeTicket(ticket) {
			return "", false, nil
		}
		confirmed := false
		wake := ""
		err := cas.ChangeLimitContext(call, path, 64<<10, func(raw []byte) ([]byte, error) {
			rows, err := decode(raw)
			if err != nil {
				return nil, err
			}
			held, ok := rows.Outstanding[threadSlot]
			// A turn Codex runs after its accepted pointer's lease still
			// confirms that pointer by its ticket while the slot is unreplaced.
			live := pending(held, now().UnixMilli()) || codex && !anyHeld && held.Accepted
			if !ok || !anyHeld && held.Ticket != ticket || !live || held.Binding != binding && !currentChat {
				return raw, nil
			}
			// A ticketless read woke a turn only when the pointer reached
			// the adapter; a reservation whose pointer was never issued
			// started nothing.
			if !anyHeld || held.Prompt != "" {
				wake = rows.Receipts[held.Binding]
			}
			delete(rows.Outstanding, threadSlot)
			confirmed = true
			rows.Touched[held.Binding] = max(0, now().UnixMilli())
			return encode(&rows)
		})
		if errors.Is(err, cas.ErrNoValue) {
			return "", false, nil
		}
		if !confirmed || err != nil {
			return "", false, err
		}
		return wake, true, nil
	}
	pointer := func(call context.Context, ticket, sender string) (string, error) {
		if err := current(call); err != nil {
			return "", err
		}
		text := ""
		err := cas.ChangeLimitContext(call, path, 64<<10, func(raw []byte) ([]byte, error) {
			rows, err := decode(raw)
			if err != nil {
				return nil, err
			}
			held, ok := rows.Outstanding[threadSlot]
			if !ok || held.Binding != binding || held.Ticket != ticket || !pending(held, now().UnixMilli()) {
				return raw, nil
			}
			if held.Prompt == "" {
				if codex && len(ticket) == 8 {
					held.Prompt = codexQueuedWakePrompt(ticket)
				} else {
					if codex {
						sender = ""
					}
					held.Prompt, err = messagesTicketPointer(ticket, sender, held.Repeat)
					if err != nil {
						return nil, err
					}
				}
				held.Codex = codex
				if codex {
					held.UnknownUntil = now().Add(codexUnknownQueueLease).UnixMilli()
					rememberCodexIssued(&rows, threadSlot, ticket)
				}
				rows.Outstanding[threadSlot] = held
			}
			text = held.Prompt
			return encode(&rows)
		})
		return text, err
	}
	confirmChat := func(ctx context.Context, ticket string) (string, bool, error) {
		if !strings.HasPrefix(nativeContext, "chat-") {
			return "", false, nil
		}
		return confirm(ctx, ticket, true)
	}
	// issued reports a Codex ticket this ledger handed to the host for this
	// thread: the held pointer's or one of the last few. It changes nothing.
	issued := func(call context.Context, ticket string) (bool, error) {
		if !codex || !validCodexWakeTicket(ticket) {
			return false, nil
		}
		if err := current(call); err != nil {
			return false, err
		}
		raw, err := cas.ReadLimitContext(call, path, 64<<10)
		if errors.Is(err, cas.ErrNoValue) || errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		rows, err := decode(raw)
		if err != nil {
			return false, err
		}
		if held, ok := rows.Outstanding[threadSlot]; ok && held.Ticket == ticket && held.Prompt != "" {
			return true, nil
		}
		return slices.Contains(rows.Issued[threadSlot], ticket), nil
	}
	return &codexWakeState{Load: load, Pointer: pointer, Issued: issued, Reserve: func(ctx context.Context, wake string) (string, error) { return claim(ctx, wake, "") }, Refresh: func(ctx context.Context, ticket, wake string) (string, error) { return claim(ctx, wake, ticket) },
		UndoUnstarted:   undoUnstarted,
		AcceptQueued:    acceptQueued,
		ConfirmWake:     func(ctx context.Context, ticket string) (string, bool, error) { return confirm(ctx, ticket, false) },
		ConfirmChatWake: confirmChat,
		Held:            heldCodex,
	}, nil
}

// rememberCodexIssued records a ticket handed to the host for a thread slot,
// keeping the last codexIssuedTickets. A full history drops the slot whose key
// sorts first.
func rememberCodexIssued(rows *codexWakeLedger, slot, ticket string) {
	if rows.Issued == nil {
		rows.Issued = map[string][]string{}
	}
	if _, ok := rows.Issued[slot]; !ok && len(rows.Issued) >= codexIssuedSlots {
		oldest := ""
		for key := range rows.Issued {
			if oldest == "" || key < oldest {
				oldest = key
			}
		}
		delete(rows.Issued, oldest)
	}
	tickets := append(slices.DeleteFunc(slices.Clone(rows.Issued[slot]), func(t string) bool { return t == ticket }), ticket)
	if len(tickets) > codexIssuedTickets {
		tickets = tickets[len(tickets)-codexIssuedTickets:]
	}
	rows.Issued[slot] = tickets
}
