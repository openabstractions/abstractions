package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type claudeSavedCLIError struct{ cause error }

func (e *claudeSavedCLIError) Error() string { return "saved Code command could not be authenticated" }
func (e *claudeSavedCLIError) Unwrap() error { return e.cause }

// claudeManagerError is a failure of OA's own helper or state folder while it
// reads the saved Code command. It says nothing about Claude Code itself.
type claudeManagerError struct{ cause error }

func (e *claudeManagerError) Error() string { return "Code settings command unavailable" }
func (e *claudeManagerError) Unwrap() error { return e.cause }

// The normal C1 reader owns the location hint. The setup selector freshly
// authenticates that installed executable; the marker supplies no Code proof.
func (c *claudePanelConnections) selectManagementCLI(ctx context.Context) (cli claudePanelCLI, err error) {
	probe := func(ctx context.Context, program string) ([]byte, error) { return c.command(ctx, program, "--version") }
	state, err := c.state()
	if err != nil {
		return nil, &claudeManagerError{err}
	}
	manager, err := c.image(ctx)
	if err != nil || manager == nil {
		return nil, &claudeManagerError{err}
	}
	defer func() {
		if closeErr := manager.Close(); closeErr != nil {
			if cli != nil {
				closeErr = errors.Join(closeErr, cli.Close())
				cli = nil
			}
			err = errors.Join(err, closeErr)
		}
	}()
	if err := manager.Check(ctx); err != nil {
		return nil, &claudeManagerError{err}
	}
	raw, err := c.command(ctx, manager.Program(), "connections", "claude", "--state-dir", state, "--cli-locator")
	if err != nil {
		return nil, &claudeManagerError{err}
	}
	if err := manager.Check(ctx); err != nil {
		return nil, &claudeManagerError{err}
	}
	return selectClaudeManagementPage(ctx, raw, probe, func(ctx context.Context, hints []string, probe func(context.Context, string) ([]byte, error)) (claudePanelCLI, error) {
		selected, err := setup.SelectClaudeSavedCLI(ctx, hints, probe)
		if selected == nil {
			return nil, err
		}
		return selected, err
	}, selectClaudePanelCLI)
}

func selectClaudeManagementPage(ctx context.Context, raw []byte, probe func(context.Context, string) ([]byte, error), saved func(context.Context, []string, func(context.Context, string) ([]byte, error)) (claudePanelCLI, error), live func(context.Context, func(context.Context, string) ([]byte, error)) (claudePanelCLI, error)) (claudePanelCLI, error) {
	var page struct {
		Outcome     string `json:"outcome"`
		Connections []struct {
			claudeSavedConnection
			CLIIdentity string `json:"cli_identity"`
		} `json:"connections"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 1<<20 || decoder.Decode(&page) != nil || decoder.Decode(new(any)) != io.EOF || page.Outcome != "page" || page.Connections == nil || len(page.Connections) > 64 {
		return nil, &claudeManagerError{errors.New("saved Code inventory unavailable")}
	}
	var identities []string
	for _, row := range page.Connections {
		if row.Registration == "saved" && row.CLIIdentity != "" {
			identities = append(identities, row.CLIIdentity)
		}
	}
	if len(identities) == 0 {
		return live(ctx, probe)
	}
	selected, err := saved(ctx, identities, probe)
	if err != nil {
		if selected != nil {
			err = errors.Join(err, selected.Close())
		}
		return nil, &claudeSavedCLIError{err}
	}
	if selected == nil {
		return nil, &claudeSavedCLIError{errors.New("saved Code command unavailable")}
	}
	return selected, nil
}

// reconnectAvailable reports whether the person's Connect could adopt the
// running Claude Code after the saved command failed verification. The
// running image is authenticated by the same selector a first connection uses.
func (c *claudePanelConnections) reconnectAvailable(ctx context.Context, savedErr error) bool {
	var saved *claudeSavedCLIError
	if c.liveCLI == nil || !errors.As(savedErr, &saved) {
		return false
	}
	live, err := c.liveCLI(ctx)
	if live == nil {
		return false
	}
	if err == nil {
		err = live.Check(ctx)
	}
	return errors.Join(err, live.Close()) == nil
}

// connectionCLI selects the Code command for the person's Connect: the saved
// command, or the running authenticated Claude Code when the saved one can no
// longer be verified. Connect then saves the running command as the record.
func (c *claudePanelConnections) connectionCLI(ctx context.Context) (claudePanelCLI, error) {
	cli, err := c.cli(ctx)
	var saved *claudeSavedCLIError
	if cli != nil || c.liveCLI == nil || !errors.As(err, &saved) {
		return cli, err
	}
	live, liveErr := c.liveCLI(ctx)
	if live == nil {
		return nil, errors.Join(err, liveErr)
	}
	return live, liveErr
}

func panelGatewayImage(ctx context.Context) (claudePanelImage, error) {
	image, err := setup.AgentGatewayProgram(ctx)
	if err != nil {
		return nil, err
	}
	if image == nil {
		return nil, errors.New("installed connection image unavailable")
	}
	return image, nil
}

// Adapt concrete selector results before converting to the Panel interface.
// A failed constructor must remain a nil interface for every consumer.
func selectClaudePanelCLI(ctx context.Context, probe func(context.Context, string) ([]byte, error)) (claudePanelCLI, error) {
	selected, err := setup.SelectClaudeCLI(ctx, probe)
	if err != nil {
		if selected != nil {
			err = errors.Join(err, selected.Close())
		}
		return nil, err
	}
	if selected == nil {
		return nil, errors.New("Code selection unavailable")
	}
	return selected, nil
}

func selectCodexPanelCLI(ctx context.Context, probe func(context.Context, string) ([]byte, error)) (claudePanelCLI, error) {
	selected, err := setup.SelectCodexCLI(ctx, probe)
	if err != nil {
		if selected != nil {
			err = errors.Join(err, selected.Close())
		}
		return nil, err
	}
	if selected == nil {
		return nil, errors.New("Codex selection unavailable")
	}
	return selected, nil
}

func claudeCLISelectionFailure(err error) (string, string) {
	if errors.Is(err, os.ErrPermission) {
		return "cli_native_access", "The Claude Code settings command could not be verified. Open OpenAbstractions from the Start menu, then check again."
	}
	var command *panelConfigurationError
	if errors.As(err, &command) {
		switch code := command.reason(); code {
		case "command_virtualized_profile":
			return code, "Open OpenAbstractions from the Start menu, then check again."
		case "command_unknown_profile", "command_profile_folder", "command_sandboxed", "command_layout", "command_tmp_root", "command_temp", "command_identity", "command_environment", "command_handles", "command_start", "command_wait", "command_exit", "command_cleanup":
			return code, "The Code settings command is unavailable. Open OpenAbstractions from the Start menu, then check again."
		}
	}
	var manager *claudeManagerError
	if errors.As(err, &manager) {
		return "cli_manager", "The Code settings command is unavailable. Open OpenAbstractions from the Start menu, then check again."
	}
	var saved *claudeSavedCLIError
	if errors.As(err, &saved) {
		return "cli_saved_verification", "Claude Code changed since you connected it. Open Claude Code, then return here."
	}
	return "cli_selection", "Open a new local Code session in Claude, leave its prompt blank, then check again. The settings command has not been verified."
}
