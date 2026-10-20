//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	logging "github.com/openabstractions/abstraction-logging/go"
	"github.com/openabstractions/abstractions/mcp-gateway/cacheproducer"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
	"golang.org/x/sys/windows"
)

type agentConnectionBatchMember struct {
	party                setup.AgentParty
	program              string
	verify               func(context.Context, string, string) error
	claudeCompanionDrift func(context.Context, string, string) error
	repairCurrent        func(context.Context) error
}

// ownedAgentConnectionUpdates uses the existing configuration owners' positive
// inventory. Every foreign row stays with its current configuration owner.
func ownedAgentConnectionUpdates(ctx context.Context, p *servicePanel, only ...setup.AgentParty) ([]agentConnectionBatchMember, error) {
	return ownedAgentConnectionUpdatesWith(ctx, p, func(ctx context.Context, party setup.AgentParty) (string, string) {
		return setup.EnrolledAgentImageUpdate(ctx, panelMachine(), party)
	}, only...)
}

func ownedAgentConnectionUpdatesWith(ctx context.Context, p *servicePanel, currency func(context.Context, setup.AgentParty) (string, string), only ...setup.AgentParty) ([]agentConnectionBatchMember, error) {
	var members []agentConnectionBatchMember
	var failures []error
	for _, party := range agentHelperParties {
		if !agentConnectionPartySelected(party.party, only) {
			continue
		}
		state, _ := currency(ctx, party.party)
		if state == "unknown" {
			failures = append(failures, fmt.Errorf("%s connection image unavailable", party.name))
			continue
		}
		if state != "update" && !(party.party == setup.AgentClaude && state == "current") {
			continue
		}
		if party.party == setup.AgentOpenCode {
			image, err := setup.HoldEnrolledAgentImage(ctx, panelMachine(), party.party)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			program, key := image.Program(), image.FileKey()
			if err := image.Close(); err != nil {
				failures = append(failures, err)
				continue
			}
			// This update changes only the native-owned image. Its current
			// enrollment receipt supplies custody; vendor configuration is not read.
			members = append(members, agentConnectionBatchMember{party: party.party, program: program, verify: func(ctx context.Context, path, fileKey string) error {
				current, err := setup.HoldEnrolledAgentImage(ctx, panelMachine(), setup.AgentOpenCode)
				if err != nil {
					return err
				}
				defer current.Close()
				if current.Program() != program || current.FileKey() != key || path != program || fileKey != key {
					return errors.New("OpenCode native integration custody changed")
				}
				return current.Check(ctx)
			}})
			continue
		}
		if party.party == setup.AgentCodex {
			c := p.codexConnectionsForPanel()
			view, err := c.read(ctx)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if !view.Owned || view.Registration != "saved" {
				continue
			}
			program := view.Program
			members = append(members, agentConnectionBatchMember{party: party.party, program: program, verify: func(ctx context.Context, path, key string) error {
				current, err := c.read(ctx)
				if err != nil {
					return err
				}
				if !current.Owned || current.Registration != "saved" || current.Program != program || path != program {
					return errors.New("Codex connection changed; preserved")
				}
				return ctx.Err()
			}})
			continue
		}
		c := p.claudeConnectionsForPanel()
		view, err := c.read(ctx)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		seen := map[string]bool{}
		for _, row := range view.Connections {
			if row.Registration != "saved" || row.ProgramKey == "" || seen[row.ProgramKey] {
				continue
			}
			if state == "current" && !(row.Kind == "user" && row.ModState == "update") {
				continue
			}
			seen[row.ProgramKey] = true
			selected := row
			member := agentConnectionBatchMember{party: party.party, program: row.Program, verify: func(ctx context.Context, path, key string) error {
				current, err := c.read(ctx)
				if err != nil {
					return err
				}
				for _, r := range current.Connections {
					if r.Kind == selected.Kind && r.ProjectKey == selected.ProjectKey && r.Name == selected.Name && r.Registration == "saved" && r.Program == path && r.ProgramKey == key && key == selected.ProgramKey {
						return ctx.Err()
					}
				}
				return errors.New("Claude connection changed; preserved")
			}}
			if state == "current" {
				member.claudeCompanionDrift = func(ctx context.Context, path, key string) error {
					current, err := c.read(ctx)
					if err != nil {
						return err
					}
					for _, r := range current.Connections {
						if r.Kind == "user" && r.ProjectKey == selected.ProjectKey && r.Name == selected.Name && r.Registration == "saved" && r.Program == path && r.ProgramKey == key && key == selected.ProgramKey && r.ModState == "update" {
							return ctx.Err()
						}
					}
					return errors.New("Claude companion drift is no longer positively bound")
				}
				verify, drift := member.verify, member.claudeCompanionDrift
				member.repairCurrent = func(ctx context.Context) error {
					return p.repairCurrentClaudeCompanion(ctx, selected.Program, verify, drift)
				}
			}
			members = append(members, member)
		}
	}
	return members, errors.Join(failures...)
}

func agentConnectionPartySelected(party setup.AgentParty, only []setup.AgentParty) bool {
	if len(only) == 0 {
		return true
	}
	for _, selected := range only {
		if selected == "" || selected == party {
			return true
		}
	}
	return false
}

func runAgentConnectionUpdateBatch(ctx context.Context) error {
	err := runAgentConnectionUpdateBatchWithResult(ctx, "", nil)
	reason := recordAgentConnectionRetry(ctx, err, func(party setup.AgentParty) applications.AgentUpdateResult {
		return findRetainedAgentUpdate(ctx, party)
	}, setup.RecordAgentUpdateRetry)
	if reason != "" {
		// The Panel shows only the fixed reason; the raw error stays in the log.
		p := &servicePanel{}
		//unchecked: logging is diagnostic; the retry and its record proceed without it
		_ = p.log.record(ctx, logging.LevelWarn, "connection update stopped", map[string]string{"panel.action": "connection.update_stopped", "reason": reason, "error": err.Error()})
		p.log.close(ctx)
	}
	return err
}

// recordAgentConnectionRetry keeps the background retry's last stop beside the
// approved intents it concerns and returns the recorded reason. Success and
// running apps clear it; the Panel then reads "current" or "restart" from the
// image and the intent alone.
func recordAgentConnectionRetry(ctx context.Context, err error, find func(setup.AgentParty) applications.AgentUpdateResult, record func([]string, string) error) string {
	reason := agentConnectionRetryReason(ctx, err)
	var intents []string
	if reason != "" {
		seen := map[string]bool{}
		for _, party := range agentHelperParties {
			found := find(party.party)
			if found.Outcome == applications.OutcomeApplied && found.Reason == "update_ready" && found.Intent != "" && !seen[found.Intent] {
				seen[found.Intent] = true
				intents = append(intents, found.Intent)
			}
		}
	}
	if len(intents) == 0 {
		reason = ""
	}
	//unchecked: the record only words the Panel's sentence; the retry and the native intent stay authoritative
	_ = record(intents, reason)
	return reason
}

// The fixed reasons the Panel shows as "Update stopped: <reason>.".
const (
	agentStopInUse   = "a connector file is in use by another program"
	agentStopCheck   = "the new connector failed its check"
	agentStopSave    = "OpenAbstractions could not save the update"
	agentStopReview  = "the prepared connector needs a new review"
	agentStopChanged = "a connection changed since the question"
	agentStopOther   = "it could not finish"
)

// agentConnectionRetryReason maps a retry stop to one fixed reason. An empty
// reason means nothing stopped the update apart from apps that still run the
// previous connector.
func agentConnectionRetryReason(ctx context.Context, err error) string {
	var pending *agentConnectionUpdatePending
	var pathErr *fs.PathError
	var linkErr *os.LinkError
	var syscallErr *os.SyscallError
	switch {
	case err == nil, ctx.Err() != nil, errors.As(err, &pending), errors.Is(err, setup.ErrAgentUpdateRestartRequired):
		return ""
	case errors.Is(err, setup.ErrAgentUpdateResumeProofRequired):
		return agentStopReview
	case errors.Is(err, windows.ERROR_SHARING_VIOLATION), errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.STATUS_SHARING_VIOLATION), errors.Is(err, windows.ERROR_ACCESS_DENIED), errors.Is(err, windows.STATUS_ACCESS_DENIED):
		return agentStopInUse
	case errors.Is(err, setup.ErrAgentUpdateDirectoriesDiffer), errors.As(err, &pathErr), errors.As(err, &linkErr), errors.As(err, &syscallErr):
		return agentStopSave
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "differs"), strings.Contains(text, "image changed"), strings.Contains(text, "bytes"):
		return agentStopCheck
	case strings.Contains(text, "changed"):
		return agentStopChanged
	case strings.Contains(text, "refused"), strings.Contains(text, "stopped"), strings.Contains(text, "unavailable"):
		return agentStopSave
	}
	return agentStopOther
}

// agentPublicationStops remembers the last publication stop this process
// logged, so the 10 s retries log "restart required" once per update and
// again only when the stop changes.
var agentPublicationStops struct {
	sync.Mutex
	last string
}

// agentPublicationStopChanged reports whether key differs from the last stop
// logged. An empty key records a publication and resets the memory.
func agentPublicationStopChanged(key string) bool {
	agentPublicationStops.Lock()
	defer agentPublicationStops.Unlock()
	changed := key != "" && key != agentPublicationStops.last
	agentPublicationStops.last = key
	return changed
}

func agentPublicationStopKey(stages []*agentConnectionBatchStage, fields map[string]string) string {
	parts := []string{fields["connection.hosts"], fields["reason"]}
	for _, stage := range stages {
		if stage != nil {
			parts = append(parts, stage.program)
		}
	}
	return strings.Join(parts, "\x00")
}

func runAgentConnectionUpdateBatchFor(ctx context.Context, only setup.AgentParty) (agentHelperUpdateResult, error) {
	return runAgentConnectionUpdateBatchReviewFor(ctx, only, applications.AgentUpdateRequest{})
}

func runAgentConnectionUpdateBatchReviewFor(ctx context.Context, only setup.AgentParty, request applications.AgentUpdateRequest) (agentHelperUpdateResult, error) {
	result := agentHelperUpdateResult{Outcome: "current"}
	err := runAgentConnectionUpdateBatchWithResult(ctx, only, func(value agentHelperUpdateResult) { result = value }, request)
	if agentManualUpdateFinished(result, err) {
		//unchecked: a stale record is ignored once its intent is no longer approved and waiting
		_ = setup.RecordAgentUpdateRetry(nil, "")
	}
	return result, err
}

// agentManualUpdateFinished reports a Panel Update that left nothing pending.
func agentManualUpdateFinished(result agentHelperUpdateResult, err error) bool {
	return err == nil && (result.Outcome == "updated" || result.Outcome == "current")
}

func runAgentConnectionUpdateBatchWithResult(ctx context.Context, only setup.AgentParty, report func(agentHelperUpdateResult), nomination ...applications.AgentUpdateRequest) error {
	request := applications.AgentUpdateRequest{}
	if len(nomination) == 1 {
		request = nomination[0]
	}
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return err
	}
	api := applications.NewApplicationsClient(binding.Transport().WithContext(ctx))
	p := &servicePanel{}
	defer p.log.close(ctx)
	ready := func(result applications.AgentUpdateResult) error {
		if err := p.readyClaudeConnectionUpdate(ctx, result); err != nil {
			return err
		}
		return p.readyCodexConnectionUpdate(ctx, result)
	}
	complete := func(result applications.AgentUpdateResult) error { return postAgentConnectionCompletion(ctx, result) }
	return runAgentConnectionUpdateBatchOwned(ctx, only, report, request, agentConnectionBatchOperations{
		find: api.FindAgentUpdate, prepare: func(request applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
			result, err := api.PrepareAgentUpdate(request)
			if fields := agentConnectionPrepareRefusalFields(request, result, err); fields != nil {
				p.logAction(ctx, "connection.update_refused", fields)
			}
			return result, err
		}, commit: api.CommitAgentUpdate, ready: ready, complete: complete,
		inventory: func(ctx context.Context, parties ...setup.AgentParty) ([]agentConnectionBatchMember, error) {
			return ownedAgentConnectionUpdates(ctx, p, parties...)
		},
		stage: func(ctx context.Context, m agentConnectionBatchMember) (*agentConnectionBatchStage, error) {
			s, err := setup.StageAgentUpdate(ctx, panelMachine(), m.party, m.program, m.verify, m.claudeCompanionDrift)
			if err != nil {
				return nil, err
			}
			return &agentConnectionBatchStage{native: s, previous: s.PreviousEnrollment(), source: s.Source.Program(), program: s.Image.Program(), resume: s.ResumeRequired, confirm: s.ConfirmResume, close: s.Close, discard: s.Discard}, nil
		},
		publish: func(ctx context.Context, stages []*agentConnectionBatchStage) error {
			native := make([]*setup.StagedAgentUpdate, len(stages))
			for i, s := range stages {
				native[i] = s.native
			}
			err := setup.PublishAgentUpdates(ctx, native)
			if err != nil {
				fields := agentConnectionPublicationStopFields(stages, err)
				if agentPublicationStopChanged(agentPublicationStopKey(stages, fields)) {
					p.logAction(ctx, "connection.update_publication_stopped", fields)
				}
			} else {
				agentPublicationStopChanged("")
			}
			return err
		},
	})
}

// agentConnectionPrepareRefusalFields names a Prepare that asked no question,
// for the Panel log; nil for a question, a kept decline or a review. The R14
// refusals left no trace anywhere a person or agent could read.
func agentConnectionPrepareRefusalFields(request applications.AgentUpdateRequest, result applications.AgentUpdateResult, err error) map[string]string {
	if err == nil && result.Outcome == applications.OutcomeApplied && result.Intent != "" {
		return nil
	}
	var members []string
	for _, m := range request.Members {
		members = append(members, m.Application)
	}
	fields := map[string]string{"connection.members": strings.Join(members, ","), "outcome": result.Outcome.String(), "reason": result.Reason}
	if err != nil {
		fields["outcome"], fields["error"] = "error", err.Error()
	}
	return fields
}

func agentConnectionPublicationStopFields(stages []*agentConnectionBatchStage, err error) map[string]string {
	reason := "other"
	switch {
	case errors.Is(err, setup.ErrAgentUpdateRestartRequired):
		reason = "restart_required"
	case errors.Is(err, setup.ErrAgentUpdateResumeProofRequired):
		reason = "resume_proof_required"
	case errors.Is(err, setup.ErrAgentUpdateDirectoriesDiffer):
		reason = "directories_differ"
	}
	seen := map[setup.AgentParty]bool{}
	unknown := false
	for _, stage := range stages {
		matched := false
		if stage != nil {
			for _, party := range agentHelperParties {
				if stage.previous.Application == "oa-"+string(party.party)+"-integration" {
					seen[party.party], matched = true, true
					break
				}
			}
		}
		unknown = unknown || !matched
	}
	var parties []string
	for _, party := range agentHelperParties {
		if seen[party.party] {
			parties = append(parties, string(party.party))
		}
	}
	if unknown || len(parties) == 0 {
		parties = append(parties, "other")
	}
	return map[string]string{"connection.hosts": strings.Join(parties, ","), "reason": reason}
}

type agentConnectionBatchStage struct {
	native          *setup.StagedAgentUpdate
	previous        setup.AgentEnrollmentCapture
	source, program string
	resume          bool
	confirm         func(string, string) error
	close, discard  func() error
}

type agentConnectionBatchOperations struct {
	find, prepare   func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error)
	commit          func(string) (applications.AgentUpdateResult, error)
	inventory       func(context.Context, ...setup.AgentParty) ([]agentConnectionBatchMember, error)
	stage           func(context.Context, agentConnectionBatchMember) (*agentConnectionBatchStage, error)
	publish         func(context.Context, []*agentConnectionBatchStage) error
	ready, complete func(applications.AgentUpdateResult) error
}

func runAgentConnectionUpdateBatchOwned(ctx context.Context, only setup.AgentParty, report func(agentHelperUpdateResult), request applications.AgentUpdateRequest, ops agentConnectionBatchOperations) error {
	find := ops.find
	ready, complete := ops.ready, ops.complete
	var retained []applications.AgentUpdateResult
	seen := map[string]bool{}
	var declined *applications.AgentUpdateResult
	if request.ReviewOf == "" {
		for _, party := range agentHelperParties {
			if !agentConnectionPartySelected(party.party, []setup.AgentParty{only}) {
				continue
			}
			result, _, err := retainedAgentConnectionUpdateResult(party.party, find, ops.commit, ready, complete)
			if err != nil {
				return err
			}
			if report != nil && result.Outcome != "" && result.Outcome != "not_now" {
				report(result.agentHelperUpdateResult)
			}
			retainedReady := result.ready
			if result.pending != nil {
				if report != nil {
					return nil
				}
				retainedReady = result.pending
			}
			if result.declined != nil {
				declined = result.declined
			}
			if retainedReady != nil && !seen[retainedReady.Intent] {
				retained = append(retained, *retainedReady)
				seen[retainedReady.Intent] = true
			}
		}
	}
	if len(retained) != 0 {
		for _, capture := range retained {
			if err := runAgentConnectionUpdateMembers(ctx, only, report, request, ops, &capture, nil); err != nil {
				return err
			}
		}
		return nil
	}
	return runAgentConnectionUpdateMembers(ctx, only, report, request, ops, nil, declined)
}

// Each retained intent keeps its own captured roster and native answer. Drain
// independent approvals through the same publication owner used by one update.
func runAgentConnectionUpdateMembers(ctx context.Context, only setup.AgentParty, report func(agentHelperUpdateResult), request applications.AgentUpdateRequest, ops agentConnectionBatchOperations, retainedReady, declined *applications.AgentUpdateResult) error {
	ready, complete := ops.ready, ops.complete
	parties := []setup.AgentParty{only}
	var err error
	if retainedReady != nil {
		parties, err = agentConnectionRetainedParties(*retainedReady)
		if err != nil {
			return err
		}
	}
	members, err := ops.inventory(ctx, parties...)
	if err != nil {
		return err
	}
	if len(members) == 0 {
		if retainedReady != nil {
			return errors.New("retained connection members are unavailable; preserved")
		}
		return nil
	}
	var stages []*agentConnectionBatchStage
	defer func() {
		for _, s := range stages {
			s.close()
		}
	}()
	resume := false
	var staged []agentConnectionBatchMember
	for _, m := range members {
		s, err := ops.stage(ctx, m)
		if err == setup.ErrAgentImageCurrent {
			if m.repairCurrent != nil {
				if err := m.repairCurrent(ctx); err != nil {
					return err
				}
			}
			continue
		}
		if err != nil {
			return err
		}
		stages = append(stages, s)
		staged = append(staged, m)
		previous := s.previous
		if request.SourceProgram == "" {
			request.SourceProgram = s.source
		}
		request.Members = append(request.Members, applications.AgentUpdateMember{Application: previous.Application, ExpectedRevision: previous.Revision, StagedProgram: s.program})
		resume = resume || s.resume
	}
	if len(stages) == 0 {
		if retainedReady != nil {
			return errors.New("retained connection publication cannot be confirmed; preserved")
		}
		return nil
	}
	if resume && request.ReviewOf == "" && retainedReady == nil {
		var found applications.AgentUpdateResult
		if declined != nil {
			found = *declined
		} else {
			found, err = ops.find(request)
		}
		if err != nil {
			return err
		}
		if found.Intent == "" && found.Outcome != applications.OutcomeUnknown {
			// Only a definite "no such update" frees the copy; a transient
			// lookup failure keeps it for the next run.
			return fmt.Errorf("retained connection update lookup stopped: %s; preserved", found.Reason)
		}
		if found.Intent == "" {
			// An earlier run copied the installed component and stopped before
			// the service recorded its update (owner R14, 2026-11-09: a refused
			// Prepare left every later Update click on this error). That copy
			// carries no question or approval: replace it with a fresh copy and
			// let Prepare ask the person.
			if err := restageOrphanedAgentConnections(ctx, ops, stages, staged, request.Members); err != nil {
				return err
			}
			resume = false
		}
		for _, s := range stages {
			if !resume {
				break
			}
			if !s.resume {
				continue
			}
			matched := false
			for _, image := range found.StagedImages {
				if image.Application == s.previous.Application && s.confirm(image.Program, image.FileKey) == nil {
					matched = true
					break
				}
			}
			if !matched {
				return errors.New("staged connection differs from retained native update; preserved")
			}
		}
	}
	prepared, stopped, err := resolveAgentConnectionUpdate(request, retainedReady, ops.prepare, report)
	if err != nil || stopped {
		return err
	}
	if err := confirmAgentConnectionStageRoster(prepared, stages); err != nil {
		return err
	}
	return finishAgentConnectionUpdateBatchWithResult(ctx, prepared.Intent, ops.commit, func() error { return ops.publish(ctx, stages) }, report, ready, complete)
}

// restageOrphanedAgentConnections discards each resumed staged copy that no
// service-owned update names and stages its member again. The fresh copy is
// verified against the installed component before the Prepare question.
func restageOrphanedAgentConnections(ctx context.Context, ops agentConnectionBatchOperations, stages []*agentConnectionBatchStage, members []agentConnectionBatchMember, request []applications.AgentUpdateMember) error {
	for i, s := range stages {
		if !s.resume {
			continue
		}
		if s.discard == nil {
			return errors.New("existing staged connection has no service-owned update; preserved")
		}
		// discard closes the stage; the placeholder keeps the deferred close from repeating it.
		stages[i] = &agentConnectionBatchStage{close: func() error { return nil }}
		if err := s.discard(); err != nil {
			return fmt.Errorf("existing staged connection could not be replaced: %w", err)
		}
		fresh, err := ops.stage(ctx, members[i])
		if err != nil {
			return err
		}
		stages[i] = fresh
		if fresh.resume || fresh.previous.Application != s.previous.Application {
			return errors.New("existing staged connection could not be replaced; preserved")
		}
		request[i] = applications.AgentUpdateMember{Application: fresh.previous.Application, ExpectedRevision: fresh.previous.Revision, StagedProgram: fresh.program}
	}
	return nil
}

func confirmAgentConnectionStageRoster(prepared applications.AgentUpdateResult, stages []*agentConnectionBatchStage) error {
	if len(prepared.StagedImages) != len(stages) || len(stages) == 0 {
		return errors.New("connection update member roster changed; preserved")
	}
	wanted := map[string]bool{}
	for _, image := range prepared.StagedImages {
		if wanted[image.Application] {
			return errors.New("duplicate native connection member; preserved")
		}
		wanted[image.Application] = true
	}
	seen := map[string]bool{}
	for _, stage := range stages {
		name := stage.previous.Application
		if !wanted[name] || seen[name] {
			return errors.New("connection update member roster changed; preserved")
		}
		seen[name] = true
		if err := confirmAgentConnectionReviewStage(prepared, name, stage.confirm); err != nil {
			return err
		}
	}
	return nil
}

func resolveAgentConnectionUpdate(request applications.AgentUpdateRequest, retained *applications.AgentUpdateResult, prepare func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error), report func(agentHelperUpdateResult)) (applications.AgentUpdateResult, bool, error) {
	if retained != nil {
		return *retained, false, nil
	}
	return prepareAgentConnectionUpdate(request, prepare, report)
}

func agentConnectionRetainedParties(prepared applications.AgentUpdateResult) ([]setup.AgentParty, error) {
	var parties []setup.AgentParty
	seen := map[setup.AgentParty]bool{}
	for _, image := range prepared.StagedImages {
		base := nativecli.AgentApplicationBase(image.Application)
		matched := false
		for _, party := range agentHelperParties {
			if base != "oa-"+string(party.party)+"-integration" {
				continue
			}
			matched = true
			if !seen[party.party] {
				parties = append(parties, party.party)
				seen[party.party] = true
			}
		}
		if !matched {
			return nil, errors.New("retained connection member is unavailable; preserved")
		}
	}
	if len(parties) == 0 {
		return nil, errors.New("retained connection members unavailable; preserved")
	}
	return parties, nil
}

func confirmAgentConnectionReviewStage(prepared applications.AgentUpdateResult, application string, confirm func(string, string) error) error {
	for _, image := range prepared.StagedImages {
		if image.Application == application && confirm(image.Program, image.FileKey) == nil {
			return nil
		}
	}
	return errors.New("review staging differs from the native question capture; preserved")
}

func prepareAgentConnectionUpdate(request applications.AgentUpdateRequest, prepare func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error), report func(agentHelperUpdateResult)) (applications.AgentUpdateResult, bool, error) {
	prepared, err := prepare(request)
	if err != nil {
		return prepared, true, err
	}
	if prepared.Outcome == applications.OutcomeApplied && prepared.Reason == "not_now" {
		if report != nil {
			report(agentConnectionBatchProjection(prepared, "not_now", false))
		}
		return prepared, true, nil
	}
	if prepared.Outcome != applications.OutcomeApplied || prepared.Intent == "" {
		return prepared, true, fmt.Errorf("native connection update refused: %s", prepared.Reason)
	}
	return prepared, false, nil
}

// A button observes the same retained service intent as login. Pending and
// declined intents return their actual state before any new Prepare or staging.
type agentConnectionBatchObservation struct {
	agentHelperUpdateResult
	ready             *applications.AgentUpdateResult
	pending, declined *applications.AgentUpdateResult
}

func retainedAgentConnectionUpdateResult(only setup.AgentParty, find func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error), commit func(string) (applications.AgentUpdateResult, error), ready ...func(applications.AgentUpdateResult) error) (agentConnectionBatchObservation, bool, error) {
	found, err := find(applications.AgentUpdateRequest{Members: []applications.AgentUpdateMember{{Application: "oa-" + string(only) + "-integration"}}})
	if err != nil {
		return agentConnectionBatchObservation{}, false, err
	}
	if found.Outcome == applications.OutcomeUnknown && found.Intent == "" {
		return agentConnectionBatchObservation{}, false, nil
	}
	if found.Outcome != applications.OutcomeApplied || found.Intent == "" {
		return agentConnectionBatchObservation{}, false, fmt.Errorf("retained connection update lookup stopped: %s", found.Reason)
	}
	result, err := commit(found.Intent)
	if err != nil {
		return agentConnectionBatchObservation{}, false, err
	}
	if result.Outcome == applications.OutcomeConflict && result.Reason == "source_changed" {
		return agentConnectionBatchObservation{}, false, nil
	}
	if result.Outcome != applications.OutcomeApplied {
		return agentConnectionBatchObservation{}, false, fmt.Errorf("retained connection update stopped: %s", result.Reason)
	}
	switch result.Reason {
	case "permission_pending":
		return agentConnectionBatchObservation{agentHelperUpdateResult: agentConnectionBatchProjection(result, result.Reason, false), pending: &result}, true, nil
	case "not_now":
		return agentConnectionBatchObservation{agentHelperUpdateResult: agentConnectionBatchProjection(result, result.Reason, false), declined: &result}, true, nil
	case "updated":
		err := finishAgentConnectionReady(found.Intent, result, commit, ready...)
		// Completion can be an Undo child. Inventory must still compare the
		// installed source, whose Prepare supplies the actual suppression intent.
		return agentConnectionBatchObservation{agentHelperUpdateResult: agentConnectionBatchProjection(result, "updated", false)}, false, err
	case "update_ready":
		return agentConnectionBatchObservation{ready: &result}, false, nil
	default:
		return agentConnectionBatchObservation{}, false, fmt.Errorf("retained connection update stopped: %s", result.Reason)
	}
}

func agentConnectionBatchProjection(result applications.AgentUpdateResult, outcome string, restart bool) agentHelperUpdateResult {
	return agentHelperUpdateResult{Outcome: outcome, Intent: result.Intent, Question: result.Question, Reason: result.Reason, RestartRequired: restart}
}

// The current helper remains in place. Its original native Keep, identical
// installed source and positive configuration ownership guard every producer
// write. Producer-owned status proves restoration before one Activity record.
func (p *servicePanel) repairCurrentClaudeCompanion(ctx context.Context, program string, verify, drift func(context.Context, string, string) error) (err error) {
	current, err := setup.HoldCurrentPublishedAgentImage(ctx, panelMachine(), setup.AgentClaude, program, verify)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, current.Close())
		}
	}()
	if drift == nil {
		return errors.New("companion drift has no positive owner")
	}
	if err := drift(ctx, current.Old.Program(), current.Old.FileKey()); err != nil {
		return err
	}
	copies, err := cacheproducer.InstalledCopies(ctx)
	if err != nil {
		return err
	}
	matched, repaired := false, false
	for _, copy := range copies {
		if copy.Owner != cacheproducer.ClaudeProducer {
			continue
		}
		_, marker, err := cacheproducer.ReadClaudeMarker(ctx, copy.Receipt)
		if err != nil || marker == nil {
			return errors.New("current companion ownership unavailable")
		}
		self, err := fileid.ParseStoredFileID(marker.Self)
		if err != nil || !identity.SamePathName(self.Path(), current.Old.Program()) {
			continue
		}
		matched = true
		if err := current.Check(ctx); err != nil {
			return err
		}
		reading, err := copy.Observe(ctx)
		if err != nil {
			return err
		}
		if reading.State == cacheproducer.CopyCurrent {
			continue
		}
		if reading.State != cacheproducer.CopyStale && reading.State != cacheproducer.CopyAbsent {
			return errors.New("current companion changed; preserved")
		}
		if err := copy.ReconcileAuthorized(ctx, current.Check); err != nil {
			return err
		}
		reading, err = copy.Observe(ctx)
		if err != nil || reading.State != cacheproducer.CopyCurrent {
			return errors.New("current companion restoration has no native readback")
		}
		repaired = true
	}
	if !matched {
		return errors.New("current companion has no owned producer receipt")
	}
	if err := current.Check(ctx); err != nil {
		return err
	}
	err = current.Close()
	closed = true
	if err != nil {
		return err
	}
	if repaired {
		p.logAction(ctx, "connection.companion_repaired", map[string]string{"connection.host": "claude"})
	}
	return nil
}

// A published image can outlive the final Commit reply. Discover the owning
// intent before enrollment-gated inventory; only Commit can establish updated.
func resumeAgentConnectionUpdates(find func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error), commit func(string) (applications.AgentUpdateResult, error), ready ...func(applications.AgentUpdateResult) error) error {
	seen := map[string]bool{}
	for _, party := range agentHelperParties {
		found, err := find(applications.AgentUpdateRequest{Members: []applications.AgentUpdateMember{{Application: "oa-" + string(party.party) + "-integration"}}})
		if err != nil {
			return err
		}
		if found.Outcome == applications.OutcomeUnknown && found.Intent == "" {
			continue
		}
		if found.Outcome != applications.OutcomeApplied || found.Intent == "" {
			return fmt.Errorf("retained connection update lookup stopped: %s", found.Reason)
		}
		if seen[found.Intent] {
			continue
		}
		seen[found.Intent] = true
		result, err := commit(found.Intent)
		if err != nil {
			return err
		}
		// A newer runtime can supersede a completed source. Fresh inventory
		// must still prove the current enrollment before preparing that update.
		if result.Outcome == applications.OutcomeConflict && result.Reason == "source_changed" {
			continue
		}
		if result.Outcome != applications.OutcomeApplied {
			return fmt.Errorf("retained connection update stopped: %s", result.Reason)
		}
		switch result.Reason {
		case "updated":
			if err := finishAgentConnectionReady(found.Intent, result, commit, ready...); err != nil {
				return err
			}
		case "update_ready", "permission_pending", "not_now":
		default:
			return fmt.Errorf("retained connection update stopped: %s", result.Reason)
		}
	}
	return nil
}

func finishAgentConnectionUpdateBatch(ctx context.Context, intent string, commit func(string) (applications.AgentUpdateResult, error), publish func() error, ready ...func(applications.AgentUpdateResult) error) error {
	return finishAgentConnectionUpdateBatchWithResult(ctx, intent, commit, publish, nil, ready...)
}

func finishAgentConnectionUpdateBatchWithResult(ctx context.Context, intent string, commit func(string) (applications.AgentUpdateResult, error), publish func() error, report func(agentHelperUpdateResult), ready ...func(applications.AgentUpdateResult) error) error {
	project := func(result applications.AgentUpdateResult, outcome string, restart bool) {
		if report != nil {
			report(agentConnectionBatchProjection(result, outcome, restart))
		}
	}
	for {
		answer, err := commit(intent)
		if err != nil {
			return err
		}
		if answer.Outcome != applications.OutcomeApplied {
			return fmt.Errorf("connection update refused: %s", answer.Reason)
		}
		switch answer.Reason {
		case "not_now":
			project(answer, "not_now", false)
			return nil
		case "updated":
			if err := finishAgentConnectionReady(intent, answer, commit, ready...); err != nil {
				return err
			}
			project(answer, "updated", false)
			return nil
		case "update_ready":
			if err := publish(); err != nil {
				if !errors.Is(err, setup.ErrAgentUpdateRestartRequired) {
					return err
				}
				if report != nil {
					project(answer, "update_pending", true)
					return nil
				}
				return &agentConnectionUpdatePending{Intent: intent, RestartRequired: true, Err: err}
			}
			final, err := commitPublishedAgentUpdate(ctx, intent, commit)
			if err != nil {
				return err
			}
			if final.Outcome != applications.OutcomeApplied || final.Reason != "updated" {
				return fmt.Errorf("connection update pending: %s", final.Reason)
			}
			if err := finishAgentConnectionReady(intent, final, commit, ready...); err != nil {
				return err
			}
			project(final, "updated", false)
			return nil
		case "permission_pending":
			if report != nil {
				project(answer, "permission_pending", false)
				return nil
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		default:
			return fmt.Errorf("connection update stopped: %s", answer.Reason)
		}
	}
}

func finishAgentConnectionReady(intent string, result applications.AgentUpdateResult, commit func(string) (applications.AgentUpdateResult, error), ready ...func(applications.AgentUpdateResult) error) error {
	if result.Phase != nil && *result.Phase == applications.AgentUpdatePhaseReady {
		if result.ReadyUnixMs == nil || result.Intent != intent {
			return errors.New("connection Ready proof incomplete")
		}
		if len(ready) > 1 && ready[1] != nil {
			return ready[1](result)
		}
		return nil
	}
	if len(ready) == 0 || ready[0] == nil {
		return nil
	}
	if err := ready[0](result); err != nil {
		return fmt.Errorf("connection companion update pending (%s): %w", intent, err)
	}
	final, err := commit(intent)
	if err != nil {
		return err
	}
	if final.Outcome != applications.OutcomeApplied || final.Reason != "updated" {
		return fmt.Errorf("connection update readback pending: %s", final.Reason)
	}
	// Terminal presentation follows both the owning companion check and the
	// final native readback. Its retained request uses this exact intent.
	if len(ready) > 1 && ready[1] != nil {
		if final.Intent != intent {
			return errors.New("connection update completion intent changed")
		}
		return ready[1](final)
	}
	return nil
}

func (p *servicePanel) readyClaudeConnectionUpdate(ctx context.Context, result applications.AgentUpdateResult) (err error) {
	for _, entry := range result.Enrollments {
		if entry.Application != "oa-claude-integration" {
			continue
		}
		if entry.State != applications.AgentEnrollmentStateEnrolled {
			return errors.New("Claude native enrollment is not ready")
		}
		native, nativeErr := p.agentEnrollmentForPanel(ctx)
		if nativeErr != nil {
			return nativeErr
		}
		current, currentErr := native.ReadAgentEnrollment(ctx, entry.Application)
		if currentErr != nil {
			return currentErr
		}
		if current.Outcome != applications.OutcomePage || current.Enrollment == nil || current.Enrollment.Revision != entry.Revision || current.Enrollment.Program != entry.Program || current.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
			return errors.New("Claude update enrollment revision changed")
		}
		image, holdErr := setup.HoldEnrolledAgentImageFrom(ctx, panelMachine(), setup.AgentClaude, entry.Program)
		if holdErr != nil {
			return holdErr
		}
		defer func() { err = errors.Join(err, image.Close()) }()
		bound := false
		for _, staged := range result.StagedImages {
			if staged.Application == entry.Application && staged.FileKey == image.FileKey() {
				bound = true
				break
			}
		}
		if !bound {
			return errors.New("Claude companion image differs from approved update")
		}
		c := p.claudeConnectionsForPanel()
		view, readErr := c.read(ctx)
		if readErr != nil {
			return readErr
		}
		return readyClaudeConnectionRows(ctx, c, image, view.Connections)
	}
	return nil
}

func readyClaudeConnectionRows(ctx context.Context, c *claudePanelConnections, image claudePanelImage, rows []claudeSavedConnection) error {
	matched := false
	for _, row := range rows {
		if row.Registration != "saved" || row.Program != image.Program() || row.ProgramKey != image.FileKey() {
			continue
		}
		matched = true
		if row.Kind == "plugin" {
			if err := c.updatePlugin(ctx, row, image); err != nil {
				return err
			}
		} else if row.Kind == "user" && c.userMod {
			switch row.ModState {
			case "current", "absent":
				continue
			case "update":
				if err := c.updateUserMod(ctx, image); err != nil {
					return err
				}
				state, err := c.state()
				if err != nil {
					return err
				}
				if c.readUserModState(ctx, image, state, image.Program()) != "current" {
					return errors.New("Claude loaded companion cache update could not be confirmed")
				}
			default:
				return errors.New("Claude companion ownership or loaded cache is unknown")
			}
		}
	}
	if !matched {
		return errors.New("owned Claude connection changed; companion preserved")
	}
	return image.Check(ctx)
}

func commitPublishedAgentUpdate(ctx context.Context, intent string, commit func(string) (applications.AgentUpdateResult, error)) (applications.AgentUpdateResult, error) {
	var result applications.AgentUpdateResult
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		result, err = commit(intent)
		if err == nil {
			return result, nil
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-timer.C:
		}
	}
	return result, err
}
