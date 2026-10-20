//go:build !windows

package main

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-identity/fileid"
	"os"
)

type activationFileHold struct {
	fileid.Handle
	program string
	before  os.FileInfo
}

func (h *activationFileHold) Recheck(ctx context.Context) error {
	now, err := h.File().Stat()
	if err != nil {
		return err
	}
	if now.Size() != h.before.Size() || now.ModTime() != h.before.ModTime() || now.Mode() != h.before.Mode() {
		return errors.New("activation: held image changed")
	}
	named, err := fileid.Open(ctx, fileid.Self, h.program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return err
	}
	defer named.Close()
	if !named.FileID().Equal(h.FileID()) || named.FileID().Links() != 1 {
		return errors.New("activation: native image replaced")
	}
	return ctx.Err()
}
func holdActivationImage(ctx context.Context, image applicationAgentImage) (activationImageHold, error) {
	held, err := fileid.Open(ctx, fileid.Self, image.Program, fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		return nil, err
	}
	if held.FileID().Key() != image.Key || held.FileID().IsDir() || held.FileID().Links() != 1 {
		return nil, errors.Join(errors.New("activation: native image replaced"), held.Close())
	}
	info, err := held.File().Stat()
	if err != nil {
		return nil, errors.Join(err, held.Close())
	}
	return &activationFileHold{Handle: held, program: image.Program, before: info}, nil
}
