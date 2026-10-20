//go:build windows

package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

// ErrAgentImageCurrent reports an owned connection whose held bytes already
// match the installed source. It authorizes no replacement or enrollment.
var ErrAgentImageCurrent = errors.New("connection image is current")

// CurrentAgentImage holds the existing Keep and identical installed source for
// companion restoration. It owns no staged image or publication authority.
type CurrentAgentImage struct {
	Old               *EnrolledAgentImage
	Source            *AgentImage
	oldPin, sourcePin agentImagePin
	verify            func(context.Context, string, string) error
}

func HoldCurrentPublishedAgentImage(ctx context.Context, machine *client.Machine, party AgentParty, program string, verify func(context.Context, string, string) error) (*CurrentAgentImage, error) {
	old, err := HoldEnrolledAgentImageFrom(ctx, machine, party, program)
	if err != nil {
		return nil, err
	}
	source, err := AgentGatewayProgram(ctx)
	if err != nil {
		return nil, errors.Join(err, old.Close())
	}
	c := &CurrentAgentImage{Old: old, Source: source, verify: verify}
	c.oldPin, err = pinAgentImage(ctx, old.image)
	if err == nil {
		c.sourcePin, err = pinAgentImage(ctx, source)
	}
	if err == nil && (c.oldPin.Hash != c.sourcePin.Hash || c.oldPin.Bytes != c.sourcePin.Bytes) {
		err = errors.New("companion repair requires current published helper bytes")
	}
	if err == nil {
		err = c.Check(ctx)
	}
	if err != nil {
		return nil, errors.Join(err, c.Close())
	}
	return c, nil
}

func (c *CurrentAgentImage) Close() error { return errors.Join(c.Old.Close(), c.Source.Close()) }
func (c *CurrentAgentImage) Check(ctx context.Context) error {
	if c.verify == nil {
		return errors.New("positive connection ownership required")
	}
	if c.oldPin.Hash != c.sourcePin.Hash || c.oldPin.Bytes != c.sourcePin.Bytes {
		return errors.New("companion repair requires current published helper bytes")
	}
	if err := errors.Join(c.Old.Check(ctx), c.Source.Check(ctx), c.verify(ctx, c.Old.Program(), c.Old.FileKey())); err != nil {
		return err
	}
	old, err := pinAgentImage(ctx, c.Old.image)
	if err != nil {
		return err
	}
	source, err := pinAgentImage(ctx, c.Source)
	if err != nil {
		return err
	}
	if !sameAgentPin(old, c.oldPin) || !sameAgentPin(source, c.sourcePin) {
		return errors.New("current helper or published source changed; companion preserved")
	}
	return ctx.Err()
}

func StageAgentUpdate(ctx context.Context, machine *client.Machine, party AgentParty, program string, verify func(context.Context, string, string) error, claudeCompanionDrift ...func(context.Context, string, string) error) (*StagedAgentUpdate, error) {
	old, err := HoldEnrolledAgentImageFrom(ctx, machine, party, program)
	if err != nil {
		return nil, err
	}
	source, err := AgentGatewayProgram(ctx)
	if err != nil {
		return nil, errors.Join(err, old.Close())
	}
	stage, err := stageHeldAgentUpdate(ctx, old, source, party, verify, claudeCompanionDrift...)
	if err != nil {
		closed := errors.Join(old.Close(), source.Close())
		if err == ErrAgentImageCurrent {
			if closed != nil {
				return nil, closed
			}
			return nil, ErrAgentImageCurrent
		}
		return nil, errors.Join(err, closed)
	}
	return stage, nil
}

// StagedAgentUpdate keeps the exact old, installed and staged objects through
// the owning Applications service's native update question.
type StagedAgentUpdate struct {
	Old                         *EnrolledAgentImage
	Source, Image               *AgentImage
	oldPin, sourcePin, imagePin agentImagePin
	verify                      func(context.Context, string, string) error
	ResumeRequired              bool
}

func (s *StagedAgentUpdate) PreviousEnrollment() AgentEnrollmentCapture {
	return AgentEnrollmentCapture{Application: s.Old.application, Program: s.Old.Program(), FileKey: s.Old.FileKey(), Revision: s.Old.revision}
}

func (s *StagedAgentUpdate) Close() error {
	return errors.Join(s.Old.Close(), s.Source.Close(), s.Image.Close())
}

// Discard closes the stage and deletes its staged copy, for a resumed copy no
// service-owned update names. It deletes through the handle it checked: a
// replaced name, a link or a hard-linked copy stays in place with an error.
func (s *StagedAgentUpdate) Discard() error {
	program := s.Image.Program()
	if err := s.Close(); err != nil {
		return err
	}
	return deleteAgentImagePinned(context.Background(), program, s.imagePin.ID)
}

// deleteAgentImagePinned opens the name itself (never a link target), checks
// it is the pinned single-link regular file and marks that handle for
// deletion, so a swap after the check cannot redirect the delete.
func deleteAgentImagePinned(ctx context.Context, program string, pinned fileid.FileID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(program)
	if err != nil {
		return err
	}
	raw, err := windows.CreateFile(name, windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(raw), program)
	defer file.Close()
	held, err := fileid.FromFile(ctx, file)
	if err != nil {
		return fmt.Errorf("staged connection is not a plain file; preserved: %w", err)
	}
	id := held.FileID()
	if err := held.Close(); err != nil {
		return err
	}
	if !id.Equal(pinned) || id.Links() != 1 || id.IsDir() {
		return errors.New("staged connection changed; preserved")
	}
	disposition := struct{ DeleteFile bool }{true}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := windows.SetFileInformationByHandle(raw, windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&disposition)), uint32(unsafe.Sizeof(disposition))); err != nil {
		return err
	}
	return file.Close()
}

func sameAgentPin(a, b agentImagePin) bool {
	return a.ID.Equal(b.ID) && a.Hash == b.Hash && a.Bytes == b.Bytes
}

// ConfirmResume binds a previously staged object to the service's retained
// intent capture. A pathname or matching payload alone supplies no ownership.
func (s *StagedAgentUpdate) ConfirmResume(program, key string) error {
	if program != s.Image.Program() || key != s.Image.FileKey() {
		return errors.New("retained update names a different staged image")
	}
	s.ResumeRequired = false
	return nil
}

func (s *StagedAgentUpdate) Check(ctx context.Context) error {
	if err := errors.Join(s.Old.Check(ctx), s.Source.Check(ctx), s.Image.Check(ctx), s.verify(ctx, s.Old.Program(), s.Old.FileKey())); err != nil {
		return err
	}
	for _, item := range []struct {
		image *AgentImage
		pin   agentImagePin
	}{{s.Old.image, s.oldPin}, {s.Source, s.sourcePin}, {s.Image, s.imagePin}} {
		actual, err := pinAgentImage(ctx, item.image)
		if err != nil {
			return err
		}
		if !sameAgentPin(actual, item.pin) {
			return errors.New("connection update image changed; existing objects preserved")
		}
	}
	return ctx.Err()
}

// stageHeldAgentUpdate creates a separate verified image before any question.
// Publication is a later operation authorized by the service's native answer.
func stageHeldAgentUpdate(ctx context.Context, old *EnrolledAgentImage, source *AgentImage, party AgentParty, verify func(context.Context, string, string) error, claudeCompanionDrift ...func(context.Context, string, string) error) (*StagedAgentUpdate, error) {
	if verify == nil {
		return nil, errors.New("positive connection ownership required")
	}
	if err := errors.Join(old.Check(ctx), source.Check(ctx), verify(ctx, old.Program(), old.FileKey())); err != nil {
		return nil, err
	}
	previous, err := pinAgentImage(ctx, old.image)
	if err != nil {
		return nil, err
	}
	installed, err := pinAgentImage(ctx, source)
	if err != nil {
		return nil, err
	}
	if previous.Hash == installed.Hash && previous.Bytes == installed.Bytes {
		// Companion restoration stays with its existing producer under the
		// current Keep. It never creates an identical helper publication.
		return nil, ErrAgentImageCurrent
	}
	digest := sha256.Sum256([]byte(old.FileKey() + "\x00" + old.revision + "\x00" + hex.EncodeToString(installed.Hash[:])))
	name := agentImageName(party) + "." + hex.EncodeToString(digest[:]) + ".tmp"
	image, err := copyAgentImageNamed(ctx, source.Program(), filepath.Dir(old.Program()), party, name)
	resume := false
	if err != nil {
		image, err = holdAgentImageNamedForReview(ctx, filepath.Dir(old.Program()), name)
		if err != nil {
			return nil, err
		}
		resume = true
	}
	staged, err := pinAgentImage(ctx, image)
	if err != nil {
		return nil, errors.Join(err, image.Close())
	}
	s := &StagedAgentUpdate{Old: old, Source: source, Image: image, oldPin: previous, sourcePin: installed, imagePin: staged, verify: verify, ResumeRequired: resume}
	if staged.ID.Equal(previous.ID) || staged.Hash != installed.Hash || staged.Bytes != installed.Bytes {
		return nil, errors.Join(errors.New("staged connection differs from installed component"), image.Close())
	}
	if err := s.Check(ctx); err != nil {
		return nil, errors.Join(err, image.Close())
	}
	return s, nil
}
