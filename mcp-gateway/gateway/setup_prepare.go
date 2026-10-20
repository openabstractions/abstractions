package gateway

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SetupPrepareBackend asks OpenAbstractions' setup step for one use through
// applications@3 RequestSetup under this gateway's OA principal and its
// existing ask right. The person answers the step's own native question;
// the reply never reports a download decision.
type SetupPrepareBackend interface {
	SetupPrepare(context.Context, SetupPrepareInput) (SetupPrepareOutput, error)
}

// SetupPrepareInput names the use the app asks OpenAbstractions to set up.
type SetupPrepareInput struct {
	Use string `json:"use" jsonschema:"code, image or video"`
}

// SetupPrepareOutput is RequestSetup's answer: asked (the person is asked in
// OpenAbstractions' own question, or already is), already_set_up,
// cannot_run (reason states the figures), invalid, forbidden or
// unavailable.
type SetupPrepareOutput struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

// SetupPrepare asks for the setup step through applications@3 RequestSetup.
func (o *OA) SetupPrepare(ctx context.Context, in SetupPrepareInput) (SetupPrepareOutput, error) {
	if in.Use != "code" && in.Use != "image" && in.Use != "video" {
		return SetupPrepareOutput{}, errors.New("gateway: use must be code, image or video")
	}
	c, err := o.appModelClient(ctx)
	if err != nil {
		return SetupPrepareOutput{}, err
	}
	result, err := c.RequestSetup(in.Use)
	if err != nil {
		return SetupPrepareOutput{}, err
	}
	return SetupPrepareOutput{Outcome: result.Outcome.String(), Reason: result.Reason}, nil
}

func addSetupPrepareTool(s *mcp.Server, backend SetupPrepareBackend, bounded func(context.Context, func(context.Context) error) error, effect *mcp.ToolAnnotations) {
	mcp.AddTool(s, &mcp.Tool{Name: ToolSetupPrepare, Description: "Ask OpenAbstractions to set up a use (code, image or video) on this computer. OpenAbstractions asks the person in its own question, naming this app; the answer is the person's and is never reported here. Answers asked, already_set_up or cannot_run. Video sets up LTX-2.5, video with sound; it answers already_set_up when OpenAbstractions already makes it here and cannot_run when the memory free for models is below its need. Restart the app after the person sets it up.", Annotations: effect},
		func(ctx context.Context, _ *mcp.CallToolRequest, in SetupPrepareInput) (*mcp.CallToolResult, SetupPrepareOutput, error) {
			var out SetupPrepareOutput
			err := bounded(ctx, func(ctx context.Context) (err error) { out, err = backend.SetupPrepare(ctx, in); return err })
			return nil, out, err
		})
}
