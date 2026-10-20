//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
)

// testPersonVariable names the stand-in person every program this driver
// starts runs as. The journey gateway fixture refuses to start without it
// (mcp-gateway/journey_gateway_fixture_test.go).
const testPersonVariable = "OA_TEST_PERSON"

// enterTestHome moves this driver's per-user folders into a fixture home
// under work before any program starts, so the runtime, the Panel, the agent
// helpers and every provider the runtime launches inherit it. No journey can
// read or wake the person's own Claude, Codex or OpenCode setup: their
// configuration, sessions and plugin caches live under the moved folders
// (backlog/JOURNEY-SCOPE.md). The layout follows personfolders.under.
func enterTestHome(work string) (string, error) {
	person := filepath.Join(work, "home")
	for _, folder := range []string{filepath.Join("AppData", "Roaming"), filepath.Join("AppData", "Local"), ".config", ".cache",
		filepath.Join(".local", "share"), filepath.Join(".local", "state"), "Desktop", "Documents", "Downloads", "Pictures"} {
		if err := os.MkdirAll(filepath.Join(person, folder), 0o700); err != nil {
			return "", err
		}
	}
	for name, value := range map[string]string{
		"HOME": person, "USERPROFILE": person,
		"APPDATA": filepath.Join(person, "AppData", "Roaming"), "LOCALAPPDATA": filepath.Join(person, "AppData", "Local"),
		"XDG_CONFIG_HOME": filepath.Join(person, ".config"), "XDG_CACHE_HOME": filepath.Join(person, ".cache"),
		"XDG_DATA_HOME": filepath.Join(person, ".local", "share"), "XDG_STATE_HOME": filepath.Join(person, ".local", "state"),
		testPersonVariable: person,
	} {
		if err := os.Setenv(name, value); err != nil {
			return "", err
		}
	}
	return person, nil
}

var credentialTarget = regexp.MustCompile(`target=(oa-isolated-\S+)`)

// removeIsolatedCredentials deletes the Credential Manager entries the
// isolated runtime named isolated stored, by exact target, and no other.
func removeIsolatedCredentials(isolated string, logf func(string, ...any)) {
	if isolated == "" {
		return
	}
	out, err := childproc.Hidden(exec.Command("cmdkey", "/list")).Output()
	if err != nil {
		return
	}
	prefix := "oa-isolated-" + isolated + "/"
	for _, m := range credentialTarget.FindAllStringSubmatch(string(out), -1) {
		target := strings.TrimSpace(m[1])
		if !strings.HasPrefix(target, prefix) {
			continue
		}
		err := childproc.Hidden(exec.Command("cmdkey", "/delete:"+target)).Run()
		if logf != nil {
			logf("removed stored credential %s: %v", target, err)
		}
	}
}

// listForeignIsolatedCredentials names the oa-isolated-* entries no journey
// run made; the suite reports them and leaves them alone.
func listForeignIsolatedCredentials() []string {
	out, err := childproc.Hidden(exec.Command("cmdkey", "/list")).Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, m := range credentialTarget.FindAllStringSubmatch(string(out), -1) {
		if !strings.HasPrefix(m[1], "oa-isolated-jrn-") {
			names = append(names, m[1])
		}
	}
	return names
}
