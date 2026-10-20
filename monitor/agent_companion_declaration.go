package main

import (
	"context"
	"errors"
)

func declarePanelCompanion(ctx context.Context, manager claudePanelImage, target claudePanelImage, cli claudePanelCLI, state, host, requirement, name, project string, command func(context.Context, string, ...string) ([]byte, error)) error {
	if manager == nil || target == nil || cli == nil || command == nil {
		return errors.New("installed companion producer unavailable")
	}
	if err := errors.Join(manager.Check(ctx), target.Check(ctx), cli.Check(ctx)); err != nil {
		return err
	}
	args := []string{"declare-companions", "--host", host, "--member", requirement, "--state-dir", state, "--cli", cli.Program(), "--helper-program", target.Program(), "--helper-key", target.FileKey(), "--name", name}
	if project != "" {
		args = append(args, "--project", project)
	}
	_, err := command(ctx, manager.Program(), args...)
	return errors.Join(err, manager.Check(ctx), target.Check(ctx), cli.Check(ctx))
}

func (c *claudePanelConnections) declareCompanion(ctx context.Context, target claudePanelImage, project string) (err error) {
	manager, err := c.image(ctx)
	if err != nil {
		return err
	}
	if manager == nil {
		return errors.New("installed companion producer unavailable")
	}
	defer func() { err = errors.Join(err, manager.Close()) }()
	cli, err := c.cli(ctx)
	if err != nil {
		return err
	}
	if cli == nil {
		return errors.New("vendor CLI unavailable")
	}
	defer func() { err = errors.Join(err, cli.Close()) }()
	state, err := c.state()
	if err != nil {
		return err
	}
	requirement, name := "none", "OpenAbstractions"
	if c.userMod || c.projectRequired {
		requirement = "required"
	}
	if c.projectRequired {
		name = "openabstractions"
	} else {
		project = ""
	}
	return declarePanelCompanion(ctx, manager, target, cli, state, "claude", requirement, name, project, c.command)
}
func (c *codexPanelConnections) declareCompanion(ctx context.Context, target claudePanelImage) (err error) {
	manager, err := c.manager(ctx)
	if err != nil {
		return err
	}
	if manager == nil {
		return errors.New("installed companion producer unavailable")
	}
	defer func() { err = errors.Join(err, manager.Close()) }()
	cli, err := c.cli(ctx)
	if err != nil {
		return err
	}
	if cli == nil {
		return errors.New("vendor CLI unavailable")
	}
	defer func() { err = errors.Join(err, cli.Close()) }()
	state, err := c.state()
	if err != nil {
		return err
	}
	return declarePanelCompanion(ctx, manager, target, cli, state, "codex", "required", "OpenAbstractions", "", c.command)
}
