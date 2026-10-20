package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	logservice "github.com/openabstractions/abstraction-logging/go/service"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

// ST-2 ruling 2a: the runtime reads providers' installed records under
// AccountData, which on macOS and Linux is the data root and never the cache
// folder, and both log writers default to one records log.
func TestInstalledStoresAndRecordsLogFollowAccountData(t *testing.T) {
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", home)
	} else {
		t.Setenv("HOME", home)
		t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
		t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
		t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	}
	data, err := layout.AccountData()
	if err != nil {
		t.Fatal(err)
	}
	root, err := defaultInstalledStoresRoot()
	if err != nil || root != filepath.Join(data, "OpenAbstractions", "providers") {
		t.Fatalf("installed stores root %q %v, want under %q", root, err, data)
	}
	runtimeLog, err := runtimeLogPath("")
	if err != nil {
		t.Fatal(err)
	}
	serviceLog, err := logservice.DefaultRecordsPath()
	if err != nil || runtimeLog != serviceLog {
		t.Fatalf("runtime log %q, logging service log %q %v", runtimeLog, serviceLog, err)
	}
	if runtime.GOOS == "windows" {
		if runtimeLog != filepath.Join(home, "openabstractions", "logging", "records.jsonl") {
			t.Fatalf("windows records log moved: %q", runtimeLog)
		}
		return
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, runtimeLog} {
		if strings.HasPrefix(path, cache+string(filepath.Separator)) {
			t.Fatalf("%s lies under the cache folder %s", path, cache)
		}
	}
}
