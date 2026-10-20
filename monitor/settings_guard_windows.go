package main

import (
	"context"
	"errors"
	"fmt"
	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"path/filepath"
	"strings"
)

func panelSettingsGuard(ctx context.Context) (func(), error) {
	return panelSettingsGuardFor(ctx, panelExplicit, layout.Current(), listen.SameEndpointName)
}

func panelSettingsGuardFor(ctx context.Context, binding *explicitRuntime, platform layout.Platform, compareEndpoint func(string, string) (bool, error)) (func(), error) {
	return panelSettingsGuardForEndpoint(ctx, binding, platform, compareEndpoint, bootstrap.InstalledEndpoint)
}

func panelSettingsGuardForEndpoint(ctx context.Context, binding *explicitRuntime, platform layout.Platform, compareEndpoint func(string, string) (bool, error), installedEndpoint func(string) (string, error)) (func(), error) {
	local, roaming, err := layout.WindowsAccountFolders(platform)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(local, ".openabstractions-settings-lifecycle")
	if binding != nil {
		if binding.StateDir == "" {
			return nil, errSetupNeedsStateDir
		}
		pipeName := strings.TrimPrefix(binding.Endpoint, `\\.\pipe\`)
		if !rightsText(binding.Endpoint, 4096) || !filepath.IsAbs(binding.Program) || pipeName == binding.Endpoint || pipeName == "" || strings.ContainsAny(pipeName, `\/`) {
			return nil, errors.New("private Panel lifecycle requires a complete native runtime binding")
		}
		installed, err := installedEndpoint("runtime-v1")
		if err != nil {
			return nil, err
		}
		same, err := compareEndpoint(installed, binding.Endpoint)
		if err != nil {
			return nil, err
		}
		if same {
			return nil, errors.New("an installed runtime cannot select a private Panel lifecycle")
		}
		if err := layout.CheckLocalStatePath(ctx, binding.Program); err != nil {
			return nil, err
		}
		program, err := fileid.Open(ctx, fileid.Self, binding.Program, fileid.Options{Links: fileid.RefuseLinks})
		if err != nil {
			return nil, err
		}
		info, statErr := program.File().Stat()
		closeErr := program.Close()
		if statErr != nil || closeErr != nil {
			return nil, errors.Join(statErr, closeErr)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("private Panel lifecycle requires a regular runtime program")
		}
		if err := layout.CheckPrivateStatePath(ctx, binding.StateDir, layout.AccountStateRootNames(local, roaming)); err != nil {
			return nil, err
		}
		if err := privatedir.CheckDir(binding.StateDir); err != nil {
			return nil, err
		}
		state, err := fileid.Pending(ctx, fileid.Self, binding.StateDir)
		if err != nil {
			return nil, err
		}
		path = filepath.Join(state.Path(), ".openabstractions-settings-lifecycle")
	}
	for _, target := range []string{path, path + ".lock"} {
		if err := layout.CheckLocalStatePath(ctx, target); err != nil {
			return nil, err
		}
	}
	return cas.LockContext(ctx, path, true)
}
