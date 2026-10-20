package main

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// VISION 2026-10-31 design conditions: Activity records the origin chain, and
// a turn woken by a peer cannot wake another session without its own question,
// within a hop limit.
//
// A woken turn's messages leave through the receiver server OA started for it,
// bound to the woken inbox context. While that start runs, every message sent
// from that application and context carries the chain of question IDs that
// woke it: the waking question's own chain plus that question. A message whose
// chain already holds messageWakeHopLimit IDs never starts a receiver and
// projects no wake for an open idle chat; it waits for the receiver's next read.
//
// Two woken turns in a row: a person's turn may wake a second assistant, whose
// turn may wake a third; the third's questions start nothing. A longer chain is
// a loop or a runaway more often than a task the person asked for, and each
// step that starts an agent costs one start from the person's hourly start setting.
const messageWakeHopLimit = 2

// messageWakeCheck is how often a running woken turn is checked again. A stop
// from Activity or a withdrawn start permit ends the turn within it.
const messageWakeCheck = 2 * time.Second

type messageOriginKey struct{}

func withMessageOrigin(ctx context.Context, origin []string) context.Context {
	if len(origin) == 0 {
		return ctx
	}
	return context.WithValue(ctx, messageOriginKey{}, slices.Clone(origin))
}

func messageOriginFrom(ctx context.Context) []string {
	origin, _ := ctx.Value(messageOriginKey{}).([]string)
	return slices.Clone(origin)
}

// validMessageOrigin bounds a stored chain: at most the hop limit, each a
// message ID of the canonical record's shape.
func validMessageOrigin(origin []string) bool {
	if len(origin) > messageWakeHopLimit {
		return false
	}
	for _, id := range origin {
		if id == "" || len(id) > 128 || strings.ContainsRune(id, ',') || strings.IndexFunc(id, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
}

// messageChainWakes reports whether this message may wake its receiver: its
// chain holds fewer than messageWakeHopLimit woken turns. One bound governs a
// dormant receiver's start and an open idle chat's wake pointer, for every
// kind that wakes. A message past it waits for the receiver's next read.
func messageChainWakes(r *messageRecord) bool {
	return len(r.Origin) < messageWakeHopLimit
}

// messageWakeChain is the chain a turn woken by this question carries.
func messageWakeChain(r *messageRecord) []string {
	return append(slices.Clone(r.Origin), r.ID)
}

// messageTurnWakeLease ends an open chat's turn row this long after its last
// TurnWake key, the gateway's wake ticket lease, when no turn signal arrives.
const messageTurnWakeLease = 30 * time.Minute

// A row is either a serve-run start (attempt set), which ends with its start,
// or an open chat's woken turn (until set), which TurnWake records and ends.
type messageWake struct {
	attempt string
	origin  []string
	until   time.Time
}

type messageWakes struct {
	mu   sync.Mutex
	rows map[string]messageWake
}

// turn records the chain of the wake that started an open chat's current
// turn. A live row keeps the longer chain and its lease restarts. Inside a
// serve-run start the start's row stays and keeps the longer chain.
func (w *messageWakes) turn(application, contextName string, origin []string, now time.Time) {
	key := application + "\x00" + contextName
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.rows == nil {
		w.rows = map[string]messageWake{}
	}
	row, ok := w.rows[key]
	if ok && row.attempt != "" {
		if len(origin) > len(row.origin) {
			row.origin = slices.Clone(origin)
			w.rows[key] = row
		}
		return
	}
	if ok && now.Before(row.until) && len(row.origin) > len(origin) {
		origin = row.origin
	}
	w.rows[key] = messageWake{origin: slices.Clone(origin), until: now.Add(messageTurnWakeLease)}
}

// endTurn ends an open chat's turn row. A serve-run start's row ends only with
// its start.
func (w *messageWakes) endTurn(application, contextName string) {
	key := application + "\x00" + contextName
	w.mu.Lock()
	defer w.mu.Unlock()
	if row, ok := w.rows[key]; ok && row.attempt == "" {
		delete(w.rows, key)
	}
}

// begin records a running woken turn for its application and inbox context
// and returns its end. A later start for the same context replaces it; the
// earlier end then leaves the later one in place.
func (w *messageWakes) begin(application, contextName, attempt string, origin []string) func() {
	key := application + "\x00" + contextName
	w.mu.Lock()
	if w.rows == nil {
		w.rows = map[string]messageWake{}
	}
	w.rows[key] = messageWake{attempt: attempt, origin: slices.Clone(origin)}
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		if w.rows[key].attempt == attempt {
			delete(w.rows, key)
		}
		w.mu.Unlock()
	}
}

// origin is the chain a message sent now from this application and context
// carries; empty when no woken turn runs there.
func (w *messageWakes) origin(application, contextName string, now time.Time) []string {
	key := application + "\x00" + contextName
	w.mu.Lock()
	defer w.mu.Unlock()
	row, ok := w.rows[key]
	if ok && row.attempt == "" && !now.Before(row.until) {
		delete(w.rows, key)
		return nil
	}
	return slices.Clone(row.origin)
}

// superviseMessageStartAuthority checks authority every interval while a woken
// turn runs and cancels it at the first refusal. The returned function ends
// the supervision.
func superviseMessageStartAuthority(ctx context.Context, authority func(context.Context) error, interval time.Duration, cancel context.CancelFunc) func() {
	done := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if authority(ctx) != nil {
					cancel()
					return
				}
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}
