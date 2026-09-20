package main

import "path/filepath"

// launchApplication routes through this account's desktop shell. The shell
// proof rejects service, account and session placements without an eligible
// interactive desktop and also escapes the runtime's kill-on-close job.
func launchApplication(program string, arguments []string) error {
	return launchThroughShellVisible(program, filepath.Dir(program), arguments...)
}
