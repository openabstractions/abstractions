package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-provider-kit/go/modelplace"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func installedSetupTransport(input setup.QualificationInput) (*setup.PublicTransfer, error) {
	if runtime.GOARCH != "amd64" {
		return nil, errors.New("installed Setup qualification requires Windows x64")
	}
	elevated, err := processElevated()
	if err != nil || elevated {
		return nil, errors.Join(err, errors.New("installed Setup qualification requires an unelevated user session"))
	}
	home, err := setup.CurrentHome()
	if err != nil || home.Isolated {
		return nil, errors.Join(err, errors.New("installed Setup qualification requires the installed Setup home"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	selected, err := bootstrap.SelectInstalled(ctx)
	if err != nil {
		return nil, err
	}
	local, err := layout.LocalAppData()
	if err != nil {
		return nil, err
	}
	// Windows runtime state uses LOCALAPPDATA. Require it to match the token's
	// native known folder before admitting the installed runtime.
	state, err := modelplace.RuntimeStateFor("windows", os.Getenv, nil)
	if err != nil {
		return nil, err
	}
	if !identity.SamePathName(filepath.Dir(filepath.Dir(state)), local) {
		return nil, errors.New("installed Setup qualification requires the account's native runtime state folder")
	}
	if err := layout.CheckLocalStatePath(ctx, state); err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	machine, err := installedSetupMachine(ctx, input, selected, state, self)
	if err != nil {
		return nil, err
	}
	transfer, err := setup.NewInstalledQualificationTransfer(machine, input.Mirrors, input.Expires)
	if err != nil {
		return nil, err
	}
	// Keep the installed home and bind ordinary Panel calls to the same peer.
	panelMachine = func() *client.Machine { return machine }
	return &transfer, nil
}

func installedSetupMachine(ctx context.Context, input setup.QualificationInput, selected bootstrap.Selection, state, self string) (*client.Machine, error) {
	if selected.EndpointFromEnvironment || selected.Endpoint != input.RuntimeEndpoint || !filepath.IsAbs(input.RuntimeProgram) || !filepath.IsAbs(input.RuntimeStateDir) ||
		!identity.SamePathName(filepath.Clean(input.RuntimeProgram), selected.Server.Program) || !identity.SamePathName(filepath.Clean(input.RuntimeStateDir), state) {
		return nil, errors.New("startup transport differs from the installed runtime selection")
	}
	panel := filepath.Join(filepath.Dir(selected.Server.Program), "Abstraction Panel.exe")
	if !identity.SamePathName(identity.CanonicalProgramPath(self), identity.CanonicalProgramPath(panel)) {
		return nil, errors.New("installed Setup qualification requires the Panel in the registered installation")
	}
	machine := client.NewVerified(selected.Endpoint, selected.Server)
	caller, err := machine.ObserveCaller(ctx)
	if err != nil {
		return nil, fmt.Errorf("verify the installed runtime connection: %w", err)
	}
	account, accountErr := selected.Server.Principal.Account()
	if accountErr != nil || caller.Outcome != wire.CallerOutcomeObserved || caller.Account != account || caller.PID != int64(os.Getpid()) || !identity.SameSubjectProgram(caller.Program, self) {
		return nil, errors.New("the installed runtime did not verify this Panel")
	}
	return machine, nil
}
