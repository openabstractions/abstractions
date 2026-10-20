//go:build windows || darwin

package setup

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

// InstallerReceipt captures the held image for the temporary installer handoff.
// The receiving process reopens, holds and checks these bytes before publishing
// its MCP configuration. The receipt grants no enrollment or update authority.
func (image *AgentImage) InstallerReceipt(ctx context.Context) (string, error) {
	pin, err := pinAgentImage(ctx, image)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(image.Program(), "\r\n") {
		return "", errors.New("integration program cannot be represented in the installer handoff")
	}
	return hex.EncodeToString(pin.Hash[:]) + "\n" + image.Program(), nil
}

// InstallerReceipt for an older kept image retains its existing native custody.
// It permits a physical config handoff while replacement waits for first use.
func (image *EnrolledAgentImage) InstallerReceipt(ctx context.Context) (string, error) {
	image.mu.Lock()
	defer image.mu.Unlock()
	if image.image == nil {
		return "", errors.New("integration image is closed")
	}
	current, err := image.current(ctx)
	if err != nil {
		return "", err
	}
	if current.Revision != image.revision {
		return "", errors.New("integration enrollment changed")
	}
	return image.image.InstallerReceipt(ctx)
}

// PrepareInstallerAgentImage reuses the installed-image producer for local
// installation orchestration. An existing candidate must still be the exact
// installed payload. Replacement and enrollment remain the native review flow.
func PrepareInstallerAgentImage(ctx context.Context, party AgentParty) (*AgentImage, error) {
	source, err := AgentGatewayProgram(ctx)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	home, err := CurrentHome()
	if err != nil {
		return nil, err
	}
	if home.Isolated {
		return nil, errors.New("agent integration requires the installed runtime")
	}
	return prepareInstallerAgentImage(ctx, filepath.Join(home.Config, "agent-integrations"), party, source)
}

func prepareInstallerAgentImage(ctx context.Context, root string, party AgentParty, source *AgentImage) (*AgentImage, error) {
	if invalidAgentImageParty(party) {
		return nil, errors.New("unknown agent integration")
	}
	directory, err := holdInstallerIntegrationDirectory(root, true)
	if err != nil {
		return nil, err
	}
	defer directory.Release()
	current, err := holdAgentImageForReview(ctx, root, party)
	if errors.Is(err, os.ErrNotExist) {
		if err := privatedir.CheckDir(root); err != nil {
			return nil, err
		}
		return copyAgentImage(ctx, source.Program(), root, party)
	}
	if err != nil {
		return nil, err
	}
	if err := sameInstalledAgentImage(ctx, current, source); err != nil {
		return nil, errors.Join(err, current.Close())
	}
	return current, nil
}

// Hold each parent before creating its immediate child. Keep the resulting
// destination held across candidate review and the exclusive copy.
func holdInstallerIntegrationDirectory(path string, private bool) (*programfiles.Hold, error) {
	held, err := programfiles.HoldDirectory(path)
	if err == nil {
		return held, nil
	}
	parentPath := filepath.Dir(path)
	if !errors.Is(err, os.ErrNotExist) || parentPath == path {
		return nil, fmt.Errorf("%w: %w", privatedir.ErrNotPrivate, err)
	}
	parent, err := holdInstallerIntegrationDirectory(parentPath, false)
	if err != nil {
		return nil, err
	}
	defer parent.Release()
	if private {
		err = privatedir.Make(path)
	} else {
		err = os.Mkdir(path, 0700)
	}
	if err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	held, err = programfiles.HoldDirectory(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", privatedir.ErrNotPrivate, err)
	}
	return held, nil
}

func sameInstalledAgentImage(ctx context.Context, candidate, source *AgentImage) error {
	installed, err := pinAgentImage(ctx, source)
	if err != nil {
		return err
	}
	current, err := pinAgentImage(ctx, candidate)
	if err != nil {
		return err
	}
	if installed.Hash != current.Hash || installed.Bytes != current.Bytes {
		return errors.New("integration image differs from the installed component; native update review required")
	}
	return errors.Join(candidate.Check(ctx), source.Check(ctx), ctx.Err())
}
