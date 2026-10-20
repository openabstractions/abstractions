package main

import (
	"path/filepath"
	"runtime"
	"strings"
)

// attachFixtureExe is the executable suffix the agent integration helpers
// carry on this platform.
var attachFixtureExe = map[bool]string{true: ".exe"}[runtime.GOOS == "windows"]

// attachFixtureFolder is an absolute folder on this platform's spelling:
// C:\a\b on Windows and /a/b elsewhere. Vendors file native paths.
func attachFixtureFolder(parts ...string) string {
	if runtime.GOOS == "windows" {
		return `C:\` + strings.Join(parts, `\`)
	}
	return "/" + filepath.Join(parts...)
}
