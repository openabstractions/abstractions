package main

import (
	"context"
	"errors"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
)

// The locator admits only a fresh protected question. The ordinary source,
// staging, policy, publication and Commit owners retain every effect check.
func (d *applicationDirectory) agentReviewReference(ctx context.Context, request applications.AgentUpdateRequest, source agentUpdateImage, release *agentNativeRelease) (*applicationAgentUpdate, error) {
	if !d.lock(ctx) {
		return nil, errors.New("review state unavailable")
	}
	var prior *applicationAgentUpdate
	err := d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		candidate := agentUpdateIndex(marks)[request.ReviewOf]
		if candidate == nil || !d.agentReviewMembersCurrent(legacy, marks, request, candidate) || candidate.Source.Hash != source.Hash || candidate.Source.Bytes != source.Bytes {
			return errors.New("review reference changed")
		}
		if candidate.Request.UndoOf != "" {
			if !candidate.Committed || !agentReleaseSameContent(release, candidate.SourceRelease) {
				return errors.New("review generation differs")
			}
		} else if candidate.Committed || candidate.Source != source {
			return errors.New("review source differs")
		}
		copy := *candidate
		prior = &copy
		return nil
	})
	d.unlock()
	if err != nil {
		return nil, err
	}
	if prior.Request.UndoOf == "" {
		view := d.agentUpdateView(ctx, prior)
		if view.Outcome != applications.OutcomeApplied || view.Reason != "not_now" {
			return nil, errors.New("review reference was not declined")
		}
	}
	return prior, nil
}

func (d *applicationDirectory) agentReviewMembersCurrent(legacy map[string]wire.ApplicationDescriptor, marks map[string]selfMark, request applications.AgentUpdateRequest, prior *applicationAgentUpdate) bool {
	if len(request.Members) == 0 || len(request.Members) > len(prior.Members) {
		return false
	}
	for _, wanted := range request.Members {
		matched := false
		for _, member := range prior.Members {
			if member.Application != wanted.Application {
				continue
			}
			revision, pin := member.Revision, member.Old
			if prior.Request.UndoOf != "" {
				revision, pin = member.CommittedRevision, member.Staged
				pin.Program = member.Old.Program
			}
			v, binding, ok := d.agentEntryLocked(legacy, member.Application)
			mark := marks[member.Application]
			matched = ok && binding == member.Binding && v.Program == pin.Program && wanted.ExpectedRevision == revision && mark.RegistrationRevision == revision && mark.ProgramKey == pin.Key && mark.Agent != nil && mark.Agent.Account == d.owner && mark.Agent.State == "enrolled" && mark.Agent.Binding == member.Binding && !mark.Removing && mark.Agent.Activation == nil
		}
		if !matched {
			return false
		}
	}
	return true
}

func agentReviewMemberSubset(request applications.AgentUpdateRequest, prior *applicationAgentUpdate) bool {
	for _, wanted := range request.Members {
		matched := false
		for _, member := range prior.Members {
			if member.Application == wanted.Application && member.Revision == wanted.ExpectedRevision {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	return len(request.Members) > 0
}
