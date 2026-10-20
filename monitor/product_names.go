package main

import "github.com/openabstractions/abstractions/mcp-gateway/product"

// engineServerTitle names a model server no registered app titles (architect
// ruling 2026-11-09): an engine OpenAbstractions shipped reads by its
// upstream name from product.EngineTitle. Other servers use "Model server".
// shipped is product standing (shippedServer): an engine's name
// alone claims nothing, since any holder of provider management may choose
// it.
func engineServerTitle(host string, shipped bool) string {
	if title, ok := product.EngineTitle(host); ok && shipped {
		return title
	}
	return "Model server"
}

// shippedEngineTitle is a shipped engine provider's upstream title.
func shippedEngineTitle(provider string) string {
	title, _ := product.EngineTitle(provider)
	return title
}
