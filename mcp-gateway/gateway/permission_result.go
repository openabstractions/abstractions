package gateway

import (
	_ "embed"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed permission_result.schema.json
var permissionResultSchemaJSON []byte

// The listed native catalogue describes the common permission result once.
// Successful operation schemas and their original payloads remain intact.
func permissionCatalogueResult(result mcp.Result) mcp.Result {
	listed, ok := result.(*mcp.ListToolsResult)
	if !ok {
		return result
	}
	copy := *listed
	copy.Tools = make([]*mcp.Tool, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		cloned := *tool
		var pending map[string]any
		if json.Unmarshal(permissionResultSchemaJSON, &pending) != nil {
			panic("invalid embedded permission result schema")
		}
		cloned.Meta = make(mcp.Meta, len(tool.Meta)+1)
		for key, value := range tool.Meta {
			cloned.Meta[key] = value
		}
		cloned.Meta["org.openabstractions/permission_result_schema"] = pending
		if tool.OutputSchema != nil {
			cloned.OutputSchema = map[string]any{"type": "object", "anyOf": []any{tool.OutputSchema, pending}}
		}
		copy.Tools = append(copy.Tools, &cloned)
	}
	return &copy
}

func permissionToolResult(verdict string, ids []string) *mcp.CallToolResult {
	outcome, message := "awaiting_permission", questionWaiting
	switch verdict {
	case "refused":
		outcome, message = "permission_refused", questionRefused
	case "blocked":
		outcome, message = "permission_blocked", questionBlocked
	}
	state := map[string]any{"outcome": outcome, "question_ids": ids, "message": message}
	raw, _ := json.Marshal(state)
	return &mcp.CallToolResult{StructuredContent: state, Meta: mcp.Meta{permissionMetaKey: state}, Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
}
