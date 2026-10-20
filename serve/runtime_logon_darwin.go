package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"syscall"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/unix"
)

func reconcileRuntimeLogon(ctx context.Context, enabled bool) error {
	return reconcileMacLogon(ctx, enabled, macLogonOps{
		selection: bootstrap.SelectInstalled, uid: os.Getuid, euid: os.Geteuid,
		executable: os.Executable,
		open: func(ctx context.Context, path string) (fileid.Handle, error) {
			return fileid.Open(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks, Read: true})
		},
		owned: func(info os.FileInfo, uid int) bool {
			st, ok := info.Sys().(*syscall.Stat_t)
			return ok && int(st.Uid) == uid && info.Mode().Perm()&0022 == 0
		},
		write: func(ctx context.Context, path string, before, after []byte, validate func() error) error {
			return cas.ChangeLimitContext(ctx, path, 16384, func(current []byte) ([]byte, error) {
				if err := validate(); err != nil {
					return nil, err
				}
				if !bytes.Equal(current, before) || (current == nil) != (before == nil) {
					return nil, cas.ErrMoved
				}
				return after, nil
			})
		},
		remove: func(ctx context.Context, path string, expected []byte, validate func() error) error {
			// Unlink beneath the held, revalidated parent rather than re-walking a
			// possibly redirected pathname at the mutation boundary.
			dir, err := fileid.Open(ctx, fileid.Self, filepath.Dir(path), fileid.Options{Links: fileid.RefuseLinks})
			if err != nil {
				return err
			}
			defer dir.Close()
			if err := validate(); err != nil {
				return err
			}
			leaf, err := dir.Beneath(filepath.Base(path), fileid.Options{Links: fileid.RefuseLinks, Read: true})
			if err != nil {
				return err
			}
			defer leaf.Close()
			current, err := io.ReadAll(io.LimitReader(leaf.File(), 16385))
			if err != nil {
				return err
			}
			if !bytes.Equal(current, expected) {
				return cas.ErrMoved
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Unlinkat(int(dir.File().Fd()), filepath.Base(path), 0)
		},
	})
}
