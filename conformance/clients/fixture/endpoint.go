package fixture

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"
)

var endpointCount atomic.Uint64

// Endpoint names a private local endpoint for a fixture service: a unique named
// pipe on Windows, and a Unix socket in dir elsewhere. dir should be short and
// private, for example a test's TempDir; socket paths are length-bounded. It
// mirrors workspace.fixture_endpoints for fixtures written in Go.
func Endpoint(dir, name string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\oa-%d-%d-%s`, time.Now().UnixNano(), endpointCount.Add(1), name)
	}
	return filepath.Join(dir, name+".sock")
}
