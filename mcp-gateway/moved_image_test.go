package main

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A connector whose image Setup moved aside answers tool calls with the
// restart sentence and passes other methods through.
func TestMovedImageMiddlewareAnswersToolCallsWithRestart(t *testing.T) {
	sentence := "Restart Claude to use the updated connector."
	reached := 0
	next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		reached++
		return &mcp.CallToolResult{}, nil
	}
	moved := movedImageMiddleware(func() string { return sentence })(next)
	result, err := moved(context.Background(), "tools/call", nil)
	tool, _ := result.(*mcp.CallToolResult)
	if err != nil || tool == nil || !tool.IsError || len(tool.Content) != 1 || tool.Content[0].(*mcp.TextContent).Text != sentence || reached != 0 {
		t.Fatalf("moved connector call: %+v %v reached=%d", result, err, reached)
	}
	if _, err := moved(context.Background(), "tools/list", nil); err != nil || reached != 1 {
		t.Fatal("tools/list did not pass through", err)
	}
	current := movedImageMiddleware(func() string { return "" })(next)
	if result, err := current(context.Background(), "tools/call", nil); err != nil || result.(*mcp.CallToolResult).IsError || reached != 2 {
		t.Fatal("current connector call was answered", result, err)
	}
}
