package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	rawcas "github.com/openabstractions/abstraction-cas/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
)

// Setup's question is a person-only native Asks occurrence (setup.models),
// and its retained facts name the apps it sets up. The person's Set up
// answer confirms those apps (VISION 2026-10-31): the runtime records Keep
// for each named app that registered itself and still waits unconfirmed,
// through the same directory change a Keep answer makes, with provenance
// setup. An app the question did not name keeps waiting for the person's
// click in Apps. Each Keep is recorded with what Undo restores (CONV-U1):
// the Panel's Activity line offers Undo, which reaches the runtime as
// AskConfirmation on the app, and the runtime returns an app Setup kept to
// unconfirmed before asking the person about it.

// setupKeptFile holds the Keeps Setup's answer made, by app name.
const setupKeptFile = "setup-kept.json"

// setupKeepProvenance names the Keeps Setup's answer made.
const setupKeepProvenance = "setup"

// setupKeep is one Keep Setup's answer made: the occurrence answered, the
// app's program, and what Undo restores: the applications@2 mark the Keep
// cleared, or the applications@3 revision the Keep wrote.
type setupKeep struct {
	Provenance string    `json:"provenance"`
	Occurrence string    `json:"occurrence"`
	At         time.Time `json:"at"`
	Program    string    `json:"program"`
	Mark       *selfMark `json:"mark,omitempty"`
	Revision   string    `json:"revision,omitempty"`
}

const setupKeptLimit = 64 << 10

func (d *applicationDirectory) readSetupKeptLocked(ctx context.Context) (map[string]setupKeep, error) {
	raw, err := rawcas.ReadLimitContext(ctx, filepath.Join(d.dir, setupKeptFile), setupKeptLimit)
	if err != nil {
		return nil, err
	}
	kept := map[string]setupKeep{}
	if raw != nil && (json.Unmarshal(raw, &kept) != nil || kept == nil) {
		return nil, errors.New("applications: invalid Setup Keeps")
	}
	return kept, nil
}

func (d *applicationDirectory) writeSetupKeptLocked(ctx context.Context, kept map[string]setupKeep) error {
	raw, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	return rawcas.ChangeLimitContext(ctx, filepath.Join(d.dir, setupKeptFile), setupKeptLimit, func([]byte) ([]byte, error) { return raw, nil })
}

// setupKeepAnswer applies the answered Setup occurrence id: with the
// person's Set up answer through the operator, each target its retained facts
// name is kept when it waits unconfirmed under the same program (and, for
// applications@3, the same revision). It returns the names it kept; an
// occurrence already applied keeps nothing again.
func (d *applicationDirectory) setupKeepAnswer(ctx context.Context, book *asks.Book, id string) ([]string, error) {
	if book == nil {
		return nil, errors.New("setup: question owner unavailable")
	}
	record, found, err := book.ApplicationRecord(id)
	if err != nil || !found {
		return nil, err
	}
	if record.Key != asks.SetupModelsKey || record.Option != "setup" || record.AnsweredBy != asks.AnsweredByOperator || record.Content == nil {
		return nil, nil
	}
	facts, err := asks.SetupFactsFromSlots(record.Content.Slots)
	if err != nil {
		return nil, err
	}
	if !d.lock(ctx) {
		return nil, ctx.Err()
	}
	defer d.unlock()
	kept, err := d.readSetupKeptLocked(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now
	if d.now != nil {
		now = d.now
	}
	var names []string
	for _, target := range facts.Targets {
		if prior, ok := kept[target.Name]; ok && prior.Occurrence == record.ID {
			continue
		}
		keep := setupKeep{Provenance: setupKeepProvenance, Occurrence: record.ID, At: now().UTC(), Program: target.Program}
		if d.v3 != nil {
			if e, ok := d.v3.entries[target.Name]; ok {
				f := d.v3.files[target.Name]
				if e.State != "pending" || e.Source != "self" || e.Revision != target.Revision || !identity.SameSubjectProgram(f.Source.Program, target.Program) {
					continue
				}
				if !d.changeApplicationV3QuestionLocked(ctx, target.Name, f.Source.Program, "", "", "enabled") {
					continue
				}
				keep.Revision = d.v3.entries[target.Name].Revision
				next := maps.Clone(kept)
				next[target.Name] = keep
				if err := d.writeSetupKeptLocked(ctx, next); err != nil {
					return names, err
				}
				kept, names = next, append(names, target.Name)
				continue
			}
		}
		v, registered := d.descriptors[target.Name]
		mark, unconfirmed := d.unconfirmed[target.Name]
		if !registered || !unconfirmed || !mark.genericPending() || mark.Removing || target.Revision != "" || !samePrograms(v.Program, target.Program) {
			continue
		}
		// The record is written before the mark is cleared: a record left
		// beside a still unconfirmed entry restores nothing, and this
		// occurrence's next application keeps it.
		keep.Mark = &mark
		next := maps.Clone(kept)
		next[target.Name] = keep
		if err := d.writeSetupKeptLocked(ctx, next); err != nil {
			return names, err
		}
		kept = next
		if err := d.clearMarkLocked(ctx, target.Name); err != nil {
			return names, err
		}
		names = append(names, target.Name)
	}
	if len(names) > 0 {
		d.notifyLocked()
	}
	return names, nil
}

// undoSetupKeepLocked returns name to unconfirmed when its Keep is still the
// one Setup's answer made, and drops the record either way. It reports
// whether it restored the entry. The caller holds the directory lock.
func (d *applicationDirectory) undoSetupKeepLocked(ctx context.Context, name string) (bool, error) {
	kept, err := d.readSetupKeptLocked(ctx)
	if err != nil {
		return false, err
	}
	keep, ok := kept[name]
	if !ok {
		return false, nil
	}
	next := maps.Clone(kept)
	delete(next, name)
	if err := d.writeSetupKeptLocked(ctx, next); err != nil {
		return false, err
	}
	if d.v3 != nil {
		if e, exists := d.v3.entries[name]; exists {
			if keep.Revision == "" || e.Revision != keep.Revision || e.State != "enabled" || e.Source != "self" {
				return false, nil
			}
			return d.pendApplicationV3Locked(ctx, name)
		}
	}
	v, registered := d.descriptors[name]
	if _, unconfirmed := d.unconfirmed[name]; keep.Mark == nil || !registered || unconfirmed || !samePrograms(v.Program, keep.Program) {
		return false, nil
	}
	mark := *keep.Mark
	// The person is asked afresh: no earlier question acts on the restored
	// registration.
	mark.Question, mark.UnboundQuestion = "", ""
	if err := d.setMarkLocked(ctx, name, mark); err != nil {
		return false, err
	}
	d.generations[name]++
	d.notifyLocked()
	return true, nil
}

// pendApplicationV3Locked returns the applications@3 entry name to pending
// with a new revision.
func (d *applicationDirectory) pendApplicationV3Locked(ctx context.Context, name string) (bool, error) {
	changed := false
	err := d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		e, ok := d.v3.entries[name]
		if !ok || e.State != "enabled" {
			return nil
		}
		revision, err := applicationToken()
		if err != nil {
			return err
		}
		e.State, e.Question, e.Revision = "pending", "", revision
		next := maps.Clone(d.v3.entries)
		next[name] = e
		if err := d.v3.persistLocked(ctx, next); err != nil {
			return err
		}
		changed = true
		return d.projectApplicationV3Locked(legacy)
	})
	if err != nil {
		return false, err
	}
	if changed {
		d.generations[name]++
		d.notifyLocked()
	}
	return changed, nil
}

// followSetupKeep applies the occurrence id once the person answers it. A
// question already followed is not followed twice; the runtime's context
// ends the wait.
func (q *setupRequests) followSetupKeep(d *applicationDirectory, book *asks.Book, id string) {
	if book == nil || id == "" {
		return
	}
	q.keepMu.Lock()
	if q.keepFollowing == nil {
		q.keepFollowing = map[string]bool{}
	}
	if q.keepFollowing[id] {
		q.keepMu.Unlock()
		return
	}
	q.keepFollowing[id] = true
	ctx := q.keepContext
	if ctx == nil {
		ctx = context.Background()
	}
	report := q.keepReport
	q.keepMu.Unlock()
	decided := book.Decided(id)
	go func() {
		defer func() {
			q.keepMu.Lock()
			delete(q.keepFollowing, id)
			q.keepMu.Unlock()
		}()
		select {
		case <-ctx.Done():
			return
		case <-decided:
		}
		if _, err := d.setupKeepAnswer(ctx, book, id); err != nil && report != nil {
			report(fmt.Errorf("setup: keeping the apps its answer named: %w", err))
		}
	}()
}

// resumeSetupKeep follows, after a runtime start, the Setup occurrence the
// receipt names, and applies an answer given while the runtime was stopped.
func (q *setupRequests) resumeSetupKeep(ctx context.Context, d *applicationDirectory, report func(error)) {
	q.keepMu.Lock()
	q.keepContext, q.keepReport = ctx, report
	q.keepMu.Unlock()
	path := q.receiptPath
	a := setupQuestionOwner(d)
	if path == "" || a == nil || a.book == nil {
		return
	}
	raw, err := rawcas.ReadLimitContext(ctx, path, setupReceiptLimit)
	if err != nil {
		return
	}
	v, err := decodeSetupRuntimeReceipt(raw)
	if err != nil || v == nil || v.Occurrence == "" {
		return
	}
	q.followSetupKeep(d, a.book, v.Occurrence)
}
