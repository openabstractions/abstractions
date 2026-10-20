package main

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/fileid"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

type agentCompanionBackups interface {
	CaptureBackup(context.Context, []byte, string, func() error) ([]byte, error)
	ValidateBackup([]byte) error
	RestoreBackup(context.Context, []byte, []byte, []byte, func() error) error
}

func (d *applicationDirectory) captureAgentCompanionBackup(ctx context.Context, raw []byte, id string, authorize func() error) (json.RawMessage, error) {
	producer, ok := d.companionProducer().(agentCompanionBackups)
	if !ok {
		return nil, errors.New("companion backup owner unavailable")
	}
	backup, err := producer.CaptureBackup(ctx, raw, id, authorize)
	if err != nil {
		return nil, err
	}
	if len(backup) == 0 || len(backup) > 256<<10 || !json.Valid(backup) || producer.ValidateBackup(backup) != nil {
		return nil, errors.New("companion backup unavailable")
	}
	return append(json.RawMessage(nil), backup...), nil
}

func (d *applicationDirectory) agentCaptureAuthority(ctx context.Context, c agentUpdateCapture) error {
	current := d.currentAgentEnrollment(ctx, c.Application)
	if current.Enrollment == nil || current.Enrollment.State != applications.AgentEnrollmentStateEnrolled || current.Enrollment.Revision != c.Revision || !fileid.SameSubjectProgram(current.Enrollment.Program, c.Old.Program) || !updateImageMatches(ctx, c.Old, c.Old.Program) {
		return errors.New("companion capture enrollment changed")
	}
	return nil
}

func (d *applicationDirectory) prepareAgentUndo(ctx context.Context, s rights.Subject, request applications.AgentUpdateRequest) applications.AgentUpdateResult {
	if len(request.Members) != 0 || request.SourceProgram != "" {
		return agentUpdateResult(applications.OutcomeInvalid, "members")
	}
	if !d.agentMaintenance || !agentHex(request.UndoOf, 24) || d.agentQuestions == nil {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	if !d.lock(ctx) {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	var parent, child *applicationAgentUpdate
	err := d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		index := agentUpdateIndex(marks)
		parent = index[request.UndoOf]
		for _, u := range index {
			if u.Request.UndoOf == request.UndoOf {
				child = u
				return nil
			}
		}
		if parent == nil || parent.Request.UndoOf != "" || parent.ReadyAt <= 0 || !committedAgentUpdateCurrent(d, legacy, marks, parent) {
			return errors.New("undo parent changed")
		}
		return nil
	})
	d.unlock()
	if err != nil || parent == nil {
		return agentUpdateResult(applications.OutcomeForbidden, "person_answer")
	}
	action, err := d.agentUpdateUndoAction(parent)
	if err != nil || !samePrograms(action.DecidedBy, s.Program) {
		return agentUpdateResult(applications.OutcomeForbidden, "person_answer")
	}
	if child != nil {
		if !reflect.DeepEqual(child.UndoAction, &action) {
			return agentUpdateResult(applications.OutcomeForbidden, "person_answer")
		}
		return d.agentUpdateView(ctx, child)
	}
	if !updateImageMatches(ctx, parent.Source, parent.Source.Program) {
		return agentUpdateResult(applications.OutcomeConflict, "source_changed")
	}
	id, err := applicationToken()
	if err != nil {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	u := &applicationAgentUpdate{ID: id, Request: applications.AgentUpdateRequest{UndoOf: parent.ID, Members: []applications.AgentUpdateMember{}}, Source: parent.Source, SourceRelease: cloneAgentRelease(parent.SourceRelease), Maintenance: true, UndoAction: &action}
	backups, ok := d.companionProducer().(agentCompanionBackups)
	if !ok {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	for _, prior := range parent.Members {
		if prior.Maintenance == nil || !prior.Maintenance.Kept.Current.provenFor(prior.Old) || len(prior.CompanionsBefore) > 0 && (len(prior.CompanionBackup) == 0 || backups.ValidateBackup(prior.CompanionBackup) != nil) || !updateImageMatches(ctx, prior.Old, prior.Backup) {
			return agentUpdateResult(applications.OutcomeUnavailable, "state")
		}
		capture, err := d.captureAgentMaintenance(ctx, prior.Application, prior.CommittedRevision)
		if err != nil || capture == nil {
			return agentUpdateResult(applications.OutcomeConflict, "registration")
		}
		old := prior.Staged
		old.Program = prior.Old.Program
		stage := prior.Old
		stage.Program = prior.Backup
		// An image-only parent recorded no prior companions; its Ready
		// readback, rebound to the prior image, is the Undo goal.
		previous := prior.CompanionsBefore
		if len(previous) == 0 {
			previous = prior.CompanionsAfter
		}
		goal, err := d.companionProducer().Rebind(ctx, previous, old.Program, stage.Key)
		if err != nil || d.companionProducer().Validate(goal) != nil {
			return agentUpdateResult(applications.OutcomeUnavailable, "state")
		}
		c := agentUpdateCapture{Application: prior.Application, Revision: prior.CommittedRevision, Binding: prior.Binding, Title: prior.Title, Old: old, Staged: stage, Publisher: prior.Publisher, Release: cloneAgentRelease(&prior.Maintenance.Kept.Current), Maintenance: capture, Backup: stage.Program + ".previous", CompanionsBefore: append(json.RawMessage(nil), prior.CompanionsAfter...), CompanionsAfter: append(json.RawMessage(nil), goal...)}
		c.CompanionBackup, err = d.captureAgentCompanionBackup(ctx, c.CompanionsBefore, u.ID, func() error { return d.agentCaptureAuthority(ctx, c) })
		if err != nil {
			return agentUpdateResult(applications.OutcomeUnavailable, "state")
		}
		u.Members = append(u.Members, c)
	}
	if !u.valid() {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	if !d.lock(ctx) {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	err = d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, base, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		index := agentUpdateIndex(marks)
		if !reflect.DeepEqual(index[parent.ID], parent) || !committedAgentUpdateCurrent(d, legacy, marks, parent) || !d.updateRegistrationsCurrent(legacy, marks, u, false) || d.agentOperator(ctx, s) != wire.ApplicationOutcomeApplied {
			return errors.New("undo binding changed")
		}
		next := maps.Clone(marks)
		for _, c := range u.Members {
			m := next[c.Application]
			a := *m.Agent
			a.UpdateReservation = u.ID
			m.Agent = &a
			next[c.Application] = m
		}
		m := next[u.Members[0].Application]
		a := *m.Agent
		a.UpdateHistory = retainAgentUpdate(a.UpdateHistory, a.Update)
		a.Update = u
		m.Agent = &a
		next[u.Members[0].Application] = m
		d.selfBase = base
		return d.persistAgentUpdate(ctx, next, legacy, map[string]bool{u.ID: true, parent.ID: true})
	})
	d.unlock()
	if err != nil {
		return agentUpdateResult(applications.OutcomeConflict, "registration")
	}
	return d.agentUpdateView(ctx, u)
}

// The common producer restores the prior version under the child's current
// Keep. Its complete pre-publication snapshot owns receipt transition custody.
func (d *applicationDirectory) restoreAgentUndo(ctx context.Context, s rights.Subject, u *applicationAgentUpdate) error {
	if u.Request.UndoOf == "" {
		return nil
	}
	producer, ok := d.companionProducer().(agentCompanionBackups)
	if !ok {
		return errors.New("undo producer unavailable")
	}
	var parent *applicationAgentUpdate
	if !d.lock(ctx) {
		return errors.New("undo state unavailable")
	}
	err := d.withRegistrationReadLocked(ctx, func(_ map[string]wire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err == nil {
			parent = agentUpdateIndex(marks)[u.Request.UndoOf]
		}
		return err
	})
	d.unlock()
	if err != nil || parent == nil || len(parent.Members) != len(u.Members) {
		return errors.New("undo parent unavailable")
	}
	authorize := func() error {
		if d.agentOperator(ctx, s) != wire.ApplicationOutcomeApplied || !d.agentMaintenanceProof(ctx, u) {
			return errors.New("undo authority changed")
		}
		if !d.lock(ctx) {
			return errors.New("undo state unavailable")
		}
		defer d.unlock()
		return d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
			marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
			if err != nil {
				return err
			}
			if !committedAgentUpdateCurrent(d, legacy, marks, u) {
				return errors.New("undo current Keep changed")
			}
			return nil
		})
	}
	for i, c := range u.Members {
		if parent.Members[i].Application != c.Application {
			return errors.New("undo member changed")
		}
		if len(parent.Members[i].CompanionBackup) == 0 {
			// The parent changed no recorded companions; only its image moves back.
			if err := authorize(); err != nil {
				return err
			}
			continue
		}
		if err := producer.RestoreBackup(ctx, parent.Members[i].CompanionBackup, c.CompanionBackup, c.CompanionsAfter, authorize); err != nil {
			return err
		}
	}
	return nil
}

func (d *applicationDirectory) agentUndoProof(ctx context.Context, u *applicationAgentUpdate) bool {
	if u.Request.UndoOf == "" || u.UndoAction == nil {
		return false
	}
	if !d.lock(ctx) {
		return false
	}
	var parent *applicationAgentUpdate
	err := d.withRegistrationReadLocked(ctx, func(_ map[string]wire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err == nil {
			parent = agentUpdateIndex(marks)[u.Request.UndoOf]
		}
		return err
	})
	d.unlock()
	if err != nil || parent == nil || parent.ReadyAt <= 0 || parent.Request.UndoOf != "" || len(parent.Members) != len(u.Members) || !reflect.DeepEqual(parent.Source, u.Source) || !reflect.DeepEqual(parent.SourceRelease, u.SourceRelease) {
		return false
	}
	action, err := d.agentUpdateUndoAction(parent)
	if err != nil || !reflect.DeepEqual(&action, u.UndoAction) {
		return false
	}
	for i, c := range u.Members {
		prior := parent.Members[i]
		if prior.Application != c.Application || prior.Binding != c.Binding || prior.Backup != c.Staged.Program || prior.Old.Key != c.Staged.Key || prior.Old.Hash != c.Staged.Hash || prior.Old.Bytes != c.Staged.Bytes || prior.Staged.Key != c.Old.Key || prior.Staged.Hash != c.Old.Hash || prior.Staged.Bytes != c.Old.Bytes || prior.Maintenance == nil || !reflect.DeepEqual(c.Release, &prior.Maintenance.Kept.Current) {
			return false
		}
	}
	return true
}

func strictlyNewerAgentRelease(next, prior *agentNativeRelease) bool {
	if !next.valid() || !prior.valid() || next.Component != prior.Component || next.PublisherKey != prior.PublisherKey || next.Channel != prior.Channel {
		return false
	}
	for i := range prior.Release {
		if next.Release[i] != prior.Release[i] {
			return next.Release[i] > prior.Release[i]
		}
	}
	return false
}

// An unsigned installed copy has no order: different installed bytes are a
// new generation an Undo did not refuse.
func differentInstalledAgentRelease(next, prior *agentNativeRelease) bool {
	return next.proven() && next.Origin == agentReleaseOriginInstalled && prior.proven() && next.ImageHash != prior.ImageHash
}

// Suppression is the retained child's native action evidence. It grants no
// authority and manufactures no answer to an Update question.
func (d *applicationDirectory) agentUndoSuppression(ctx context.Context, request applications.AgentUpdateRequest, source *agentNativeRelease) (string, error) {
	if !d.lock(ctx) {
		return "", errors.New("undo state unavailable")
	}
	defer d.unlock()
	id := ""
	err := d.withRegistrationReadLocked(ctx, func(_ map[string]wire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		for _, u := range agentUpdateIndex(marks) {
			if !u.Committed || u.Request.UndoOf == "" || strictlyNewerAgentRelease(source, u.SourceRelease) || differentInstalledAgentRelease(source, u.SourceRelease) {
				continue
			}
			for _, wanted := range request.Members {
				for _, c := range u.Members {
					m := marks[c.Application]
					if wanted.Application == c.Application && m.Agent != nil && m.Agent.State == "enrolled" && m.Agent.Account == d.owner && m.Agent.Binding == c.Binding && m.RegistrationRevision == c.CommittedRevision && m.ProgramKey == c.Staged.Key {
						id = u.ID
						return nil
					}
				}
			}
		}
		return nil
	})
	return id, err
}
