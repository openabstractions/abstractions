package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Version = "0.1.0-dev"

func boolp(v bool) *bool { return &v }

// NewServer exposes a fixed allowlist. It advertises tools only: no roots,
// sampling, prompts, resources, elicitation, subscriptions, or reverse calls.
func NewServer(backend Backend) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "openabstractions-mcp", Version: Version}, &mcp.ServerOptions{
		Capabilities:              &mcp.ServerCapabilities{},
		SupportedProtocolVersions: []string{"2026-07-28", "2025-11-25"},
	})
	read := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolp(false)}
	effect := &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: boolp(true), DestructiveHint: boolp(false)}
	submit := &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: true, OpenWorldHint: boolp(true), DestructiveHint: boolp(false)}
	cancel := &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: true, OpenWorldHint: boolp(true), DestructiveHint: boolp(true)}
	// Bound both resource occupancy and waits independently of client behavior.
	slots := make(chan struct{}, 8)
	bounded := func(ctx context.Context, call func(context.Context) error) error {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			return errors.New("gateway: concurrent request limit reached")
		}
		callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return call(callCtx)
	}
	mcp.AddTool(s, &mcp.Tool{Name: ToolApplications, Description: "List application instances and contexts visible to this OA integration principal.", Annotations: read},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ApplicationsInput) (*mcp.CallToolResult, ApplicationsOutput, error) {
			var out ApplicationsOutput
			err := bounded(ctx, func(ctx context.Context) (err error) { out, err = backend.Applications(ctx, in); return err })
			return nil, out, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: ToolModels, Description: "List model families visible to this OA integration principal.", Annotations: read},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ModelsInput) (*mcp.CallToolResult, ModelsOutput, error) {
			var out ModelsOutput
			err := bounded(ctx, func(ctx context.Context) (err error) { out, err = backend.Models(ctx, in); return err })
			return nil, out, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: ToolComplete, Description: "Run one bounded text inference call through OA.", Annotations: effect},
		func(ctx context.Context, _ *mcp.CallToolRequest, in CompleteInput) (*mcp.CallToolResult, CompleteOutput, error) {
			var out CompleteOutput
			err := bounded(ctx, func(ctx context.Context) (err error) { out, err = backend.Complete(ctx, in); return err })
			return nil, out, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: ToolJobSubmit, Description: "Idempotently submit durable image or video inference through OA and return an opaque recovery handle.", Annotations: submit},
		func(ctx context.Context, _ *mcp.CallToolRequest, in JobSubmitInput) (*mcp.CallToolResult, JobSubmitOutput, error) {
			var out JobSubmitOutput
			err := bounded(ctx, func(ctx context.Context) (err error) { out, err = backend.Submit(ctx, in); return err })
			return nil, out, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: ToolJobStatus, Description: "Read status and a bounded structured result for an OA durable inference handle.", Annotations: read},
		func(ctx context.Context, _ *mcp.CallToolRequest, in JobHandleInput) (*mcp.CallToolResult, JobStatusOutput, error) {
			var out JobStatusOutput
			err := bounded(ctx, func(ctx context.Context) (err error) { out, err = backend.Status(ctx, in); return err })
			return nil, out, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: ToolJobCancel, Description: "Explicitly request cancellation of one OA durable inference handle.", Annotations: cancel},
		func(ctx context.Context, _ *mcp.CallToolRequest, in JobHandleInput) (*mcp.CallToolResult, JobCancelOutput, error) {
			var out JobCancelOutput
			err := bounded(ctx, func(ctx context.Context) (err error) { out, err = backend.Cancel(ctx, in); return err })
			return nil, out, err
		})
	return s
}
