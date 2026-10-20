package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"github.com/openabstractions/abstraction-identity/fileid"
)

type linuxLogonOps struct {
	selection  func(context.Context) (bootstrap.Selection, error)
	uid, euid  func() int
	executable func() (string, error)
	open       func(context.Context, string) (fileid.Handle, error)
	run        func(context.Context, string, ...string) error
}

// The fixed installed user unit stays loaded for demand start. Login policy
// changes no running process: neither --now nor stop/uninstall is dispatched.
func reconcileLinuxLogon(parent context.Context, enabled bool, ops linuxLogonOps) error {
	ctx, cancel := context.WithTimeout(parent, bootstrap.DefaultActivationBudget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	uid := ops.euid()
	if uid <= 0 || ops.uid() != uid {
		return errors.New("runtime login requires the current unelevated account")
	}
	program, err := ops.executable()
	if err != nil {
		return err
	}
	self, err := ops.open(ctx, program)
	if err != nil {
		return err
	}
	defer self.Close()
	info, err := self.File().Stat()
	if err != nil {
		return fmt.Errorf("runtime login executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("runtime login executable is not a regular file")
	}
	validate := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ops.uid() != uid || ops.euid() != uid {
			return errors.New("runtime login account changed during reconciliation")
		}
		selected, err := ops.selection(ctx)
		if err != nil {
			return err
		}
		if selected.EndpointFromEnvironment || selected.Server.Process != nil || selected.Server.Principal.Kind != "posix" || selected.Server.Principal.UID != uid {
			return errors.New("runtime login refuses an override or another account's installation")
		}
		held, err := ops.open(ctx, selected.Server.Program)
		if err != nil {
			return err
		}
		defer held.Close()
		if !self.FileID().Equal(held.FileID()) {
			return errors.New("runtime login executable differs from selected installation")
		}
		return nil
	}
	if err := validate(); err != nil {
		return err
	}
	operation := "disable"
	if enabled {
		operation = "enable"
	}
	if err := ops.run(ctx, "/usr/bin/systemctl", "--user", operation, "abstraction-runtime.service"); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return validate()
}
