package main

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// A routing label only: native Applications still binds image/enrollment/Keep.
func agentImageApplication(image claudePanelImage, base string) string {
	name := nativecli.AgentApplicationName(filepath.Base(image.Program()))
	if nativecli.AgentApplicationBase(name) == base {
		return name
	}
	return base
}

func agentTransitionArguments(product, state, cli string, prior setup.AgentEnrollmentCapture) []string {
	return []string{"update", product, "--state-dir", state, "--" + product, cli, "--previous-program", prior.Program, "--previous-application", prior.Application, "--previous-file-key", prior.FileKey, "--previous-revision", prior.Revision}
}

func (c *claudePanelConnections) transitionHelper(ctx context.Context, row claudeSavedConnection, image claudePanelImage, prior setup.AgentEnrollmentCapture) (err error) {
	if prior.Program != row.Program || prior.FileKey != row.ProgramKey || prior.Revision == "" {
		return errors.New("prior Claude native capture differs")
	}
	cli, err := c.cli(ctx)
	if err != nil {
		return err
	}
	if cli == nil {
		return errors.New("Claude CLI unavailable")
	}
	defer func() { err = errors.Join(err, cli.Close()) }()
	state, err := c.state()
	if err != nil {
		return err
	}
	if err := errors.Join(image.Check(ctx), cli.Check(ctx)); err != nil {
		return err
	}
	_, err = c.command(ctx, image.Program(), agentTransitionArguments("claude", state, cli.Program(), prior)...)
	return errors.Join(err, image.Check(ctx), cli.Check(ctx), ctx.Err())
}

func (c *codexPanelConnections) transitionHelper(ctx context.Context, old string, image claudePanelImage, prior setup.AgentEnrollmentCapture) (err error) {
	if prior.Program != old || prior.FileKey == "" || prior.Revision == "" {
		return errors.New("prior Codex native capture differs")
	}
	cli, err := c.cli(ctx)
	if err != nil {
		return err
	}
	if cli == nil {
		return errors.New("Codex CLI unavailable")
	}
	defer func() { err = errors.Join(err, cli.Close()) }()
	state, err := c.state()
	if err != nil {
		return err
	}
	if err := errors.Join(image.Check(ctx), cli.Check(ctx)); err != nil {
		return err
	}
	_, err = c.command(ctx, image.Program(), agentTransitionArguments("codex", state, cli.Program(), prior)...)
	return errors.Join(err, image.Check(ctx), cli.Check(ctx), ctx.Err())
}
