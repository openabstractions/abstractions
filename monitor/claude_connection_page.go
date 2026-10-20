package main

import "github.com/openabstractions/abstractions/mcp-gateway/product"

var claudeConnectionTitle = product.AgentConnectionTitle("claude")
var claudeConnectionsPage = agentConnectionPage(claudeConnectionTitle, "claude", "All your Claude Code projects, including local Code sessions in Claude Desktop.", false)
