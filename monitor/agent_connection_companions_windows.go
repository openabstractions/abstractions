//go:build windows

package main

import (
	"context"
	"errors"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func (p *servicePanel) readyCodexConnectionUpdate(ctx context.Context, result applications.AgentUpdateResult) (err error) {
	for _, entry := range result.Enrollments {
		if entry.Application != "oa-codex-integration" {
			continue
		}
		native, e := p.agentEnrollmentForPanel(ctx)
		if e != nil {
			return e
		}
		verify := func() error {
			current, e := native.ReadAgentEnrollment(ctx, entry.Application)
			if e != nil {
				return e
			}
			if current.Outcome != applications.OutcomePage || current.Enrollment == nil || current.Enrollment.Revision != entry.Revision || current.Enrollment.Program != entry.Program || current.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
				return errors.New("Codex update enrollment changed")
			}
			return ctx.Err()
		}
		if err = verify(); err != nil {
			return err
		}
		image, e := setup.HoldEnrolledAgentImageFrom(ctx, panelMachine(), setup.AgentCodex, entry.Program)
		if e != nil {
			return e
		}
		defer func() { err = errors.Join(err, image.Close()) }()
		bound := false
		for _, staged := range result.StagedImages {
			if staged.Application == entry.Application && staged.FileKey == image.FileKey() {
				bound = true
			}
		}
		if !bound {
			return errors.New("Codex companion image differs from committed update")
		}
		c := p.codexConnectionsForPanel()
		return c.refreshKeptCompanion(ctx, image, verify)
	}
	return nil
}

// This path refreshes only an existing positively owned connection. It never
// calls enroll, declare or the first-connection action, and cannot start Keep.
func (c *codexPanelConnections) refreshKeptCompanion(ctx context.Context, image claudePanelImage, verify func() error) (err error) {
	if verify == nil || !c.action.TryLock() {
		return errors.New("Codex companion update unavailable")
	}
	defer c.action.Unlock()
	view, e := c.read(ctx)
	if e != nil {
		return e
	}
	if !view.Owned || view.Registration != "saved" || view.Program != image.Program() {
		return errors.New("Codex connection changed; preserved")
	}
	cli, e := c.cli(ctx)
	if e != nil {
		return e
	}
	if cli == nil {
		return errors.New("Codex CLI unavailable")
	}
	defer func() { err = errors.Join(err, cli.Close()) }()
	state, e := c.state()
	if e != nil {
		return e
	}
	if err = errors.Join(verify(), image.Check(ctx), cli.Check(ctx)); err != nil {
		return err
	}
	raw, e := c.command(ctx, image.Program(), "connect", "codex", "--state-dir", state, "--codex", cli.Program())
	if e != nil {
		return e
	}
	installed, e := decodeCodexView(raw)
	if e != nil {
		return e
	}
	if !installed.Owned || installed.Registration != "saved" || installed.Program != image.Program() || installed.SessionStart != "installed" {
		return errors.New("Codex companion installation was not confirmed")
	}
	return errors.Join(verify(), image.Check(ctx), cli.Check(ctx))
}
