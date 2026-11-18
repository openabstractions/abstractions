package cacheproducer

import (
	"fmt"
	"os"
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/internal/durabletemp"
	"github.com/openabstractions/abstractions/mcp-gateway/internal/personfolders"
)

// TestMain runs this package's tests with the person's per-user folders
// inside a temporary folder (personfolders.Main), on a file system that keeps
// file IDs (durabletemp.Enter): the fixtures pin folders and programs by
// stored file ID.
func TestMain(m *testing.M) {
	temp, err := durabletemp.Enter()
	if err != nil {
		fmt.Fprintln(os.Stderr, "durabletemp:", err)
		os.Exit(2)
	}
	code := personfolders.Main(m)
	durabletemp.Leave(temp)
	os.Exit(code)
}
