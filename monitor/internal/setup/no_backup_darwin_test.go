//go:build darwin

package setup

import (
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// assertTimeMachineExcluded checks the sticky Time Machine exclusion the
// layout's MarkNoBackup writes, and tmutil's reading of it where present.
func assertTimeMachineExcluded(t *testing.T, dir string) {
	t.Helper()
	buf := make([]byte, 256)
	if _, err := unix.Getxattr(dir, "com.apple.metadata:com_apple_backup_excludeItem", buf); err != nil {
		t.Fatalf("%s: no Time Machine exclusion: %v", dir, err)
	}
	tmutil, err := exec.LookPath("tmutil")
	if err != nil {
		return
	}
	out, err := exec.Command(tmutil, "isexcluded", dir).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "[Excluded]") {
		t.Fatalf("tmutil isexcluded %s: %v %s", dir, err, out)
	}
}
