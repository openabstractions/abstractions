package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/openabstractions/abstraction-facade/go/client"
)

// UpdateEnrolledAgentImage operates only on the existing native-owned fixed copy.
// Vendor configuration remains unchanged. A changed image requires a new service Keep.
func UpdateEnrolledAgentImage(ctx context.Context, machine *client.Machine, party AgentParty, verify func(context.Context, string, string) error) (*AgentImage, bool, string, error) {
	if verify == nil {
		return nil, false, "", errors.New("positive connection ownership required")
	}
	old, err := HoldEnrolledAgentImage(ctx, machine, party)
	if err != nil {
		return nil, false, "", err
	}
	source, err := AgentGatewayProgram(ctx)
	if err != nil {
		old.Close()
		return nil, false, "", err
	}
	defer source.Close()
	return updateHeldAgentImage(ctx, old, source, party, verify, replaceOwnedAgentImage)
}

func updateHeldAgentImage(ctx context.Context, old *EnrolledAgentImage, source *AgentImage, party AgentParty, verify func(context.Context, string, string) error, replace func(context.Context, string, agentImagePin, string, agentImagePin) (agentReplacementResult, error)) (*AgentImage, bool, string, error) {
	defer old.Close()
	if err := old.Check(ctx); err != nil {
		return nil, false, "", err
	}
	if err := verify(ctx, old.Program(), old.FileKey()); err != nil {
		return nil, false, "", err
	}
	oldPin, err := pinAgentImage(ctx, old.image)
	if err != nil {
		return nil, false, "", err
	}
	sourcePin, err := pinAgentImage(ctx, source)
	if err != nil {
		return nil, false, "", err
	}
	root := filepath.Dir(old.Program())
	if oldPin.Hash == sourcePin.Hash && oldPin.Bytes == sourcePin.Bytes {
		if err := old.Check(ctx); err != nil {
			return nil, false, "", err
		}
		image, err := holdAgentImageForReview(ctx, root, party)
		return image, false, "", err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, false, "", err
	}
	suffix := hex.EncodeToString(nonce[:])
	stage, err := copyAgentImageNamed(ctx, source.Program(), root, party, agentImageName(party)+"."+suffix+".tmp")
	if err != nil {
		return nil, false, "", err
	}
	stagePin, err := pinAgentImage(ctx, stage)
	stagePath := stage.Program()
	closeErr := stage.Close()
	if err != nil || closeErr != nil {
		return nil, false, "", errors.Join(err, closeErr)
	}
	if stagePin.Hash != sourcePin.Hash || stagePin.Bytes != sourcePin.Bytes {
		return nil, false, "", errors.New("staged integration differs from installed component")
	}
	if err := errors.Join(old.Check(ctx), source.Check(ctx), verify(ctx, old.Program(), old.FileKey())); err != nil {
		return nil, false, "", err
	}
	target := old.Program()
	if err := old.Close(); err != nil {
		return nil, false, "", err
	}
	replaced, err := replace(ctx, target, oldPin, stagePath, stagePin)
	backupPath := replaced.Backup
	if err != nil {
		return nil, replaced.Published, backupPath, fmt.Errorf("integration update stopped. Recovery files preserved (previous image: %s, staged image: %s): %w", backupPath, stagePath, err)
	}
	// Copies earlier updates moved aside go; this update's stays for its caller.
	removeSetAsideAgentImages(ctx, root, nil, backupPath)
	image, err := holdAgentImageForReview(ctx, root, party)
	if err != nil {
		return nil, true, backupPath, fmt.Errorf("integration update requires inspection; previous image retained at %s; current image path %s; staged image path %s: %w", backupPath, target, stagePath, err)
	}
	current, err := pinAgentImage(ctx, image)
	if err != nil || current.Hash != sourcePin.Hash || current.ID.Key() != stagePin.ID.Key() {
		image.Close()
		return nil, true, backupPath, fmt.Errorf("integration update requires inspection; previous image retained at %s; current image path %s; staged image path %s: %w", backupPath, target, stagePath, errors.Join(err, errors.New("integration image could not be confirmed")))
	}
	return image, true, backupPath, nil
}
