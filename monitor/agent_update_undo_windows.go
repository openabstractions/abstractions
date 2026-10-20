package main

import (
	"context"
	"errors"
	"strings"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func activateAgentUpdateNotification(ctx context.Context, record askclient.NotificationRecord, action string) error {
	const prefix = "oa-agent-update-"
	if action != "undo" || !strings.HasPrefix(record.Request.RequestRef.Key, prefix) {
		return nil
	}
	parent := strings.TrimPrefix(record.Request.RequestRef.Key, prefix)
	if len(parent) != 48 {
		return errors.New("connection Undo reference invalid")
	}
	// Native Act has already authenticated the person. The durable action owns
	// this bounded operation; every service/producer boundary rechecks Keep.
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	return runAgentConnectionUndo(work, parent)
}

func runAgentConnectionUndo(ctx context.Context, parent string) (err error) {
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return err
	}
	api := applications.NewApplicationsClient(binding.Transport().WithContext(ctx))
	child, err := api.PrepareAgentUpdate(applications.AgentUpdateRequest{Members: []applications.AgentUpdateMember{}, UndoOf: parent})
	if err != nil {
		return err
	}
	if child.Outcome != applications.OutcomeApplied || child.Intent == "" || child.Question != "" {
		return errors.New("connection Undo unavailable: " + child.Reason)
	}
	current, err := api.CommitAgentUpdate(child.Intent)
	if err != nil {
		return err
	}
	if current.Outcome != applications.OutcomeApplied {
		return errors.New("connection Undo stopped: " + current.Reason)
	}
	if current.Reason == "updated" {
		return finishAgentConnectionReady(child.Intent, current, api.CommitAgentUpdate, nil, func(result applications.AgentUpdateResult) error { return postAgentConnectionCompletion(ctx, result) })
	}
	if current.Reason != "update_ready" {
		return errors.New("connection Undo is not publishable")
	}
	var stages []*setup.StagedAgentUpdate
	defer func() {
		for _, stage := range stages {
			err = errors.Join(err, stage.Close())
		}
	}()
	for _, capture := range child.StagedImages {
		var party setup.AgentParty
		for _, known := range agentHelperParties {
			if capture.Application == "oa-"+string(known.party)+"-integration" {
				party = known.party
			}
		}
		if party == "" {
			return errors.New("connection Undo member unknown")
		}
		read, e := api.ReadAgentEnrollment(capture.Application)
		if e != nil {
			return e
		}
		if read.Enrollment == nil || read.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
			return errors.New("connection Undo enrollment changed")
		}
		entry := *read.Enrollment
		verify := func(ctx context.Context, program, key string) error {
			now, e := api.ReadAgentEnrollment(entry.Application)
			if e != nil {
				return e
			}
			if now.Enrollment == nil || now.Enrollment.State != applications.AgentEnrollmentStateEnrolled || now.Enrollment.Revision != entry.Revision || now.Enrollment.Program != entry.Program || program != entry.Program {
				return errors.New("connection Undo Keep changed")
			}
			// Recheck the service's exact installed-source and retained-action
			// capture while the native publisher holds all current/staged objects.
			bound, e := api.CommitAgentUpdate(child.Intent)
			if e != nil {
				return e
			}
			if bound.Outcome != applications.OutcomeApplied || bound.Reason != "update_ready" || bound.Intent != child.Intent {
				return errors.New("connection Undo source or action changed")
			}
			return ctx.Err()
		}
		stage, e := setup.HoldRetainedAgentUpdate(ctx, panelMachine(), party, entry.Program, capture.Program, capture.FileKey, verify)
		if e != nil {
			return e
		}
		stages = append(stages, stage)
	}
	if len(stages) == 0 {
		return errors.New("connection Undo has no retained members")
	}
	return finishAgentConnectionUpdateBatch(ctx, child.Intent, api.CommitAgentUpdate, func() error { return setup.PublishAgentUpdates(ctx, stages) }, nil, func(result applications.AgentUpdateResult) error { return postAgentConnectionCompletion(ctx, result) })
}
