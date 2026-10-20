//go:build windows || darwin

package nativecli

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-identity/fileid"
)

// AdoptImage transfers an already held file and directory to one lifetime owner.
// This file hold supplies no vendor classification, enrollment or Rights grant.
func AdoptImage(file *os.File, directory *programfiles.Hold, program, key string) *Image {
	return &Image{file: file, directory: directory, program: program, key: key}
}

type Image struct {
	mu           sync.Mutex
	file         *os.File
	directory    *programfiles.Hold
	program, key string
}

func (a *Image) Program() string { return a.program }
func (a *Image) FileKey() string { return a.key }

// Check revalidates the named object while the original read-only hold remains.
func (a *Image) Check(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return errors.New("agent integration image is closed")
	}
	h, err := fileid.Open(ctx, fileid.Self, a.program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return err
	}
	defer h.Close()
	if h.FileID().Key() != a.key || h.FileID().IsDir() || h.FileID().Links() != 1 {
		return errors.New("agent integration image changed; reconnect requires review")
	}
	return ctx.Err()
}

func (a *Image) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return nil
	}
	err := errors.Join(a.file.Close(), a.directory.Release())
	a.file = nil
	return err
}
