package setup

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
)

// HoldRetainedAgentUpdate opens only the exact service-returned prior object.
// Publication reuses the existing all-member native custody barrier.
func HoldRetainedAgentUpdate(ctx context.Context, machine *client.Machine, party AgentParty, program, retained, key string, verify func(context.Context, string, string) error) (*StagedAgentUpdate, error) {
	if verify == nil || key == "" || !identity.SamePathName(filepath.Dir(program), filepath.Dir(retained)) {
		return nil, errors.New("retained update scope unavailable")
	}
	old, err := HoldEnrolledAgentImageFrom(ctx, machine, party, program)
	if err != nil {
		return nil, err
	}
	source, err := AgentGatewayProgram(ctx)
	if err != nil {
		return nil, errors.Join(err, old.Close())
	}
	image, err := holdAgentImageNamedForReview(ctx, filepath.Dir(program), filepath.Base(retained))
	if err != nil {
		return nil, errors.Join(err, old.Close(), source.Close())
	}
	stage, err := retainedHeldAgentUpdate(ctx, old, source, image, key, verify)
	if err != nil {
		return nil, errors.Join(err, old.Close(), source.Close(), image.Close())
	}
	return stage, nil
}

func retainedHeldAgentUpdate(ctx context.Context, old *EnrolledAgentImage, source, image *AgentImage, key string, verify func(context.Context, string, string) error) (*StagedAgentUpdate, error) {
	if verify == nil || key == "" || image.FileKey() != key || !identity.SamePathName(filepath.Dir(old.Program()), filepath.Dir(image.Program())) {
		return nil, errors.New("retained update differs from the captured image")
	}
	previous, err := pinAgentImage(ctx, old.image)
	if err != nil {
		return nil, err
	}
	installed, err := pinAgentImage(ctx, source)
	if err != nil {
		return nil, err
	}
	retained, err := pinAgentImage(ctx, image)
	if err != nil {
		return nil, err
	}
	if retained.ID.Equal(previous.ID) {
		return nil, errors.New("retained update names current object")
	}
	stage := &StagedAgentUpdate{Old: old, Source: source, Image: image, oldPin: previous, sourcePin: installed, imagePin: retained, verify: verify}
	if err := stage.Check(ctx); err != nil {
		return nil, err
	}
	return stage, nil
}
