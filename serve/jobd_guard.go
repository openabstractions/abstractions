package main

import (
	"context"

	"github.com/openabstractions/abstraction-download/go/serve"
)

// exitUpgradeInProgress is the status jobd and openabstractions share for a
// refusal while an installer replaces the installation.
const exitUpgradeInProgress = 3

// jobdHost is `openabstractions serve jobd`: the supervisor loop, entered only
// after the upgrade exclusion the other start paths honour.
type jobdHost struct {
	guard func(context.Context) error
	jobs  func([]string) error
}

func serveJobd(args []string) error {
	return jobdHost{guard: jobdUpgradeGuard, jobs: serve.Jobs}.run(args)
}

func (h jobdHost) run(args []string) error {
	if !asksHelp(args) {
		if err := h.guard(context.Background()); err != nil {
			return err
		}
	}
	return h.jobs(args)
}

// asksHelp lets a person read the options during an upgrade.
func asksHelp(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-h", "-help", "--help":
			return true
		}
	}
	return false
}
