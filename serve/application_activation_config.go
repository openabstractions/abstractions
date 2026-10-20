package main

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// A single unfinished conditional edit in the existing enrollment metadata.
// It grants no class: all admission readers refuse until the same edit finishes.
type applicationActivationTransition struct {
	Before wire.ApplicationDescriptor `json:"before"`
	After  wire.ApplicationDescriptor `json:"after"`
}

type activationImageHold interface {
	Recheck(context.Context) error
	Close() error
}

func (t *applicationActivationTransition) valid(program string) bool {
	if t == nil || !validApplicationDescriptor(t.Before) || !validApplicationDescriptor(t.After) || t.Before.Program != program {
		return false
	}
	next := t.Before
	next.Activation = t.After.Activation
	return reflect.DeepEqual(next, t.After)
}

func activationDescriptorBinding(v wire.ApplicationDescriptor) string {
	raw, _ := json.Marshal(struct {
		Descriptor wire.ApplicationDescriptor
		Revision   string
	}{Descriptor: v})
	return applicationV3Hash(raw)
}

func (d *applicationDirectory) setActivation(ctx context.Context, subject rights.Subject, request applications.ActivationConfiguration, recheck func() error) wire.ApplicationChange {
	return d.setActivationConditional(ctx, subject, request, recheck, nil, nil)
}

// Reconciliation derives intent from the existing native WAL. The serialized
// setter below requires that exact marker and observed descriptor to remain.
func (d *applicationDirectory) reconcileActivation(ctx context.Context, subject rights.Subject, expected wire.ApplicationDescriptor, recheck func() error) wire.ApplicationChange {
	if !validApplicationDescriptor(expected) {
		return appChange(wire.ApplicationOutcomeInvalid, "descriptor")
	}
	if out := d.agentOperator(ctx, subject); out != wire.ApplicationOutcomeApplied {
		return appChange(out, "")
	}
	if recheck == nil {
		recheck = func() error { return ctx.Err() }
	}
	if err := recheck(); err != nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "caller")
	}
	if !d.lock(ctx) {
		return appChange(wire.ApplicationOutcomeUnavailable, "")
	}
	var transition *applicationActivationTransition
	out := appChange(wire.ApplicationOutcomeConflict, "descriptor")
	err := d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		if d.v3 != nil {
			if _, ok := d.v3.entries[expected.Name]; ok {
				out = appChange(wire.ApplicationOutcomeForbidden, "signed_manifest")
				return nil
			}
		}
		actual, ok := legacy[expected.Name]
		if !ok {
			out = appChange(wire.ApplicationOutcomeUnknown, "")
			return nil
		}
		if !reflect.DeepEqual(actual, expected) {
			return nil
		}
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		mark := marks[expected.Name]
		if mark.Agent == nil || mark.Agent.Activation == nil {
			out = appChange(wire.ApplicationOutcomeConflict, "activation_not_pending")
			return nil
		}
		if !mark.Agent.Activation.valid(mark.Program) {
			return errors.New("activation: invalid transition")
		}
		copy := *mark.Agent.Activation
		transition = &copy
		return nil
	})
	d.unlock()
	if err != nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "state")
	}
	if transition == nil {
		return out
	}
	request := applications.ActivationConfiguration{Expected: transition.Before, Activation: transition.After.Activation}
	return d.setActivationConditional(ctx, subject, request, recheck, transition, &expected)
}

func (d *applicationDirectory) setActivationConditional(ctx context.Context, subject rights.Subject, request applications.ActivationConfiguration, recheck func() error, resume *applicationActivationTransition, observed *wire.ApplicationDescriptor) wire.ApplicationChange {
	if !validApplicationDescriptor(request.Expected) {
		return appChange(wire.ApplicationOutcomeInvalid, "descriptor")
	}
	next := request.Expected
	next.Activation = request.Activation
	if !validApplicationDescriptor(next) {
		return appChange(wire.ApplicationOutcomeInvalid, "activation")
	}
	if out := d.agentOperator(ctx, subject); out != wire.ApplicationOutcomeApplied {
		return appChange(out, "")
	}
	if recheck == nil {
		recheck = func() error { return ctx.Err() }
	}
	if err := recheck(); err != nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "caller")
	}
	// Capture the existing mark, current native image and original person answer
	// outside the storage callback. The serialized edit fences every captured fact.
	if !d.lock(ctx) {
		return appChange(wire.ApplicationOutcomeUnavailable, "")
	}
	var mark selfMark
	manifest := false
	err := d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if d.v3 != nil {
			_, manifest = d.v3.entries[request.Expected.Name]
		}
		if err == nil {
			mark = marks[request.Expected.Name]
		}
		return err
	})
	d.unlock()
	if err != nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "state")
	}
	if manifest {
		return appChange(wire.ApplicationOutcomeForbidden, "signed_manifest")
	}
	if mark.Removing || mark.Kind != "agent" || mark.Agent == nil || mark.Agent.State != "enrolled" || mark.Agent.Account != d.owner || mark.Program != next.Program {
		return appChange(wire.ApplicationOutcomeConflict, "enrollment")
	}
	image, err := d.currentAgentImage(ctx, next.Name)
	if err != nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "native_identity")
	}
	if image.Program != mark.Program || image.Key != mark.ProgramKey {
		return appChange(wire.ApplicationOutcomeConflict, "native_identity")
	}
	held, err := holdActivationImage(ctx, image)
	if err != nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "native_identity")
	}
	defer held.Close()
	callerRecheck := recheck
	recheck = func() error {
		if err := callerRecheck(); err != nil {
			return err
		}
		if d.agentOperator(ctx, subject) != wire.ApplicationOutcomeApplied {
			return errors.New("activation: operator authority changed")
		}
		return held.Recheck(ctx)
	}
	if d.agentQuestions == nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "person_answer")
	}
	answer, found, err := d.agentQuestions.a.book.ApplicationRecord(mark.Question)
	if err != nil {
		return appChange(wire.ApplicationOutcomeUnavailable, "person_answer")
	}
	if !found || !agentPersonAnswer(mark, answer) {
		return appChange(wire.ApplicationOutcomeConflict, "enrollment")
	}
	if !d.lock(ctx) {
		return appChange(wire.ApplicationOutcomeUnavailable, "")
	}
	defer d.unlock()
	out := appChange(wire.ApplicationOutcomeConflict, "descriptor")
	beforeBinding, afterBinding := activationDescriptorBinding(request.Expected), activationDescriptorBinding(next)
	transition := &applicationActivationTransition{Before: request.Expected, After: next}
	prepared := false
	err = d.withRegistrationLegacyEditLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		if d.v3 != nil {
			if _, ok := d.v3.entries[next.Name]; ok {
				out = appChange(wire.ApplicationOutcomeForbidden, "signed_manifest")
				return nil
			}
		}
		actual, ok := legacy[next.Name]
		if !ok {
			out = appChange(wire.ApplicationOutcomeUnknown, "")
			return nil
		}
		marks, base, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		current := marks[next.Name]
		// A captured recovery request can never fall back to creating a marker.
		// Both checks run under the same writer as the eventual descriptor edit.
		if resume != nil && (current.Agent == nil || !reflect.DeepEqual(current.Agent.Activation, resume) || observed == nil || !reflect.DeepEqual(actual, *observed)) {
			out = appChange(wire.ApplicationOutcomeConflict, "activation")
			return nil
		}
		if !reflect.DeepEqual(current, mark) || image.Binding != activationDescriptorBinding(actual) {
			out = appChange(wire.ApplicationOutcomeConflict, "enrollment")
			return nil
		}
		if err := recheck(); err != nil {
			return err
		}
		if current.Agent.Activation == nil && current.Agent.Binding == afterBinding && reflect.DeepEqual(actual, next) {
			out = appChange(wire.ApplicationOutcomeApplied, "")
			return nil
		}
		if current.Agent.Activation != nil {
			if !reflect.DeepEqual(current.Agent.Activation, transition) || current.Agent.Binding != beforeBinding || (!reflect.DeepEqual(actual, request.Expected) && !reflect.DeepEqual(actual, next)) {
				return nil
			}
		} else {
			if current.Agent.Binding != beforeBinding || !reflect.DeepEqual(actual, request.Expected) {
				return nil
			}
			a := *current.Agent
			current.Agent = &a
			a.Activation = transition
			marks = maps.Clone(marks)
			marks[next.Name] = current
			d.selfBase = base
			if err = d.persistUnconfirmedAtDescriptorLocked(ctx, marks, legacy); err != nil {
				return err
			}
		}
		if err := recheck(); err != nil {
			return err
		}
		if d.activationConfigFault != nil {
			if err := d.activationConfigFault("prepared"); err != nil {
				return err
			}
		}
		legacy[next.Name] = next
		prepared = true
		return nil
	}, nil, false, true)
	if err != nil {
		d.notifyLocked()
		return appChange(wire.ApplicationOutcomeUnavailable, "state")
	}
	if !prepared {
		return out
	}
	if d.activationConfigFault != nil {
		if err := d.activationConfigFault("descriptor"); err != nil {
			d.notifyLocked()
			return appChange(wire.ApplicationOutcomeUnavailable, "state")
		}
	}
	err = d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		if !reflect.DeepEqual(legacy[next.Name], next) {
			return errors.New("activation: descriptor changed")
		}
		marks, base, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		current := marks[next.Name]
		if current.Removing || current.Agent == nil || !reflect.DeepEqual(current.Agent.Activation, transition) || current.RegistrationRevision != mark.RegistrationRevision || current.ProgramKey != image.Key || current.Question != mark.Question {
			return errors.New("activation: enrollment changed")
		}
		if err := recheck(); err != nil {
			return err
		}
		a := *current.Agent
		current.Agent = &a
		a.Activation = nil
		a.Binding = afterBinding
		marks = maps.Clone(marks)
		marks[next.Name] = current
		d.selfBase = base
		return d.persistUnconfirmedAtDescriptorLocked(ctx, marks, legacy)
	})
	if err != nil {
		d.notifyLocked()
		return appChange(wire.ApplicationOutcomeUnavailable, "state")
	}
	// Replace immutable admission snapshots and inbox rows. A watcher which
	// checked the preceding binding can retire only its preceding row, as with
	// ordinary inbox renewal. Public instance/revision and native pin stay live.
	rebase := func(a *applicationAgentAdmission) *applicationAgentAdmission {
		if a != nil && a.Enrollment.Application == next.Name && a.Enrollment.Revision == mark.RegistrationRevision && a.Image.Key == image.Key && a.Image.Binding == beforeBinding {
			copy := *a
			copy.Image.Binding = afterBinding
			return &copy
		}
		return a
	}
	for key, lease := range d.instances {
		lease.agentAdmission = rebase(lease.agentAdmission)
		d.instances[key] = lease
	}
	if d.nativeInboxes != nil {
		for key, row := range d.nativeInboxes.rows {
			copy := *row
			copy.admission = rebase(row.admission)
			if copy.admission != row.admission {
				d.nativeInboxes.rows[key] = &copy
			}
		}
	}
	d.notifyLocked()
	return appChange(wire.ApplicationOutcomeApplied, "")
}
