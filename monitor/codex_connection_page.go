package main

import "github.com/openabstractions/abstractions/mcp-gateway/product"

var codexConnectionTitle = product.AgentConnectionTitle("codex")
var codexConnectionsPage = agentConnectionPage(codexConnectionTitle, "codex", "This account across desktop, CLI and IDE.", false)
