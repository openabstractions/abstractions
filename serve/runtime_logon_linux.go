package main

import (
	"context"
	"os"

	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"github.com/openabstractions/abstraction-identity/fileid"
)

func reconcileRuntimeLogon(ctx context.Context, enabled bool) error {
	return reconcileLinuxLogon(ctx, enabled, linuxLogonOps{
		selection: bootstrap.SelectInstalled, uid: os.Getuid, euid: os.Geteuid,
		executable: os.Executable,
		open: func(ctx context.Context, path string) (fileid.Handle, error) {
			return fileid.Open(ctx, fileid.Self, path, fileid.Options{})
		},
		run: startCommand,
	})
}
