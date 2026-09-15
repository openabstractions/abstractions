package fixture

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEndpointIsPrivateAndUnique(t *testing.T) {
	dir := t.TempDir()
	a, b := Endpoint(dir, "runtime"), Endpoint(dir, "runtime")
	if runtime.GOOS == "windows" {
		if !strings.HasPrefix(a, `\\.\pipe\oa-`) || !strings.HasSuffix(a, "-runtime") {
			t.Fatalf("windows endpoint %q", a)
		}
		if a == b {
			t.Fatalf("two endpoints for one name collided: %q", a)
		}
		return
	}
	if a != filepath.Join(dir, "runtime.sock") {
		t.Fatalf("socket endpoint %q", a)
	}
}
