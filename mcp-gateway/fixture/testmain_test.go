package fixture

import (
	"os"
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/internal/personfolders"
)

// TestMain keeps this fixture package's reachable per-user state temporary.
func TestMain(m *testing.M) { os.Exit(personfolders.Main(m)) }
