package main

import (
	"os"
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/internal/personfolders"
)

// TestMain keeps every test's per-user folders in a temporary stand-in.
func TestMain(m *testing.M) { os.Exit(personfolders.Main(m)) }
