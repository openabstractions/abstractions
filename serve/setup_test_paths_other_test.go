//go:build !windows

package main

// Setup fixture programs: an app with the ask right, the operator Panel,
// a second app and one without the ask right.
const (
	setupTestApp   = "/Applications/OpenCode.app/Contents/MacOS/opencode"
	setupTestPanel = "/Applications/OpenAbstractions.app/Contents/MacOS/Abstraction Panel"
	setupTestOther = "/opt/other/bin/other"
	setupTestNoAsk = "/opt/noask/bin/app"
)
