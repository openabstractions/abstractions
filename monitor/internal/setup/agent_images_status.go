//go:build windows || darwin

package setup

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"runtime"

	"github.com/openabstractions/abstraction-facade/go/client"
)

// AgentImageCurrency reads the native-owned image and installed payload without
// preparing, replacing or enrolling either image. The caller supplies its
// positively owned C1 registration; an unproved observation stays unknown.
func AgentImageCurrency(ctx context.Context, machine *client.Machine, party AgentParty, program, key string) (state string) {
	var old *EnrolledAgentImage
	var err error
	if runtime.GOOS == "darwin" {
		old, err = HoldEnrolledAgentImageFrom(ctx, machine, party, program)
	} else {
		old, err = HoldEnrolledAgentImage(ctx, machine, party)
	}
	if err != nil {
		return "unknown"
	}
	defer func() {
		if old.Close() != nil {
			state = "unknown"
		}
	}()
	source, err := AgentGatewayProgram(ctx)
	if err != nil {
		return "unknown"
	}
	defer func() {
		if source.Close() != nil {
			state = "unknown"
		}
	}()
	return agentImageCurrency(ctx, old, source, program, key)
}

// EnrolledAgentImageUpdate compares the fixed enrolled integration image with
// the installed runtime's gateway, as after a runtime upgrade: "update" with
// the installed gateway's hash, "current", "absent" when no integration image
// was made, or "unknown". It reads both images and changes neither: the person
// updates the image and answers its Keep question in the Panel.
func EnrolledAgentImageUpdate(ctx context.Context, machine *client.Machine, party AgentParty) (state, installed string) {
	old, err := HoldEnrolledAgentImage(ctx, machine, party)
	if errors.Is(err, os.ErrNotExist) {
		return "absent", ""
	}
	if err != nil {
		return "unknown", ""
	}
	defer old.Close()
	source, err := AgentGatewayProgram(ctx)
	if err != nil {
		return "unknown", ""
	}
	defer source.Close()
	state = agentImageCurrency(ctx, old, source, old.Program(), old.FileKey())
	if state != "update" {
		return state, ""
	}
	pin, err := pinAgentImage(ctx, source)
	if err != nil {
		return "unknown", ""
	}
	return state, hex.EncodeToString(pin.Hash[:])
}

func agentImageCurrency(ctx context.Context, old *EnrolledAgentImage, source *AgentImage, program, key string) string {
	if program == "" || old.Program() != program || (key != "" && old.FileKey() != key) || old.Check(ctx) != nil {
		return "unknown"
	}
	installed, err := pinAgentImage(ctx, source)
	if err != nil {
		return "unknown"
	}
	owned, err := pinAgentImage(ctx, old.image)
	if err != nil || errors.Join(old.Check(ctx), source.Check(ctx), ctx.Err()) != nil {
		return "unknown"
	}
	if installed.Bytes == owned.Bytes && installed.Hash == owned.Hash {
		return "current"
	}
	return "update"
}
