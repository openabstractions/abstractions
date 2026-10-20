//go:build windows || darwin

package setup

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

// HoldAgentImageForReview holds the fixed existing candidate for the native
// operator's permission review. It creates no image and proves no enrollment,
// installed bytes, or execution authority. Execution requires a fresh
// HoldEnrolledAgentImage with this exact FileKey after the person's answer.
func HoldAgentImageForReview(ctx context.Context, party AgentParty) (*AgentImage, error) {
	home, err := CurrentHome()
	if err != nil {
		return nil, err
	}
	if home.Isolated {
		return nil, errors.New("integration review requires the installed runtime")
	}
	if _, err := bootstrap.SelectInstalled(ctx); err != nil {
		return nil, err
	}
	return holdAgentImageForReview(ctx, filepath.Join(home.Config, "agent-integrations"), party)
}

func holdAgentImageForReview(ctx context.Context, root string, party AgentParty) (*AgentImage, error) {
	if party != AgentClaude && party != AgentCodex && party != AgentOpenCode {
		return nil, errors.New("unknown integration review target")
	}
	return holdAgentImageNamedForReview(ctx, root, agentImageName(party))
}

func holdAgentImageNamedForReview(ctx context.Context, root, name string) (*AgentImage, error) {
	if err := fileid.SafeName(name); err != nil {
		return nil, err
	}
	if err := privatedir.CheckDir(root); err != nil {
		return nil, err
	}
	directory, err := programfiles.HoldDirectory(root)
	if err != nil {
		return nil, err
	}
	program := filepath.Join(root, name)
	file, key, err := openAgentImage(ctx, program)
	if err != nil {
		return nil, errors.Join(err, directory.Release())
	}
	image := newAgentImage(file, directory, program, key)
	if err := image.Check(ctx); err != nil {
		return nil, errors.Join(err, image.Close())
	}
	return image, nil
}
