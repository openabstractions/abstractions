package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"path/filepath"
	"strings"

	cas "github.com/openabstractions/abstraction-cas/go"
	store "github.com/openabstractions/abstraction-cas/go/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
)

const messageRemovalQuestion = "message-binding:"
const messagePublicationQuestion = messageRemovalQuestion + "publish:"

func (d *applicationDirectory) publishMessageLegacyLocked(ctx context.Context, v wire.ApplicationDescriptor, mark *selfMark, pin *applicationLegacyPin) wire.ApplicationChange {
	mode := "operator"
	if mark != nil {
		mode = "self"
	}
	question := messagePublicationQuestion + mode
	if d.v3 != nil {
		if _, exists := d.v3.entries[v.Name]; exists {
			return appChange(wire.ApplicationOutcomeConflict, "name")
		}
	}
	if current, exists := d.descriptors[v.Name]; exists {
		candidate := d.unconfirmed[v.Name]
		left, _ := json.Marshal(current)
		right, _ := json.Marshal(v)
		if !candidate.Removing || candidate.Question != question {
			// No publication of this name is in flight: the same program
			// with the same identity updates the entry in place; it keeps its
			// receiver, so no message permission is invalidated.
			return d.updateSameProgramLocked(ctx, current, v, pin)
		}
		if !bytes.Equal(left, right) {
			return appChange(wire.ApplicationOutcomeConflict, "name")
		}
		if mark != nil && (candidate.Program != mark.Program || candidate.ProgramKey != mark.ProgramKey || candidate.Publisher != mark.Publisher || candidate.Trusted != mark.Trusted || candidate.Description != mark.Description || candidate.OriginalName != mark.OriginalName) {
			return appChange(wire.ApplicationOutcomeConflict, "source")
		}
	} else {
		if mark != nil && len(d.unconfirmedOfLocked(v.Program)) > 0 {
			return appChange(wire.ApplicationOutcomeConflict, "unconfirmed")
		}
		if len(d.descriptors) >= 64 {
			return appChange(wire.ApplicationOutcomeUnavailable, "capacity")
		}
		candidate := selfMark{Program: v.Program, ProgramKey: identity.ProgramKey(v.Program)}
		if mark != nil {
			candidate = *mark
		}
		revision, err := applicationToken()
		if err != nil {
			return appChange(wire.ApplicationOutcomeUnavailable, "persistence")
		}
		candidate.RegistrationRevision, candidate.Question, candidate.Removing = revision, question, true
		candidate.RemovalRevision, err = legacyApplicationRemovalRevision(v, candidate)
		if err != nil {
			return appChange(wire.ApplicationOutcomeUnavailable, "persistence")
		}
		if err := d.setLegacyPinLocked(ctx, v.Name, pin); err != nil {
			return appChange(wire.ApplicationOutcomeUnavailable, "persistence")
		}
		if err := d.setMarkLocked(ctx, v.Name, candidate); err != nil {
			return appChange(wire.ApplicationOutcomeUnavailable, "persistence")
		}
		next := maps.Clone(d.descriptors)
		next[v.Name] = v
		if err := d.persistLocked(ctx, next); err != nil {
			return appChange(wire.ApplicationOutcomeUnavailable, "persistence")
		}
		d.generations[v.Name]++
		d.notifyLocked()
	}
	reservedMark := d.unconfirmed[v.Name]
	if err := d.messageBlock(ctx, v.Name); err != nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "message_binding")
	}
	err := d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		current, exists := legacy[v.Name]
		left, _ := json.Marshal(current)
		right, _ := json.Marshal(v)
		marks, base, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		candidate := marks[v.Name]
		actual, e := legacyApplicationRemovalRevision(current, candidate)
		if !exists || !bytes.Equal(left, right) || candidate != reservedMark || !candidate.Removing || candidate.Question != question || e != nil || actual != candidate.RemovalRevision {
			return errors.New("applications: publication intent changed")
		}
		next := maps.Clone(marks)
		if mark == nil {
			delete(next, v.Name)
		} else {
			candidate.Removing = false
			candidate.RemovalRevision = ""
			candidate.Question = ""
			next[v.Name] = candidate
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		err = cas.ChangeLimitContext(ctx, filepath.Join(d.dir, selfRegisteredFile), d.store.MaxBytes, func(current []byte) ([]byte, error) {
			if (current == nil) != (base.Data == nil) || !bytes.Equal(current, base.Data) {
				return nil, cas.ErrMoved
			}
			return raw, nil
		})
		if err == nil {
			d.unconfirmed = next
			d.selfBase = store.Value{Data: raw}
		}
		return err
	})
	if err != nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "persistence")
	}
	d.notifyLocked()
	d.generations[v.Name]++
	return appChange(wire.ApplicationOutcomeApplied, "")
}

// reserveMessageV3 freezes a different receiver before retiring questions or
// editing Rights. The existing index is the durable reservation; a retry uses
// the unchanged expected revision. No callback runs inside descriptors CAS.
func (d *applicationDirectory) reserveMessageV3(ctx context.Context, name, expected, source string, replacement *applicationV3Source) (bool, error) {
	if d.messageBlock == nil {
		return false, nil
	}
	if !d.lock(ctx) {
		return false, ctx.Err()
	}
	if d.v3 == nil {
		d.unlock()
		return false, errors.New("applications: manifest owner unavailable")
	}
	reserved := false
	err := d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		if _, collision := legacy[name]; collision {
			return errors.New("applications: name conflict")
		}
		entry, exists := d.v3.entries[name]
		if !exists {
			if expected != "" {
				return errors.New("applications: registration changed")
			}
			return nil
		}
		if entry.Revision != expected {
			return errors.New("applications: registration changed")
		}
		if replacement != nil && source == "self" && (entry.Source != "self" || !d.v3.files[name].Source.sameProgram(*replacement)) {
			return errors.New("applications: registration owner changed")
		}
		if entry.State != "removing" && replacement != nil && d.v3.files[name].Source.sameProgram(*replacement) {
			return nil
		}
		if entry.State == "removing" && entry.Source == "self" && !strings.HasPrefix(entry.Question, messageRemovalQuestion) {
			return errors.New("applications: Remove intent reserved")
		}
		if entry.State != "removing" {
			if replacement == nil {
				if err := d.retireAgentUpdateMemberLocked(ctx, name, legacy); err != nil {
					return err
				}
			}
			entry.State = "removing"
			entry.Question = messageRemovalQuestion + entry.Revision
			next := maps.Clone(d.v3.entries)
			next[name] = entry
			if err := d.v3.persistLocked(ctx, next); err != nil {
				return err
			}
		}
		reserved = true
		return d.projectApplicationV3Locked(legacy)
	})
	d.unlock()
	if err != nil {
		return false, err
	}
	if reserved || expected == "" {
		if err := d.messageBlock(ctx, name); err != nil {
			return false, err
		}
	}
	return reserved, nil
}

// reserveMessageLegacyLocked reuses the existing self-mark Remove reservation.
// Its question is a private lifecycle intent, never a person's Keep answer.
func (d *applicationDirectory) reserveMessageLegacyLocked(ctx context.Context, name string) error {
	programKey := identity.ProgramKey(d.descriptors[name].Program)
	reserved := false
	err := d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		v, ok := legacy[name]
		if !ok {
			return errors.New("applications: registration changed")
		}
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		mark := marks[name]
		if mark.Removing {
			actual, err := legacyApplicationRemovalRevision(v, mark)
			if err != nil || !strings.HasPrefix(mark.Question, messageRemovalQuestion) || actual != mark.RemovalRevision {
				return errors.New("applications: Remove intent reserved")
			}
			return nil
		}
		if mark.RegistrationRevision == "" {
			mark.RegistrationRevision, err = applicationToken()
			if err != nil {
				return err
			}
		}
		mark.Program, mark.ProgramKey = v.Program, programKey
		mark.Removing = true
		mark.Question = messageRemovalQuestion + mark.RegistrationRevision
		mark.RemovalRevision, err = legacyApplicationRemovalRevision(v, mark)
		if err != nil {
			return err
		}
		next := maps.Clone(marks)
		next[name] = mark
		if err := d.persistUnconfirmedAtDescriptorLocked(ctx, next, legacy); err != nil {
			return err
		}
		reserved = true
		return nil
	})
	if err == nil && reserved {
		d.generations[name]++
		d.notifyLocked()
	}
	return err
}

func (d *applicationDirectory) removeMessageLegacyLocked(ctx context.Context, name string) error {
	if err := d.reserveMessageLegacyLocked(ctx, name); err != nil {
		return err
	}
	if d.messageBlock != nil {
		if err := d.messageBlock(ctx, name); err != nil {
			return err
		}
	}
	var next map[string]wire.ApplicationDescriptor
	var raw []byte
	err := cas.ChangeLimitContext(ctx, filepath.Join(d.dir, "descriptors.json"), d.store.MaxBytes, func(current []byte) ([]byte, error) {
		legacy, err := decodeLegacyApplications(current)
		if err != nil {
			return nil, err
		}
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return nil, err
		}
		v, ok := legacy[name]
		mark := marks[name]
		actual, e := legacyApplicationRemovalRevision(v, mark)
		if !ok || !mark.Removing || !strings.HasPrefix(mark.Question, messageRemovalQuestion) || e != nil || actual != mark.RemovalRevision {
			return nil, errors.New("applications: Remove intent changed")
		}
		next = maps.Clone(legacy)
		delete(next, name)
		raw, err = json.Marshal(next)
		return raw, err
	})
	if err != nil {
		return err
	}
	d.base = store.Value{Data: raw}
	if err := d.projectRegistrationsLocked(next); err != nil {
		return err
	}
	// An interrupted cleanup leaves only an inert mark for an absent descriptor.
	return d.clearMarkLocked(ctx, name)
}
