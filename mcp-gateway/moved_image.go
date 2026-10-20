package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// movedImageMiddleware answers every tool call with sentence once Setup has
// moved this connector's image aside for an update. The runtime names this
// process by the moved file from then on and refuses it, so the call would
// only fail with a less useful reason. A dedicated helper's sentence starts
// its handover to the published connector (agent_handover.go). sentence is
// read per call because the move happens while the connector runs.
func movedImageMiddleware(sentence func() string) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				if text := sentence(); text != "" {
					return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
				}
			}
			return next(ctx, method, request)
		}
	}
}
