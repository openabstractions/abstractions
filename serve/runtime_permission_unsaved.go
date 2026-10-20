package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"

	"github.com/openabstractions/abstraction-facade/go/grants"
	logging "github.com/openabstractions/abstraction-logging/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// closedError is a fixed diagnostic sentence without data; it is its own
// error class (grants.PermissionErrorClass).
type closedError string

func (e closedError) Error() string      { return string(e) }
func (e closedError) ErrorClass() string { return string(e) }

// classedError names its class and keeps its cause, whose text may carry
// data, out of that class.
type classedError struct {
	class string
	cause error
}

func (e classedError) Error() string      { return e.class + ": " + e.cause.Error() }
func (e classedError) Unwrap() error      { return e.cause }
func (e classedError) ErrorClass() string { return e.class }

// useLogs gives the runtime's Asks its diagnostic record sink.
func (a *runtimeAsks) useLogs(sink logging.Sink) {
	if a != nil {
		a.logs = sink
		a.logQuestions(sink)
	}
}

// allowNotSaved is a save attempt that failed. A conflict is a stale
// revision its caller relists and retries; the Allow stays for that retry.
func allowNotSaved(outcome rwire.PolicyEditOutcome, err error) bool {
	return err != nil || outcome != rwire.PolicyEditOutcomeApplied && outcome != rwire.PolicyEditOutcomeConflict
}

// releaseUnsavedAllow applies the owner's rule for an Allow whose permission
// could not be saved (2026-11-09): the answer is not consent that stands.
// The Book releases it, the asker reads not permitted, and its next request
// asks afresh. It is released on the first save that fails other than by a
// stale revision; no retry runs first. Rights' completion receipts decide
// what was saved; an Allow with a receipt stays. The operator must still be admitted. It reports
// whether the answer was released.
func (a *runtimeAsks) releaseUnsavedAllow(id string, by rwire.Subject, outcome string, cause error, authorize func() error) bool {
	if a == nil || a.book == nil || a.rights == nil || a.rights.policy == nil || by.Account != a.rights.owner || authorize == nil {
		return false
	}
	if authorize() != nil {
		return false
	}
	record, _, _ := a.book.ApplicationRecord(id)
	// Read under the Book lock: a recheck that saved this Allow meanwhile
	// left its receipt, and the Allow stays.
	released, err := a.book.ReleaseUnsavedAllow(id, func() (bool, error) {
		if typed, err := a.rights.policy.AppliedPermissionEffect(id); err != nil || typed {
			return true, err
		}
		return a.rights.policy.AppliedFirstUseAllow(id)
	})
	if err != nil {
		a.reportPermission(fmt.Errorf("permission Allow %s not released: %v", id, err))
		return false
	}
	if !released {
		return false
	}
	unsavedReasks.released(unsavedRecordKey(record))
	if a.logs != nil {
		attrs := map[string]string{"permission.stage": "effect", "permission.outcome": outcome, "question.id": id, "question.outcome": "not_applied"}
		if class := grants.PermissionErrorClass(cause); class != "" {
			attrs["permission.error"] = class
		}
		if err := a.logs.Write(logging.Record{Schema: logging.SchemaVersion, Time: logging.At(time.Now()), Level: logging.LevelWarn, Msg: "Permission not saved", Attrs: attrs}); err != nil {
			a.reportPermission(err)
		}
	}
	return true
}

// unsavedReaskInterval bounds re-asks after an unsaved Allow: one re-ask per
// app and target each hour. A further failed save within the hour keeps the
// answer released, and the app's requests read unavailable until it passes.
const unsavedReaskInterval = time.Hour

type unsavedReask struct {
	first time.Time
	quiet bool
}

type unsavedReaskBook struct {
	mu    sync.Mutex
	items map[string]unsavedReask
	now   func() time.Time
}

var unsavedReasks = &unsavedReaskBook{items: map[string]unsavedReask{}, now: time.Now}

func (b *unsavedReaskBook) released(key string) {
	if key == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	for k, v := range b.items {
		if now.Sub(v.first) >= unsavedReaskInterval {
			delete(b.items, k)
		}
	}
	if v, ok := b.items[key]; ok {
		v.quiet = true
		b.items[key] = v
		return
	}
	b.items[key] = unsavedReask{first: now}
}

// saved clears key after its Allow was saved, so a later failed save of the
// same target re-asks once again.
func (b *unsavedReaskBook) saved(key string) {
	if key == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.items, key)
}

// unsavedReaskKey reads the key of record id before its save.
func (a *runtimeAsks) unsavedReaskKey(id string) string {
	if a == nil || a.book == nil {
		return ""
	}
	record, found, err := a.book.ApplicationRecord(id)
	if err != nil || !found {
		return ""
	}
	return unsavedRecordKey(record)
}

// held reports that key's re-ask for this hour is spent.
func (b *unsavedReaskBook) held(key string) bool {
	if key == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.items[key]
	return ok && v.quiet && b.now().Sub(v.first) < unsavedReaskInterval
}

// unsavedRecordKey names the app and target a released Allow asked about.
func unsavedRecordKey(r asks.Record) string {
	switch {
	case r.TypedPermission != nil:
		return unsavedTargetKey(r.TypedPermission.Effect)
	case r.Permission != nil:
		return unsavedTargetKey(*r.Permission)
	}
	return ""
}

func unsavedTargetKey(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
