package main

import (
	"context"
	"encoding/json"
	"github.com/openabstractions/abstractions/mcp-gateway/cacheproducer"
)

func claudePluginCacheGeneration(files map[string][]byte) string {
	return cacheproducer.ClaudeGeneration(files)
}
func setClaudePluginCacheGeneration(files map[string][]byte) string {
	return cacheproducer.SetClaudeGeneration(files)
}
func claudeInstalledPluginFolder(ctx context.Context, e map[string]json.RawMessage, id, scope, project, version, source string) (string, error) {
	return cacheproducer.ClaudeInstalledFolder(ctx, e, id, scope, project, version, source)
}
func verifyClaudePluginCopy(ctx context.Context, folder string, files map[string][]byte) error {
	return cacheproducer.VerifyClaudeCopy(ctx, folder, files)
}
