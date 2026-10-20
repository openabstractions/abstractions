package cacheproducer

import (
	"os"
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/internal/personfolders"
)

func TestMain(m *testing.M) { os.Exit(personfolders.Main(m)) }
