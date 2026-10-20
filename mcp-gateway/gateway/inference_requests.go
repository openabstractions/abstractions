package gateway

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
)

const ToolInferenceRequests = "oa_inference_requests"

type inferenceRequestsBackend interface {
	InferenceRequests(context.Context, int64, int64) (wire.RequestPage, error)
}

func (o *OA) InferenceRequests(ctx context.Context, cursor, maxEntries int64) (wire.RequestPage, error) {
	client, err := o.machine.ResolveInferenceOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return wire.RequestPage{}, err
	}
	return client.Requests(ctx, cursor, maxEntries)
}

type inferenceRequestsInput struct {
	Cursor     int64 `json:"cursor"`
	MaxEntries int64 `json:"max_entries"`
}

func addInferenceRequestsTool(server *mcp.Server, backend inferenceRequestsBackend, bounded func(context.Context, func(context.Context) error) error, read *mcp.ToolAnnotations) {
	mcp.AddTool(server, &mcp.Tool{Name: ToolInferenceRequests, Description: "Read retained inference requests with recorded preferred model, unavailable hosts, selection and execution placement. Uses the native program's audit.read permission; this read grants nothing. cursor starts at 0 and max_entries is 1..256.", Annotations: read}, func(ctx context.Context, _ *mcp.CallToolRequest, input inferenceRequestsInput) (*mcp.CallToolResult, any, error) {
		var result wire.RequestPage
		err := bounded(ctx, func(ctx context.Context) (err error) {
			result, err = backend.InferenceRequests(ctx, input.Cursor, input.MaxEntries)
			return err
		})
		if err != nil {
			return nil, nil, err
		}
		raw, err := wire.ToJSON(&result)
		if err != nil {
			result = wire.RequestPage{Outcome: wire.AuditOutcomeUnavailable, Entries: []wire.RequestEntry{}, Next: input.Cursor}
			raw, err = wire.ToJSON(&result)
			if err != nil {
				return nil, nil, err
			}
		}
		answer, err := appModelResult(raw, result.Outcome != wire.AuditOutcomePage)
		return answer, nil, err
	})
}
