package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"io"
	"net/http"
	"runtime"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type agentHelperUpdateResult struct {
	Outcome         string `json:"outcome"`
	Enrollment      string `json:"enrollment"`
	Backup          string `json:"backup,omitempty"`
	RestartRequired bool   `json:"restart_required,omitempty"`
	Intent          string `json:"intent,omitempty"`
	Question        string `json:"question,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// The callback observes positive C1 ownership again; it never changes vendor configuration.
type agentHelperUpdater func(context.Context, func(context.Context, string, string) error) (claudePanelImage, bool, string, error)
type agentHelperUpdaterFrom func(context.Context, string, func(context.Context, string, string) error) (claudePanelImage, bool, string, error)

func selectedAgentUpdater(update agentHelperUpdater, from agentHelperUpdaterFrom, program string) agentHelperUpdater {
	if from == nil {
		return update
	}
	return func(ctx context.Context, verify func(context.Context, string, string) error) (claudePanelImage, bool, string, error) {
		return from(ctx, program, verify)
	}
}

func (p *servicePanel) updateAgentHelper(ctx context.Context, party setup.AgentParty, verify func(context.Context, string, string) error) (claudePanelImage, bool, string, error) {
	return p.updateAgentHelperFrom(ctx, party, "", verify)
}

func finishAgentHelperUpdate(ctx context.Context, update agentHelperUpdater, verify func(context.Context, string, string) error, enroll func(context.Context, claudePanelImage) (string, error), admit func(context.Context, claudePanelImage) (claudePanelImage, error)) (result agentHelperUpdateResult, err error) {
	return finishAgentHelperUpdateWithReady(ctx, update, verify, enroll, admit, nil)
}

func finishAgentHelperUpdateWithReady(ctx context.Context, update agentHelperUpdater, verify func(context.Context, string, string) error, enroll func(context.Context, claudePanelImage) (string, error), admit func(context.Context, claudePanelImage) (claudePanelImage, error), ready func(context.Context, claudePanelImage) error) (result agentHelperUpdateResult, err error) {
	var current string
	defer func() {
		if err != nil {
			result.Outcome = ""
			result.Enrollment = ""
			if result.Backup != "" {
				err = fmt.Errorf("integration update requires inspection; previous image retained at %s; current image path %s: %w", result.Backup, current, err)
			}
		}
	}()
	if update == nil || enroll == nil || admit == nil {
		return result, errors.New("integration update unavailable")
	}
	image, changed, backup, err := update(ctx, verify)
	result.Backup = backup
	if err != nil {
		return result, err
	}
	if image == nil {
		return result, errors.New("updated integration unavailable")
	}
	defer func() { err = errors.Join(err, image.Close()) }()
	current = image.Program()
	if !changed {
		result.Outcome = "current"
		result.Enrollment = "enrolled"
		if err := image.Check(ctx); err != nil {
			return result, err
		}
		if ready != nil {
			held, admitErr := admit(ctx, image)
			if admitErr != nil {
				return result, admitErr
			}
			if held == nil {
				return result, errors.New("current native enrollment unavailable")
			}
			defer func() { err = errors.Join(err, held.Close()) }()
			if held.FileKey() != image.FileKey() {
				return result, errors.New("current integration admission changed")
			}
			if err := held.Check(ctx); err != nil {
				return result, err
			}
			return result, ready(ctx, held)
		}
		return result, nil
	}
	// The owning Applications service creates the fresh Keep occurrence.
	state, err := enroll(ctx, image)
	result.Enrollment = state
	if err != nil {
		return result, err
	}
	if state != "enrolled" {
		result.Outcome = "permission_pending"
		return result, ctx.Err()
	}
	held, err := admit(ctx, image)
	if err != nil {
		return result, err
	}
	if held == nil {
		return result, errors.New("updated native enrollment unavailable")
	}
	defer func() { err = errors.Join(err, held.Close()) }()
	if held.FileKey() != image.FileKey() {
		return result, errors.New("integration changed during Keep")
	}
	result.Outcome = "updated"
	if err := errors.Join(image.Check(ctx), held.Check(ctx), ctx.Err()); err != nil {
		return result, err
	}
	if ready != nil {
		return result, ready(ctx, held)
	}
	return result, nil
}

func (c *codexPanelConnections) updateHelper(ctx context.Context, revision string) (agentHelperUpdateResult, error) {
	if !c.action.TryLock() {
		return agentHelperUpdateResult{}, errors.New("Codex setup is in progress")
	}
	defer c.action.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	snapshot := c.current
	if c.closed || revision != fmt.Sprint(c.revision) || !snapshot.Owned || snapshot.Registration != "saved" {
		c.mu.Unlock()
		return agentHelperUpdateResult{}, errors.New("owned Codex connection changed; check again")
	}
	c.cancel = cancel
	c.mu.Unlock()
	if c.image != nil {
		if err := c.image.Close(); err != nil {
			return agentHelperUpdateResult{}, err
		}
		c.image = nil
	}
	verify := func(ctx context.Context, program, key string) error {
		view, err := c.read(ctx)
		if err != nil {
			return err
		}
		if !view.Owned || view.Registration != "saved" || view.Program != program || snapshot.Program != program {
			return errors.New("Codex connection ownership changed; preserved")
		}
		return ctx.Err()
	}
	update := selectedAgentUpdater(c.update, c.updateFrom, snapshot.Program)
	if runtime.GOOS != "darwin" {
		return finishAgentHelperUpdate(ctx, update, verify, c.enroll, c.admit)
	}
	if snapshot.Previous != nil && c.retire != nil {
		if err := c.retire(ctx, *snapshot.Previous); err != nil {
			return agentHelperUpdateResult{Outcome: "cleanup_pending", Backup: snapshot.Previous.Program}, nil
		}
	}
	var prior setup.AgentEnrollmentCapture
	bound := func(ctx context.Context, verify func(context.Context, string, string) error) (claudePanelImage, bool, string, error) {
		image, changed, backup, err := update(ctx, verify)
		if capture, ok := image.(interface {
			PreviousEnrollment() setup.AgentEnrollmentCapture
		}); ok {
			prior = capture.PreviousEnrollment()
		}
		return image, changed, backup, err
	}
	cleanupPending := false
	result, err := finishAgentHelperUpdateWithReady(ctx, bound, verify, c.enroll, c.admit, func(ctx context.Context, image claudePanelImage) error {
		if image.Program() == snapshot.Program {
			return nil
		}
		if err := c.transitionHelper(ctx, snapshot.Program, image, prior); err != nil {
			return err
		}
		if c.retire == nil || c.retire(ctx, prior) != nil {
			cleanupPending = true
		}
		return nil
	})
	if err == nil && cleanupPending {
		result.Outcome = "cleanup_pending"
	}
	return result, err
}

func (c *claudePanelConnections) updateHelper(ctx context.Context, request claudeDisconnectRequest) (agentHelperUpdateResult, error) {
	if !c.actionMu.TryLock() {
		return agentHelperUpdateResult{}, errors.New("Claude setup is in progress")
	}
	defer c.actionMu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	if c.closed || request.Revision != fmt.Sprint(c.revision) {
		c.mu.Unlock()
		return agentHelperUpdateResult{}, errors.New("saved connection changed; check again")
	}
	var selected *claudeSavedConnection
	for _, row := range c.rows {
		if row.Kind == request.Kind && row.ProjectKey == request.ProjectKey && row.ProgramKey == request.ProgramKey && row.Name == request.Name && row.Registration == "saved" {
			copy := row
			selected = &copy
			break
		}
	}
	c.activeCancel = cancel
	c.mu.Unlock()
	if selected == nil {
		return agentHelperUpdateResult{}, errors.New("owned Claude connection unavailable")
	}
	if runtime.GOOS == "darwin" && selected.Previous != nil && c.retire != nil {
		if err := c.retire(ctx, *selected.Previous); err != nil {
			return agentHelperUpdateResult{Outcome: "cleanup_pending", Backup: selected.Previous.Program}, nil
		}
	}
	if c.heldImage != nil {
		if err := c.heldImage.Close(); err != nil {
			return agentHelperUpdateResult{}, err
		}
		c.heldImage = nil
	}
	verify := func(ctx context.Context, program, key string) error {
		view, err := c.read(ctx)
		if err != nil {
			return err
		}
		for _, row := range view.Connections {
			if row.Kind == selected.Kind && row.ProjectKey == selected.ProjectKey && row.Name == selected.Name && row.Program == program && row.ProgramKey == key && row.ProgramKey == selected.ProgramKey && row.Registration == "saved" {
				return ctx.Err()
			}
		}
		return errors.New("Claude connection ownership changed; preserved")
	}
	if runtime.GOOS != "darwin" {
		var ready func(context.Context, claudePanelImage) error
		if selected.Kind == "plugin" {
			ready = func(ctx context.Context, image claudePanelImage) error { return c.updatePlugin(ctx, *selected, image) }
		} else if selected.Kind == "user" && c.userMod {
			ready = c.updateUserMod
		}
		return finishAgentHelperUpdateWithReady(ctx, selectedAgentUpdater(c.update, c.updateFrom, selected.Program), verify, c.enroll, c.admit, ready)
	}
	var prior setup.AgentEnrollmentCapture
	update := selectedAgentUpdater(c.update, c.updateFrom, selected.Program)
	bound := func(ctx context.Context, verify func(context.Context, string, string) error) (claudePanelImage, bool, string, error) {
		image, changed, backup, err := update(ctx, verify)
		if capture, ok := image.(interface {
			PreviousEnrollment() setup.AgentEnrollmentCapture
		}); ok {
			prior = capture.PreviousEnrollment()
		}
		return image, changed, backup, err
	}
	cleanupPending := false
	ready := func(ctx context.Context, image claudePanelImage) error {
		if image.Program() != selected.Program {
			if selected.Kind != "user" {
				return errors.New("immutable helper transition requires the explicit user connection; project/plugin entry preserved")
			}
			if err := c.transitionHelper(ctx, *selected, image, prior); err != nil {
				return err
			}
			if c.userMod {
				if err := c.updateUserMod(ctx, image); err != nil {
					return err
				}
			}
			if c.retire == nil || c.retire(ctx, prior) != nil {
				cleanupPending = true
			}
			return nil
		}
		if selected.Kind == "plugin" {
			return c.updatePlugin(ctx, *selected, image)
		}
		if selected.Kind == "user" && c.userMod {
			return c.updateUserMod(ctx, image)
		}
		return nil
	}
	result, err := finishAgentHelperUpdateWithReady(ctx, bound, verify, c.enroll, c.admit, ready)
	if err == nil && cleanupPending {
		result.Outcome = "cleanup_pending"
	}
	return result, err
}

func (p *servicePanel) agentHelperUpdateRoutes(mux *http.ServeMux, key string) {
	p.agentHelperUpdateRoutesWithBatch(mux, key, runAgentConnectionUpdateBatchFor, runAgentConnectionUpdateBatchReviewFor)
}

func (p *servicePanel) agentHelperUpdateRoutesWithBatch(mux *http.ServeMux, key string, batch func(context.Context, setup.AgentParty) (agentHelperUpdateResult, error), review func(context.Context, setup.AgentParty, applications.AgentUpdateRequest) (agentHelperUpdateResult, error)) {
	for _, party := range []string{"codex", "claude"} {
		mux.HandleFunc("/connections/"+party+"/update", guard(key, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "POST required", 405)
				return
			}
			var edit claudeDisconnectRequest
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
			d.DisallowUnknownFields()
			if d.Decode(&edit) != nil || d.Decode(new(any)) != io.EOF {
				http.Error(w, "invalid update request", 400)
				return
			}
			var result agentHelperUpdateResult
			var err error
			if runtime.GOOS == "windows" {
				result, err = batch(r.Context(), setup.AgentParty(party))
				if err == nil && result.Outcome == "not_now" && result.Intent != "" {
					result, err = review(r.Context(), setup.AgentParty(party), applications.AgentUpdateRequest{ReviewOf: result.Intent})
				}
			} else if party == "codex" {
				result, err = p.codexConnectionsForPanel().updateHelper(r.Context(), edit.Revision)
			} else {
				result, err = p.claudeConnectionsForPanel().updateHelper(r.Context(), edit)
			}
			if err != nil {
				panelError(w, err)
				return
			}
			panelJSON(w, result)
		}))
	}
}
