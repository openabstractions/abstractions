package gateway

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ReplyOnlyGate is a backend that marks turns another agent's message
// started (S5 ruling, 2026-11-22). It returns the refusal for a tool call
// such a turn may not make, or "".
type ReplyOnlyGate interface {
	ReplyOnlyRefusal(ctx context.Context, name string, arguments json.RawMessage) string
}

// replyOnlyMiddleware refuses a tool call the gate refuses, with the gate's
// reason as the tool's error text. Nothing reaches the backend.
func replyOnlyMiddleware(gate ReplyOnlyGate) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, request)
			}
			params, ok := request.GetParams().(*mcp.CallToolParamsRaw)
			if !ok {
				return next(ctx, method, request)
			}
			if reason := gate.ReplyOnlyRefusal(ctx, params.Name, params.Arguments); reason != "" {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: reason}}}, nil
			}
			return next(ctx, method, request)
		}
	}
}
