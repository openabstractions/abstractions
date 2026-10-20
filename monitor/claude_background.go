package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

type claudeBackgroundState struct {
	Outcome   string `json:"outcome"`
	State     string `json:"state"`
	Preserved bool   `json:"preserved,omitempty"`
}

func (c *claudePanelConnections) watchPermission(ctx context.Context, action string, manager claudePanelImage, cli claudePanelCLI, state string) (result claudeBackgroundState, err error) {
	return c.watchPermissionScope(ctx, action, manager, cli, state, "messages")
}
func (c *claudePanelConnections) watchPermissionScope(ctx context.Context, action string, manager claudePanelImage, cli claudePanelCLI, state, scope string) (result claudeBackgroundState, err error) {
	if action != "status" && action != "enable" && action != "disable" {
		return result, errors.New("background message action unavailable")
	}
	if err := errors.Join(manager.Check(ctx), cli.Check(ctx)); err != nil {
		return result, err
	}
	args := []string{"watch-permission", "claude", action, "--state-dir", state, "--claude", cli.Program()}
	if scope == "asks" {
		args = append(args, "--scope", "asks")
	} else if scope != "messages" {
		return result, errors.New("background scope unavailable")
	}
	raw, err := c.command(ctx, manager.Program(), args...)
	if err != nil {
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 4096 || decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF || result.Outcome != "page" {
		return result, errors.New("background message settings could not be confirmed")
	}
	switch result.State {
	case "unreviewed", "owned", "preexisting", "disabled":
	default:
		return result, errors.New("background message settings are unknown")
	}
	return result, errors.Join(manager.Check(ctx), cli.Check(ctx), ctx.Err())
}

func (c *claudePanelConnections) readBackground(ctx context.Context) (result claudeBackgroundState) {
	return c.readBackgroundScope(ctx, "messages")
}
func (c *claudePanelConnections) readBackgroundScope(ctx context.Context, scope string) (result claudeBackgroundState) {
	result = claudeBackgroundState{Outcome: "page", State: "unknown"}
	if c.image == nil || c.cli == nil || c.state == nil || c.command == nil {
		return result
	}
	manager, err := c.image(ctx)
	if err != nil || manager == nil {
		return result
	}
	defer func() {
		if manager.Close() != nil {
			result.State = "unknown"
		}
	}()
	cli, err := c.cli(ctx)
	if err != nil || cli == nil {
		return result
	}
	defer func() {
		if cli.Close() != nil {
			result.State = "unknown"
		}
	}()
	state, err := c.state()
	if err != nil {
		return result
	}
	observed, err := c.watchPermissionScope(ctx, "status", manager, cli, state, scope)
	if err == nil {
		result = observed
	}
	return result
}

func (c *claudePanelConnections) setBackground(ctx context.Context, revision, action string) (result claudeBackgroundState, err error) {
	return c.setBackgroundScope(ctx, revision, action, "messages")
}
func (c *claudePanelConnections) setBackgroundScope(ctx context.Context, revision, action, scope string) (result claudeBackgroundState, err error) {
	if scope != "messages" && scope != "asks" {
		return result, errors.New("background scope unavailable")
	}
	if action != "enable" && action != "disable" && action != "reset" {
		return result, errors.New("background message action unavailable")
	}
	if !c.actionMu.TryLock() {
		return result, errors.New("connection setup is already in progress")
	}
	defer c.actionMu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	if c.closed || revision != fmt.Sprint(c.revision) {
		c.mu.Unlock()
		return result, errors.New("connection setup changed; check again")
	}
	var row *claudeSavedConnection
	for _, candidate := range c.rows {
		if candidate.Kind == "user" && candidate.Registration == "saved" {
			copy := candidate
			row = &copy
			break
		}
	}
	c.activeCancel = cancel
	c.mu.Unlock()
	if row == nil {
		return result, errors.New("a saved user connection is required")
	}
	if c.image == nil || c.cli == nil || c.state == nil || c.command == nil {
		return result, errors.New("background message setup unavailable")
	}
	manager, err := c.image(ctx)
	if err != nil || manager == nil {
		return result, errors.New("connection management unavailable")
	}
	defer func() { err = errors.Join(err, manager.Close()) }()
	cli, err := c.cli(ctx)
	if err != nil || cli == nil {
		return result, errors.New("Claude settings command unavailable")
	}
	defer func() { err = errors.Join(err, cli.Close()) }()
	state, err := c.state()
	if err != nil {
		return result, err
	}
	if err := errors.Join(manager.Check(ctx), cli.Check(ctx)); err != nil {
		return result, err
	}
	if action == "enable" {
		review := c.backgroundReview
		if scope == "asks" {
			review = c.asksBackgroundReview
		}
		if review == nil {
			return result, errors.New("native background message review unavailable")
		}
		if row.HelperState != "current" {
			return result, errors.New("update the OA connection before enabling background messages")
		}
		if c.prepare == nil || c.admit == nil {
			return result, errors.New("current connection admission unavailable")
		}
		image, prepareErr := c.prepare(ctx)
		if prepareErr != nil || image == nil {
			return result, errors.New("update the OA connection and review Keep before enabling background messages")
		}
		defer func() { err = errors.Join(err, image.Close()) }()
		held, admitErr := c.admit(ctx, image)
		if admitErr != nil || held == nil {
			return result, errors.New("current connection Keep could not be confirmed")
		}
		defer func() { err = errors.Join(err, held.Close()) }()
		if held.Program() != row.Program || held.FileKey() != row.ProgramKey {
			return result, errors.New("saved connection admission changed; check again")
		}
		approved, reviewErr := review(ctx, cli, state)
		if reviewErr != nil {
			return result, reviewErr
		}
		if !approved {
			return claudeBackgroundState{Outcome: "declined", State: "unreviewed"}, nil
		}
		if err := held.Check(ctx); err != nil {
			return result, err
		}
		if err := c.verifyBackgroundConnection(ctx, *row, manager, state); err != nil {
			return result, err
		}
		result, err = c.watchPermissionScope(ctx, "enable", manager, cli, state, scope)
		if err != nil {
			return result, err
		}
		if result.State != "owned" && result.State != "preexisting" {
			return result, errors.New("background approval could not be confirmed")
		}
		installed, installErr := c.installUserMod(ctx, held, cli, state)
		if err = installErr; err != nil {
			return result, err
		}
		if !installed {
			return result, errors.New("background setup changed before companion installation")
		}
	} else {
		if err := c.verifyBackgroundConnection(ctx, *row, manager, state); err != nil {
			return result, err
		}
		result, err = c.watchPermissionScope(ctx, "disable", manager, cli, state, scope)
		if err != nil {
			return result, err
		}
		if result.State != "disabled" {
			return result, errors.New("background disable could not be confirmed")
		}
		other := "messages"
		if scope == "messages" {
			other = "asks"
		}
		remaining, checkErr := c.watchPermissionScope(ctx, "status", manager, cli, state, other)
		if checkErr != nil {
			return result, checkErr
		}
		if remaining.State == "unreviewed" || remaining.State == "disabled" {
			err = c.removeUserMod(ctx, manager, cli, state, row.Program)
		} else if remaining.State == "owned" || remaining.State == "preexisting" {
			// Refresh the owned receipt projection for the next plugin reload.
			_, err = c.installUserMod(ctx, manager, cli, state)
		} else {
			err = errors.New("background companion state unavailable")
		}
	}
	return result, errors.Join(err, manager.Check(ctx), cli.Check(ctx), ctx.Err())
}

func (c *claudePanelConnections) verifyBackgroundConnection(ctx context.Context, row claudeSavedConnection, manager claudePanelImage, state string) error {
	if err := manager.Check(ctx); err != nil {
		return err
	}
	raw, err := c.command(ctx, manager.Program(), "connections", "claude", "--state-dir", state)
	if err != nil {
		return err
	}
	var page struct {
		Outcome     string                  `json:"outcome"`
		Connections []claudeSavedConnection `json:"connections"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 1<<20 || decoder.Decode(&page) != nil || decoder.Decode(new(any)) != io.EOF || page.Outcome != "page" || len(page.Connections) > 64 {
		return errors.New("saved connection could not be rechecked")
	}
	for _, current := range page.Connections {
		if current.Kind == "user" && current.Registration == "saved" && current.ProjectKey == row.ProjectKey && current.Program == row.Program && current.ProgramKey == row.ProgramKey && current.Name == row.Name {
			return manager.Check(ctx)
		}
	}
	return errors.New("saved connection changed during background review")
}

func (c *claudePanelConnections) removeUserMod(ctx context.Context, manager claudePanelImage, cli claudePanelCLI, state, program string) error {
	if err := errors.Join(manager.Check(ctx), cli.Check(ctx)); err != nil {
		return err
	}
	_, err := c.command(ctx, manager.Program(), "plugin", "claude", "remove", "--scope", "user", "--state-dir", state, "--claude", cli.Program(), "--registered-program", program)
	return errors.Join(err, manager.Check(ctx), cli.Check(ctx), ctx.Err())
}

func (p *servicePanel) claudeBackgroundRoutes(mux *http.ServeMux, key string) {
	for _, scope := range []string{"messages", "asks"} {
		path := "/connections/claude/background"
		if scope == "asks" {
			path += "-asks"
		}
		mux.HandleFunc(path, guard(key, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "POST required", 405)
				return
			}
			var request struct {
				Revision string `json:"revision"`
				Action   string `json:"action"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
				http.Error(w, "invalid background message request", 400)
				return
			}
			result, err := p.claudeConnectionsForPanel().setBackgroundScope(r.Context(), request.Revision, request.Action, scope)
			if err != nil {
				http.Error(w, "Background message setup could not be confirmed. Check again before retrying.", http.StatusBadRequest)
				return
			}
			panelJSON(w, result)
		}))
	}
}
