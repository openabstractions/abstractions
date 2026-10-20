package setup

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/client"
)

func UpdateEnrolledAgentImage(ctx context.Context, machine *client.Machine, party AgentParty, verify func(context.Context, string, string) error) (*AgentImage, bool, string, error) {
	return UpdateEnrolledAgentImageFrom(ctx, machine, party, "", verify)
}

// Darwin publishes a separate immutable version. It never renames, unlinks or
// writes the previous helper, and makes no old-process quiescence claim.
func UpdateEnrolledAgentImageFrom(ctx context.Context, machine *client.Machine, party AgentParty, program string, verify func(context.Context, string, string) error) (*AgentImage, bool, string, error) {
	if verify == nil {
		return nil, false, "", errors.New("positive connection ownership required")
	}
	old, err := HoldEnrolledAgentImageFrom(ctx, machine, party, program)
	if err != nil {
		return nil, false, "", err
	}
	source, err := AgentGatewayProgram(ctx)
	if err != nil {
		old.Close()
		return nil, false, "", err
	}
	defer source.Close()
	return updateVersionedAgentImage(ctx, old, source, party, verify)
}

func updateVersionedAgentImage(ctx context.Context, old *EnrolledAgentImage, source *AgentImage, party AgentParty, verify func(context.Context, string, string) error) (*AgentImage, bool, string, error) {
	defer old.Close()
	if err := errors.Join(old.Check(ctx), source.Check(ctx), verify(ctx, old.Program(), old.FileKey())); err != nil {
		return nil, false, "", err
	}
	previous, err := pinAgentImage(ctx, old.image)
	if err != nil {
		return nil, false, "", err
	}
	installed, err := pinAgentImage(ctx, source)
	if err != nil {
		return nil, false, "", err
	}
	root := filepath.Dir(old.Program())
	if previous.Hash == installed.Hash && previous.Bytes == installed.Bytes {
		image, err := holdAgentImageNamedForReview(ctx, root, filepath.Base(old.Program()))
		return image, false, "", err
	}
	name := agentImageName(party) + "-" + hex.EncodeToString(installed.Hash[:])
	application, err := agentApplicationName(party, name)
	if err != nil {
		return nil, false, old.Program(), err
	}
	image, err := copyAgentImageNamed(ctx, source.Program(), root, party, name)
	if err != nil {
		// A prior explicit attempt may already have a native-owned pending or
		// enrolled capture. Unknown existing files are preserved and refused.
		view, readErr := old.read(ctx, application)
		if readErr != nil || view.Outcome != applications.OutcomePage || view.Enrollment == nil || view.Enrollment.Application != application || view.Enrollment.Program != filepath.Join(root, name) || (view.Enrollment.State != applications.AgentEnrollmentStatePending && view.Enrollment.State != applications.AgentEnrollmentStateEnrolled) {
			return nil, false, old.Program(), fmt.Errorf("new helper publication stopped; existing objects preserved: %w", err)
		}
		image, readErr = holdAgentImageNamedForReview(ctx, root, name)
		if readErr != nil {
			return nil, false, old.Program(), readErr
		}
	}
	fail := func(err error) (*AgentImage, bool, string, error) {
		image.Close()
		return nil, false, old.Program(), err
	}
	actual, err := pinAgentImage(ctx, image)
	if err != nil {
		return fail(err)
	}
	if actual.ID.Equal(previous.ID) || actual.Hash != installed.Hash || actual.Bytes != installed.Bytes {
		return fail(errors.New("new helper does not match the installed component"))
	}
	after, err := pinAgentImage(ctx, source)
	if err != nil || after.Hash != installed.Hash || after.Bytes != installed.Bytes {
		return fail(errors.Join(err, errors.New("installed component changed during copy")))
	}
	if err := errors.Join(old.Check(ctx), source.Check(ctx), verify(ctx, old.Program(), old.FileKey())); err != nil {
		return fail(err)
	}
	if err := errors.Join(old.Check(ctx), source.Check(ctx), image.Check(ctx)); err != nil {
		return fail(err)
	}
	currentSource, err := pinAgentImage(ctx, source)
	if err != nil || currentSource.Hash != installed.Hash || currentSource.Bytes != installed.Bytes {
		return fail(errors.Join(err, errors.New("installed component changed before publication")))
	}
	currentImage, err := pinAgentImage(ctx, image)
	if err != nil || currentImage.Hash != installed.Hash || currentImage.Bytes != installed.Bytes || !currentImage.ID.Equal(actual.ID) {
		return fail(errors.Join(err, errors.New("new helper changed before publication")))
	}
	image.previous = AgentEnrollmentCapture{Application: old.application, Program: old.Program(), FileKey: old.FileKey(), Revision: old.revision}
	return image, true, old.Program(), nil
}
